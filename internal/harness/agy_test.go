package harness_test

import (
	"slices"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

func agyAdapter(t *testing.T) harness.Adapter {
	t.Helper()
	adapter, known := harness.For(descriptors(t), "agy")
	if !known {
		t.Fatal("the descriptor carries no agy adapter")
	}
	return adapter
}

// The mode flag, against tests/test-permissions.sh:177-178.
func TestAgyGrantsTheWriteOnlyToAWritingLeg(t *testing.T) {
	adapter := agyAdapter(t)

	writing, err := adapter.Spec(invocation(t, "agy", true))
	if err != nil {
		t.Fatalf("building the writing spec: %v", err)
	}
	if !hasFlagPair(writing.Args, "--mode", "accept-edits") {
		t.Errorf("a writing leg accepts edits; got %v", writing.Args)
	}

	reading, err := adapter.Spec(invocation(t, "agy", false))
	if err != nil {
		t.Fatalf("building the reading spec: %v", err)
	}
	if slices.Contains(reading.Args, "--mode") {
		t.Errorf("a reading leg asks for no mode at all; got %v", reading.Args)
	}
	if slices.Contains(reading.Args, "--ignore-user-config") {
		t.Error("--ignore-user-config is codex's flag, not this one's")
	}
}

// `--print` takes the prompt as its VALUE, so every other flag has to come
// before it. Written the other way round the CLI answers a question about the
// flag it was handed, in prose, and burns a real call doing it.
func TestAgyPutsEveryFlagBeforePrint(t *testing.T) {
	inv := invocation(t, "agy", true)

	spec, err := agyAdapter(t).Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	at := slices.Index(spec.Args, "--print")
	if at < 0 {
		t.Fatalf("the invocation carries no --print: %v", spec.Args)
	}
	if at != len(spec.Args)-2 {
		t.Errorf("--print is not the last flag: %v", spec.Args)
	}
	if spec.Args[at+1] != inv.Prompt.Text {
		t.Errorf("--print does not take the prompt as its value: %q", spec.Args[at+1])
	}
	for _, flag := range []string{"--output-format", "--disable-slash-commands", "--add-dir", "--mode", "--json-schema", "--model", "--effort"} {
		found := slices.Index(spec.Args, flag)
		if found < 0 {
			t.Errorf("the invocation carries no %s", flag)
			continue
		}
		if found > at {
			t.Errorf("%s comes after --print, so the CLI would read it as the prompt", flag)
		}
	}
}

// Antigravity does not take the shell's working directory as its workspace. It
// keeps its own project root, and without one it resolves against $HOME.
func TestAgyNamesTheCheckoutAsTheWorkspace(t *testing.T) {
	inv := invocation(t, "agy", false)

	spec, err := agyAdapter(t).Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if !hasFlagPair(spec.Args, "--add-dir", inv.Workdir) {
		t.Errorf("the checkout is not named as the workspace: %v", spec.Args)
	}
	// Unlike Claude Code, this one takes the schema as a PATH.
	if !hasFlagPair(spec.Args, "--json-schema", inv.Schema.Path) {
		t.Error("the schema is not passed as a path")
	}
}

func TestAgyAgainstTheStub(t *testing.T) {
	adapter := agyAdapter(t)
	inv := invocation(t, "agy", false)

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	res := runAgainstStub(t, spec, payloadFile(t, "CROSSREV_REVIEW_PAYLOAD", cannedPayload))
	if res.Err != nil {
		t.Fatalf("running the stub: %v", res.Err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("the stub refused the invocation: exit %d, stderr %s", res.ExitCode, res.Stderr)
	}

	envelope := adapter.Envelope(inv, res)
	if !envelope.OK {
		t.Fatalf("the envelope reports a failure: %+v", envelope)
	}
	if got := string(envelope.Payload); got != cannedPayload {
		t.Errorf("payload = %s, want %s", got, cannedPayload)
	}
	// Antigravity reports no answering model — the JSON carries a conversation
	// id, a status, usage counts and nothing identifying what served the turn.
	if envelope.ModelReported != nil {
		t.Errorf("model_reported = %q, and this harness reports none", deref(envelope.ModelReported))
	}
	if envelope.Usage == nil || deref(envelope.Tokens) != 2 {
		t.Errorf("tokens = %d, want the stub's 1 in and 1 out", deref(envelope.Tokens))
	}
}

// A run whose status is not SUCCESS is a failure even on a zero exit, and the
// message is chosen on whether one is actually there.
func TestAgyFailureEnvelope(t *testing.T) {
	adapter := agyAdapter(t)
	inv := invocation(t, "agy", false)

	tests := []struct {
		name string
		res  exec.Result
		want string
	}{
		{
			name: "a non-SUCCESS status on a zero exit",
			res:  exec.Result{Stdout: []byte(`{"status":"ERROR","error":"the model refused"}`)},
			want: "the model refused",
		},
		{
			name: "the response field is the second place looked",
			res:  exec.Result{Stdout: []byte(`{"status":"ERROR","response":"nothing to do"}`)},
			want: "nothing to do",
		},
		{
			name: "an empty stdout falls through to stderr",
			res:  exec.Result{ExitCode: 1, Stderr: []byte("banner\ninvalid credential\n")},
			want: "invalid credential",
		},
		{
			name: "neither stream said anything",
			res:  exec.Result{ExitCode: 9},
			want: "agy exited 9 with no output on either stream",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelope := adapter.Envelope(inv, tt.res)
			if envelope.OK {
				t.Fatal("the envelope reports success")
			}
			if got := deref(envelope.Error); got != tt.want {
				t.Errorf("error = %q, want %q", got, tt.want)
			}
		})
	}
}

// structured_output is the parsed object when a schema was given. The response
// string is the same JSON, and parsing it is the fallback for a run with no
// schema rather than a second-guess of the first.
func TestAgyPayloadPrefersStructuredOutput(t *testing.T) {
	adapter := agyAdapter(t)
	inv := invocation(t, "agy", false)

	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{
			name:   "structured output wins",
			stdout: `{"status":"SUCCESS","structured_output":{"a":1},"response":"{\"b\":2}"}`,
			want:   `{"a":1}`,
		},
		{
			name:   "the response string is the fallback",
			stdout: `{"status":"SUCCESS","response":"{\"b\":2}"}`,
			want:   `{"b":2}`,
		},
		{
			name:   "prose in the response is no payload",
			stdout: `{"status":"SUCCESS","response":"I could not do it"}`,
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelope := adapter.Envelope(inv, exec.Result{Stdout: []byte(tt.stdout)})
			if got := string(envelope.Payload); got != tt.want {
				t.Errorf("payload = %q, want %q", got, tt.want)
			}
		})
	}
}
