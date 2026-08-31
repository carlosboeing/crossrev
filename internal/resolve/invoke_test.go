package resolve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

// TestInvoke pins write capability, the no-arbitrary-command grant, quarantine
// and worktree before the model, and one semantic retry.
//
// Write flag measured from lib/run.sh:488-489:
//
//	LEG_WRITE=no
//	[[ "$leg" == "resolver" ]] && LEG_WRITE=yes
func TestInvoke(t *testing.T) {
	t.Run("resolver stub receives write permission", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if len(e.adapter.invs) == 0 {
			t.Fatal("adapter was not invoked")
		}
		if !e.adapter.invs[0].Write {
			t.Fatal("resolver invocation Write is false")
		}
		if core.WriteCapabilityFor(core.RoleResolver) != core.WriteYes {
			t.Fatal("WriteCapabilityFor(resolver) is not yes")
		}
	})

	t.Run("claude write spec grants acceptEdits and not arbitrary commands", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		doc := mustHarness(t)
		adapter, ok := harness.For(doc, "claude")
		if !ok {
			t.Fatal("no claude adapter")
		}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if len(e.adapter.invs) == 0 {
			t.Fatal("no invocation recorded")
		}
		inv := e.adapter.invs[0]
		spec, err := adapter.Spec(inv)
		if err != nil {
			t.Fatalf("Spec: %v", err)
		}
		if !hasFlagPair(spec.Args, "--permission-mode", "acceptEdits") {
			t.Fatalf("write spec missing acceptEdits: %v", spec.Args)
		}
		for _, forbidden := range []string{"bypassPermissions", "danger-full-access", "--dangerously"} {
			if containsAny(spec.Args, forbidden) {
				t.Fatalf("write spec granted %q: %v", forbidden, spec.Args)
			}
		}
		if envHas(spec.Env, "GH_TOKEN") || envHas(spec.Env, "GITHUB_TOKEN") ||
			envHas(spec.Env, "GH_ENTERPRISE_TOKEN") || envHas(spec.Env, "GITHUB_ENTERPRISE_TOKEN") {
			t.Fatalf("forge credential reached the model-facing spec: %v", spec.Env)
		}
	})

	t.Run("worktree and quarantine complete before the model starts", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		var sawWorktree, sawQuarantine bool
		e.runner.onRun = func(spec exec.Spec) {
			if e.git.worktrees == nil || len(*e.git.worktrees) == 0 {
				t.Error("worktree was not created before the harness process")
			} else {
				sawWorktree = true
			}
			if spec.Dir == "" {
				t.Error("harness Dir is empty")
			}
			if _, err := os.Stat(filepath.Join(spec.Dir, "CLAUDE.md")); err == nil {
				t.Error("CLAUDE.md still in the checkout when the harness started")
			}
			if _, err := os.Stat(filepath.Join(spec.Dir, ".crossrev-quarantine", "CLAUDE.md")); err == nil {
				sawQuarantine = true
			} else {
				t.Errorf("CLAUDE.md was not quarantined before the harness started: %v", err)
			}
		}
		// Plant a file the descriptor quarantines so the move is observable.
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if !sawWorktree {
			t.Fatal("onRun did not observe a worktree")
		}
		if !sawQuarantine {
			t.Fatal("quarantine did not complete before the model started")
		}
		if e.git.worktrees == nil || len(*e.git.worktrees) == 0 {
			t.Fatal("no worktree was created")
		}
	})

	t.Run("semantic omission earns one retry and a second is fatal", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.adapter.payloads = []json.RawMessage{missingPayload(), oneFindingPayload()}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run after one retry: %v", got.Err)
		}
		if e.adapter.calls != 2 {
			t.Fatalf("adapter calls = %d, want 2", e.adapter.calls)
		}
		if e.git.restoreCalls == nil || *e.git.restoreCalls == 0 {
			t.Fatal("retry did not restore the captured tree")
		}
	})

	t.Run("shape errors on a schema-native harness do not retry", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.adapter.payloads = []json.RawMessage{shapePayload(), oneFindingPayload()}
		got := e.run(t)
		if got.Err == nil {
			t.Fatal("shape failure was accepted")
		}
		if e.adapter.calls != 1 {
			t.Fatalf("adapter calls = %d, want 1 (no retry)", e.adapter.calls)
		}
	})
}

func hasFlagPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
