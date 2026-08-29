package harness_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

func codexAdapter(t *testing.T) harness.Adapter {
	t.Helper()
	adapter, known := harness.For(descriptors(t), "codex")
	if !known {
		t.Fatal("the descriptor carries no codex adapter")
	}
	return adapter
}

// The sandbox flag, against tests/test-permissions.sh:155-162.
func TestCodexSandboxIsNamedOnBothLegs(t *testing.T) {
	adapter := codexAdapter(t)

	writing, err := adapter.Spec(invocation(t, "codex", true))
	if err != nil {
		t.Fatalf("building the writing spec: %v", err)
	}
	if !hasFlagPair(writing.Args, "--sandbox", "workspace-write") {
		t.Errorf("a writing leg gets a workspace-writable sandbox; got %v", writing.Args)
	}

	reading, err := adapter.Spec(invocation(t, "codex", false))
	if err != nil {
		t.Fatalf("building the reading spec: %v", err)
	}
	// Explicit rather than left to the default, because codex reads a user
	// config that can set one. Saying it costs nothing and means a
	// machine-level setting cannot quietly hand the review leg a writable tree.
	if !hasFlagPair(reading.Args, "--sandbox", "read-only") {
		t.Errorf("a reading leg is pinned read-only; got %v", reading.Args)
	}

	for _, spec := range []exec.Spec{writing, reading} {
		if !slices.Contains(spec.Args, "--ignore-user-config") {
			t.Error("both legs ignore the user config")
		}
	}
}

func TestCodexArgumentShape(t *testing.T) {
	adapter := codexAdapter(t)
	inv := invocation(t, "codex", false)

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if got := spec.Args[:3]; !slices.Equal(got, []string{"exec", "--skip-git-repo-check", "--json"}) {
		t.Errorf("the invocation does not open with exec --skip-git-repo-check --json: %v", got)
	}
	// `--json` streams events on stdout while `-o` still writes the final
	// payload to a file, so the token counts arrive without changing where the
	// payload comes from.
	if !hasFlagPair(spec.Args, "-o", inv.PayloadPath) {
		t.Error("the payload file is not named")
	}
	// Codex takes the schema as a FILE PATH, where Claude Code takes it inline.
	if !hasFlagPair(spec.Args, "--output-schema", inv.Schema.Path) {
		t.Error("the schema is not passed as a path")
	}
	if !hasFlagPair(spec.Args, "-m", inv.Model) {
		t.Error("the configured model is not passed through")
	}
	// No --effort flag; it is a config override.
	if !hasFlagPair(spec.Args, "-c", "model_reasoning_effort="+inv.Effort) {
		t.Error("the effort is not passed as a config override")
	}
	if last := spec.Args[len(spec.Args)-1]; last != inv.Prompt.Text {
		t.Errorf("the last argument is not the prompt: %q", last)
	}
}

// The hardening arguments come from the descriptor, and a leg that cannot
// resolve them halts rather than running unhardened
// (tests/test-permissions.sh:166-175).
func TestCodexRefusesToRunUnhardened(t *testing.T) {
	// A descriptor with codex's sandbox_args emptied is the only way to reach
	// this, because the shipped one carries the flag.
	stripped := mutate(t, func(document map[string]any) {
		for _, entry := range document["harnesses"].([]any) {
			harnessEntry := entry.(map[string]any)
			if harnessEntry["name"] == "codex" {
				harnessEntry["sandbox_args"] = []any{}
			}
		}
	})
	doc, err := harness.Load(stripped)
	if err != nil {
		t.Fatalf("loading the stripped descriptor: %v", err)
	}
	adapter, _ := harness.For(doc, "codex")

	_, err = adapter.Spec(invocation(t, "codex", false))
	if err == nil {
		t.Fatal("the adapter built a spec with no hardening arguments")
	}
	if !errorIs(err, harness.ErrHardening) {
		t.Fatalf("err = %v, want ErrHardening", err)
	}
	var refusal *harness.Refusal
	if !asRefusal(err, &refusal) {
		t.Fatal("the error is not a Refusal")
	}
	if refusal.Reason != "could not resolve hardening arguments for codex" {
		t.Errorf("Reason = %q", refusal.Reason)
	}
	if !strings.Contains(refusal.Action, "Refusing to run codex unhardened.") {
		t.Errorf("Action = %q", refusal.Action)
	}
}

