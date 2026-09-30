package harness_test

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

func grokAdapter(t *testing.T) harness.Adapter {
	t.Helper()
	adapter, known := harness.For(descriptors(t), "grok")
	if !known {
		t.Fatal("the descriptor carries no grok adapter")
	}
	return adapter
}

// The permission and sandbox flags, against tests/test-adapters.sh:206-254.
func TestGrokPermissionsPerLeg(t *testing.T) {
	adapter := grokAdapter(t)

	reading, err := adapter.Spec(invocation(t, "grok", false))
	if err != nil {
		t.Fatalf("building the reading spec: %v", err)
	}
	if !hasFlagPair(reading.Args, "--sandbox", "read-only") {
		t.Error("a review leg is pinned read-only")
	}
	if !hasFlagPair(reading.Args, "--deny", "Edit") || !hasFlagPair(reading.Args, "--deny", "Write") {
		t.Error("a review leg denies Edit and Write")
	}
	if hasFlagPair(reading.Args, "--allow", "Edit") || hasFlagPair(reading.Args, "--allow", "Write") {
		t.Error("a review leg is granted no Edit or Write")
	}

	writing, err := adapter.Spec(invocation(t, "grok", true))
	if err != nil {
		t.Fatalf("building the writing spec: %v", err)
	}
	if !hasFlagPair(writing.Args, "--sandbox", "workspace") {
		t.Error("a resolve leg gets a workspace sandbox")
	}
	if !hasFlagPair(writing.Args, "--allow", "Edit") || !hasFlagPair(writing.Args, "--allow", "Write") {
		t.Error("a resolve leg may Edit and Write")
	}
	if hasFlagPair(writing.Args, "--sandbox", "read-only") {
		t.Error("a resolve leg is not pinned read-only")
	}

	// dontAsk on both legs: the headless default can prompt and hang.
	for _, spec := range []exec.Spec{reading, writing} {
		if !hasFlagPair(spec.Args, "--permission-mode", "dontAsk") {
			t.Error("both legs run dontAsk, not a promptable default")
		}
		joined := strings.Join(spec.Args, " ")
		for _, forbidden := range []string{"bypassPermissions", "--always-approve", "--yolo", "--dangerously"} {
			if strings.Contains(joined, forbidden) {
				t.Errorf("a blanket bypass reached the CLI: %s", forbidden)
			}
		}
	}
}

// `--prompt-file` takes a path and turns on headless mode. `-p` / `--print` /
// `--single` consume the next argv as the prompt, so a flag written after them
// is answered as the question. `--json-schema` takes an inline JSON string, not
// a path.
func TestGrokTakesThePromptAsAPathAndTheSchemaInline(t *testing.T) {
	inv := invocation(t, "grok", false)

	spec, err := grokAdapter(t).Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if !hasFlagPair(spec.Args, "--prompt-file", inv.Prompt.Path) {
		t.Errorf("the prompt is not passed as a file: %v", spec.Args)
	}
	if !hasFlagPair(spec.Args, "--json-schema", inv.Schema.Text) {
		t.Error("the schema is not passed inline")
	}
	if slices.Contains(spec.Args, inv.Schema.Path) {
		t.Error("handing --json-schema a path fails with a parse error about the leading slash")
	}
	for _, forbidden := range []string{"-p", "--print", "--single"} {
		if slices.Contains(spec.Args, forbidden) {
			t.Errorf("%s consumes the next argv as the prompt", forbidden)
		}
	}
	if !hasFlagPair(spec.Args, "--reasoning-effort", inv.Effort) {
		t.Error("the effort is not passed through")
	}
}

func TestGrokAgainstTheStub(t *testing.T) {
	adapter := grokAdapter(t)

	for _, write := range []bool{false, true} {
		name := "a review leg"
		variable := "CROSSREV_REVIEW_PAYLOAD"
		if write {
			name, variable = "a resolve leg", "CROSSREV_RESOLVE_PAYLOAD"
		}
		t.Run(name, func(t *testing.T) {
			inv := invocation(t, "grok", write)
			if !write {
				// The stub asserts the production shape: a supplied
				// review streams for its tripwire, and the legacy json
				// shape no longer runs anywhere.
				inv.ReadMode = harness.ReadModeSupplied
			}
			spec, err := adapter.Spec(inv)
			if err != nil {
				t.Fatalf("building the spec: %v", err)
			}
			res := runAgainstStub(t, spec, payloadFile(t, variable, cannedPayload))
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
			// modelUsage carries call counts rather than token totals, so there
			// is no share to rank and first is the only report.
			if got := deref(envelope.ModelReported); got != inv.Model {
				t.Errorf("model_reported = %q, want %q", got, inv.Model)
			}
			if got := deref(envelope.Tokens); got != 7 {
				t.Errorf("tokens = %d, want the stub's 3 in and 4 out", got)
			}
		})
	}
}

