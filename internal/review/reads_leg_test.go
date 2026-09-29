package review_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// A broken served tool degrades where the policy says degrade: the pass
// still runs on the supplied prompt, the reason travels in the pass
// comment, and the marker carries the envelope.
func TestReadsDegradeOnABrokenTool(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.serveErr = errors.New("connection refused")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	texts := ui.Texts(got.Messages)
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "Served reads degraded (self_test_failed)") {
		t.Errorf("no degrade warning in the pass comment: %q", texts)
	}
	if len(got.Marker.Reads) == 0 {
		t.Fatal("the marker carries no reads envelope")
	}
	envelope, err := prstate.DecodeReadsEnvelope(got.Marker.Reads)
	if err != nil {
		t.Fatalf("decoding the marker envelope: %v", err)
	}
	if envelope.DeclaredMode != "served" || envelope.EffectiveMode != "supplied" || envelope.Reason != "self_test_failed" {
		t.Errorf("envelope = %+v, want served-to-supplied with self_test_failed", envelope)
	}
}

// A broken served tool halts where the policy says halt: the call
// publishes nothing and the failure names reads_unavailable.
func TestReadsHaltOnABrokenTool(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.cfg = mustConfig(t, "version: 2\npolicy:\n  on_reads_unavailable: halt\n")
	e.runner.serveErr = errors.New("connection refused")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("Run: want the halt to stop the leg")
	}
	if !strings.Contains(got.Err.Error(), "reads_unavailable") {
		t.Errorf("err = %v, want the reads_unavailable name", got.Err)
	}
	if e.runner.calls != 0 {
		t.Errorf("harness calls = %d, want 0 (halted before any child)", e.runner.calls)
	}
}

// With nothing to byte-check against, the self-test is skipped rather than
// failed: a pass over an empty file listing runs clean, with no warning
// and no envelope on the marker.
func TestReadsSkipWithNothingToByteCheck(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(`{"verdict":"converged","findings":[]}`)}}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	for _, line := range ui.Texts(got.Messages) {
		if strings.Contains(line, "Served reads degraded") {
			t.Errorf("a skipped self-test degrades: %q", line)
		}
	}
	if len(got.Marker.Reads) != 0 {
		t.Errorf("the marker carries a reads envelope with no served reads: %s", got.Marker.Reads)
	}
}

// A grok review leg at a moved pin is refused with
// review_isolation_unverified before any child starts: the tripwire block
// is verified on the pinned version, or the pin moves.
func TestUnverifiedGrokReviewIsRefused(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	raw := harness.DescriptorJSON()
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decoding the descriptor: %v", err)
	}
	for _, entry := range document["harnesses"].([]any) {
		if entry.(map[string]any)["name"] == "grok" {
			install := entry.(map[string]any)["install"].(map[string]any)
			install["pinned_version"] = "9.9.9"
			install["command"] = "install 9.9.9"
		}
	}
	mutated, _ := json.Marshal(document)
	doc, err := harness.Load(mutated)
	if err != nil {
		t.Fatalf("loading the moved pin: %v", err)
	}
	e.doc = doc
	req := e.request(t)
	req.HarnessOverride = "grok"
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}

	got := runLeg(t, e, req)
	if got.Err == nil {
		t.Fatal("Run: want the unverified grok review refused")
	}
	if !strings.Contains(got.Err.Error(), "review_isolation_unverified") {
		t.Errorf("err = %v, want the review_isolation_unverified name", got.Err)
	}
	if e.runner.calls != 0 {
		t.Errorf("harness calls = %d, want 0 (refused before any child)", e.runner.calls)
	}
}

// A command event on the review leg halts with review_leg_ran_command and
// publishes nothing, under either policy.
func TestReviewTripwireHaltsTheLeg(t *testing.T) {
	for _, policy := range []string{"", "version: 2\npolicy:\n  on_reads_unavailable: halt\n"} {
		e := newEnv(t)
		writeRequiredHead(e, "a.go", "package a\n")
		if policy != "" {
			e.cfg = mustConfig(t, policy)
		}
		e.runner.script = []exec.Result{{
			ExitCode: 0,
			Stdout:   []byte("{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Bash\",\"input\":{\"command\":\"id\"}}]}}\n" + string(claudeStdout(batchAnswerFor(t, []string{"a.go"})))),
		}}

		got := runLeg(t, e, e.request(t))
		if got.Err == nil {
			t.Fatalf("policy %q: want the tripwire to halt the leg", policy)
		}
		if !strings.Contains(got.Err.Error(), "review_leg_ran_command") {
			t.Errorf("policy %q: err = %v, want the review_leg_ran_command name", policy, got.Err)
		}
	}
}
