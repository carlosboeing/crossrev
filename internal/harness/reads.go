// reads.go — which read path a review leg runs with, and the tripwire that
// watches it.
//
// A review leg reads in one of three modes, named by the descriptor's
// `read_mode`:
//
//	served   CrossRev's own read tool is the only read path. Harness file
//	         and command tools are disabled where a flag exists, and the
//	         harness's own event stream feeds the tripwire.
//	file_tool  Reserved for a later slice (9). Accepted by the validator so
//	         descriptors can name it, but no wiring reads it yet: a leg
//	         resolves it to supplied and records why.
//	supplied The prompt carries everything. No read tool is granted; harness
//	         read and command tools are disabled where a flag exists. Where
//	         the harness emits a tool record (grok), the tripwire reads it;
//	         where it emits none (agy) or is denied through config with no
//	         record (opencode), doctor names the gap.
//
// The zero Invocation carries no mode and keeps the adapters' legacy shape,
// which is what every existing caller and stub test pins. Legs always set an
// explicit mode from the descriptor, so the legacy shape never runs in
// production; it stays as the documented default for a zero value.

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/runlog"
)

// ReadMode is one leg's read path.
type ReadMode string

// The descriptor's own vocabulary for the three read paths.
const (
	ReadModeServed   ReadMode = "served"
	ReadModeFileTool ReadMode = "file_tool"
	ReadModeSupplied ReadMode = "supplied"
)

// ParseReadMode answers the mode a descriptor names, and whether it names
// one. The empty string is not a mode: absence defaults to supplied at the
// accessor, fail closed.
func ParseReadMode(text string) (ReadMode, bool) {
	switch ReadMode(text) {
	case ReadModeServed, ReadModeFileTool, ReadModeSupplied:
		return ReadMode(text), true
	default:
		return "", false
	}
}

// readModeNames is the sentence the validator joins.
func readModeNames() string { return "served, file_tool or supplied" }

// verifiedCommandBlocks is the served-or-tripwire command block verified live
// on each pinned version, or the pin moves. A harness whose block is
// unverified never reviews: the review leg is refused with
// review_isolation_unverified before any child starts, while its resolve leg
// is unaffected. agy and opencode carry no entry: agy emits no tool record
// and opencode is denied through its isolation config, so there is no
// command block to verify and doctor names the gap instead. grok carries no
// entry either: its empty tools allowlist left the command tool callable on
// the pinned version, so every grok review is refused until a block is
// verified and recorded again.
var verifiedCommandBlocks = map[string]string{
	"codex":  "0.159.2",
	"claude": "2.1.237",
}

// IsolationVerified reports whether harness's command block at this pinned
// version is the one verified live. Moving the pin unverifies the block
// until the flags are verified again.
func IsolationVerified(name, pinnedVersion string) bool {
	want, ok := verifiedCommandBlocks[name]
	return ok && pinnedVersion != "" && pinnedVersion == want
}

// ReviewIsolationRefusal refuses a review leg whose read path cannot be
// verified: a served block (codex, claude) or a tripwire block (grok) the
// verified table does not cover at this pin. Supplied harnesses with no
// command block (agy, opencode) and file_tool legs, which resolve to
// supplied, are never refused here.
func ReviewIsolationRefusal(doc Document, name string) *Refusal {
	entry, found := doc.For(name)
	if !found {
		return nil
	}
	switch entry.ReadMode() {
	case ReadModeServed:
		if IsolationVerified(name, entry.Install.PinnedVersion) {
			return nil
		}
	case ReadModeSupplied:
		if name != "grok" || IsolationVerified(name, entry.Install.PinnedVersion) {
			return nil
		}
	default:
		return nil
	}
	return &Refusal{
		Reason: fmt.Sprintf("the %s review leg cannot be verified at pin %s (review_isolation_unverified)", name, entry.Install.PinnedVersion),
		Action: "The served-or-tripwire command block is verified on each pinned version, or the pin moves. Verify the flags on this version and record the pin, or point this leg at another harness with --harness.",
		Kind:   ErrIsolationUnverified,
	}
}

