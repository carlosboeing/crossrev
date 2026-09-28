package harness_test

import (
	"slices"
	"strings"
	"testing"
)

// Resolve legs edit without running commands. Each settled harness denies
// commands on its resolve leg; codex, claude and grok also leave a tool
// record the resolve leg trips on, while agy carries no tool events at all.

func TestCodexResolveSpecDeniesCommands(t *testing.T) {
	adapter := codexAdapter(t)
	spec, err := adapter.Spec(invocation(t, "codex", true))
	if err != nil {
		t.Fatalf("building the resolve spec: %v", err)
	}
	if !hasFlagPair(spec.Args, "--disable", "shell_tool") {
		t.Errorf("a resolve leg disables the shell tool; got %v", spec.Args)
	}
	if !hasFlagPair(spec.Args, "--disable", "unified_exec") {
		t.Errorf("a resolve leg disables unified exec; got %v", spec.Args)
	}
	if !hasFlagPair(spec.Args, "--sandbox", "workspace-write") {
		t.Errorf("a resolve leg keeps its workspace-write sandbox; got %v", spec.Args)
	}
}

func TestClaudeResolveSpecDeniesCommands(t *testing.T) {
	adapter := claudeAdapter(t)
	spec, err := adapter.Spec(invocation(t, "claude", true))
	if err != nil {
		t.Fatalf("building the resolve spec: %v", err)
	}
	if !hasFlagPair(spec.Args, "--permission-mode", "acceptEdits") {
		t.Errorf("a resolve leg accepts edits; got %v", spec.Args)
	}
	if !hasFlagPair(spec.Args, "--disallowedTools", "Bash") {
		t.Errorf("a resolve leg denies Bash; got %v", spec.Args)
	}
	if !hasFlagPair(spec.Args, "--output-format", "stream-json") {
		t.Errorf("a resolve leg streams its tool record; got %v", spec.Args)
	}
}

func TestGrokResolveSpecDeniesCommands(t *testing.T) {
	adapter := grokAdapter(t)
	spec, err := adapter.Spec(invocation(t, "grok", true))
	if err != nil {
		t.Fatalf("building the resolve spec: %v", err)
	}
	if !hasFlagPair(spec.Args, "--output-format", "streaming-json") {
		t.Errorf("a resolve leg streams its tool record; got %v", spec.Args)
	}
	found := false
	for i := 0; i+1 < len(spec.Args); i++ {
		if spec.Args[i] == "--tools" {
			found = true
			allow := strings.Split(spec.Args[i+1], ",")
			for _, want := range []string{"Edit", "Write"} {
				if !slices.Contains(allow, want) {
					t.Errorf("--tools allowlist misses %s; got %q", want, spec.Args[i+1])
				}
			}
			for _, banned := range []string{"Bash", "bash", "shell", "run_terminal_command"} {
				if slices.Contains(allow, banned) {
					t.Errorf("--tools allowlist carries a shell tool %s; got %q", banned, spec.Args[i+1])
				}
			}
		}
	}
	if !found {
		t.Errorf("a resolve leg passes a --tools allowlist; got %v", spec.Args)
	}
	if !hasFlagPair(spec.Args, "--sandbox", "workspace") {
		t.Errorf("a resolve leg keeps its workspace sandbox; got %v", spec.Args)
	}
}

func TestAgyResolveSpecDeniesCommands(t *testing.T) {
	adapter := agyAdapter(t)
	spec, err := adapter.Spec(invocation(t, "agy", true))
	if err != nil {
		t.Fatalf("building the resolve spec: %v", err)
	}
	if !hasFlagPair(spec.Args, "--mode", "accept-edits") {
		t.Errorf("a resolve leg accepts edits; got %v", spec.Args)
	}
	joined := strings.Join(spec.Args, " ")
	for _, forbidden := range []string{"--dangerously-skip-permissions", "--yolo", "--always-approve", "bypassPermissions"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("a resolve leg passes no bypass flag, got %s: %v", forbidden, spec.Args)
		}
	}
}
