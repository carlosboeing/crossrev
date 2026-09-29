package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
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
			// write creates files in 2.x while edit only replaces text, and
			// apply_patch is a second writer beside both: each mirrors the
			// leg's grant, so a review leg holds no writer under any name.
			for _, key := range []string{"write", "apply_patch"} {
				if permission[key] != tt.edit {
					t.Errorf("%s = %v, want %q", key, permission[key], tt.edit)
				}
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

// isolationFile reads the whole config the adapter wrote for one leg, not
// just its top-level permission block: the pinned agent carries its own.
func isolationFile(t *testing.T, spec exec.Spec) map[string]any {
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
		return config
	}
	t.Fatal("the spec names no OPENCODE_CONFIG")
	return nil
}

// The isolation config defines the agent the leg runs as, with its own
// permission block mirroring the leg's grant: agent rules take precedence
// over every merged config, so a global agents.build allow cannot widen the
// leg back. The agent is a primary one — newer runtimes silently fall back
// to the default agent on a subagent dispatch, dropping the block outright.
func TestOpencodeIsolationDefinesAPinnedAgent(t *testing.T) {
	adapter := opencodeAdapter(t)

	for _, tt := range []struct {
		name  string
		write bool
		grant string
	}{
		{name: "a reading leg pins a denying agent", write: false, grant: "deny"},
		{name: "a writing leg pins an allowing agent", write: true, grant: "allow"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := adapter.Spec(invocation(t, "opencode", tt.write))
			if err != nil {
				t.Fatalf("building the spec: %v", err)
			}
			config := isolationFile(t, spec)
			agents, ok := config["agent"].(map[string]any)
			if !ok {
				t.Fatal("the isolation config defines no agent")
			}
			pinned, ok := agents["crossrev"].(map[string]any)
			if !ok {
				t.Fatal("the isolation config defines no crossrev agent")
			}
			if pinned["mode"] != "primary" {
				t.Errorf("agent mode = %v, want primary — a subagent dispatch falls back to the default agent and drops the permission block", pinned["mode"])
			}
			permission, ok := pinned["permission"].(map[string]any)
			if !ok {
				t.Fatal("the pinned agent carries no permission block")
			}
			if permission["*"] != "deny" {
				t.Errorf("the agent's fail-closed base rule is missing: %v", permission["*"])
			}
			for _, key := range []string{"edit", "write", "apply_patch"} {
				if permission[key] != tt.grant {
					t.Errorf("agent %s = %v, want %q", key, permission[key], tt.grant)
				}
			}
			for _, key := range []string{"bash", "task", "skill", "webfetch", "websearch", "external_directory", "question", "doom_loop"} {
				if permission[key] != "deny" {
					t.Errorf("agent %s = %v, want deny in every shape", key, permission[key])
				}
			}
		})
	}
}

// The leg runs as its pinned agent on both majors: without --agent it runs
// the default build agent, whose global rules are appended after the
// config-file rules with last match winning.
func TestOpencodePinsItsAgentOnBothMajors(t *testing.T) {
	adapter := opencodeAdapter(t)

	for _, major := range []int{1, 2} {
		inv := invocation(t, "opencode", false)
		inv.CLIMajor = major
		spec, err := adapter.Spec(inv)
		if err != nil {
			t.Fatalf("building the spec: %v", err)
		}
		if !hasFlagPair(spec.Args, "--agent", "crossrev") {
			t.Errorf("major %d: the leg does not run as its pinned agent: %v", major, spec.Args)
		}
	}
}

// Project and parent-directory configs never load: the variable the docs
// name for skipping project config discovery rides every opencode child,
// run and session export alike.
func TestOpencodeDisablesProjectConfig(t *testing.T) {
	adapter := opencodeAdapter(t)

	for _, major := range []int{1, 2} {
		inv := invocation(t, "opencode", false)
		inv.CLIMajor = major
		run, err := adapter.Spec(inv)
		if err != nil {
			t.Fatalf("building the run spec: %v", err)
		}
		export, err := adapter.ExportSpec(inv, "a-session")
		if err != nil {
			t.Fatalf("building the export spec: %v", err)
		}
		for name, spec := range map[string]exec.Spec{"run": run, "export": export} {
			found := false
			for _, entry := range spec.Env {
				if entry == "OPENCODE_DISABLE_PROJECT_CONFIG=1" {
					found = true
				}
			}
			if !found {
				t.Errorf("major %d %s: OPENCODE_DISABLE_PROJECT_CONFIG=1 is not set: %v", major, name, spec.Env)
			}
		}
	}
}

