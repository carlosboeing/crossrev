package resolve

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// The resolve leg's retries say what they are doing, and say the discarded
// attempt's edits were put back (lib/run.sh:882-883 and :894-895).
//
// A resolver edits the working tree, so a silent retry left the operator with
// no account of a pass that cost two invocations and rewrote the checkout twice.
func TestAResolveSemanticRetryWarnsOnceAndAsksAgain(t *testing.T) {
	e := setup(t)
	e.git.staged = true
	e.addReview(t, defaultFindings(), "issues-remain")
	// The first answer numbers a finding that was never numbered, which is
	// exit 2 in lib/validate.sh: the shape is right and the content is not.
	e.adapter.payloads = []json.RawMessage{
		json.RawMessage(`{"blocked":false,"blocked_reason":null,"summary":"Drifted.","commit_subject":null,"resolutions":[{"finding_number":9,"resolution":"fixed","reply":"done","persist":null,"duplicate_of":null}]}`),
		oneFindingPayload(),
	}

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	warned := warningContaining(got.Messages, "contradicts what it was given")
	if warned.Text == "" {
		t.Fatalf("the semantic retry was silent: %q", ui.Texts(got.Messages))
	}
	if warned.Kind != ui.KindWarn {
		t.Errorf("kind = %v, want KindWarn: lib/run.sh:882 is ui_warn", warned.Kind)
	}
	for _, want := range []string{"asked once more", "put back"} {
		if !strings.Contains(warned.Action, want) {
			t.Errorf("consequence = %q, want it to say %q", warned.Action, want)
		}
	}
}

// A capture that will not go back stops the leg rather than asking again on top
// of the discarded attempt's edits (_run_retry_reset, lib/run.sh:680-686).
func TestAResolveRetryRefusesWhenTheTreeCannotBePutBack(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.git.restoreTreeErr = errors.New("read-tree refused")
	e.adapter.payloads = []json.RawMessage{
		json.RawMessage(`{"blocked":false,"blocked_reason":null,"summary":"Drifted.","commit_subject":null,"resolutions":[{"finding_number":9,"resolution":"fixed","reply":"done","persist":null,"duplicate_of":null}]}`),
		oneFindingPayload(),
	}

	got := e.run(t)
	if got.Err == nil {
		t.Fatal("a retry over an unrestorable tree ran anyway")
	}
	if !strings.Contains(got.Err.Error(), "could not be put back") {
		t.Fatalf("refusal = %q, want the reset one", got.Err)
	}
	if e.adapter.calls != 1 {
		t.Errorf("the harness was asked %d time(s); a retry must not run over discarded edits", e.adapter.calls)
	}
}

// An exhausted budget restores too, and warns when it cannot
// (_run_invoke_abort, lib/run.sh:698-704).
func TestAnExhaustedResolveBudgetWarnsWhenTheTreeStays(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.git.restoreTreeErr = errors.New("read-tree refused")
	e.adapter.payloads = []json.RawMessage{json.RawMessage(`{"not":"a resolve"}`)}

	got := e.run(t)
	if got.Err == nil {
		t.Fatal("a rejected shape did not fail the leg")
	}
	warned := warningContaining(got.Messages, "could not be put back")
	if warned.Text == "" {
		t.Fatalf("the abort was silent: %q", ui.Texts(got.Messages))
	}
	if !strings.Contains(warned.Action, "still in the checkout") {
		t.Errorf("consequence = %q", warned.Action)
	}
}

func warningContaining(lines []ui.Line, text string) ui.Line {
	for _, line := range lines {
		if strings.Contains(line.Text, text) {
			return line
		}
	}
	return ui.Line{}
}
