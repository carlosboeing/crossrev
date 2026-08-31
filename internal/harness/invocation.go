// invocation.go — what a leg asks a harness to do, what it gets back, and the
// two checks that say the cross-model property held.
//
// The Bash adapters take seven positional arguments —
// `adapter_<name> <prompt_file> <schema_file> <workdir> <model> <effort>
// <endpoint_name> <write>` (lib/adapters/claude.sh:10) — and print one JSON
// object. Invocation is the first, Envelope is the second, and both keep the
// shell's own names so a marker written by either side reads the same.

package harness

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// File is a document the orchestrator wrote for the leg: where it lives, and
// what is in it.
//
// Both halves are carried because the harnesses disagree about which they want.
// Claude Code takes the schema INLINE as a JSON string and Codex takes a file
// path; handing Claude a path fails with a JSON parse error about the leading
// slash, which reads like a corrupt schema rather than a wrong argument type
// (lib/adapters/claude.sh:45-48). Grok is the mirror image for the prompt:
// `--prompt-file` takes a path, while `-p` / `--print` / `--single` consume the
// next argv as the prompt (lib/adapters/grok.sh:7-11).
//
// Path is also what says the document EXISTS. The Bash test is `[[ -n
// "$schema_file" ]]`, so a leg with no schema passes the empty string and every
// adapter skips its schema flag.
type File struct {
	// Path is where the document was written, empty when there is none.
	Path string
	// Text is the document's contents.
	Text string
}

// Present reports that the orchestrator supplied this document, which is the
// `[[ -n "$schema_file" ]]` of every adapter.
func (f File) Present() bool { return f.Path != "" }

// Argument is the document as it reaches a command line, which is not the same
// string as Text.
//
// Every adapter spells the read `"$(cat "$file")"` — lib/adapters/claude.sh:49
// and :111, grok.sh:49, agy.sh:94, codex.sh:95, opencode.sh:105 and :187 — and
// command substitution removes EVERY trailing newline. Both shipped schemas end
// in one (measured: the last byte of each is 0x0a), so passing Text verbatim
// sends the model a byte Bash never sent it.
//
// It is not cosmetic for one of the six. opencode.sh:105 interpolates the
// schema between a ```json fence and its closing fence, so the extra newline
// lands inside the fenced block in the prompt the model reads.
//
// Trailing spaces and tabs are kept, because command substitution keeps them:
// `printf '%s' "$(printf 'a  \n')"` answers "a  ".
func (f File) Argument() string { return strings.TrimRight(f.Text, "\n") }

// Endpoint is a named Anthropic-compatible endpoint, resolved.
//
// The adapter is handed the resolved URL and token rather than the name alone,
// because resolving one is cfg_endpoint's job (lib/adapters/claude.sh:80) and
// internal/config is a tier-2 peer this package may not import.
type Endpoint struct {
	// Name is the configured endpoint name. Empty means the vendor's own API,
	// which is what every adapter but one is limited to.
	Name string
	// URL is the resolved base URL.
	URL string
	// TokenVar is the environment variable the token was read from, named in
	// the refusal when it holds nothing.
	TokenVar string
	// Token is the resolved token. Empty is a refusal, never a fallback:
	// CrossRev will not silently use the vendor's own API instead.
	Token string
}

// Named reports a configured endpoint.
//
// The string "null" counts as unset, because that is what jq prints for a
// missing key and what every adapter's `[[ -n "$endpoint" && "$endpoint" !=
// "null" ]]` guards against (lib/adapters/claude.sh:78).
func (e Endpoint) Named() bool { return e.Name != "" && e.Name != "null" }

