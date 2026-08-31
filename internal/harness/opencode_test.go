package harness_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

func opencodeAdapter(t *testing.T) *harness.Opencode {
	t.Helper()
	adapter, known := harness.For(descriptors(t), "opencode")
	if !known {
		t.Fatal("the descriptor carries no opencode adapter")
	}
	concrete, ok := adapter.(*harness.Opencode)
	if !ok {
		t.Fatalf("the opencode adapter is a %T", adapter)
	}
	return concrete
}

// isolation reads the config the adapter wrote for one leg.
func isolation(t *testing.T, spec exec.Spec) map[string]any {
	t.Helper()
	for _, entry := range spec.Env {
		variable, value, _ := strings.Cut(entry, "=")
		if variable != "OPENCODE_CONFIG" {
			continue
		}
		raw, err := os.ReadFile(value) //nolint:gosec // the adapter wrote this path
		if err != nil {
			t.Fatalf("reading the isolation config: %v", err)
		}
		var config map[string]any
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatalf("decoding the isolation config: %v", err)
		}
		permission, ok := config["permission"].(map[string]any)
		if !ok {
			t.Fatal("the isolation config carries no permission block")
		}
		return permission
	}
	t.Fatal("the spec names no OPENCODE_CONFIG")
	return nil
}

// The permission block, against tests/test-permissions.sh:183-192.
//
// This harness grants `edit` and `bash` out of the box, so the isolation config
// IS the security story, and which shape a leg gets is the write-grant question.
func TestOpencodeIsolationIsFailClosedInBothShapes(t *testing.T) {
	adapter := opencodeAdapter(t)

	for _, tt := range []struct {
		name  string
		write bool
		edit  string
	}{
		{name: "a reading leg denies edit", write: false, edit: "deny"},
		{name: "a writing leg grants edit", write: true, edit: "allow"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := adapter.Spec(invocation(t, "opencode", tt.write))
			if err != nil {
				t.Fatalf("building the spec: %v", err)
			}
			permission := isolation(t, spec)

			// The base rule is what holds the surface no key names: a tool a
			// future opencode adds, a custom tool, and whatever an MCP server
			// or plugin registers all match "*" and are denied.
			if permission["*"] != "deny" {
				t.Errorf(`the fail-closed base rule is missing: %v`, permission["*"])
			}
			if permission["edit"] != tt.edit {
				t.Errorf("edit = %v, want %q", permission["edit"], tt.edit)
			}
			for _, key := range []string{"bash", "task", "skill", "webfetch", "websearch", "external_directory", "question", "doom_loop"} {
				if permission[key] != "deny" {
					t.Errorf("%s = %v, want deny in every shape", key, permission[key])
				}
			}
			// read stays a map, not the string "allow": a string would replace
			// opencode's own *.env deny and let the model quote an untracked
			// .env into a public comment.
			read, ok := permission["read"].(map[string]any)
			if !ok {
				t.Fatalf("read = %v, want a map", permission["read"])
			}
			if read["*.env"] != "deny" || read["*.env.*"] != "deny" || read["*.env.example"] != "allow" {
				t.Errorf("the read map does not keep the .env denial: %v", read)
			}
		})
	}
}

