package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// TestReconcile pins that an already-settled finding is not answered twice,
// that a redrive answers its own findings again, and that unthreaded replies
// are seeded from issue comments (lib/run.sh:2291-2344).
func TestReconcile(t *testing.T) {
	t.Run("an already-settled finding is not answered twice", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.forge.threads = []forge.ReviewThread{{
			ID:            "thread-1",
			Path:          "app.ts",
			Line:          2,
			RootCommentID: 55,
			FindingIDs:    []core.FindingID{mustFinding(t)},
		}}
		e.forge.reviewComments = []forge.IssueComment{{
			ID:          6001,
			AuthorLogin: e.forge.viewer,
			Body:        "already answered" + prstate.EncodeFindingMarker(mustFindingID(t, testFinding), 1, core.LegResolve),
		}}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if len(e.forge.replies) != 0 {
			t.Fatalf("replied %d times to a finding already on the thread: %+v", len(e.forge.replies), e.forge.replies)
		}
		if countOp(e.forge.order, "ReviewReply") != 0 {
			t.Fatalf("ReviewReply ran: %v", e.forge.order)
		}
	})

	t.Run("a redrive answers its own findings again", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.addResolve(t, prstate.Marker{
			Pass:          1,
			State:         core.PassComplete,
			Blocked:       prstate.Some(true),
			BlockedReason: prstate.Some("needs a schema"),
			Resolutions:   json.RawMessage(`[{"finding_id":"` + testFinding + `","resolution":"escalated"}]`),
			Harness:       prstate.Some("claude"),
			HeadSHA:       prstate.Some(e.head.SHA()),
			TS:            e.now.Unix() - 30,
		}, 9100)
		e.forge.threads = []forge.ReviewThread{{
			ID:            "thread-1",
			Path:          "app.ts",
			Line:          2,
			RootCommentID: 55,
			FindingIDs:    []core.FindingID{mustFinding(t)},
		}}
		e.forge.reviewComments = []forge.IssueComment{{
			ID:          6001,
			AuthorLogin: e.forge.viewer,
			Body:        "the attempt that declined" + prstate.EncodeFindingMarker(mustFindingID(t, testFinding), 1, core.LegResolve),
		}}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if len(e.forge.replies) != 1 {
			t.Fatalf("replies = %d, want 1 on a redrive: %+v", len(e.forge.replies), e.forge.replies)
		}
	})

	t.Run("unthreaded issue comments seed the count on resume", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.forge.comments = append(e.forge.comments, forge.IssueComment{
			ID:          9200,
			AuthorLogin: e.forge.viewer,
			Body:        "top-level fallback" + prstate.EncodeFindingMarker(mustFindingID(t, testFinding), 1, core.LegResolve),
		})
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Marker.Unthreaded.Value() < 1 {
			t.Fatalf("unthreaded = %d, want at least the seeded fallback", got.Marker.Unthreaded.Value())
		}
		if countOp(e.forge.order, "CommentCreate") < 1 {
			// claim create is one; a second would be another fallback
		}
		for _, created := range e.forge.created {
			if strings.Contains(created.Body, "**Fixed.**") {
				t.Fatalf("posted a second reply for a finding already unthreaded: %s", created.Body)
			}
		}
	})

	t.Run("reply is posted before the thread is resolved", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.forge.threads = []forge.ReviewThread{{
			ID:            "thread-1",
			Path:          "app.ts",
			Line:          2,
			RootCommentID: 55,
			FindingIDs:    []core.FindingID{mustFinding(t)},
		}}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if !firstOpBefore(e.forge.order, "ReviewReply", "ThreadResolve") {
			t.Fatalf("thread resolved before the reply: %v", e.forge.order)
		}
	})

	t.Run("a failed thread reply warns that the top-level fallback is unthreaded", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.forge.replyErr = errors.New("reply refused")
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		want := "could not reply in the thread rooted at comment 55 on acme/widget#42\n   The resolution is still recorded in the pass marker, but the collaborator reading the thread will not see the reason. Check the token has pull-requests write."
		if !strings.Contains(ui.Joined(got.Messages), want) {
			t.Errorf("messages = %q, want warning %q", got.Messages, want)
		}
	})

	t.Run("a missing review thread warns before replying at top level", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.forge.threads = nil
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		want := "no review thread was found for finding aaaaaaaaaaaaaaaa, so its reply is a top-level comment\n   The reply is on the pull request rather than under the code it answers. This is expected when GitHub refused to anchor the original inline comment, and unexpected otherwise."
		if !strings.Contains(ui.Joined(got.Messages), want) {
			t.Errorf("messages = %q, want warning %q", got.Messages, want)
		}
	})

	t.Run("an unthreaded fallback reports the aggregate warning", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.forge.replyErr = errors.New("reply refused")
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		want := "1 reply could not be threaded and landed as top-level comments\n   Each one names the finding it answers, so nothing is lost, but a reader following the diff will not see it beside the code."
		if !strings.Contains(ui.Joined(got.Messages), want) {
			t.Errorf("messages = %q, want warning %q", got.Messages, want)
		}
	})

	t.Run("a failed thread resolution warns that the thread remains open", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.staged = true
		e.forge.threadErr = errors.New("resolve refused")
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		want := "could not resolve review thread thread-1\n   The thread stays open, so the next pass sees it as unsettled and may raise it again. Resolve it by hand, or retry the leg."
		if !strings.Contains(ui.Joined(got.Messages), want) {
			t.Errorf("messages = %q, want warning %q", got.Messages, want)
		}
	})

	t.Run("replyAndResolve preserves findings key order when mutating resolution and tracked_as", func(t *testing.T) {
		customFinding := `{"id":"` + testFinding + `","path":"a.go","line":3,"severity":"high"}`
		findings, err := harness.DecodeStream([]byte(customFinding))
		if err != nil {
			t.Fatalf("decode findings: %v", err)
		}
		customResolution := `{"finding_id":"` + testFinding + `","reply":"fixed","resolution":"fixed","crossrev_tracked":""}`
		recs, err := harness.DecodeStream([]byte(customResolution))
		if err != nil {
			t.Fatalf("decode recs: %v", err)
		}

		e := setup(t)
		s := &session{
			pass:     1,
			repo:     e.slug,
			req:      Request{PR: 42},
			settings: legSettings{Harness: "claude", Model: "claude-3-7-sonnet"},
		}
		threads := []forge.ReviewThread{{
			ID:            "thread-1",
			RootCommentID: 55,
			FindingIDs:    []core.FindingID{mustFindingID(t, testFinding)},
		}}
		already := map[string]bool{}
		leg := &Leg{Forge: e.forge}
		resolved, escalated, unthreaded, findingsOut, _ := leg.replyAndResolve(context.Background(), s, recs, findings, threads, "sha123", already, 0)
		if resolved != 1 {
			t.Fatalf("resolved = %d, want 1", resolved)
		}
		_ = escalated
		_ = unthreaded
		wantFindingsOut := `[{"id":"` + testFinding + `","path":"a.go","line":3,"severity":"high","resolution":"fixed","tracked_as":null}]`
		if string(findingsOut) != wantFindingsOut {
			t.Fatalf("findingsOut = %s, want %s", string(findingsOut), wantFindingsOut)
		}
	})
}

func mustFindingID(t *testing.T, s string) core.FindingID {
	t.Helper()
	id, err := prstate.ParseFindingID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func countOp(order []string, op string) int {
	n := 0
	for _, o := range order {
		if o == op {
			n++
		}
	}
	return n
}

func firstOpBefore(order []string, earlier, later string) bool {
	a, b := -1, -1
	for i, op := range order {
		if op == earlier && a < 0 {
			a = i
		}
		if op == later && b < 0 {
			b = i
		}
	}
	return a >= 0 && b >= 0 && a < b
}
