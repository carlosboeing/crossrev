package harness_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

func claudeAdapter(t *testing.T) harness.Adapter {
	t.Helper()
	adapter, known := harness.For(descriptors(t), "claude")
	if !known {
		t.Fatal("the descriptor carries no claude adapter")
	}
	return adapter
}

// The flags, against tests/test-permissions.sh:148-153.
func TestClaudeGrantsTheWriteOnlyToAWritingLeg(t *testing.T) {
	adapter := claudeAdapter(t)

	writing, err := adapter.Spec(invocation(t, "claude", true))
	if err != nil {
		t.Fatalf("building the writing spec: %v", err)
	}
	if !hasFlagPair(writing.Args, "--permission-mode", "acceptEdits") {
		t.Errorf("a writing leg accepts edits; got %v", writing.Args)
	}

	reading, err := adapter.Spec(invocation(t, "claude", false))
	if err != nil {
		t.Fatalf("building the reading spec: %v", err)
	}
	// There is no permission mode meaning "deny": plan mode changes what the
	// model does rather than what it may touch. So a reading leg passes no mode
	// at all and the headless default denies the write.
	if slices.Contains(reading.Args, "--permission-mode") {
		t.Errorf("a reading leg asks for no permission mode at all; got %v", reading.Args)
	}
	if slices.Contains(reading.Args, "--ignore-user-config") {
		t.Error("--ignore-user-config is codex's flag, not this one's")
	}
}

func TestClaudeArgumentShape(t *testing.T) {
	adapter := claudeAdapter(t)
	inv := invocation(t, "claude", false)

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if got := spec.Args[:3]; !slices.Equal(got, []string{"-p", "--output-format", "json"}) {
		t.Errorf("the invocation does not open with -p --output-format json: %v", got)
	}
	// The prompt is the last argument, which is where the CLI takes it.
	if last := spec.Args[len(spec.Args)-1]; last != inv.Prompt.Text {
		t.Errorf("the last argument is not the prompt: %q", last)
	}
	if !hasFlagPair(spec.Args, "--model", inv.Model) {
		t.Error("the configured model is not passed through")
	}
	if !hasFlagPair(spec.Args, "--effort", inv.Effort) {
		t.Error("the configured effort is not passed through")
	}
	if !hasFlagPair(spec.Args, "--json-schema", inv.Schema.Text) {
		t.Error("Claude Code takes the schema inline; handing it a path fails with a JSON parse error about the leading slash")
	}
}

// A leg with no schema passes no schema flag, which is `[[ -n "$schema_file" ]]`.
func TestClaudeOmitsTheSchemaFlagWhenThereIsNoSchema(t *testing.T) {
	inv := invocation(t, "claude", false)
	inv.Schema = harness.File{}

	spec, err := claudeAdapter(t).Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if slices.Contains(spec.Args, "--json-schema") {
		t.Errorf("a leg with no schema asks for none; got %v", spec.Args)
	}
}

// A named endpoint is set on the invocation only. Never exported, never in a
// workflow env block: these variables are process-scoped, so a leg that leaks
// them silently redirects the OTHER leg too.
func TestClaudeSetsTheEndpointOnTheInvocationAlone(t *testing.T) {
	inv := invocation(t, "claude", false)
	inv.Endpoint = harness.Endpoint{
		Name: "an-endpoint", URL: "https://example.invalid", TokenVar: "AN_ENDPOINT_TOKEN", Token: "sekret",
	}

	spec, err := claudeAdapter(t).Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if !slices.Contains(spec.Env, "ANTHROPIC_BASE_URL=https://example.invalid") {
		t.Errorf("the base URL does not reach the child: %v", spec.Env)
	}
	if !slices.Contains(spec.Env, "ANTHROPIC_AUTH_TOKEN=sekret") {
		t.Error("the endpoint token does not reach the child")
	}
	for _, arg := range spec.Args {
		if strings.Contains(arg, "sekret") {
			t.Error("the endpoint token reached the argument list, where a process listing can read it")
		}
	}
}