func TestOpencodeArgumentShape(t *testing.T) {
	adapter := opencodeAdapter(t)
	inv := invocation(t, "opencode", false)

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if got := spec.Args[:4]; !slices.Equal(got, []string{"run", "--pure", "--format", "json"}) {
		t.Errorf("the invocation does not open with run --pure --format json: %v", got)
	}
	if !hasFlagPair(spec.Args, "--dir", inv.Workdir) {
		t.Error("the checkout is not named as the workspace")
	}
	if !hasFlagPair(spec.Args, "--model", inv.Model) {
		t.Error("the configured model is not passed through")
	}
	if !hasFlagPair(spec.Args, "--variant", inv.Effort) {
		t.Error("the effort is passed as --variant")
	}
	if slices.Contains(spec.Args, "--auto") {
		t.Error("--auto is a blanket bypass and is never passed")
	}

	// The schema travels inside the prompt, because this harness has no schema
	// flag at all.
	prompt := spec.Args[len(spec.Args)-1]
	if !strings.HasPrefix(prompt, inv.Prompt.Text) {
		t.Error("the prompt is not the last argument")
	}
	if !strings.Contains(prompt, "This harness does not constrain your output.") {
		t.Error("the prompt does not correct the skill's claim that the harness constrains the output")
	}
	if !strings.Contains(prompt, inv.Schema.Text) {
		t.Error("the schema does not travel inside the prompt")
	}

	// OPENCODE_CONFIG_DIR at an empty directory displaces the agents and
	// commands that would otherwise load from beside the operator's config.
	var configDir string
	for _, entry := range spec.Env {
		if variable, value, _ := strings.Cut(entry, "="); variable == "OPENCODE_CONFIG_DIR" {
			configDir = value
		}
	}
	if configDir == "" {
		t.Fatal("the spec names no OPENCODE_CONFIG_DIR")
	}
	info, err := os.Stat(configDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("OPENCODE_CONFIG_DIR does not name an existing directory: %v", err)
	}
	entries, err := os.ReadDir(configDir)
	if err != nil || len(entries) != 0 {
		t.Errorf("the config directory is not empty: %v", entries)
	}
}

// A leg with no schema carries no instruction either, because there is nothing
// to instruct about.
func TestOpencodePromptIsUntouchedWithNoSchema(t *testing.T) {
	inv := invocation(t, "opencode", false)
	inv.Schema = harness.File{}

	spec, err := opencodeAdapter(t).Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if last := spec.Args[len(spec.Args)-1]; last != inv.Prompt.Text {
		t.Errorf("the prompt was rewritten with no schema to add: %q", last)
	}
}

func TestOpencodeRefusesWithNoScratchDirectory(t *testing.T) {
	inv := invocation(t, "opencode", false)
	inv.Scratch = ""

	_, err := opencodeAdapter(t).Spec(inv)
	if err == nil {
		t.Fatal("the adapter accepted an invocation with nowhere to write its isolation config")
	}
	if !errorIs(err, harness.ErrScratch) {
		t.Fatalf("err = %v, want ErrScratch", err)
	}
}

func TestOpencodeAgainstTheStub(t *testing.T) {
	adapter := opencodeAdapter(t)

	for _, mode := range []string{"bare", "fenced", "prose", "split"} {
		t.Run(mode, func(t *testing.T) {
			inv := invocation(t, "opencode", false)
			payload := cannedPayload
			if mode == "split" {
				// The stub's split mode needs this phrase to cut the answer in
				// two, with the seam inside a JSON string.
				payload = `{"title":"Unchecked fetch response"}`
			}

			spec, err := adapter.Spec(inv)
			if err != nil {
				t.Fatalf("building the spec: %v", err)
			}
			res := runAgainstStub(t, spec,
				payloadFile(t, "CROSSREV_REVIEW_PAYLOAD", payload),
				"CROSSREV_OPENCODE_MODE="+mode)
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
			if got := string(envelope.Payload); got != payload {
				t.Errorf("payload = %s, want %s", got, payload)
			}

			// The answering model and the usage record come from a SECOND
			// child, because `opencode export` is its own process.
			sessionID := adapter.SessionID(res)
			if sessionID != "stub-session" {
				t.Fatalf("session id = %q", sessionID)
			}
			exportSpec, err := adapter.ExportSpec(inv, sessionID)
			if err != nil {
				t.Fatalf("building the export spec: %v", err)
			}
			exported := runAgainstStub(t, exportSpec)
			if exported.ExitCode != 0 {
				t.Fatalf("the export was refused: exit %d, stderr %s", exported.ExitCode, exported.Stderr)
			}
			adapter.MergeExport(&envelope, exported.Stdout)

			if got := deref(envelope.ModelReported); got != "stub-model" {
				t.Errorf("model_reported = %q", got)
			}
			// 10 in, 4 cache reads, 2 out. Reasoning is persisted beside the
			// total and never added to it.
			if got := deref(envelope.Tokens); got != 16 {
				t.Errorf("tokens = %d, want 16", got)
			}
			if got := deref(envelope.Usage.Reasoning); got != 1 {
				t.Errorf("reasoning = %d, want the stub's 1", got)
			}
		})
	}
}

