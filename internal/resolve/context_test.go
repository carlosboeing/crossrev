package resolve

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
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

	t.Run("automatic trigger trusts CROSSREV_APP_SLUG not ViewerLogin", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.forge.comments[len(e.forge.comments)-1].AuthorLogin = "crossrev[bot]"
		t.Setenv("CROSSREV_APP_SLUG", "crossrev")
		got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerAutomatic})
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeInvoked {
			t.Fatalf("Outcome = %q, want invoked; ViewerLogin=tester must not hide crossrev[bot] markers", got.Outcome)
		}
		if got.Pass != 1 {
			t.Errorf("Pass = %d, want 1", got.Pass)
		}
	})

	t.Run("empty trigger is human", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: ""})
		if got.Err != nil {
			t.Fatalf("empty trigger: %v", got.Err)
		}
		if got.Outcome != OutcomeInvoked {
			t.Fatalf("Outcome = %q, want invoked (lib/run.sh:1731 trigger=human)", got.Outcome)
		}
	})

	t.Run("entry guards", func(t *testing.T) {
		tests := []struct {
			name        string
			setup       func(*testing.T, *testEnv, *Request)
			wantOutcome Outcome
			wantErr     string
		}{
			{
				name: "automatic fork is refused",
				setup: func(_ *testing.T, e *testEnv, req *Request) {
					req.Trigger = TriggerAutomatic
					e.forge.pr.IsCrossRepository = true
				},
				wantOutcome: OutcomeRefused,
				wantErr:     "comes from a fork",
			},
			{
				name: "a pull request that is not OPEN is refused",
				setup: func(_ *testing.T, e *testEnv, _ *Request) {
					e.forge.pr.State = "CLOSED"
				},
				wantOutcome: OutcomeRefused,
				wantErr:     "is not open",
			},
			{
				name: "automatic draft skips",
				setup: func(_ *testing.T, e *testEnv, req *Request) {
					req.Trigger = TriggerAutomatic
					e.forge.pr.IsDraft = true
				},
				wantOutcome: OutcomeSkipped,
			},
			{
				name: "unknown trigger is refused",
				setup: func(_ *testing.T, _ *testEnv, req *Request) {
					req.Trigger = "cron"
				},
				wantOutcome: OutcomeRefused,
				wantErr:     "unknown resolve trigger",
			},
			{
				name: "ServesLeg refuses a review-only harness",
				setup: func(t *testing.T, e *testEnv, req *Request) {
					raw := strings.Replace(string(harness.DescriptorJSON()),
						`"name": "grok"`, `"name": "grok", "legs": ["review"]`, 1)
					doc, err := harness.Load([]byte(raw))
					if err != nil {
						t.Fatalf("Load review-only grok: %v", err)
					}
					e.doc = doc
					req.Harness = "grok"
					e.addReview(t, defaultFindings(), "issues-remain")
				},
				wantOutcome: OutcomeRefused,
				wantErr:     "cannot serve the resolve leg",
			},
			{
				name: "AssertPushTarget refuses a worktree at the wrong revision",
				setup: func(t *testing.T, e *testEnv, _ *Request) {
					e.addReview(t, defaultFindings(), "issues-remain")
					e.git.wrongHead = e.base
				},
				wantOutcome: OutcomeRefused,
				wantErr:     "the tree is at revision",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := setup(t)
				req := Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman}
				tt.setup(t, e, &req)
				got := e.runReq(t, req)
				if got.Outcome != tt.wantOutcome {
					t.Errorf("Outcome = %q, want %q (err=%v)", got.Outcome, tt.wantOutcome, got.Err)
				}
				if tt.wantErr != "" {
					if got.Err == nil || !strings.Contains(got.Err.Error(), tt.wantErr) {
						t.Errorf("Err = %v, want it to contain %q", got.Err, tt.wantErr)
					}
				}
				if e.runner.specs != nil {
					t.Errorf("harness started on a guard: %d specs", len(e.runner.specs))
				}
			})
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