// An endpoint whose token is unset is a refusal, never a fallback: crossrev
// will not silently move the leg onto the vendor's own API.
func TestClaudeRefusesAnEndpointWithNoToken(t *testing.T) {
	inv := invocation(t, "claude", false)
	inv.Endpoint = harness.Endpoint{Name: "an-endpoint", URL: "https://example.invalid", TokenVar: "AN_ENDPOINT_TOKEN"}

	_, err := claudeAdapter(t).Spec(inv)
	if err == nil {
		t.Fatal("the adapter accepted an endpoint with no token")
	}
	if !errorIs(err, harness.ErrEndpointToken) {
		t.Fatalf("err = %v, want ErrEndpointToken", err)
	}
	var refusal *harness.Refusal
	if !asRefusal(err, &refusal) {
		t.Fatal("the error is not a Refusal")
	}
	if want := "the endpoint 'an-endpoint' needs $AN_ENDPOINT_TOKEN, which is unset"; refusal.Reason != want {
		t.Errorf("Reason = %q, want %q", refusal.Reason, want)
	}
	if !strings.Contains(refusal.Action, "will not fall back to the vendor's own API") {
		t.Errorf("Action does not say it will not fall back: %q", refusal.Action)
	}
}

// The whole invocation, against the fake CLI the offline suite uses.
func TestClaudeAgainstTheStub(t *testing.T) {
	adapter := claudeAdapter(t)
	inv := invocation(t, "claude", false)

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
	if envelope.Harness != "claude" || deref(envelope.Endpoint) != "vendor" {
		t.Errorf("harness = %q, endpoint = %q", envelope.Harness, deref(envelope.Endpoint))
	}
	// The stub answers as the model that was asked for, which is what the real
	// CLI reports when nothing substitutes it.
	if got := deref(envelope.ModelReported); got != inv.Model {
		t.Errorf("model_reported = %q, want %q", got, inv.Model)
	}
	if envelope.Usage == nil {
		t.Fatal("no usage record")
	}
	// 1200 fresh + 40000 cache reads + 5 unsplit writes + 300 output. The
	// split the stub reports is zero on both TTLs, so the write lands unsplit.
	want := harness.Usage{
		InputFresh: 1200, CacheRead: 40000, CacheWriteUnsplit: 5, Output: 300,
	}
	if envelope.Usage.InputFresh != want.InputFresh || envelope.Usage.CacheRead != want.CacheRead ||
		envelope.Usage.CacheWriteUnsplit != want.CacheWriteUnsplit || envelope.Usage.Output != want.Output {
		encoded, _ := json.Marshal(envelope.Usage)
		t.Errorf("usage = %s", encoded)
	}
	if got := deref(envelope.Tokens); got != 41505 {
		t.Errorf("tokens = %d, want 41505", got)
	}
	if got := deref(envelope.Usage.CostUSD); got != 0.12 {
		t.Errorf("cost_usd = %v, want the harness's own 0.12", got)
	}
	if got := deref(envelope.Usage.CostSource); got != "harness" {
		t.Errorf("cost_source = %q, want harness", got)
	}
}

// A failing run answers an envelope rather than dying, and takes its message
// from the payload when there is one.
func TestClaudeFailureEnvelope(t *testing.T) {
	adapter := claudeAdapter(t)
	inv := invocation(t, "claude", false)

	tests := []struct {
		name string
		res  exec.Result
		want string
	}{
		{
			name: "the result field carries the diagnosis",
			res:  exec.Result{ExitCode: 1, Stdout: []byte(`{"is_error":true,"result":"the model refused"}`)},
			want: "the model refused",
		},
		{
			// On an EMPTY stdout jq exits 0 with no output, so a fallback
			// written as `jq … || head "$err"` never fires and the error
			// becomes the empty string — exactly when stderr holds the only
			// diagnosis.
			name: "an empty stdout falls through to stderr",
			res:  exec.Result{ExitCode: 1, Stderr: []byte("banner\nError: unauthorized\n")},
			want: "Error: unauthorized",
		},
		{
			name: "neither stream said anything",
			res:  exec.Result{ExitCode: 7},
			want: "claude exited 7 with no output on either stream",
		},
		{
			// is_error is the second half of the test: a zero exit with the
			// flag set is still a failure.
			name: "a zero exit with is_error set",
			res:  exec.Result{Stdout: []byte(`{"is_error":true,"result":"quota exhausted"}`)},
			want: "quota exhausted",
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
			if envelope.Payload != nil || envelope.Endpoint != nil || envelope.Usage != nil || envelope.Tokens != nil {
				t.Error("a failure envelope carries no payload, endpoint or telemetry")
			}
		})
	}
}

// hasFlagPair reports a flag immediately followed by its value.
func hasFlagPair(args []string, flag, value string) bool {
	for at := 0; at+1 < len(args); at++ {
		if args[at] == flag && args[at+1] == value {
			return true
		}
	}
	return false
}
