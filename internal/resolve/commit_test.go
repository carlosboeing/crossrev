package resolve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/sandbox"
	"github.com/carlosboeing/crossrev/internal/vcs"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// TestCommit pins restore-before-commit, git.hooks, one commit for the
// current resolver change, and a kept worktree on refusal
// (lib/run.sh:2222-2278, lib/github.sh:436-527).
func TestCommit(t *testing.T) {
	t.Run("quarantine is restored before the commit snapshot", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.git.beforeCommit = func(dir string) {
			q := filepath.Join(dir, sandbox.QuarantineDir)
			if _, err := os.Stat(q); err == nil {
				t.Error("quarantine was still in the tree when commit ran")
			}
		}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if e.git.commitCalls == 0 {
			t.Fatal("commit was not attempted")
		}
	})

	t.Run("git.hooks skip passes --no-verify", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.git.show = map[string][]byte{
			e.base.SHA() + ":.github/crossrev.yml": []byte("version: 1\nresolver:\n  harness: claude\ngit:\n  hooks: skip\n"),
		}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if e.git.commitOpts.RunHooks {
			t.Fatal("hooks ran on git.hooks: skip")
		}
		if e.git.pushHooks {
			t.Fatal("push hooks ran on git.hooks: skip")
		}
	})

	t.Run("git.hooks run honours the repository's own hooks", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.git.show = map[string][]byte{
			e.base.SHA() + ":.github/crossrev.yml": []byte("version: 1\nresolver:\n  harness: claude\ngit:\n  hooks: run\n"),
		}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if !e.git.commitOpts.RunHooks {
			t.Fatal("hooks were skipped on git.hooks: run")
		}
		if !e.git.pushHooks {
			t.Fatal("push hooks were skipped on git.hooks: run")
		}
	})

	t.Run("one commit for the current resolver change", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if e.git.commitCalls != 1 {
			t.Fatalf("commitCalls = %d, want 1", e.git.commitCalls)
		}
		if e.git.pushCalls != 1 {
			t.Fatalf("pushCalls = %d, want 1", e.git.pushCalls)
		}
		if got.Marker.CommitSHA.Value() == "" {
			t.Fatal("commit sha was not recorded on the marker")
		}
	})

	t.Run("a rejected subject falls back to the generic one", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.adapter.payloads = []json.RawMessage{json.RawMessage(
			`{"blocked":false,"blocked_reason":null,"summary":"Fixed.","commit_subject":"line\nbreak","resolutions":[{"finding_number":1,"resolution":"fixed","reply":"done","persist":null,"duplicate_of":null}]}`,
		)}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if !strings.Contains(e.git.commitOpts.Message, "fix: resolve crossrev review findings (pass 1)") {
			t.Fatalf("generic subject missing: %q", e.git.commitOpts.Message)
		}
		if strings.Contains(e.git.commitOpts.Message, "line\nbreak") {
			t.Fatal("rejected subject was used")
		}
		want := "the resolver's commit subject was rejected, so the commit carries a generic one\n   A subject must be one line of at most 100 characters, with no control characters. The fix itself is unaffected."
		if !strings.Contains(ui.Joined(got.Messages), want) {
			t.Errorf("messages = %q, want warning %q", got.Messages, want)
		}
	})

	t.Run("a prior commit_sha skips the fix step", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		claim := prstate.Marker{
			Version:     core.MarkerVersion,
			Leg:         core.LegResolve,
			Pass:        1,
			State:       core.PassStarted,
			TS:          e.now.Unix(),
			HeadSHA:     prstate.Some(e.head.SHA()),
			Harness:     prstate.Some("claude"),
			Blocked:     prstate.Some(false),
			CommitSHA:   prstate.Some("cccccccccccccccccccccccccccccccccccccccc"),
			Summary:     prstate.Some("already pushed"),
			Resolutions: json.RawMessage(`[{"finding_id":"` + testFinding + `","resolution":"fixed","reply":"done"}]`),
		}
		e.addResolve(t, claim, 9100)
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if e.git.commitCalls != 0 {
			t.Fatalf("committed again: %d", e.git.commitCalls)
		}
		if got.Marker.CommitSHA.Value() != "cccccccccccccccccccccccccccccccccccccccc" {
			t.Fatalf("commit sha = %q", got.Marker.CommitSHA.Value())
		}
	})

	t.Run("a refused commit keeps the worktree", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.git.commitErr = &vcs.Refusal{
			Message: "could not commit the resolver's changes — the hook says no",
			Hint:    "This repository sets git.hooks: run, so its own commit hooks ran on this commit and one of them may have refused it. Setting git.hooks: skip makes the resolver commit the way it already does on a GitHub-hosted runner, which has no hooks installed. Check `git status` in the checkout.",
		}
		got := e.run(t)
		if got.Outcome != OutcomeRefused {
			t.Fatalf("Outcome = %q, want refused", got.Outcome)
		}
		if e.git.worktrees == nil || len(*e.git.worktrees) == 0 {
			t.Fatal("worktree was not created")
		}
		dir := (*e.git.worktrees)[0]
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("failed worktree was removed: %v", err)
		}
		if e.git.removedWorktree {
			t.Fatal("worktree was removed after a refused commit")
		}
	})

	t.Run("a deferral-only repository write still commits", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.git.show = map[string][]byte{
			e.base.SHA() + ":.github/crossrev.yml": repositoryBacklogConfig("folder", ".crossrev/backlog"),
		}
		e.adapter.payloads = []json.RawMessage{deferredPayload(persistDoc(), nil)}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if e.git.commitCalls != 1 {
			t.Fatalf("commitCalls = %d, want 1 for a backlog write", e.git.commitCalls)
		}
		if !strings.HasPrefix(e.git.commitOpts.Message, "chore: record deferred crossrev findings (pass 1)") {
			t.Fatalf("deferral subject = %q", e.git.commitOpts.Message)
		}
	})

	t.Run("resolutions and findings key order is preserved in commit message", func(t *testing.T) {
		customFinding := `{"id":"` + testFinding + `","path":"a.go","line":3,"severity":"high","title":"Bug title"}`
		findings, err := harness.DecodeStream([]byte(customFinding))
		if err != nil {
			t.Fatalf("decode findings: %v", err)
		}
		customResolution := `{"finding_id":"` + testFinding + `","reply":"fixed bug","resolution":"fixed"}`
		recs, err := harness.DecodeStream([]byte(customResolution))
		if err != nil {
			t.Fatalf("decode recs: %v", err)
		}

		l := &Leg{}
		s := &session{
			pass: 1,
			repo: mustSlug(t),
			req:  Request{PR: 42},
		}
		marker := prstate.Marker{
			HeadSHA: prstate.Some("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		}

		msg, _ := l.commitMessage(s, recs, findings, marker, 1)
		if !strings.Contains(msg, "- Bug title.") {
			t.Fatalf("missing title bullet in commit message: %s", msg)
		}
		marshaledRecs := marshalResolutions(recs)
		wantRecs := `[{"finding_id":"` + testFinding + `","reply":"fixed bug","resolution":"fixed"}]`
		if string(marshaledRecs) != wantRecs {
			t.Fatalf("marshalResolutions = %s, want %s", string(marshaledRecs), wantRecs)
		}
		marshaledFindings, _ := json.Marshal(findings)
		wantFindings := `[{"id":"` + testFinding + `","path":"a.go","line":3,"severity":"high","title":"Bug title"}]`
		if string(marshaledFindings) != wantFindings {
			t.Fatalf("marshalFindings = %s, want %s", string(marshaledFindings), wantFindings)
		}
	})

	t.Run("resolver claims a fix but leaves no staged changes warns", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = false
		e.adapter.payloads = []json.RawMessage{json.RawMessage(
			`{"blocked":false,"blocked_reason":null,"summary":"Fixed.","resolutions":[{"finding_number":1,"resolution":"fixed","reply":"done"}]}`,
		)}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		want := "the resolver reported 1 fix(es) but changed no files\n   The replies below will claim a fix that is not in the diff, so their threads stay open and the pass halts for a person. Treat those resolutions as unverified and read the thread before merging."
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
}
