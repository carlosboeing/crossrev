// claude.go — lib/adapters/claude.sh.
//
// Returns two things, not one: the payload, and execution metadata naming the
// harness, the resolved endpoint, the answering model where the harness reports
// one, a normalized usage record of token buckets, and what the turn cost in
// tokens. Invoked with no GitHub credential in its environment, and with
// repository-provided harness customisation disabled.
//
// It is the only adapter that can reach a named endpoint, because a named
// endpoint is Anthropic-compatible.

package harness

import (
	"encoding/json"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/runlog"
)

// Claude is the adapter for Claude Code.
type Claude struct{ base }

var _ Adapter = (*Claude)(nil)

// NotInstalled is lib/adapters/claude.sh:19-21.
func (a *Claude) NotInstalled() *Refusal {
	return a.notInstalled("Install it from https://claude.com/claude-code, or point this leg at another harness with --harness.")
}

// Spec builds the child process (lib/adapters/claude.sh:23-94).
func (a *Claude) Spec(inv Invocation) (exec.Spec, error) {
	args := []string{"-p", "--output-format", "json"}

	// A resolve leg has to change files, and headless Claude Code denies a
	// write tool unless something grants it. Locally that something is the
	// operator's own ~/.claude/settings.json; a runner is a fresh container
	// with no such file, so the leg verified findings, worked out the fix and
	// then could not apply it.
	//
	// It has to be this flag rather than a settings file: the quarantine moves
	// every path a harness auto-loads configuration from, so a settings file
	// written into the workspace would be moved out of the way before claude
	// started — and a grant that survived the quarantine would be the hole the
	// quarantine exists to close.
	//
	// acceptEdits, not bypassPermissions: the line worth holding is between
	// editing files and running arbitrary commands, and the resolve leg only
	// needs the first.
	//
	// A reading leg passes no mode at all. There is no permission mode meaning
	// "deny" — plan mode changes what the model does rather than what it may
	// touch — and the headless default already denies the write, which is the
	// behaviour that exposed this in the first place.
	if inv.Write {
		args = append(args, "--permission-mode", "acceptEdits")
	}

	// Claude Code takes the schema INLINE as a JSON string. Codex takes a file
	// path. Verified: handing Claude a path fails with a JSON parse error about
	// the leading slash, which reads like a corrupt schema rather than a wrong
	// argument type.
	if inv.Schema.Present() {
		if inv.Schema.Text == "" {
			return exec.Spec{}, a.schemaTextMissing()
		}
		args = append(args, "--json-schema", inv.Schema.Argument())
	}

	// Model ids must be fully qualified. `--model sonnet-5` fails with "It may
	// not exist or you may not have access to it", which reads like an
	// entitlement problem rather than a typo.
	if wanted(inv.Model) {
		args = append(args, "--model", inv.Model)
	}
	if wanted(inv.Effort) {
		args = append(args, "--effort", inv.Effort)
	}
	args = append(args, inv.Prompt.Argument())

	var additions []string
	if inv.Endpoint.Named() {
		if inv.Endpoint.Token == "" {
			return exec.Spec{}, &Refusal{
				Reason: "the endpoint '" + inv.Endpoint.Name + "' needs $" + inv.Endpoint.TokenVar + ", which is unset",
				Action: "Export it, or set it as a repository secret for CI. CrossRev will not fall back to the vendor's own API.",
				Kind:   ErrEndpointToken,
			}
		}
		// Set on this invocation only. Never exported, never in a workflow env
		// block. These variables are process-scoped, so a leg that leaks them
		// silently redirects the OTHER leg too — both legs run on one model, the
		// loop completes normally, and the cross-model property that justifies
		// the whole design is gone with no error anywhere.
		additions = []string{
			"ANTHROPIC_BASE_URL=" + inv.Endpoint.URL,
			"ANTHROPIC_AUTH_TOKEN=" + inv.Endpoint.Token,
		}
	}

	return a.spec(inv, args, additions...), nil
}

func (a *Claude) schemaTextMissing() *Refusal {
	return &Refusal{
		Reason: "the claude adapter was given a schema path with no schema text",
		Action: "Claude Code takes the schema inline as a JSON string, so the orchestrator has to read the file before the leg runs.",
		Kind:   ErrSchemaUnavailable,
	}
}

// Envelope reads what the child produced (lib/adapters/claude.sh:114-163).
func (a *Claude) Envelope(inv Invocation, res exec.Result) Envelope {
	answer, _ := decodeOrdered(res.Stdout)
	isError := answer.member("is_error")

	if res.ExitCode != 0 || (isError.kind == kindBool && isError.boolean) {
		// Chosen on whether a message is there, not on jq's exit status. On an
		// EMPTY stdout jq exits 0 with no output, so a `jq … || head "$err"`
		// fallback never fires and the error becomes the empty string — exactly
		// when stderr holds the only diagnosis. Found by a reviewer in the agy
		// adapter, which copied this line.
		message := firstAlternative(answer, "result")
		if message == "" {
			message = HarnessError(res.Stderr)
		}
		if message == "" {
			message = "claude exited " + itoa(res.ExitCode) + " with no output on either stream"
		}
		return failed(a.Name(), runlog.Redact(message))
	}

	// Buckets, not one number: the normalized usage record comes from four sums
	// across modelUsage plus the write-TTL split and thinking count that only
	// top-level .usage carries. The answering model is the canonicalModel of the
	// key holding the largest token share.
	usage := ParseClaude(res.Stdout)

	endpoint := vendorEndpoint
	if inv.Endpoint.Named() {
		endpoint = inv.Endpoint.Name
	}
	envelope := succeeded(a.Name(), endpoint, resultPayload(res.Stdout), usage)
	if usage != nil {
		if model := ModelReportedFromModels(usage.Models); model != "" {
			envelope.ModelReported = &model
		}
	}
	return envelope
}

// resultPayload is `.result` read back as JSON.
//
// The real CLI puts the payload in there as a JSON *string*, which is why the
// Bash reads it with `jq -r '.result // empty'` and then parses the text a
// second time (lib/adapters/claude.sh:136 and :156). A null or a false is not a
// value there, so it answers no payload; a `.result` that is neither a string
// nor absent is compacted as it stands, which is what `jq -r` followed by
// `jq -c .` does for one.
func resultPayload(stdout []byte) json.RawMessage {
	result, ok := alternativeValue(rawMember(stdout, "result"))
	if !ok {
		return nil
	}
	var text string
	if err := json.Unmarshal(result, &text); err == nil {
		payload, parsed := parseJSON(text)
		if !parsed {
			return nil
		}
		return payload
	}
	payload, parsed := parseJSON(string(result))
	if !parsed {
		return nil
	}
	return payload
}