// Invocation is one leg's request to one harness.
type Invocation struct {
	// Prompt is the prompt document.
	Prompt File
	// Schema is the output schema, absent for a leg that constrains nothing.
	Schema File
	// Workdir is the checkout the harness runs in.
	Workdir string
	// Model is the model to ask for, empty for the harness's own default. The
	// string "null" counts as empty, matching every adapter's guard.
	Model string
	// Effort is the reasoning effort to ask for, on the same terms.
	Effort string
	// Endpoint is the resolved named endpoint, zero for the vendor's own API.
	Endpoint Endpoint
	// Write is whether this leg may change files. It is derived from the leg
	// rather than configured (lib/adapters/claude.sh:12).
	Write bool
	// Env is the environment the orchestrator built for this leg, as NAME=VALUE
	// entries. An adapter subtracts its strip set from it and adds whatever the
	// invocation needs; it never inherits this process's own, which is what
	// exec.Inherit is for.
	Env []string
	// Scratch is a directory the adapter may write files into. Its lifetime is
	// the caller's: opencode's isolation config has to outlive the run itself,
	// because the export call is the same invocation shape
	// (lib/adapters/opencode.sh:191-192).
	Scratch string
	// PayloadPath is where a harness that writes its answer to a file must
	// write it. Codex is the only one — `-o` (lib/adapters/codex.sh:50) — and
	// the orchestrator names it after the transcript base so the payload lands
	// in the run directory beside the streams.
	PayloadPath string
	// CodexHome is the directory holding the session rollout, which carries the
	// model and effort Codex's event stream does not report. The caller
	// resolves the fallback: `${CODEX_HOME:-$HOME/.codex}`, because
	// cred_prepare exports that variable only in automated mode and a local run
	// would otherwise report no model on every leg
	// (lib/adapters/codex.sh:140-143).
	CodexHome string
}

// wanted reports a model or effort the caller actually asked for. Both adapters
// and config carry "null" through as a string, so it is not a value.
func wanted(value string) bool { return value != "" && value != "null" }

// Envelope is what an adapter answers: the payload, and execution metadata
// naming the harness, the resolved endpoint, the answering model where the
// harness reports one, a normalized usage record and what the turn cost in
// tokens (lib/adapters/claude.sh:4-8).
//
// The JSON names are the shell's, because lib/run.sh reads this object by key.
type Envelope struct {
	OK bool `json:"ok"`
	// Payload is the model's answer, already parsed out of whatever the harness
	// wrapped it in. It is null when the harness produced nothing that parses,
	// which is a handoff rather than a failure for a harness that does not
	// constrain its own output.
	Payload json.RawMessage `json:"payload"`
	// Harness is the name of the adapter that answered.
	Harness string `json:"harness"`
	// Endpoint is "vendor" or the configured endpoint name on success, and null
	// on failure.
	Endpoint *string `json:"endpoint"`
	// ModelReported is the answering model where the harness names one.
	ModelReported *string `json:"model_reported"`
	// EffortReported is the answering effort where the harness names one. Codex
	// is the only harness that does, and it does so in its rollout rather than
	// in its event stream.
	EffortReported *string `json:"effort_reported"`
	// Tokens is the usage record's total, repeated at the top level because
	// that is where the marker reads it.
	Tokens *int64 `json:"tokens"`
	// Usage is the normalized record.
	Usage *Usage `json:"usage"`
	// Error is the failure text, and it is a string rather than null on every
	// failure — including one whose text is empty.
	Error *string `json:"error"`
}

// failed builds the envelope every adapter's error path prints: no payload, no
// endpoint, no telemetry, and the message.
func failed(name, message string) Envelope {
	return Envelope{Harness: name, Error: &message}
}

// succeeded builds the shared half of every adapter's success envelope.
func succeeded(name, endpoint string, payload json.RawMessage, usage *Usage) Envelope {
	envelope := Envelope{
		OK:       true,
		Payload:  payload,
		Harness:  name,
		Endpoint: &endpoint,
		Usage:    usage,
	}
	if usage != nil {
		envelope.Tokens = usage.Total
	}
	return envelope
}

// vendorEndpoint is the label every adapter but one reports, because only one
// of them can reach a named endpoint.
const vendorEndpoint = "vendor"

// --- the divergence guard ----------------------------------------------------

// endpointVariables are the two that redirect a Claude-compatible harness
// process-wide (lib/legs.sh:496-497).
var endpointVariables = []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"}

