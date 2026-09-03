package review_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// A harness that does not constrain its own output gets one more attempt, and
// the run says so (lib/run.sh:897-902):
//
//	ui_warn "$harness returned an object that does not match the schema — $problem" \
//	  "That harness does not constrain its own output, so this is the expected
//	   failure rather than a bug. Anything it edited has been put back, and it
//	   is being retried once; a second mismatch is fatal."
//
// The port retried in silence, so a pass that cost two invocations read exactly
// like one that cost one.
func TestAShapeRetryWarnsOnceAndAsksAgain(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.doc = docNotSchemaNative(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(`{"not":"a review"}`)},
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 2 {
		t.Fatalf("the harness was invoked %d time(s), want 2", e.runner.calls)
	}
	var warned ui.Line
	for _, line := range got.Messages {
		if strings.Contains(line.Text, "does not match the schema") {
			warned = line
		}
	}
	if warned.Text == "" {
		t.Fatalf("the retry was silent; the leg said %q", ui.Texts(got.Messages))
	}
	if warned.Kind != ui.KindWarn {
		t.Errorf("kind = %v, want KindWarn: lib/run.sh:900 is ui_warn", warned.Kind)
	}
	if !strings.Contains(warned.Action, "it is being retried once") {
		t.Errorf("consequence = %q, want the retry sentence", warned.Action)
	}
}

// A second mismatch from a harness that does not constrain its output names the
// model failing the JSON instruction, never a native schema check
// (lib/run.sh:906-911).
func TestASecondShapeMismatchNamesTheJSONInstruction(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.doc = docNotSchemaNative(t)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(`{"not":"a review"}`)}}

	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("two mismatches in a row did not fail the leg")
	}
	_, action := refusalPair(t, got.Err)
	if !strings.Contains(action, "JSON instruction") {
		t.Errorf("action = %q, want it to name the JSON instruction", action)
	}
	if strings.Contains(action, "validates output against the schema natively") {
		t.Errorf("action blames a native schema check the harness does not have: %q", action)
	}
	if e.runner.calls != 2 {
		t.Errorf("the harness was invoked %d time(s), want 2", e.runner.calls)
	}
}

// A harness that DOES validate natively gets no shape retry, and the refusal
// says the mismatch is an adapter or harness bug (lib/run.sh:905-907).
func TestASchemaNativeHarnessGetsNoShapeRetry(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(`{"not":"a review"}`)}}

	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("a mismatch did not fail the leg")
	}
	if e.runner.calls != 1 {
		t.Errorf("the harness was invoked %d time(s), want 1", e.runner.calls)
	}
	_, action := refusalPair(t, got.Err)
	if !strings.Contains(action, "validates output against the schema natively") {
		t.Errorf("action = %q, want the native-schema wording", action)
	}
}

// Semantic drift earns one more attempt, and the run says why
// (lib/run.sh:886-890).
func TestASemanticRetryWarnsOnceAndAsksAgain(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	calls := 0
	e.validate = func([]byte) error {
		calls++
		if calls == 1 {
			return semanticProblem("finding 3 was answered twice")
		}
		return nil
	}
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 2 {
		t.Fatalf("the harness was invoked %d time(s), want 2", e.runner.calls)
	}
	var warned ui.Line
	for _, line := range got.Messages {
		if strings.Contains(line.Text, "contradicts what it was given") {
			warned = line
		}
	}
	if warned.Text == "" {
		t.Fatalf("the semantic retry was silent; the leg said %q", ui.Texts(got.Messages))
	}
	if !strings.Contains(warned.Action, "asked once more") {
		t.Errorf("consequence = %q, want the drift sentence", warned.Action)
	}
}

// docNotSchemaNative is the descriptor with claude declared as a harness that
// does not constrain its own output, which is what opencode is
// (lib/harnesses.json, `"schema_native": false`).
func docNotSchemaNative(t *testing.T) harness.Document {
	t.Helper()
	raw := bytes.ReplaceAll(harness.DescriptorJSON(),
		[]byte(`"schema_native": true`), []byte(`"schema_native": false`))
	doc, err := harness.Load(raw)
	if err != nil {
		t.Fatalf("harness.Load: %v", err)
	}
	return doc
}

func semanticProblem(text string) error { return &validate.SemanticError{Problem: text} }

func refusalPair(t *testing.T, err error) (reason, action string) {
	t.Helper()
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("err = %#v, want a *ui.FatalError", err)
	}
	return fatal.Reason, fatal.Action
}