// tests/stub/codex is a deliberate tripwire rather than a stub, and this proves
// it is still one.
//
// The no-config default names codex as the reviewer, and codex is installed on
// the machine this suite is developed on — so a test whose fixture config failed
// to load would silently reach the real CLI, make a real billed call, and pass
// or fail for reasons nothing to do with crossrev. That happened once. The
// tripwire makes it impossible rather than unlikely, and a Go adapter that made
// it pass would take the guard away from the whole suite.
func TestCodexStubStaysATripwire(t *testing.T) {
	adapter := codexAdapter(t)
	inv := invocation(t, "codex", false)

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	res := runAgainstStub(t, spec)
	if res.Err != nil {
		t.Fatalf("running the tripwire: %v", res.Err)
	}
	if res.ExitCode != 97 {
		t.Fatalf("exit %d, want 97: the tripwire must keep refusing", res.ExitCode)
	}
	if !strings.Contains(string(res.Stderr), "the real codex CLI must never be reached") {
		t.Errorf("stderr does not carry the tripwire's own message: %s", res.Stderr)
	}

	// And the adapter reports it the way it reports any other failing run.
	envelope := adapter.Envelope(inv, res)
	if envelope.OK {
		t.Fatal("a tripwire exit is a failure")
	}
	if !strings.Contains(deref(envelope.Error), "the real codex CLI must never be reached") {
		t.Errorf("error = %q, want the tripwire's message", deref(envelope.Error))
	}
}

// The payload comes from the file `-o` named, and the token counts come from
// the event stream.
func TestCodexEnvelopeReadsThePayloadFileAndTheEventStream(t *testing.T) {
	adapter := codexAdapter(t)
	inv := invocation(t, "codex", false)

	if err := os.WriteFile(inv.PayloadPath, []byte(cannedPayload+"\n"), 0o600); err != nil {
		t.Fatalf("writing the payload file: %v", err)
	}
	events := strings.Join([]string{
		`{"type":"thread.started","thread_id":"sess-1"}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7,"reasoning_output_tokens":3}}`,
	}, "\n")

	envelope := adapter.Envelope(inv, exec.Result{Stdout: []byte(events)})
	if !envelope.OK {
		t.Fatalf("the envelope reports a failure: %+v", envelope)
	}
	if got := string(envelope.Payload); got != cannedPayload {
		t.Errorf("payload = %s, want %s", got, cannedPayload)
	}
	if deref(envelope.Endpoint) != "vendor" {
		t.Errorf("endpoint = %q", deref(envelope.Endpoint))
	}
	// `cached_input_tokens` is the cached subset of `input_tokens`, so fresh
	// input is the subtraction and the record's one derived field.
	if envelope.Usage.InputFresh != 60 || envelope.Usage.CacheRead != 40 || envelope.Usage.Output != 7 {
		t.Errorf("usage = %+v", envelope.Usage)
	}
	if !slices.Equal(envelope.Usage.Derived, []string{"input_fresh"}) {
		t.Errorf("derived = %v", envelope.Usage.Derived)
	}
	if got := deref(envelope.Tokens); got != 107 {
		t.Errorf("tokens = %d, want 107", got)
	}
	// The event stream names no model, and there is no rollout under the
	// CodexHome this invocation points at, so the report is a miss rather than
	// a wrong name.
	if envelope.ModelReported != nil || envelope.EffortReported != nil {
		t.Errorf("model_reported = %v, effort_reported = %v; a miss is null",
			envelope.ModelReported, envelope.EffortReported)
	}
}

// An empty payload file is a failure even on a zero exit, and the message comes
// from stderr alone — this adapter has no "exited N with no output" fallback.
func TestCodexFailsOnAnEmptyPayloadFile(t *testing.T) {
	adapter := codexAdapter(t)
	inv := invocation(t, "codex", false)

	envelope := adapter.Envelope(inv, exec.Result{Stderr: []byte("banner\nfatal: the model refused\n")})
	if envelope.OK {
		t.Fatal("a run that wrote no payload is a failure")
	}
	if got := deref(envelope.Error); got != "fatal: the model refused" {
		t.Errorf("error = %q", got)
	}

	silent := adapter.Envelope(inv, exec.Result{ExitCode: 3})
	if silent.OK {
		t.Fatal("a non-zero exit is a failure")
	}
	if got := deref(silent.Error); got != "" {
		t.Errorf("error = %q, want the empty string the Bash answers from an empty stderr", got)
	}
}

// Nothing to write the payload into is a refusal rather than a temporary file
// nobody named.
func TestCodexRefusesWithNoPayloadPath(t *testing.T) {
	inv := invocation(t, "codex", false)
	inv.PayloadPath = ""

	if _, err := codexAdapter(t).Spec(inv); err == nil {
		t.Fatal("the adapter accepted an invocation with nowhere to write the payload")
	}
}