// AssertEnvClean is legs_assert_env_clean (lib/legs.sh:494-501): layer one of
// the divergence guard, and the specific failure it exists for.
//
// These variables are process-scoped, so a leg that leaks them silently
// redirects the OTHER leg too — both legs run on one model, the loop completes
// normally, and the cross-model property that justifies the whole design is
// gone with no error anywhere.
//
// It takes the environment rather than reading one, so the check is a function
// of its argument and a test needs no process-wide state to exercise it.
func AssertEnvClean(env []string) error {
	var leaked []string
	for _, name := range endpointVariables {
		for _, entry := range env {
			variable, value, found := strings.Cut(entry, "=")
			if found && variable == name && value != "" {
				leaked = append(leaked, name)
				break
			}
		}
	}
	if len(leaked) == 0 {
		return nil
	}
	return &Refusal{
		Reason: fmt.Sprintf("these endpoint variables are set in the environment CrossRev inherited: %s",
			strings.Join(leaked, " ")),
		Action: "They redirect the harness process-wide, so a leg would silently run on the wrong model and the loop would complete normally with no error anywhere. Unset them; crossrev sets them per invocation.",
		Kind:   ErrEndpointLeaked,
	}
}

// LegSettings is what the orchestrator asked one leg to run: the three things it
// controls, and therefore the three a comparison can be made of.
type LegSettings struct {
	Harness  string
	Endpoint string
	Model    string
}

// The two answers ConfiguredDifference gives (lib/legs.sh:538-546).
const (
	ConfiguredSame      = "same"
	ConfiguredDifferent = "different"
)

// ConfiguredDifference is legs_configured_difference (lib/legs.sh:538-546):
// layer one's other half — did the two legs differ in anything the orchestrator
// controls?
func ConfiguredDifference(reviewer, resolver LegSettings) string {
	if reviewer != resolver {
		return ConfiguredDifferent
	}
	return ConfiguredSame
}

// AssertModelsDiverged is legs_assert_models_diverged (lib/legs.sh:554-561):
// layer two, where the harness reports it.
//
// Do not halt merely because a harness reports no model — that would disqualify
// the codex adapter for a field Codex does not emit. What this adds is
// detection of SERVER-SIDE substitution; where it is unavailable the marker
// records its absence rather than implying a check that never ran.
func AssertModelsDiverged(configured, reviewerModel, resolverModel string) error {
	if configured != ConfiguredDifferent {
		return nil // one model was asked for
	}
	if !wanted(reviewerModel) || !wanted(resolverModel) {
		return nil
	}
	if reviewerModel != resolverModel {
		return nil
	}
	return &Refusal{
		Reason: fmt.Sprintf("both legs were configured to differ but the same model answered each: %s", reviewerModel),
		Action: "This is the failure the cross-model design exists to prevent, and it completes normally when unchecked. Check the endpoint block and that no endpoint variable is exported.",
		Kind:   ErrModelsConverged,
	}
}

// SameModel is _same_model (lib/run.sh:1449-1455): is the model that answered
// the one that was asked for, written at a different precision?
//
// Containment rather than an alias table, deliberately — an alias-to-id table
// goes stale for the same reason the price table does, and it would have to be
// maintained per harness. An alias is the family token its canonical id carries,
// `opus` inside `claude-opus-4-5-20251101`, and a date pin is that id with more
// of it, so one name inside the other is one model written at two precisions. A
// substitution shares no such token, which is the case worth shouting about.
func SameModel(want, got string) bool {
	want, got = strings.ToLower(want), strings.ToLower(got)
	if want == "" || got == "" {
		return false
	}
	return strings.Contains(got, want) || strings.Contains(want, got)
}

// --- the environment an adapter hands its child ------------------------------

// childEnv is the `env -u NAME … [VAR=value] program` every adapter opens with.
//
// The strip set comes from cred.StripFor, which joins the four forge
// credentials to this harness's foreign vendor credentials — one list rather
// than two, because an adapter that asked for one and forgot the other is the
// failure that is for (internal/cred/strip.go:20-22).
//
// The additions come last, which is `env`'s own rule: it stops parsing options
// at the first assignment, so a `-u` written after one becomes the command it
// tries to run (lib/adapters/opencode.sh:173-175). Nothing here can reproduce
// that mistake — Spec.Env is a set rather than an argument list — and the order
// is kept anyway so the two read the same.
func childEnv(doc Document, name string, env []string, additions ...string) []string {
	strip := stripFor(doc, name)
	kept := make([]string, 0, len(env)+len(additions))
	for _, entry := range env {
		variable, _, found := strings.Cut(entry, "=")
		if found && slices.Contains(strip, variable) {
			continue
		}
		kept = append(kept, entry)
	}
	return append(kept, additions...)
}
