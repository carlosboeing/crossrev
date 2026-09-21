package review_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// A codec test proves the config parsed. This proves the leg obeyed it: a
// plural-only reviewers: config must run the harness it names, not the
// singular default sitting underneath it in the merge.
func TestAPluralOnlyConfigRunsTheHarnessItNames(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.cfg = mustConfig(t, "version: 2\nreviewers:\n  - id: reviewer1\n    harness: claude\n    model: opus\n    effort: high\n")
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(convergedPayload())}}

	req := e.request(t)
	req.HarnessOverride = ""
	leg := e.leg(t)
	got := leg.Run(t.Context(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	specs := e.runner.Specs()
	if len(specs) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(specs))
	}
	invoked := specs[0]
	if invoked.Path != "claude" {
		t.Fatalf("a plural-only config ran %q; the default codex fallback is still in the path", invoked.Path)
	}
	if !hasFlagPair(invoked.Args, "--model", "opus") {
		t.Errorf("the invoked harness was not asked for model opus: %q", invoked.Args)
	}
	if !hasFlagPair(invoked.Args, "--effort", "high") {
		t.Errorf("the invoked harness was not asked for high effort: %q", invoked.Args)
	}
}

// The endpoint is resolved from the leg settings, so it must come from the
// canonical reviewer too: a plural-only config naming an endpoint must reach
// the child, not the vendor's API.
func TestTheEndpointComesFromTheCanonicalReviewer(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.cfg = mustConfig(t, "version: 2\nreviewers:\n  - id: reviewer1\n    harness: claude\n    endpoint: ollama\nendpoints:\n  ollama:\n    base_url: http://localhost:11434\n    token_env: OLLAMA_TOKEN\n")
	e.legEnv = []string{"PATH=/usr/bin", "HOME=/tmp", "OLLAMA_TOKEN=t"}
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(convergedPayload())}}

	req := e.request(t)
	req.HarnessOverride = ""
	leg := e.leg(t)
	got := leg.Run(t.Context(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.runner.specs) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(e.runner.specs))
	}
	env := strings.Join(e.runner.specs[0].Env, " ")
	if !strings.Contains(env, "ANTHROPIC_BASE_URL=http://localhost:11434") {
		t.Errorf("the child did not get the canonical reviewer's base URL: %q", env)
	}
	if marker, _ := got.Marker.MarshalJSON(); !strings.Contains(string(marker), `"endpoint":"ollama"`) {
		t.Errorf("the marker does not name the endpoint: %s", marker)
	}
}

// The regression guard on the other side: reviewer: configs are the common
// case and must be untouched.
func TestTheShorthandStillRunsWhatItAlwaysDid(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.cfg = mustConfig(t, "version: 2\nreviewer:\n  harness: claude\n  model: opus\n")
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(convergedPayload())}}

	req := e.request(t)
	req.HarnessOverride = ""
	leg := e.leg(t)
	got := leg.Run(t.Context(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	specs := e.runner.Specs()
	if len(specs) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(specs))
	}
	if specs[0].Path != "claude" {
		t.Errorf("a shorthand config ran %q, want claude", specs[0].Path)
	}
	if !hasFlagPair(specs[0].Args, "--model", "opus") {
		t.Errorf("the invoked harness was not asked for model opus: %q", specs[0].Args)
	}
}

// hasFlagPair reports whether args carries flag followed by value.
func hasFlagPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
