package resolve

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
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
