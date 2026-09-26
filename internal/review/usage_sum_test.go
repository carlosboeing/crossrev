package review_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// claudeStdoutWithUsage answers payload with one modelUsage entry carrying
// the given counters, so the envelope reports usage and names model as the
// answering model.
func claudeStdoutWithUsage(t *testing.T, payload, model string, in, read, write, out int64) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"result":   payload,
		"is_error": false,
		"modelUsage": map[string]any{
			model: map[string]any{
				"inputTokens":              in,
				"outputTokens":             out,
				"cacheReadInputTokens":     read,
				"cacheCreationInputTokens": write,
				"canonicalModel":           model,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// usageBuckets reads the marker's usage record back into comparable buckets.
func usageBuckets(t *testing.T, raw json.RawMessage) map[string]int64 {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("usage decode: %v", err)
	}
	out := map[string]int64{}
	for _, key := range []string{"input_fresh", "cache_read", "cache_write_5m", "cache_write_1h", "cache_write_unsplit", "output", "total"} {
		value, ok := got[key].(float64)
		if !ok {
			t.Fatalf("usage[%s] = %v, want a number", key, got[key])
		}
		out[key] = int64(value)
	}
	return out
}

// writeNumberedFiles writes n required head files packing into ceil(n/40)
// batches of 40, and answers their paths in pack order.
func writeNumberedFiles(e *env, n int) []string {
	paths := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		path := fmt.Sprintf("file%02d.go", i)
		writeRequiredHead(e, path, "package x\n")
		paths = append(paths, path)
	}
	return paths
}

// TestReviewSumsUsageAcrossEveryAcceptedBatch pins the honest pass total:
// three batches whose envelopes carry distinct bucket values sum exactly
// into the marker's tokens and usage, instead of the first envelope alone.
func TestReviewSumsUsageAcrossEveryAcceptedBatch(t *testing.T) {
	e := newEnv(t)
	paths := writeNumberedFiles(e, 81)
	buckets := [][4]int64{{100, 10, 20, 30}, {200, 40, 50, 60}, {300, 70, 80, 90}}
	for i := 0; i < 3; i++ {
		lo, hi := i*40, (i+1)*40
		if hi > len(paths) {
			hi = len(paths)
		}
		e.runner.script = append(e.runner.script, exec.Result{ExitCode: 0, Stdout: claudeStdoutWithUsage(t,
			batchAnswerFor(t, paths[lo:hi]), "claude-test", buckets[i][0], buckets[i][1], buckets[i][2], buckets[i][3])})
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked", got.Outcome)
	}
	if string(got.Marker.Tokens) != "1050" {
		t.Errorf("marker tokens = %s, want 1050 (the three envelopes summed)", got.Marker.Tokens)
	}
	want := map[string]int64{"input_fresh": 600, "cache_read": 120, "cache_write_5m": 0, "cache_write_1h": 0, "cache_write_unsplit": 150, "output": 180, "total": 1050}
	for key, wantValue := range want {
		if usageBuckets(t, got.Marker.Usage)[key] != wantValue {
			t.Errorf("usage[%s] = %d, want %d", key, usageBuckets(t, got.Marker.Usage)[key], wantValue)
		}
	}
	if model, _ := got.Marker.ModelReported.Get(); model != "claude-test" {
		t.Errorf("model_reported = %q, want claude-test", model)
	}
	for _, line := range got.Messages {
		if line.Kind == ui.KindWarn && strings.Contains(line.Text, "model") {
			t.Errorf("unexpected model warning on a single-model pass: %q", line.Text)
		}
	}
}

// TestReviewResumeAddsOnlyFreshBatchUsage pins that a resumed pass sums only
// the calls it accepted: batch one's envelope stays out of the marker when
// batch two is the run's only fresh call.
func TestReviewResumeAddsOnlyFreshBatchUsage(t *testing.T) {
	e := newEnv(t)
	paths := writeNumberedFiles(e, 41)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdoutWithUsage(t, batchAnswerFor(t, paths[:40]), "claude-test", 1000, 100, 200, 300)},
		{ExitCode: 1, Stderr: []byte("harness died")},
	}
	initial := runLeg(t, e, e.request(t))
	if initial.Err == nil {
		t.Fatal("first Run: want the batch-two failure to stop the leg")
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdoutWithUsage(t, batchAnswerFor(t, paths[40:]), "claude-test", 100, 10, 20, 30)},
	}
	resumed := runLeg(t, e, e.request(t))
	if resumed.Err != nil {
		t.Fatalf("resumed Run: %v", resumed.Err)
	}
	if string(resumed.Marker.Tokens) != "160" {
		t.Errorf("resumed marker tokens = %s, want 160 (the fresh envelope alone)", resumed.Marker.Tokens)
	}
	want := map[string]int64{"input_fresh": 100, "cache_read": 10, "cache_write_5m": 0, "cache_write_1h": 0, "cache_write_unsplit": 20, "output": 30, "total": 160}
	for key, wantValue := range want {
		if usageBuckets(t, resumed.Marker.Usage)[key] != wantValue {
			t.Errorf("usage[%s] = %d, want %d", key, usageBuckets(t, resumed.Marker.Usage)[key], wantValue)
		}
	}
}

// TestReviewUsageWarnsOnceWhenTheModelChanges pins the substitution signal: a
// later call answering under another model warns once naming both, the marker
// keeps the first, and a third model adds no second warning.
func TestReviewUsageWarnsOnceWhenTheModelChanges(t *testing.T) {
	e := newEnv(t)
	paths := writeNumberedFiles(e, 81)
	models := []string{"claude-alpha", "claude-beta", "claude-gamma"}
	for i := 0; i < 3; i++ {
		lo, hi := i*40, (i+1)*40
		if hi > len(paths) {
			hi = len(paths)
		}
		e.runner.script = append(e.runner.script, exec.Result{ExitCode: 0, Stdout: claudeStdoutWithUsage(t,
			batchAnswerFor(t, paths[lo:hi]), models[i], 100, 10, 20, 30)})
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	var warns []ui.Line
	for _, line := range got.Messages {
		if line.Kind == ui.KindWarn && strings.Contains(line.Text, "claude-alpha") {
			warns = append(warns, line)
		}
	}
	if len(warns) != 1 {
		t.Fatalf("model warnings = %d, want 1", len(warns))
	}
	if !strings.Contains(warns[0].Text, "claude-beta") {
		t.Errorf("warning names %q, want it to name claude-beta too", warns[0].Text)
	}
	if strings.Contains(warns[0].Text, "claude-gamma") || strings.Contains(warns[0].Action, "claude-gamma") {
		t.Errorf("warning names the third model, want the first mismatch only: %q", warns[0].Text)
	}
	if model, _ := got.Marker.ModelReported.Get(); model != "claude-alpha" {
		t.Errorf("model_reported = %q, want claude-alpha (the first call)", model)
	}
	if string(got.Marker.Tokens) != "480" {
		t.Errorf("marker tokens = %s, want 480 (all three envelopes summed)", got.Marker.Tokens)
	}
}
