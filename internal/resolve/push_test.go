package resolve

import (
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// TestPush pins the push guard, a single push of the current change, and
// pushInsteadOf remaining a warning (lib/github.sh:469-527, lib/legs.sh:392-439).
func TestPush(t *testing.T) {
	t.Run("fixed files reach the origin once", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.git.commitSHA = "dddddddddddddddddddddddddddddddddddddddd"
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if e.git.pushCalls != 1 {
			t.Fatalf("pushCalls = %d, want 1", e.git.pushCalls)
		}
		if e.git.pushBranch != "feature" {
			t.Fatalf("push branch = %q, want feature", e.git.pushBranch)
		}
		if e.git.pushRemote != "origin" {
			t.Fatalf("push remote = %q, want origin", e.git.pushRemote)
		}
		if got.Marker.CommitSHA.Value() != e.git.commitSHA {
			t.Fatalf("marker commit = %q, want %q", got.Marker.CommitSHA.Value(), e.git.commitSHA)
		}
	})

	t.Run("an unreadable remote head produces the full concurrent push warning", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.git.remoteHeadErr = errors.New("remote unreachable")
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		want := "could not read feature on origin, so the check for a concurrent push did not run\n   If someone pushed to that branch while this leg was working, this push may not include their commit. Confirm the branch looks right before merging."
		found := false
		for _, m := range got.Messages {
			if m == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("messages = %q, want warning %q", got.Messages, want)
		}
	})

	t.Run("a moved remote head refuses the push and keeps the commit", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.git.commitSHA = "dddddddddddddddddddddddddddddddddddddddd"
		e.git.remoteHead = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		got := e.run(t)
		if got.Outcome != OutcomeRefused {
			t.Fatalf("Outcome = %q, want refused", got.Outcome)
		}
		if got.Err == nil || !strings.Contains(got.Err.Error(), "moved while this leg was running") {
			t.Errorf("Err = %v, want the concurrent-push refusal", got.Err)
		}
		if e.git.pushCalls != 0 {
			t.Fatal("pushed over a moved head")
		}
		if e.git.removedWorktree {
			t.Fatal("worktree was removed after a refused push")
		}
	})

	t.Run("pushInsteadOf is a warning not a refusal", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.git.commitSHA = "dddddddddddddddddddddddddddddddddddddddd"
		e.git.pushTarget = vcs.PushTarget{
			Repo: e.slug,
			Warnings: []vcs.Warning{{
				Message: "remote 'origin' push URL 'https://github.com/acme/widget.git' is rewritten to 'git@github.com:acme/widget.git'",
				Hint:    "The guard approved the configured URL, but git push will send commits to the rewritten one.",
			}},
		}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("pushInsteadOf became a refusal: %v", got.Err)
		}
		if e.git.pushCalls != 1 {
			t.Fatalf("pushCalls = %d, want 1", e.git.pushCalls)
		}
		found := false
		for _, m := range got.Messages {
			if strings.Contains(m, "rewritten") {
				found = true
			}
		}
		if !found {
			t.Fatalf("warning missing from messages: %v", got.Messages)
		}
	})

	t.Run("a head-repo mismatch refuses before the commit leaves", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		other, err := core.ParseSlug("other/widget")
		if err != nil {
			t.Fatal(err)
		}
		e.git.pushMismatch = other
		got := e.run(t)
		if got.Outcome != OutcomeRefused {
			t.Fatalf("Outcome = %q, want refused", got.Outcome)
		}
		if got.Err == nil || !strings.Contains(got.Err.Error(), "pushes to") {
			t.Errorf("Err = %v, want the head-repo mismatch", got.Err)
		}
		if e.git.pushCalls != 0 {
			t.Fatal("pushed to the wrong repository")
		}
	})

	t.Run("a refused push keeps the worktree and the reason", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.git.commitSHA = "dddddddddddddddddddddddddddddddddddddddd"
		e.git.pushErr = &vcs.Refusal{
			Message: "could not push to feature — remote rejected",
			Hint:    "The commit exists locally. If branch protection refused it, that is the backstop working — check the rule, or push by hand.",
		}
		got := e.run(t)
		if got.Outcome != OutcomeRefused {
			t.Fatalf("Outcome = %q, want refused", got.Outcome)
		}
		if !strings.Contains(got.Err.Error(), "could not push to feature") {
			t.Errorf("Err = %v", got.Err)
		}
		if e.git.removedWorktree {
			t.Fatal("worktree was removed after a refused push")
		}
	})
}
