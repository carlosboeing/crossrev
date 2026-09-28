package harness_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// The tripwire reads each harness's own tool record. File edits never trip;
// a command event does, naming the command.

func TestTripwireCodexCommandEvent(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"sess-1"}`,
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"app.ts","kind":"modify"}]}}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"ls /tmp","exit_code":0}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":4}}`,
	}, "\n")
	command, found := harness.ResolveCommand("codex", []byte(stream))
	if !found {
		t.Fatal("a command_execution item did not trip")
	}
	if command != "ls /tmp" {
		t.Errorf("command = %q, want the shell command", command)
	}

	edits := strings.Join([]string{
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"app.ts","kind":"modify"}]}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":4}}`,
	}, "\n")
	if _, found := harness.ResolveCommand("codex", []byte(edits)); found {
		t.Error("file_change items tripped the codex wire")
	}
}

func TestTripwireClaudeCommandEvent(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"app.ts"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git status"}}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"{}"}`,
	}, "\n")
	command, found := harness.ResolveCommand("claude", []byte(stream))
	if !found {
		t.Fatal("a Bash tool_use block did not trip")
	}
	if command != "git status" {
		t.Errorf("command = %q, want the shell command", command)
	}

	edits := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"app.ts"}}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"{}"}`,
	}, "\n")
	if _, found := harness.ResolveCommand("claude", []byte(edits)); found {
		t.Error("Edit blocks tripped the claude wire")
	}
}

func TestTripwireGrokCommandEvent(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"tool_call","toolCallId":"1","toolName":"write","rawInput":{"file_path":"app.ts"}}`,
		`{"type":"tool_call","toolCallId":"2","toolName":"run_terminal_command","rawInput":{"command":"cat app.ts"}}`,
		`{"type":"end","stopReason":"EndTurn","sessionId":"s"}`,
	}, "\n")
	command, found := harness.ResolveCommand("grok", []byte(stream))
	if !found {
		t.Fatal("a tool_call carrying a command did not trip")
	}
	if command != "cat app.ts" {
		t.Errorf("command = %q, want the shell command", command)
	}

	edits := strings.Join([]string{
		`{"type":"tool_call","toolCallId":"1","toolName":"write","rawInput":{"file_path":"app.ts"}}`,
		`{"type":"end","stopReason":"EndTurn","sessionId":"s"}`,
	}, "\n")
	if _, found := harness.ResolveCommand("grok", []byte(edits)); found {
		t.Error("write calls tripped the grok wire")
	}
}

func TestTripwireAgyHasNoToolRecord(t *testing.T) {
	// agy's json output carries no tool events, so there is nothing to trip
	// on. Commands denied, no tripwire: a documented gap.
	stdout := []byte(`{"status":"SUCCESS","response":"{}","structured_output":{}}`)
	if _, found := harness.ResolveCommand("agy", stdout); found {
		t.Error("agy tripped with no tool record to read")
	}
	if _, found := harness.ResolveCommand("opencode", stdout); found {
		t.Error("opencode tripped with no command record to read")
	}
}