// InstalledIsolationRefusal checks the CLI that will actually run against
// the verified command block: the descriptor pin says what the operator
// asked for, and a local install at a different version passes that gate
// while running unverified flags. The probe is `<binary> --version`, which
// starts no model and costs no call. An install that is not the verified
// version, a probe that fails, or a banner naming no version is refused
// with review_isolation_unverified before any child starts. Adapters with
// no probe — supplied harnesses with no command block — are unaffected.
//
// Both halves have to hold: the pin's block verified (grok carries no entry
// at any pin) and the installed version exactly the verified one. General
// compatibility history is not isolation evidence: an intermediate version
// between two recorded runs, or a prerelease token over a verified one,
// was never verified under this served-or-tripwire configuration and is
// refused the way an unknown version is.
func InstalledIsolationRefusal(ctx context.Context, runner exec.Runner, adapter Adapter, name, pin string, inv Invocation) *Refusal {
	pinned, ok := adapter.(VersionPinned)
	if !ok {
		return nil
	}
	action := "The served-or-tripwire command block is verified on each pinned version, or the pin moves. Verify the flags on the installed version and record the pin, or point this leg at another harness with --harness."
	if !IsolationVerified(name, pin) {
		return &Refusal{
			Reason: fmt.Sprintf("the %s command block carries no verified pin (review_isolation_unverified)", name),
			Action: action,
			Kind:   ErrIsolationUnverified,
		}
	}
	res := runner.Run(ctx, pinned.VersionProbe(inv))
	token := ""
	if res.Err == nil && res.ExitCode == 0 {
		token = versionToken.FindString(string(res.Stdout))
	}
	if token == "" {
		return &Refusal{
			Reason: fmt.Sprintf("the installed %s CLI reported no version, so the verified command block cannot be confirmed (review_isolation_unverified)", name),
			Action: action,
			Kind:   ErrIsolationUnverified,
		}
	}
	want, _ := verifiedCommandBlocks[name]
	if normalizeIsolationToken(token) != want {
		return &Refusal{
			Reason: fmt.Sprintf("the installed %s CLI reports version %s, which carries no verified command block for this configuration (review_isolation_unverified)", name, token),
			Action: action,
			Kind:   ErrIsolationUnverified,
		}
	}
	return nil
}

// normalizeIsolationToken strips the probe banner down to the comparable
// release: a leading `v` names the same release, while any prerelease
// suffix names a different one. The comparison stays an exact string
// match, never a numeric span.
func normalizeIsolationToken(token string) string {
	return strings.TrimPrefix(token, "v")
}

// --- the served tool ---------------------------------------------------------

// ServeConfig is the read-server command one served call hands its harness:
// the crossrev binary re-executed as `__read-server` over stdio.
type ServeConfig struct {
	// Command is the crossrev binary path.
	Command string
	// Args are the server argv after the program name, starting with
	// `__read-server`.
	Args []string
}

// MCPServer answers the MCP server object for an MCP config file: the
// command plus its args array.
func (s *ServeConfig) MCPServer() map[string]any {
	if s == nil {
		return nil
	}
	args := make([]any, 0, len(s.Args))
	for _, arg := range s.Args {
		args = append(args, arg)
	}
	return map[string]any{"command": s.Command, "args": args}
}

// tomlQuote renders one TOML basic string, which is what `codex exec -c`
// parses the config override as. Paths and flags carry no escapes in
// practice; Quote covers the ones that do.
func tomlQuote(text string) string { return strconv.Quote(text) }