// An authentication rejection has a shape of its own, and naming it matters more
// than usual here: opencode falls through to a DIFFERENT provider when the
// configured one cannot authenticate, so "the harness failed" sends the reader
// looking in the wrong place entirely.
func TestOpencodeClassifiesACredentialRejection(t *testing.T) {
	adapter := opencodeAdapter(t)
	inv := invocation(t, "opencode", false)

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	res := runAgainstStub(t, spec,
		payloadFile(t, "CROSSREV_REVIEW_PAYLOAD", cannedPayload),
		"CROSSREV_OPENCODE_MODE=error")
	if res.ExitCode == 0 {
		t.Fatal("the stub was supposed to refuse the credential")
	}

	envelope := adapter.Envelope(inv, res)
	if envelope.OK {
		t.Fatal("a credential rejection is a failure")
	}
	if !strings.HasPrefix(deref(envelope.Error), "opencode rejected its credential.") {
		t.Errorf("error = %q, and it does not name the credential", deref(envelope.Error))
	}

	// A bare error event is not that shape — rate limits, overloads, tool
	// failures — and falls through to the generic harness-error branch.
	other := runAgainstStub(t, spec,
		payloadFile(t, "CROSSREV_REVIEW_PAYLOAD", cannedPayload),
		"CROSSREV_OPENCODE_MODE=error-other")
	generic := adapter.Envelope(inv, other)
	if generic.OK {
		t.Fatal("a provider overload is still a failure")
	}
	if strings.Contains(deref(generic.Error), "rejected its credential") {
		t.Errorf("a provider overload was reported as a credential failure: %q", deref(generic.Error))
	}
}

// No text event at all is a different fault from a malformed answer: the run
// finished and said nothing.
func TestOpencodeReportsARunThatSaidNothing(t *testing.T) {
	adapter := opencodeAdapter(t)
	inv := invocation(t, "opencode", false)

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	res := runAgainstStub(t, spec,
		payloadFile(t, "CROSSREV_REVIEW_PAYLOAD", cannedPayload),
		"CROSSREV_OPENCODE_MODE=empty")
	if res.ExitCode != 0 {
		t.Fatalf("the stub refused the invocation: exit %d, stderr %s", res.ExitCode, res.Stderr)
	}

	envelope := adapter.Envelope(inv, res)
	if envelope.OK {
		t.Fatal("a run with no text event produced no answer")
	}
	want := "opencode produced no answer: the run finished without a single text event."
	if got := deref(envelope.Error); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// Extraction can legitimately miss — prose with no braces anywhere — and that is
// a handoff, not a failure: a nil payload lets the orchestrator spend the extra
// attempt a non-schema-native harness is granted.
func TestOpencodeMissingPayloadIsAHandoffNotAFailure(t *testing.T) {
	adapter := opencodeAdapter(t)
	inv := invocation(t, "opencode", false)

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	res := runAgainstStub(t, spec,
		payloadFile(t, "CROSSREV_REVIEW_PAYLOAD", cannedPayload),
		"CROSSREV_OPENCODE_MODE=nojson")
	if res.ExitCode != 0 {
		t.Fatalf("the stub refused the invocation: exit %d, stderr %s", res.ExitCode, res.Stderr)
	}

	envelope := adapter.Envelope(inv, res)
	if !envelope.OK {
		t.Fatalf("prose with no braces is not a harness failure: %+v", envelope)
	}
	if envelope.Payload != nil {
		t.Errorf("payload = %s, want null", envelope.Payload)
	}
}

// The export is telemetry, not the answer: if it failed, the model and the usage
// record stay nil and the review stands.
func TestOpencodeSurvivesAFailedExport(t *testing.T) {
	adapter := opencodeAdapter(t)
	inv := invocation(t, "opencode", false)

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	res := runAgainstStub(t, spec, payloadFile(t, "CROSSREV_REVIEW_PAYLOAD", cannedPayload))
	envelope := adapter.Envelope(inv, res)

	exportSpec, err := adapter.ExportSpec(inv, adapter.SessionID(res))
	if err != nil {
		t.Fatalf("building the export spec: %v", err)
	}
	exported := runAgainstStub(t, exportSpec, "CROSSREV_OPENCODE_NO_EXPORT=1")
	if exported.ExitCode == 0 {
		t.Fatal("the stub was supposed to fail the export")
	}
	adapter.MergeExport(&envelope, exported.Stdout)

	if !envelope.OK {
		t.Error("a failed export does not fail the leg")
	}
	if envelope.ModelReported != nil || envelope.Usage != nil || envelope.Tokens != nil {
		t.Error("a failed export leaves the telemetry unset rather than wrong")
	}
	if string(envelope.Payload) != cannedPayload {
		t.Error("the answer survives a failed export")
	}
}

// The export carries the same environment as the run, isolation config
// included, because the Bash reuses the same array. A stub that checked only
// `run` would miss a config deleted before the export.
func TestOpencodeExportCarriesTheIsolation(t *testing.T) {
	adapter := opencodeAdapter(t)
	inv := invocation(t, "opencode", false)

	run, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the run spec: %v", err)
	}
	export, err := adapter.ExportSpec(inv, "a-session")
	if err != nil {
		t.Fatalf("building the export spec: %v", err)
	}
	if !slices.Equal(run.Env, export.Env) {
		t.Errorf("the export environment differs from the run's:\n  run    %v\n  export %v", run.Env, export.Env)
	}
	if !slices.Equal(export.Args, []string{"export", "a-session"}) {
		t.Errorf("export args = %v", export.Args)
	}
	if _, err := adapter.ExportSpec(inv, ""); err == nil {
		t.Error("a stream with no session id has nothing to export")
	}
}

