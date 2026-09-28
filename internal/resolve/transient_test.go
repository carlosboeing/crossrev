package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// failedResolveEnvelope is a harness that errored instead of answering.
func failedResolveEnvelope(message string) harness.Envelope {
	return harness.Envelope{Harness: "claude", Error: &message}
}

// okResolveEnvelope is a harness that answered, carrying usage buckets for
// the attempts the retry sums.
func okResolveEnvelope(payload string, in, out int64) harness.Envelope {
	return harness.Envelope{
		OK:       true,
		Payload:  []byte(payload),
		Harness:  "claude",
		Usage:    &harness.Usage{InputFresh: in, Output: out},
	}
}

func resolveRunLog(t *testing.T, e *testEnv) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(e.log.Dir(), "run.log"))
	if err != nil {
		t.Fatalf("read run.log: %v", err)
	}
	return string(body)
}

// A transient harness failure is asked once more, with the discarded
// attempt's edits put back the way the refused-attempt path does them.
func TestATransientHarnessFailureRetriesOnce(t *testing.T) {
	e := setup(t)
	e.git.staged = true
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.envelopes = []harness.Envelope{
		failedResolveEnvelope("UNAVAILABLE (code 503): Deadline expired"),
		okResolveEnvelope(string(oneFindingPayload()), 7, 3),
	}

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.adapter.calls != 2 {
		t.Fatalf("the harness was asked %d time(s), want 2", e.adapter.calls)
	}
	warned := warningContaining(got.Messages, "transient")
	if warned.Text == "" {
		t.Fatalf("the transient retry was silent: %q", ui.Texts(got.Messages))
	}
	if warned.Kind != ui.KindWarn {
		t.Errorf("kind = %v, want KindWarn", warned.Kind)
	}
	for _, want := range []string{"asked once more", "put back"} {
		if !strings.Contains(warned.Action, want) {
			t.Errorf("consequence = %q, want it to say %q", warned.Action, want)
		}
	}
	if *e.git.restoreCalls < 1 {
		t.Errorf("the tree was put back %d time(s); the retry must restore between attempts", *e.git.restoreCalls)
	}
	if log := resolveRunLog(t, e); !strings.Contains(log, "attempt=2") {
		t.Errorf("the run log records no second attempt:\n%s", log)
	}
}

// A successful but empty answer carries nothing to resolve: it is retried
// rather than recorded as an empty resolution.
func TestAnEmptyResolveAnswerRetriesOnce(t *testing.T) {
	e := setup(t)
	e.git.staged = true
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.envelopes = []harness.Envelope{
		okResolveEnvelope("", 10, 5),
		okResolveEnvelope(string(oneFindingPayload()), 7, 3),
	}

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.adapter.calls != 2 {
		t.Fatalf("the harness was asked %d time(s), want 2", e.adapter.calls)
	}
	if warned := warningContaining(got.Messages, "empty"); warned.Text == "" {
		t.Errorf("the empty-answer retry was silent: %q", ui.Texts(got.Messages))
	}
	if *e.git.restoreCalls < 1 {
		t.Errorf("the tree was put back %d time(s); the retry must restore between attempts", *e.git.restoreCalls)
	}
}

// Both attempts' usage is spent, so the accepted envelope sums both.
func TestATransientResolveRetrySumsBothAttemptsUsage(t *testing.T) {
	e := setup(t)
	e.git.staged = true
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.envelopes = []harness.Envelope{
		okResolveEnvelope("", 10, 5),
		okResolveEnvelope(string(oneFindingPayload()), 7, 3),
	}

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Envelope.Usage == nil {
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
func TestATransientResolveFailureTwiceFailsWithTheSecondMessage(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.envelopes = []harness.Envelope{
		failedResolveEnvelope("UNAVAILABLE (code 503): Deadline expired"),
		failedResolveEnvelope("The stream was interrupted"),
	}

	got := e.run(t)
	if got.Err == nil {
		t.Fatal("two transient failures did not fail the leg")
	}
	if !strings.Contains(got.Err.Error(), "The stream was interrupted") {
		t.Errorf("err = %q, want the second attempt's message", got.Err)
	}
	if e.adapter.calls != 2 {
		t.Errorf("the harness was asked %d time(s), want 2", e.adapter.calls)
	}
}

// Authentication failures are not transient: no second call is spent on one.
func TestAnAuthenticationResolveFailureIsNotRetried(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.envelopes = []harness.Envelope{
		failedResolveEnvelope("invalid api key"),
		okResolveEnvelope(string(oneFindingPayload()), 7, 3),
	}

	if got := e.run(t); got.Err == nil {
		t.Fatal("an authentication failure did not fail the leg")
	}
	if e.adapter.calls != 1 {
		t.Errorf("the harness was asked %d time(s), want 1", e.adapter.calls)
	}
}