// The isolation entries are the only ones: an operator-exported
// OPENCODE_CONFIG would otherwise travel beside the adapter's own — the leg
// allowlist inherits it — and whichever duplicate the runtime honours first
// decides what the leg may do.
func TestOpencodeIsolationWinsOverInheritedEnv(t *testing.T) {
	adapter := opencodeAdapter(t)
	inv := invocation(t, "opencode", false)
	inv.Env = append(inv.Env,
		"OPENCODE_CONFIG=/operator/config.json",
		"OPENCODE_CONFIG_DIR=/operator/config-dir",
		"OPENCODE_DISABLE_PROJECT_CONFIG=",
	)

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	for _, variable := range []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_DISABLE_PROJECT_CONFIG"} {
		count := 0
		for _, entry := range spec.Env {
			name, value, _ := strings.Cut(entry, "=")
			if name != variable {
				continue
			}
			count++
			if variable == "OPENCODE_CONFIG" {
				if value == "/operator/config.json" {
					t.Errorf("the operator's %s survived beside the isolation config", variable)
				}
				if _, err := os.Stat(value); err != nil {
					t.Errorf("the surviving %s names nothing the adapter wrote: %v", variable, err)
				}
			}
			if variable == "OPENCODE_DISABLE_PROJECT_CONFIG" && value != "1" {
				t.Errorf("%s = %q, want 1", variable, value)
			}
		}
		if count != 1 {
			t.Errorf("%s appears %d times, want exactly the isolation entry", variable, count)
		}
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

// A 2.x leg runs standalone with the same isolation config: the background
// service ignores the process's OPENCODE_CONFIG, so without --standalone the
// leg would start unconstrained (issue #272). --pure and --dir are gone —
// 2.x rejects both before any model call — and the checkout travels as the
// child's working directory, which base.spec already sets to the workdir.
// --variant is gone too: the effort rides the model as provider/model#variant.
func TestOpencodeArgumentShapeOn2x(t *testing.T) {
	adapter := opencodeAdapter(t)
	inv := invocation(t, "opencode", false)
	inv.CLIMajor = 2

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if got := spec.Args[:4]; !slices.Equal(got, []string{"run", "--standalone", "--format", "json"}) {
		t.Errorf("the invocation does not open with run --standalone --format json: %v", got)
	}
	for _, flag := range []string{"--pure", "--dir", "--variant", "--auto"} {
		if slices.Contains(spec.Args, flag) {
			t.Errorf("2.x rejects %s, but the spec passes it: %v", flag, spec.Args)
		}
	}
	if !hasFlagPair(spec.Args, "--model", "opencode/a-model#high") {
		t.Errorf("the effort does not ride the model as a variant: %v", spec.Args)
	}
	if spec.Dir != inv.Workdir {
		t.Errorf("the child's working directory = %q, want the checkout %q — 2.x takes no --dir, so the directory is the isolation", spec.Dir, inv.Workdir)
	}

	prompt := spec.Args[len(spec.Args)-1]
	if !strings.HasPrefix(prompt, inv.Prompt.Text) {
		t.Error("the prompt is not the last argument")
	}
	if !strings.Contains(prompt, "This harness does not constrain your output.") {
		t.Error("the prompt does not correct the skill's claim that the harness constrains the output")
	}
}

// 2.x takes no --variant, so an effort with no model to ride on cannot be
// passed at all. The leg is refused rather than started without the effort
// it was configured with.
func TestOpencodeRefusesEffortWithoutModelOn2x(t *testing.T) {
	inv := invocation(t, "opencode", false)
	inv.CLIMajor = 2
	inv.Model = ""
	inv.Effort = "high"

	_, err := opencodeAdapter(t).Spec(inv)
	if err == nil {
		t.Fatal("the adapter accepted an effort with no model on 2.x")
	}
	if !errorIs(err, harness.ErrEffortWithoutModel) {
		t.Fatalf("err = %v, want ErrEffortWithoutModel", err)
	}
}

// The session record moved in 2.x: `export` is now `session export`, and it
// answers the same `.info` shape the usage parser reads. The environment
// still matches the run's in both majors.
func TestOpencodeExportSpecFollowsMajor(t *testing.T) {
	adapter := opencodeAdapter(t)

	for _, tt := range []struct {
		name  string
		major int
		want  []string
	}{
		{name: "1.x exports the session id", major: 1, want: []string{"export", "a-session"}},
		{name: "an unprobed install keeps the 1.x export", major: 0, want: []string{"export", "a-session"}},
		{name: "2.x exports through session export", major: 2, want: []string{"session", "export", "--standalone", "a-session"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inv := invocation(t, "opencode", false)
			inv.CLIMajor = tt.major
			run, err := adapter.Spec(inv)
			if err != nil {
				t.Fatalf("building the run spec: %v", err)
			}
			export, err := adapter.ExportSpec(inv, "a-session")
			if err != nil {
				t.Fatalf("building the export spec: %v", err)
			}
			if !slices.Equal(export.Args, tt.want) {
				t.Errorf("export args = %v, want %v", export.Args, tt.want)
			}
			if !slices.Equal(run.Env, export.Env) {
				t.Errorf("the export environment differs from the run's:\n  run    %v\n  export %v", run.Env, export.Env)
			}
		})
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
	// The JSON literal, which is what lib/adapters/opencode.sh:257-258
	// substitutes. A Go nil is a different value: the validator rejects `null`
	// as "the payload is not a JSON object" and accepts an absent one, so a
	// nil here published prose as an empty review instead of retrying.
	if string(envelope.Payload) != "null" {
		t.Errorf("payload = %q, want the JSON literal null", envelope.Payload)
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

// An install past the supported major version is refused by version, before the
// leg starts. opencode 2.x does not accept the flags this adapter passes and
// does not read the isolation config it writes (issue #272), so a run on it
// would start without the constraints the config exists to hold. A probe that
// names no version is refused the same way: "could not confirm" is not
// "supported", and the gate fails closed.
func TestOpencodeRefusesAnInstallPastTheSupportedMajor(t *testing.T) {
	adapter := opencodeAdapter(t)
	pinned, ok := any(adapter).(harness.VersionPinned)
	if !ok {
		t.Fatal("the opencode adapter does not implement harness.VersionPinned")
	}

	inv := invocation(t, "opencode", false)
	probe := pinned.VersionProbe(inv)
	if !slices.Equal(probe.Args, []string{"--version"}) {
		t.Errorf("the version probe is `opencode --version`; got %v", probe.Args)
	}
	if probe.Dir != inv.Scratch {
		t.Errorf("the probe's working directory = %q, want the scratch directory %q — it runs before the quarantine, so it must not start in the checkout", probe.Dir, inv.Scratch)
	}

	for _, tt := range []struct {
		name string
		out  string
	}{
		{name: "3.x", out: "opencode v3.1.4\n"},
		{name: "0.x", out: "opencode v0.9.9\n"},
		{name: "no version token at all", out: "no version token here\n"},
		{name: "no output at all", out: ""},
	} {
		t.Run("refuses "+tt.name, func(t *testing.T) {
			refusal := pinned.VersionRefusal([]byte(tt.out))
			if refusal == nil {
				t.Fatal("the install was accepted")
			}
			if !errorIs(refusal, harness.ErrVersionUnsupported) {
				t.Errorf("err = %v, want ErrVersionUnsupported", refusal)
			}
			if !strings.Contains(refusal.Reason, "opencode 1.x and 2.x") {
				t.Errorf("Reason does not name the supported range: %q", refusal.Reason)
			}
			if !strings.Contains(refusal.Action, "issues/272") {
				t.Errorf("Action does not point at issue #272: %q", refusal.Action)
			}
			if !strings.Contains(refusal.Action, "@opencode/cli@2.0.15") {
				t.Errorf("Action does not name the supported install: %q", refusal.Action)
			}
		})
	}

	for _, out := range []string{
		"1.18.21 (test stub)\n",
		"opencode v1.18.21\n",
		"opencode v1.0.0\n",
		"opencode v2.0.15\n",
		"opencode v2.0.0\n",
	} {
		t.Run(fmt.Sprintf("accepts %q", strings.TrimSpace(out)), func(t *testing.T) {
			if refusal := pinned.VersionRefusal([]byte(out)); refusal != nil {
				t.Errorf("refusal = %v, want nil", refusal)
			}
		})
	}
}

// fixedRunner answers one canned result to every child it is handed.
type fixedRunner struct{ res exec.Result }

func (r fixedRunner) Run(context.Context, exec.Spec) exec.Result { return r.res }

// A probe that does not answer refuses rather than admitting the leg: the gate
// fails closed, because an unconfirmed version is not a supported one. A
// missing binary stays the not-installed refusal it has always been.
func TestCheckVersionRefusesAProbeThatDoesNotAnswer(t *testing.T) {
	adapter, known := harness.For(descriptors(t), "opencode")
	if !known {
		t.Fatal("the descriptor carries no opencode adapter")
	}
	inv := invocation(t, "opencode", false)

	for _, tt := range []struct {
		name string
		res  exec.Result
	}{
		{name: "a probe that failed to run", res: exec.Result{Err: exec.ErrPipesAbandoned}},
		{name: "a probe that exited non-zero", res: exec.Result{ExitCode: 1}},
		{name: "a probe that printed nothing", res: exec.Result{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			major, refusal := harness.CheckVersion(context.Background(), fixedRunner{tt.res}, adapter, inv)
			if refusal == nil {
				t.Fatal("the leg was allowed to start on an unconfirmed version")
			}
			if major != 0 {
				t.Errorf("major = %d, want 0 for an unconfirmed version", major)
			}
			if !errorIs(refusal, harness.ErrVersionUnsupported) {
				t.Errorf("err = %v, want ErrVersionUnsupported", refusal)
			}
			if !strings.Contains(refusal.Reason, "opencode 1.x and 2.x") {
				t.Errorf("Reason does not name the supported range: %q", refusal.Reason)
			}
		})
	}

	// The gate reports the confirmed major beside the refusal, because the
	// two majors take different flags: the legs build the spec from it.
	for _, tt := range []struct {
		name  string
		out   string
		major int
	}{
		{name: "1.x", out: "opencode v1.18.21 (test stub)\n", major: 1},
		{name: "2.x", out: "opencode v2.0.15\n", major: 2},
	} {
		t.Run("a supported version lets the leg start on "+tt.name, func(t *testing.T) {
			res := exec.Result{Stdout: []byte(tt.out)}
			major, refusal := harness.CheckVersion(context.Background(), fixedRunner{res}, adapter, inv)
			if refusal != nil {
				t.Fatalf("refusal = %v, want nil", refusal)
			}
			if major != tt.major {
				t.Errorf("major = %d, want %d", major, tt.major)
			}
		})
	}

	t.Run("a missing binary stays the not-installed refusal", func(t *testing.T) {
		// The real start failure, from the real runner: a missing binary
		// starts nothing and answers the one error shape IsNotFound must keep
		// matching, rather than a fabricated one.
		missing := exec.NewOSRunner().Run(context.Background(), exec.Spec{
			Path: "crossrev-no-such-binary-for-tests", Args: []string{"--version"},
		})
		major, refusal := harness.CheckVersion(context.Background(), fixedRunner{missing}, adapter, inv)
		if refusal == nil {
			t.Fatal("a missing binary was allowed to start the leg")
		}
		if major != 0 {
			t.Errorf("major = %d, want 0 when no probe answered", major)
		}
		if !errorIs(refusal, harness.ErrNotInstalled) {
			t.Errorf("err = %v, want ErrNotInstalled", refusal)
		}
	})
}