// The extraction ladder, measured against jq (lib/adapters/opencode.sh:66-78).
func TestExtractJSONLadder(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "bare", text: `{"a":1}`, want: `{"a":1}`},
		{name: "fenced", text: "```json\n{\"a\":1}\n```", want: `{"a":1}`},
		{name: "fenced with a trailing newline", text: "```json\n{\"a\":1}\n```\n", want: `{"a":1}`},
		{name: "a fenced array with a trailing newline", text: "```json\n[{\"a\":1},{\"b\":2}]\n```\n", want: `[{"a":1},{"b":2}]`},
		{name: "a fenced array with none", text: "```json\n[{\"a\":1},{\"b\":2}]\n```", want: `[{"a":1},{"b":2}]`},
		{name: "an unlabelled fence with CRLF", text: "```\r\n{\"a\":1}\r\n```", want: `{"a":1}`},
		{name: "prose around an object", text: "Here:\n{\"a\":1}\nthanks", want: `{"a":1}`},
		{name: "a number is a value", text: "7", want: "7"},
		{name: "null falls through every rung", text: "null", want: ""},
		{name: "false falls through every rung", text: "false", want: ""},
		{name: "prose with no braces", text: "I cannot produce JSON.", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, found := harness.ExtractJSON(tt.text)
			if tt.want == "" {
				if found {
					t.Errorf("payload = %s, want nothing", payload)
				}
				return
			}
			if !found {
				t.Fatal("nothing was extracted")
			}
			if got := string(payload); got != tt.want {
				t.Errorf("payload = %s, want %s", got, tt.want)
			}
		})
	}
}

// The scratch directory is where the config goes, and nothing is written into
// the checkout: a settings file inside the workspace would be moved out of the
// way by the quarantine, and one that survived it would be the hole the
// quarantine exists to close.
func TestOpencodeWritesNothingIntoTheCheckout(t *testing.T) {
	inv := invocation(t, "opencode", true)

	if _, err := opencodeAdapter(t).Spec(inv); err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	entries, err := os.ReadDir(inv.Workdir)
	if err != nil {
		t.Fatalf("reading the checkout: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the adapter wrote into the checkout: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(inv.Scratch, "config.json")); err != nil {
		t.Errorf("the isolation config is not in the scratch directory: %v", err)
	}
}