// An authentication rejection is classified as a credential failure naming
// Grok. That is the mitigation for a vendor silently switching archetype: the
// operator sees a credential that was consumed, not a harness that stopped
// working.
func TestGrokClassifiesACredentialRejection(t *testing.T) {
	adapter := grokAdapter(t)
	inv := invocation(t, "grok", false)

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	res := runAgainstStub(t, spec, "CROSSREV_GROK_UNAUTH=1")
	if res.ExitCode == 0 {
		t.Fatal("the stub was supposed to refuse the credential")
	}

	envelope := adapter.Envelope(inv, res)
	if envelope.OK {
		t.Fatal("a credential rejection is a failure")
	}
	message := deref(envelope.Error)
	if !strings.HasPrefix(message, "Grok rejected the credential. CrossRev classifies this as a credential failure, not a generic harness error. ") {
		t.Errorf("error = %q, and it does not name the credential", message)
	}
}

func TestGrokFailureEnvelope(t *testing.T) {
	adapter := grokAdapter(t)
	inv := invocation(t, "grok", false)

	tests := []struct {
		name string
		res  exec.Result
		want string
	}{
		{
			name: "the error field carries the diagnosis",
			res:  exec.Result{ExitCode: 1, Stdout: []byte(`{"error":"the model refused"}`)},
			want: "the model refused",
		},
		{
			name: "the text field is the second place looked",
			res:  exec.Result{ExitCode: 1, Stdout: []byte(`{"text":"nothing to do"}`)},
			want: "nothing to do",
		},
		{
			name: "neither stream said anything",
			res:  exec.Result{ExitCode: 4},
			want: "grok exited 4 with no output on either stream",
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

	// A zero exit is a success even with an error field set, which is the one
	// place this adapter differs from the agy one: grok has no status field to
	// check (lib/adapters/grok.sh:83).
	envelope := adapter.Envelope(inv, exec.Result{Stdout: []byte(`{"text":"prose","usage":{"input_tokens":1}}`)})
	if !envelope.OK {
		t.Error("a zero exit is a success for this adapter")
	}
}

// Live grok 1.0.5 with --json-schema puts the constrained object on
// structuredOutput. .text is the model's prose, and on a schema run it is often
// several draft JSON objects concatenated — which fromjson rejects.
func TestGrokPayloadLadder(t *testing.T) {
	adapter := grokAdapter(t)
	inv := invocation(t, "grok", false)

	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{name: "structuredOutput wins", stdout: `{"structuredOutput":{"a":1},"text":"{\"b\":2}"}`, want: `{"a":1}`},
		{name: "the snake_case sibling is next", stdout: `{"structured_output":{"a":1}}`, want: `{"a":1}`},
		{name: "a text string is parsed", stdout: `{"text":"{\"b\":2}"}`, want: `{"b":2}`},
		{name: "concatenated drafts are not a payload", stdout: `{"text":"{\"b\":2}{\"b\":2}"}`, want: ""},
		{name: "prose is not a payload", stdout: `{"text":"I could not do it"}`, want: ""},
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

// A resolve leg leaves --json-schema off so the model edits before it
// answers: under the flag a prompt carrying the full diff ends in one
// structured turn with zero tool calls, and the claimed fixes land nowhere.
func TestGrokResolveSpecOmitsJsonSchema(t *testing.T) {
	adapter := grokAdapter(t)
	inv := invocation(t, "grok", true)
	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building resolve spec: %v", err)
	}
	for _, arg := range spec.Args {
		if arg == "--json-schema" {
			t.Errorf("resolve spec must not pass --json-schema: got %v", spec.Args)
		}
	}
}

// A supplied review streams, and --json-schema implies --output-format
// json, so the flag cannot travel with it: the schema travels inside the
// prompt instead, and the prompt file the child reads is the composed copy.
func TestGrokSuppliedReviewCarriesTheSchemaInThePrompt(t *testing.T) {
	adapter := grokAdapter(t)
	inv := invocation(t, "grok", false)
	inv.ReadMode = harness.ReadModeSupplied

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the supplied review spec: %v", err)
	}
	if slices.Contains(spec.Args, "--json-schema") {
		t.Errorf("a streaming supplied review must not pass --json-schema: got %v", spec.Args)
	}
	var promptPath string
	for at := 0; at+1 < len(spec.Args); at++ {
		if spec.Args[at] == "--prompt-file" {
			promptPath = spec.Args[at+1]
		}
	}
	if promptPath == "" {
		t.Fatalf("no --prompt-file in %v", spec.Args)
	}
	if promptPath == inv.Prompt.Path {
		t.Error("the child reads the composed prompt, not the bare prompt file")
	}
	raw, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("reading the composed prompt: %v", err)
	}
	if !strings.Contains(string(raw), inv.Prompt.Text) {
		t.Error("the composed prompt lost the review prompt")
	}
	if !strings.Contains(string(raw), inv.Schema.Text) {
		t.Error("the composed prompt carries no schema")
	}
}

