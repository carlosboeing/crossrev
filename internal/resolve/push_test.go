package resolve

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/vcs"

	"github.com/carlosboeing/crossrev/internal/ui"
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
		for _, m := range ui.Texts(got.Messages) {
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
		for _, m := range ui.Texts(got.Messages) {
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

	// A resolver's fix survives a failed push: the resolutions reach the marker
	// only after the push lands, so a failed push leaves the claim open (started,
	// no resolutions) and exactly one re-run is a fresh invoke that recommits and
	// pushes. Deferred-issue filing runs on both attempts, and the second matches
	// the first attempt's issue instead of filing again.
	t.Run("a failed push redrives with a fresh invoke", func(t *testing.T) {
		findings := json.RawMessage(`[
  {"id":"aaaaaaaaaaaaaaaa","path":"app.ts","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"nil deref","why":"crash","fix":"check"},
  {"id":"bbbbbbbbbbbbbbbb","path":"app.ts","line":5,"side":"RIGHT","severity":"medium","category":"correctness","pre_existing":false,"title":"follow-up","why":"later","fix":"later"}
]`)
		payload := json.RawMessage(`{"blocked":false,"blocked_reason":null,"summary":"Fixed and deferred.","commit_subject":null,"resolutions":[
  {"finding_number":1,"resolution":"fixed","reply":"done","persist":null,"duplicate_of":null},
  {"finding_number":2,"resolution":"deferred","reply":"needs a follow-up","persist":{"title":"Follow-up","body":"Measured before filing."},"duplicate_of":null}]}`)

		e := setup(t)
		e.addReview(t, findings, "issues-remain")
		e.git.show = map[string][]byte{e.base.SHA() + ":.github/crossrev.yml": githubIssuesConfig()}
		e.git.staged = true
		e.git.pushErr = errors.New("remote rejected the push: boom")
		e.adapter.payloads = []json.RawMessage{payload, payload}
		e.forge.threads = []forge.ReviewThread{
			{ID: "thread-1", Path: "app.ts", Line: 2, RootCommentID: 55, FindingIDs: []core.FindingID{mustFinding(t)}},
			{ID: "thread-2", Path: "app.ts", Line: 5, RootCommentID: 56, FindingIDs: []core.FindingID{mustFindingID(t, "bbbbbbbbbbbbbbbb")}},
		}

		first := e.run(t)
		if first.Outcome != OutcomeRefused {
			t.Fatalf("first Outcome = %q, want refused", first.Outcome)
		}
		if first.Err == nil || !strings.Contains(first.Err.Error(), "remote rejected") {
			t.Fatalf("first Err = %v, want the push failure", first.Err)
		}
		if e.git.pushCalls != 1 {
			t.Fatalf("pushCalls = %d, want the one failed push", e.git.pushCalls)
		}
		if first.Marker.State != core.PassStarted {
			t.Fatalf("claim state = %q, want started: a failed push records nothing", first.Marker.State)
		}
		if resolutionCount(first.Marker) != 0 {
			t.Fatalf("claim carries %d resolutions, want none: a failed push records nothing", resolutionCount(first.Marker))
		}
		if len(e.forge.edits) != 0 {
			t.Fatalf("claim edits = %d, want 0: a failed push records nothing", len(e.forge.edits))
		}
		for _, name := range e.forge.addedLabels {
			if name == "crossrev/halted" {
				t.Fatal("a failed push halted the pass; the retry must resume the open claim")
			}
		}
		if len(e.forge.issues) != 1 {
			t.Fatalf("issues filed = %d, want the one deferred filing", len(e.forge.issues))
		}

		// The retry sees the already-filed issue the way production does: the
		// forge answers it by finding marker, so the second persist matches.
		e.forge.byFinding = map[string]int{"bbbbbbbbbbbbbbbb": e.forge.issues[0].Number}
		e.git.pushErr = nil

		second := e.run(t)
		if second.Err != nil {
			t.Fatalf("second Run: %v", second.Err)
		}
		if second.Outcome != OutcomeComplete {
			t.Fatalf("second Outcome = %q, want complete", second.Outcome)
		}
		if e.adapter.calls != 2 {
			t.Fatalf("adapter calls = %d, want a fresh invoke per attempt", e.adapter.calls)
		}
		if e.git.commitCalls != 2 || e.git.pushCalls != 2 {
			t.Fatalf("commit/push calls = %d/%d, want the retry to recommit and push", e.git.commitCalls, e.git.pushCalls)
		}
		if len(e.forge.issues) != 1 {
			t.Fatalf("issues filed = %d, want 1: the retry must match, not file again", len(e.forge.issues))
		}
		for _, msg := range ui.Texts(second.Messages) {
			if strings.Contains(msg, "already recorded its resolutions") {
				t.Fatalf("the retry skipped the resolver: %q", msg)
			}
			if strings.Contains(msg, "driving pass 1 again") {
				t.Fatalf("the retry redrove a settled pass instead of resuming the open claim: %q", msg)
			}
		}
		recorded := false
		for _, edit := range e.forge.edits {
			if strings.Contains(edit.Body, "resolutions recorded") && strings.Contains(edit.Body, "Pushed `") {
				recorded = true
			}
		}
		if !recorded {
			t.Fatal("no post-push resolutions-recorded claim edit after the retry")
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
