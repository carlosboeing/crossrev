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
	"fmt"
	"strings"

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
	// A served review leg reads only through CrossRev's tool: the MCP
	// config naming the read server, an empty built-in tool list, the
	// served allowlist, and the stream-json output the tripwire and the
	// envelope both read — replacing the PR 273 tool list. The answer and
	// the usage come from the final result event.
	if inv.ReadMode == ReadModeServed && !inv.Write {
		return a.servedReviewSpec(inv)
	}
	// A supplied review leg carries no read tool at all: an empty tool list
	// and no MCP server, answering from the supplied prompt alone.
	if inv.ReadMode == ReadModeSupplied && !inv.Write {
		return a.suppliedReviewSpec(inv)
	}
	args := []string{"-p", "--output-format", "json"}
	if inv.Write {
		// A resolve leg streams its tool record so the tripwire can read it.
		// stream-json under -p requires --verbose at flag parsing; without
		// it the leg never edits.
		args[2] = "stream-json"
		args = append(args, "--verbose")
	}

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
	//
	// It does pass an explicit tool list, though, and excludes every MCP
	// server: the review needs Read, Grep and Glob and nothing else, so the
	// leg runs the same tool set whatever the operator's own settings carry.
	// --tools restricts the built-in tools only and leaves MCP tools alone, so
	// --strict-mcp-config travels with it; with no --mcp-config to keep, the
	// session loads no MCP servers at all. Agent and Skill are removed
	// outright rather than assumed covered: they start subagents and run
	// operator skills, which no read-only review needs, and a bare name in
	// --disallowedTools removes the tool from context. The named-endpoint path
	// builds through here, so it gets the same list.
	if inv.Write {
		// A resolve leg edits without running commands: acceptEdits keeps
		// file writes while --disallowedTools Bash removes the shell
		// (`claude --help`). stream-json above leaves the tool record the
		// tripwire reads.
		args = append(args, "--permission-mode", "acceptEdits", "--disallowedTools", "Bash")
	} else {
		args = append(args, "--tools", "Read,Grep,Glob", "--disallowedTools", "Agent,Skill", "--strict-mcp-config")
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
	// No positional prompt: `-p` reads it from stdin ("Print response and
	// exit (useful for pipes)"). A leg's prompt is hundreds of kilobytes,
	// and argv is the wrong vehicle for it.

	var additions []string
	if inv.Endpoint.Named() {
		if inv.Endpoint.Token == "" {
			return exec.Spec{}, &Refusal{
				Reason: fmt.Sprintf("the endpoint %q needs $%s, which is unset", inv.Endpoint.Name, inv.Endpoint.TokenVar),
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

	spec := a.spec(inv, args, additions...)
	spec.Stdin = promptStdin(inv)
	return spec, nil
}

// servedReviewSpec is the served review leg: the MCP config travels in a
// file outside the quarantined checkout, --strict-mcp-config keeps the
// session from loading any other server, --tools empties the built-ins, and
// the allowlist names the one served read. stream-json under -p requires
// --verbose at flag parsing, the same requirement the resolve leg meets.
func (a *Claude) servedReviewSpec(inv Invocation) (exec.Spec, error) {
	mcpPath, err := WriteMCPConfig(inv.Scratch, inv.Serve)
	if err != nil {
		return exec.Spec{}, err
	}
	args := []string{"-p", "--mcp-config", mcpPath, "--strict-mcp-config",
		"--tools", "", "--allowedTools", "mcp__crossrev__read_file",
		"--output-format", "stream-json", "--verbose"}
	return a.finishReviewSpec(inv, args)
}

// suppliedReviewSpec is the supplied review leg: no read tool, no MCP
// server, buffered json output. There is no tool record to watch, so a
// supplied review degrades the tripwire along with the reads; the reason
// records it.
func (a *Claude) suppliedReviewSpec(inv Invocation) (exec.Spec, error) {
	args := []string{"-p", "--output-format", "json",
		"--tools", "", "--strict-mcp-config"}
	return a.finishReviewSpec(inv, args)
}

// finishReviewSpec appends the schema, model and effort to a review args
// prefix and builds the child. The schema stays inline: Claude Code takes
// it as a JSON string, never as a path.
func (a *Claude) finishReviewSpec(inv Invocation, args []string) (exec.Spec, error) {
	if inv.Schema.Present() {
		if inv.Schema.Text == "" {
			return exec.Spec{}, a.schemaTextMissing()
		}
		args = append(args, "--json-schema", inv.Schema.Argument())
	}
	if wanted(inv.Model) {
		args = append(args, "--model", inv.Model)
	}
	if wanted(inv.Effort) {
		args = append(args, "--effort", inv.Effort)
	}
	var additions []string
	if inv.Endpoint.Named() {
		if inv.Endpoint.Token == "" {
			return exec.Spec{}, &Refusal{
				Reason: fmt.Sprintf("the endpoint %q needs $%s, which is unset", inv.Endpoint.Name, inv.Endpoint.TokenVar),
				Action: "Export it, or set it as a repository secret for CI. CrossRev will not fall back to the vendor's own API.",
				Kind:   ErrEndpointToken,
			}
		}
		additions = []string{
			"ANTHROPIC_BASE_URL=" + inv.Endpoint.URL,
			"ANTHROPIC_AUTH_TOKEN=" + inv.Endpoint.Token,
		}
	}
	spec := a.spec(inv, args, additions...)
	spec.Stdin = promptStdin(inv)
	return spec, nil
}

func (a *Claude) schemaTextMissing() *Refusal {
	return &Refusal{
		Reason: "the claude adapter was given a schema path with no schema text",
		Action: "Claude Code takes the schema inline as a JSON string, so the orchestrator has to read the file before the leg runs.",
		Kind:   ErrSchemaUnavailable,
	}
}

// VersionProbe is `claude --version`, which costs no model call. Scratch
// dir, for the same reason opencode probes there: --version needs no
// checkout, and the probe must not read repository-provided configuration.
func (a *Claude) VersionProbe(inv Invocation) exec.Spec {
	probe := a.spec(inv, []string{"--version"})
	probe.Dir = inv.Scratch
	return probe
}

// VersionRefusal fails closed when the probe names no version: an install
// CrossRev cannot confirm is not one it drives. Any named version is one
// the adapter drives — whether a served review may run on it is the review
// leg's installed-version gate, which reads the verified table, not the
// adapter.
func (a *Claude) VersionRefusal(probe []byte) *Refusal {
	if versionToken.FindString(string(probe)) == "" {
		return &Refusal{
			Reason: "the claude CLI did not report a version",
			Action: "CrossRev cannot confirm this install is one it drives, so the leg is refused rather than started. Install it from https://claude.com/claude-code, or point this leg at another harness with --harness.",
			Kind:   ErrVersionUnsupported,
		}
	}
	return nil
}

// Envelope reads what the child produced (lib/adapters/claude.sh:114-163).
func (a *Claude) Envelope(inv Invocation, res exec.Result) Envelope {
	// A resolve leg streams NDJSON; the terminal result event carries the
	// same cumulative usage, modelUsage and cost as the buffered json object
	// (verified against the stream-json event reference). A served review
	// leg streams the same way, and its answer and usage come from the
	// final result event. Any other review leg keeps the single object.
	stdout := res.Stdout
	if inv.Write || (inv.ReadMode == ReadModeServed && !inv.Write) {
		if result := claudeStreamResult(res.Stdout); result != nil {
			stdout = result
		}
	}
	answer, _ := decodeOrdered(stdout)
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
	usage := ParseClaude(stdout)

	endpoint := vendorEndpoint
	if inv.Endpoint.Named() {
		endpoint = inv.Endpoint.Name
	}
	envelope := succeeded(a.Name(), endpoint, resultPayload(stdout), usage)
	if usage != nil {
		if model := ModelReportedFromModels(usage.Models); model != "" {
			envelope.ModelReported = &model
		}
	}
	return envelope
}

// claudeStreamResult is the terminal result event of a stream-json run: the
// last line whose top-level type is "result". Each line is decoded alone, so
// one malformed line skips rather than taking the whole stream down, the way
// opencodeText reads.
func claudeStreamResult(stdout []byte) []byte {
	var last []byte
	for _, line := range strings.Split(string(stdout), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		event, err := decodeOrdered([]byte(trimmed))
		if err != nil {
			continue
		}
		if kind, _ := event.member("type").asString(); kind != "result" {
			continue
		}
		last = []byte(trimmed)
	}
	return last
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