// TOMLArgs renders the served args as the TOML array `codex exec -c`
// accepts for mcp_servers.crossrev.args.
func (s *ServeConfig) TOMLArgs() string {
	quoted := make([]string, 0, len(s.Args))
	for _, arg := range s.Args {
		quoted = append(quoted, tomlQuote(arg))
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

// CodexConfigArgs answers the `-c` pairs wiring the served tool into a codex
// leg: the server command, its args array, and the per-server approval mode
// that lets the model call the tool without a headless prompt it cannot
// answer. The approval key is per server, not top level: a top-level key
// approves nothing and leaves read_file unapproved on a headless leg.
func (s *ServeConfig) CodexConfigArgs() []string {
	return []string{
		"-c", "mcp_servers.crossrev.command=" + tomlQuote(s.Command),
		"-c", "mcp_servers.crossrev.args=" + s.TOMLArgs(),
		"-c", "mcp_servers.crossrev.default_tools_approval_mode=" + tomlQuote("approve"),
	}
}

// mcpConfigFile is the MCP config filename the claude adapter writes under
// Invocation.Scratch.
const mcpConfigFile = "crossrev-mcp.json"

// WriteMCPConfig writes the MCP config naming the served tool and answers
// its path. The scratch directory is the orchestrator's, outside the
// quarantined checkout, so repository-provided MCP servers cannot merge
// beside it — and --strict-mcp-config keeps the session from loading any
// other file.
func WriteMCPConfig(scratch string, serve *ServeConfig) (string, error) {
	if scratch == "" {
		return "", &Refusal{
			Reason: "the served adapter was given no scratch directory",
			Action: "The MCP config travels in a file outside the workspace, so the orchestrator has to name a directory to write one into.",
			Kind:   ErrScratch,
		}
	}
	if serve == nil || serve.Command == "" {
		return "", &Refusal{
			Reason: "the served adapter was given no serve command",
			Action: "A served leg hands the harness the read-server command; without one the leg cannot claim the served mode. This is a CrossRev bug.",
			Kind:   ErrScratch,
		}
	}
	document := map[string]any{"mcpServers": map[string]any{"crossrev": serve.MCPServer()}}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", &Refusal{
			Reason: "the served MCP config did not build as valid JSON",
			Action: "This is a CrossRev bug. The config is what grants a served leg its only read tool, so the leg is refused rather than run without it.",
			Kind:   ErrScratch,
		}
	}
	path := filepath.Join(scratch, mcpConfigFile)
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return "", &Refusal{
			Reason: "the served adapter could not write its MCP config",
			Action: "Check that the scratch directory is writable.",
			Kind:   ErrScratch,
			Err:    err,
		}
	}
	return path, nil
}

// --- the review-leg tripwire -------------------------------------------------

// ReviewCommand reports whether a review-leg transcript shows the harness
// running a command. A review leg reads without running commands; any
// command event halts with review_leg_ran_command, discards the call
// unpublished, and redacts the command into the run log only. Served read
// calls are the leg's own work and never trip. agy emits no tool events and
// opencode is denied its shell through its isolation config with no command
// record to watch, so neither trips here and doctor names both gaps.
func ReviewCommand(harnessName string, stdout []byte) (string, bool) {
	switch harnessName {
	case "codex":
		return CodexResolveCommand(stdout)
	case "claude":
		return ClaudeResolveCommand(stdout)
	case "grok":
		return GrokResolveCommand(stdout)
	default:
		return "", false
	}
}

// ReviewCommandRefusal is the halt for a command event on a review leg. The
// command itself reaches the run log only, redacted there; the refusal names
// the failure mode every surface reports it under.
func ReviewCommandRefusal(harnessName string) *Refusal {
	return &Refusal{
		Reason: fmt.Sprintf("the %s harness ran a command on a review leg that reads without running commands (review_leg_ran_command)", harnessName),
		Action: "The review leg denies commands, so nothing has been written to the pull request and the call is discarded unpublished. The command is in the run log. Re-run the leg; if it trips again, the harness is running commands its flags should deny.",
		Kind:   ErrReviewCommand,
	}
}

// RedactedCommand is the run-log half of the halt: the command, redacted,
// for the run log only.
func RedactedCommand(command string) string {
	return "review command: " + runlog.Redact(command)
}
