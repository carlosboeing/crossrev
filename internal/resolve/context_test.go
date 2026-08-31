package resolve

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/forge"
)

// TestContext pins context loading: one pull-request read, policy from the
// base revision, threads and backlog candidates through forge, and 1-based
// finding numbers.
func TestContext(t *testing.T) {
	t.Run("policy is read from the base revision", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.show = map[string][]byte{
			e.base.SHA() + ":.github/crossrev.yml": []byte("version: 1\nresolver:\n  harness: claude\n"),
		}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		var sawBase, sawHead bool
		for _, c := range e.git.showCalls {
			if c.Path == ".github/crossrev.yml" && c.Revision.SHA() == e.base.SHA() {
				sawBase = true
			}
			if c.Path == ".github/crossrev.yml" && c.Revision.SHA() == e.head.SHA() {
				sawHead = true
			}
		}
		if !sawBase {
			t.Fatal("config was not read at the base revision")
		}
		if sawHead {
			t.Fatal("config was read from the head revision")
		}
	})

	t.Run("threads load through forge and findings are numbered from 1", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.forge.threads = []forge.ReviewThread{{
			ID:            "thread-1",
			Path:          "app.ts",
			Line:          2,
			RootCommentID: 55,
			Comments: []forge.ThreadComment{{
				Author: "codex",
				Body:   "nil deref <!-- crossrev:f {\"id\":\"" + testFinding + "\",\"pass\":1,\"leg\":\"review\"} -->",
			}},
		}}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if !contains(e.forge.order, "ReviewThreads") {
			t.Fatalf("threads were not loaded through forge: %v", e.forge.order)
		}
		if !strings.Contains(string(got.Prompt), "### 1. `"+testFinding+"`") {
			t.Fatalf("prompt did not number the finding as 1:\n%s", got.Prompt)
		}
	})

	t.Run("github_issues candidates keep the current dedupe input shape", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.show = map[string][]byte{
			e.base.SHA() + ":.github/crossrev.yml": []byte("version: 1\nbacklog:\n  destination: github_issues\n"),
		}
		e.forge.candidates = []forge.IssueCandidate{{
			Number: 19,
			Title:  "same crash",
			State:  "open",
			Body:   "seen before",
		}}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if !containsPrefix(e.forge.order, "IssueCandidates:") {
			t.Fatalf("candidates were not loaded through forge: %v", e.forge.order)
		}
		if !strings.Contains(string(got.Prompt), "### candidates for finding 1 (`"+testFinding+"`)") {
			t.Fatalf("prompt is missing the per-finding candidate block:\n%s", got.Prompt)
		}
		if !strings.Contains(string(got.Prompt), "**#19** (open) same crash") {
			t.Fatalf("prompt is missing the candidate issue:\n%s", got.Prompt)
		}
	})

	t.Run("stop label returns before a harness process", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.forge.pr.Labels = append(e.forge.pr.Labels, forge.Label{Name: "crossrev/stop"})
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeStopped {
			t.Fatalf("Outcome = %q, want %q", got.Outcome, OutcomeStopped)
		}
		if e.runner.specs != nil {
			t.Fatal("harness ran under crossrev/stop")
		}
	})
}

func contains(order []string, op string) bool {
	for _, s := range order {
		if s == op {
			return true
		}
	}
	return false
}

func containsPrefix(order []string, prefix string) bool {
	for _, s := range order {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
