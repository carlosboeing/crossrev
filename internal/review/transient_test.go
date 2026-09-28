package review_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// agyErrorStdout is an agy run that ended without SUCCESS: the adapter reads
// the error member, whatever the exit code.
func agyErrorStdout(message string) []byte {
	raw, err := json.Marshal(map[string]any{
		"status": "ERROR",
		"error":  message,
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// agySuccessStdout is an agy run that answered SUCCESS, with usage buckets
// for the attempts the retry sums.
func agySuccessStdout(t *testing.T, response string, in, out int64) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"status":   "SUCCESS",
		"response": response,
		"usage": map[string]any{
			"input_tokens":  in,
			"output_tokens": out,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// agyStructuredStdout is an agy run that answered SUCCESS with the parsed
// object under structured_output.
func agyStructuredStdout(t *testing.T, payload string, in, out int64) []byte {
	t.Helper()
	var structured any
	if err := json.Unmarshal([]byte(payload), &structured); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"status":            "SUCCESS",
		"structured_output": structured,
		"usage": map[string]any{
			"input_tokens":  in,
			"output_tokens": out,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestATransient503IsRetriedOnce(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: agyErrorStdout("UNAVAILABLE (code 503): Deadline expired")},
		{ExitCode: 0, Stdout: agyStructuredStdout(t, issuesPayload(twoFindings), 7, 3)},
	}
	req := e.request(t)
	req.HarnessOverride = "agy"

	got := runLeg(t, e, req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 2 {
		t.Fatalf("the harness was invoked %d time(s), want 2", e.runner.calls)
	}
	var warned bool
	for _, line := range got.Messages {
		if strings.Contains(line.Text, "transient") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("the transient retry was silent; the leg said %q", ui.Texts(got.Messages))
	}
	if log := readRunLog(t, e); !strings.Contains(log, "attempt=2") {
		t.Errorf("the run log records no second attempt:\n%s", log)
	}
}

// The stream interruption the harness reports on a normal exit is a
// transport failure, not the signal death the interrupt path already owns.
func TestAnInterruptedStreamIsRetriedOnce(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{
		{ExitCode: 1, Stderr: []byte("banner\nThe stream was interrupted\n")},
		{ExitCode: 0, Stdout: agyStructuredStdout(t, issuesPayload(twoFindings), 7, 3)},
	}
	req := e.request(t)
	req.HarnessOverride = "agy"

	got := runLeg(t, e, req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 2 {
		t.Fatalf("the harness was invoked %d time(s), want 2", e.runner.calls)
	}
}

// A SUCCESS with an empty response answered nothing: empty output is never
// clean coverage, and a harness that constrains its own output failing to
// produce it is a harness failure rather than model drift.
func TestASuccessfulEmptyAnswerIsRetriedOnce(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: []byte(`{"status":"SUCCESS","response":"","duration_seconds":112.07,"num_turns":1}`)},
		{ExitCode: 0, Stdout: agyStructuredStdout(t, issuesPayload(twoFindings), 7, 3)},
	}
	req := e.request(t)
	req.HarnessOverride = "agy"

	got := runLeg(t, e, req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 2 {
		t.Fatalf("the harness was invoked %d time(s), want 2", e.runner.calls)
	}
	var warned bool
	for _, line := range got.Messages {
		if strings.Contains(line.Text, "empty") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("the empty-answer retry was silent; the leg said %q", ui.Texts(got.Messages))
	}
	if log := readRunLog(t, e); !strings.Contains(log, "attempt=2") {
		t.Errorf("the run log records no second attempt:\n%s", log)
	}
}

// A second empty answer lands where an empty answer always landed: the
// shape refusal, with its existing words rather than a new fatal.
func TestARepeatedEmptyAnswerStillRefusesAsASchemaMismatch(t *testing.T) {
	e := newEnv(t)
	// The batch path, where the validator compares the answer against the
	// numbered units and an empty document is a shape error. The frozen
	// path accepts emptiness the way jq does, by parity.
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: []byte(`{"status":"SUCCESS","response":""}`)},
		{ExitCode: 0, Stdout: []byte(`{"status":"SUCCESS","response":""}`)},
	}
	req := e.request(t)
	req.HarnessOverride = "agy"

	got := runLeg(t, e, req)
	if got.Err == nil {
		t.Fatal("two empty answers did not fail the leg")
	}
	if !strings.Contains(got.Err.Error(), "does not match the schema") {
		t.Errorf("err = %q, want the schema-mismatch refusal", got.Err)
	}
	if e.runner.calls != 2 {
		t.Errorf("the harness was invoked %d time(s), want 2", e.runner.calls)
	}
}

// Both attempts' usage is spent, so the accepted envelope sums both.
func TestATransientRetrySumsBothAttemptsUsage(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: agySuccessStdout(t, "", 10, 5)},
		{ExitCode: 0, Stdout: agyStructuredStdout(t, issuesPayload(twoFindings), 7, 3)},
	}
	req := e.request(t)
	req.HarnessOverride = "agy"

	got := runLeg(t, e, req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Envelope == nil || got.Envelope.Usage == nil {
		t.Fatal("the accepted envelope carries no usage")
	}
	usage := got.Envelope.Usage
	if usage.InputFresh != 17 || usage.Output != 8 {
		t.Errorf("usage = in %d out %d, want in 17 out 8", usage.InputFresh, usage.Output)
	}
	if usage.Total == nil || *usage.Total != 25 {
		t.Errorf("total = %v, want 25", usage.Total)
	}
}

// A second transient failure fails with the second attempt's message.
func TestATransientFailureTwiceFailsWithTheSecondMessage(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: agyErrorStdout("UNAVAILABLE (code 503): Deadline expired")},
		{ExitCode: 0, Stdout: agyErrorStdout("UNAVAILABLE (code 503): upstream connect error")},
	}
	req := e.request(t)
	req.HarnessOverride = "agy"

	got := runLeg(t, e, req)
	if got.Err == nil {
		t.Fatal("two transient failures did not fail the leg")
	}
	if !strings.Contains(got.Err.Error(), "upstream connect error") {
		t.Errorf("err = %q, want the second attempt's message", got.Err)
	}
	if e.runner.calls != 2 {
		t.Errorf("the harness was invoked %d time(s), want 2", e.runner.calls)
	}
}

// Authentication failures are not transient: no second call is spent on one.
func TestAnAuthenticationFailureIsNotRetried(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: agyErrorStdout("invalid api key")},
	}
	req := e.request(t)
	req.HarnessOverride = "agy"

	if got := runLeg(t, e, req); got.Err == nil {
		t.Fatal("an authentication failure did not fail the leg")
	}
	if e.runner.calls != 1 {
		t.Errorf("the harness was invoked %d time(s), want 1", e.runner.calls)
	}
}

// Quota stops wait out a window rather than retrying immediately.
func TestAQuotaFailureIsNotRetried(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: agyErrorStdout("RESOURCE_EXHAUSTED (code 429): quota exceeded")},
	}
	req := e.request(t)
	req.HarnessOverride = "agy"

	if got := runLeg(t, e, req); got.Err == nil {
		t.Fatal("a quota failure did not fail the leg")
	}
	if e.runner.calls != 1 {
		t.Errorf("the harness was invoked %d time(s), want 1", e.runner.calls)
	}
}
