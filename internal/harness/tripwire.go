// tripwire.go — the resolve-leg command tripwire.
//
// A resolve leg edits files and never runs commands. The deny flags on each
// adapter hold that line, and the tripwire keeps watching it afterwards: when
// a command event appears in the harness's own tool record, the leg fails
// before any push, naming the harness and the command.
//
// The event shapes come from outside this repository, verified against the
// installed CLIs' --help and the public event references each harness
// publishes:
//
//   - codex `exec --json` streams `item.completed` events whose item is either
//     a `file_change` (an edit) or a `command_execution` carrying `command`
//     (codex-cli-sdk-go's event table; life-is-blue's observed JSONL).
//   - claude `--output-format stream-json` streams assistant events whose
//     content holds `tool_use` blocks naming each tool, with a terminal
//     `result` event carrying the same usage and cost as `json`
//     (corticalstack's output-formats reference; the terminal-result note).
//   - grok `--output-format streaming-json` streams one ACP update per line,
//     with `tool_call` events carrying `toolName` and `rawInput` and a
//     terminal `end` event carrying the usage envelope (the headless-mode
//     reference; the tool_call transcript fix).
//
// agy `--output-format json` carries no tool events at all, so there is
// nothing to watch: commands denied, no tripwire, a documented gap in
// docs/troubleshooting.md.

package harness

import (
	"strings"
)

// ResolveCommand reports whether a resolve-leg transcript shows the harness
// running a command. It reads the harness's own tool record — the same stdout
// the envelope already parsed — so a denial the flags missed still fails the
// leg before anything is pushed. The command is the string to name in the
// refusal. agy and opencode never trip: agy emits no tool events, and opencode
// is denied its shell through its isolation config with no command record to
// watch.
func ResolveCommand(harnessName string, stdout []byte) (string, bool) {
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

// CodexResolveCommand is the codex half of the tripwire: the first
// command_execution item's command. file_change items are edits and never
// trip.
func CodexResolveCommand(stdout []byte) (string, bool) {
	nodes, err := decodeStream(stdout)
	if err != nil {
		nodes = nil
		for _, line := range strings.Split(string(stdout), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if n, err := DecodeOrdered([]byte(trimmed)); err == nil {
				nodes = append(nodes, n)
			}
		}
	}
	for _, event := range nodes {
		item := event.member("item")
		if !item.present() {
			continue
		}
		kind, _ := item.member("type").asString()
		if !strings.Contains(strings.ToLower(kind), "command") {
			continue
		}
		if command, ok := item.member("command").asString(); ok && command != "" {
			return command, true
		}
		// A command item with no command string is still a command run;
		// name what the record does carry rather than passing it silently.
		if aggregated, ok := item.member("aggregated_output").asString(); ok && aggregated != "" {
			return aggregated, true
		}
		return kind, true
	}
	return "", false
}

// ClaudeResolveCommand is the claude half: the first Bash tool_use block's
// command. Edit, Write and Read blocks are the resolve leg's own work and
// never trip.
func ClaudeResolveCommand(stdout []byte) (string, bool) {
	for _, line := range strings.Split(string(stdout), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		event, err := decodeOrdered([]byte(trimmed))
		if err != nil {
			continue
		}
		if command, found := claudeLineCommand(event); found {
			return command, true
		}
	}
	// A buffered json object carries no tool record, so there is nothing to
	// trip on — which is why the resolve leg streams.
	return "", false
}

func claudeLineCommand(event node) (string, bool) {
	// Assistant events carry content blocks; a bare tool_use object decodes
	// the same way, so check both shapes.
	contents := event.member("message").member("content")
	if contents.kind == kindArray {
		for _, block := range contents.items {
			if command, found := claudeBlockCommand(block); found {
				return command, true
			}
		}
	}
	if command, found := claudeBlockCommand(event); found {
		return command, true
	}
	return "", false
}

func claudeBlockCommand(block node) (string, bool) {
	kind, _ := block.member("type").asString()
	if kind != "tool_use" {
		return "", false
	}
	name, _ := block.member("name").asString()
	if !isClaudeShellTool(name) {
		return "", false
	}
	if command, ok := block.member("input").member("command").asString(); ok && command != "" {
		return command, true
	}
	return name, true
}

// isClaudeShellTool matches the Bash tool under the spellings the CLI
// reports: "Bash" and the permission-qualified "Bash(...)".
func isClaudeShellTool(name string) bool {
	if strings.EqualFold(name, "Bash") {
		return true
	}
	if len(name) > 5 && strings.EqualFold(name[:4], "bash") && (name[4] == '(' || name[4] == ' ') {
		return true
	}
	return false
}

// GrokResolveCommand is the grok half: the first tool_call whose rawInput
// carries a command. The toolName is what the record calls the shell;
// rawInput.command is the command to name. Edit and write calls carry file
// paths, not commands, and never trip.
func GrokResolveCommand(stdout []byte) (string, bool) {
	for _, line := range strings.Split(string(stdout), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		event, err := decodeOrdered([]byte(trimmed))
		if err != nil {
			continue
		}
		if kind, _ := event.member("type").asString(); kind != "tool_call" {
			continue
		}
		raw := event.member("rawInput")
		if !raw.truthy() {
			continue
		}
		if command, ok := raw.member("command").asString(); ok && command != "" {
			return command, true
		}
	}
	return "", false
}
