package resolve

import (
	"os"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// TestRecovery pins that a fatal path edits only the open claim, never a
// completed review, and keeps the failed worktree and transcript
// (lib/run.sh:114-165, :718-762).
func TestRecovery(t *testing.T) {
	t.Run("a fatal path completes the open claim and not the review", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.git.commitErr = &vcs.Refusal{
			Message: "could not commit the resolver's changes — the hook says no",
			Hint:    "The working tree still holds them.",
		}
		got := e.run(t)
		if got.Outcome != OutcomeRefused {
			t.Fatalf("Outcome = %q, want refused", got.Outcome)
		}

		var lastResolve prstate.Marker
		var sawResolve bool
		for _, edit := range e.forge.edits {
			raw, ok := prstate.DecodeMarker(edit.Body)
			if !ok {
				continue
			}
			m, err := prstate.ParseMarker(raw)
			if err != nil {
				t.Fatalf("parse edit: %v", err)
			}
			if m.Leg == core.LegResolve {
				lastResolve = m
				sawResolve = true
			}
		}
		if !sawResolve {
			t.Fatal("the open claim was not completed")
		}
		if lastResolve.State != core.PassComplete {
			t.Errorf("claim state = %q, want complete", lastResolve.State)
		}
		reason, _ := lastResolve.BlockedReason.Get()
		if !strings.Contains(reason, "could not commit the resolver's changes") {
			t.Errorf("blocked_reason = %q", reason)
		}
		blocked, _ := lastResolve.Blocked.Get()
		if !blocked {
			t.Error("claim was not marked blocked")
		}
		for _, edit := range e.forge.edits {
			if edit.CommentID == 9001 {
				raw, ok := prstate.DecodeMarker(edit.Body)
				if !ok {
					continue
				}
				m, err := prstate.ParseMarker(raw)
				if err != nil {
					t.Fatal(err)
				}
				if v, _ := m.Verdict.Get(); v == string(core.VerdictBlocked) {
					t.Fatal("fatal path rewrote the completed review as blocked")
				}
			}
		}
	})

	t.Run("a completed resolve marker is not rewritten as blocked", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.addResolve(t, prstate.Marker{
			Pass:        1,
			State:       core.PassComplete,
			Blocked:     prstate.Some(false),
			CommitSHA:   prstate.Some("ffffffffffffffffffffffffffffffffffffffff"),
			Resolutions: []byte(`[{"finding_id":"` + testFinding + `","resolution":"fixed"}]`),
			Harness:     prstate.Some("claude"),
			HeadSHA:     prstate.Some(e.head.SHA()),
			Summary:     prstate.Some("done"),
		}, 9100)
		got := e.run(t)
		if got.Outcome != OutcomeAlreadyResolved && got.Err != nil {
			// settled with a commit is not redrivable
		}
		if got.Outcome != OutcomeAlreadyResolved {
			t.Fatalf("Outcome = %q, want already_resolved", got.Outcome)
		}
		for _, edit := range e.forge.edits {
			if edit.CommentID == 9100 {
				t.Fatalf("rewrote a completed resolve marker: %s", edit.Body)
			}
		}
	})

	t.Run("a failed run keeps the worktree and the run log", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.git.commitErr = &vcs.Refusal{Message: "could not commit the resolver's changes — boom", Hint: "check git status"}
		got := e.run(t)
		if got.Outcome != OutcomeRefused {
			t.Fatalf("Outcome = %q", got.Outcome)
		}
		if e.git.worktrees == nil || len(*e.git.worktrees) == 0 {
			t.Fatal("no worktree")
		}
		if e.git.removedWorktree {
			t.Fatal("worktree removed on failure")
		}
		if e.log.Dir() == "" {
			t.Fatal("run log directory missing")
		}
		if _, err := os.Stat(e.log.Dir()); err != nil {
			t.Fatalf("run log was removed: %v", err)
		}
	})

	t.Run("labels halt the pull request on a fatal claim", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.git.commitErr = &vcs.Refusal{Message: "could not commit the resolver's changes — boom", Hint: "check"}
		_ = e.run(t)
		halted := false
		for _, name := range e.forge.addedLabels {
			if name == "crossrev/halted" {
				halted = true
			}
		}
		if !halted {
			t.Fatalf("halted label missing: %v", e.forge.addedLabels)
		}
	})
}