// The review leg keeps the flag: it reads without writing, so the structured
// turn is its whole job.
func TestGrokReviewSpecKeepsJsonSchema(t *testing.T) {
	adapter := grokAdapter(t)
	inv := invocation(t, "grok", false)
	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building review spec: %v", err)
	}
	if !hasFlagPair(spec.Args, "--json-schema", inv.Schema.Text) {
		t.Errorf("review spec must still constrain the answer: got %v", spec.Args)
	}
}

// Without the flag nothing constrains the answer to bare JSON, so the write
// leg's payload is read out of prose: a fenced block or a bare object
// surrounded by words. Prose with no object in it stays no payload.
func TestGrokWriteLegParsesPayloadFromProse(t *testing.T) {
	adapter := grokAdapter(t)
	inv := invocation(t, "grok", true)
	body := `{"blocked":false,"blocked_reason":null,"summary":"s","commit_subject":null,"resolutions":[]}`

	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "a fenced block", text: "Here is what I did:\n```json\n" + body + "\n```\nDone.", want: body},
		{name: "a bare object in prose", text: "Fixed it. " + body + " Let me know.", want: body},
		{name: "prose with no object", text: "I could not do it", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := `{"text":` + strconv.Quote(tt.text) + `}`
			envelope := adapter.Envelope(inv, exec.Result{Stdout: []byte(stdout)})
			if got := string(envelope.Payload); got != tt.want {
				t.Errorf("payload = %q, want %q", got, tt.want)
			}
		})
	}
}

// The resolve leg streams, so its answer arrives as text deltas beside the
// terminal end event. A fenced payload there is the same prose-tolerant read.
func TestGrokWriteLegParsesStreamedFencedPayload(t *testing.T) {
	adapter := grokAdapter(t)
	inv := invocation(t, "grok", true)
	body := `{"blocked":false,"blocked_reason":null,"summary":"s","commit_subject":null,"resolutions":[]}`
	stdout := "{\"type\":\"text\",\"data\":\"Here is what I did:\\n```json\\n\"}\n" +
		"{\"type\":\"text\",\"data\":" + strconv.Quote(body) + "}\n" +
		"{\"type\":\"text\",\"data\":\"\\n```\\n\"}\n" +
		`{"type":"end","usage":{"input_tokens":3,"output_tokens":4}}`
	envelope := adapter.Envelope(inv, exec.Result{Stdout: []byte(stdout)})
	if got := string(envelope.Payload); got != body {
		t.Errorf("payload = %q, want %q", got, body)
	}
}

// This adapter names the endpoint host out of the descriptor where the other
// three spell it (lib/adapters/grok.sh:28-31).
func TestGrokEndpointRefusalNamesTheDescriptorsHost(t *testing.T) {
	doc := descriptors(t)
	adapter, _ := harness.For(doc, "grok")
	inv := invocation(t, "grok", false)
	inv.Endpoint = harness.Endpoint{Name: "an-endpoint"}

	_, err := adapter.Spec(inv)
	var refusal *harness.Refusal
	if !asRefusal(err, &refusal) {
		t.Fatalf("err = %v, want a Refusal", err)
	}
	if !strings.Contains(refusal.Action, "reached through the "+doc.EndpointHost()+" adapter") {
		t.Errorf("Action = %q", refusal.Action)
	}
}
