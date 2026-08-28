package ghexec_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/forge/ghexec"
)

func TestCommentCreateArgv(t *testing.T) {
	c, r := client(t, out("9001\n"))

	id, err := c.CommentCreate(context.Background(), testSlug(t), 42, "Summary.")
	if err != nil {
		t.Fatalf("CommentCreate: %v", err)
	}
	if id != 9001 {
		t.Errorf("id = %d, want 9001", id)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/issues/42/comments",
		"-f", "body=Summary.", "--jq", ".id")
}

func TestCommentCreateReportsARefusal(t *testing.T) {
	c, _ := client(t, bad())
	if _, err := c.CommentCreate(context.Background(), testSlug(t), 42, "Summary."); err == nil {
		t.Error("a refused post answered with an id")
	}
}

// Every later write to a comment is an edit of its id, so an id that cannot be
// read is refused rather than carried as zero.
func TestCommentCreateRefusesAnUnreadableID(t *testing.T) {
	c, _ := client(t, out("null\n"))
	if _, err := c.CommentCreate(context.Background(), testSlug(t), 42, "Summary."); err == nil {
		t.Error("an unreadable id was accepted")
	}
}

func TestCommentEditArgv(t *testing.T) {
	c, r := client(t)
	if err := c.CommentEdit(context.Background(), testSlug(t), 9001, "Updated."); err != nil {
		t.Fatalf("CommentEdit: %v", err)
	}
	r.wantArgs(t, 0, "api", "--method", "PATCH", "repos/acme/widget/issues/comments/9001",
		"-f", "body=Updated.")
}

func TestCommentEditReportsARefusal(t *testing.T) {
	c, _ := client(t, bad())
	if err := c.CommentEdit(context.Background(), testSlug(t), 9001, "Updated."); err == nil {
		t.Error("a refused edit reported success")
	}
}

// A body carrying a marker that the filter could not process stops the write:
// the notice standing in for the marker loses the record rather than masking it.
func TestAMarkerBodyRefusesWhenTheFilterFails(t *testing.T) {
	r := &recorder{}
	c := ghexec.New(r, withheld{})
	body := "Summary.\n\n<!-- crossrev: {\"v\":1,\"leg\":\"review\"} -->"

	if _, err := c.CommentCreate(context.Background(), testSlug(t), 42, body); err == nil {
		t.Error("a marker body was published unfiltered")
	}
	if err := c.CommentEdit(context.Background(), testSlug(t), 9001, body); err == nil {
		t.Error("a marker body was published unfiltered")
	}
	if len(r.specs) != 0 {
		t.Errorf("gh was invoked %v, want not at all", r.argvs())
	}
}

// A body with no marker in it publishes the notice, which is what the shell
// does: only the marker case refuses.
func TestABodyWithNoMarkerPublishesTheNotice(t *testing.T) {
	r := &recorder{results: []exec.Result{out("9001\n")}}
	c := ghexec.New(r, withheld{})

	if _, err := c.CommentCreate(context.Background(), testSlug(t), 42, "plain text"); err != nil {
		t.Fatalf("CommentCreate: %v", err)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/issues/42/comments",
		"-f", "body="+withheldNotice, "--jq", ".id")
}

func reviewComment(t *testing.T, body string) forge.ReviewComment {
	t.Helper()
	commit, err := core.NewRevision("1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return forge.ReviewComment{
		Repo:   testSlug(t),
		Number: 42,
		Commit: commit,
		Path:   "app.ts",
		Line:   40,
		Side:   core.SideRight,
		Body:   body,
	}
}

func TestReviewCommentCreateArgv(t *testing.T) {
	c, r := client(t)

	got, err := c.ReviewCommentCreate(context.Background(), reviewComment(t, "Finding."))
	if err != nil {
		t.Fatalf("ReviewCommentCreate: %v", err)
	}
	if got != forge.PlacementInline {
		t.Errorf("placement = %q, want inline", got)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/pulls/42/comments",
		"-f", "body=Finding.",
		"-f", "commit_id=1111111111111111111111111111111111111111",
		"-f", "path=app.ts",
		"-F", "line=40",
		"-f", "side=RIGHT")
}

// GitHub refusing the anchor must not lose the finding: it goes up as a
// top-level comment naming the location.
func TestReviewCommentCreateFallsBackToATopLevelComment(t *testing.T) {
	c, r := client(t, bad(), out("9001\n"))

	got, err := c.ReviewCommentCreate(context.Background(), reviewComment(t, "Finding."))
	if err != nil {
		t.Fatalf("ReviewCommentCreate: %v", err)
	}
	if got != forge.PlacementFallback {
		t.Errorf("placement = %q, want fallback", got)
	}
	if len(r.specs) != 2 {
		t.Fatalf("gh was invoked %v, want the inline attempt and the fallback", r.argvs())
	}
	r.wantArgs(t, 1, "api", "--method", "POST", "repos/acme/widget/issues/42/comments",
		"-f", "body=**app.ts:40** (RIGHT)\n\nFinding.", "--jq", ".id")
}

// The fallback comment is a write like any other, so a refusal there is
// reported rather than swallowed.
func TestReviewCommentCreateReportsARefusedFallback(t *testing.T) {
	c, _ := client(t, bad(), bad())
	if _, err := c.ReviewCommentCreate(context.Background(), reviewComment(t, "Finding.")); err == nil {
		t.Error("a refused fallback reported success")
	}
}

func TestReviewReplyArgv(t *testing.T) {
	c, r := client(t)
	if err := c.ReviewReply(context.Background(), testSlug(t), 42, 5000, "Fixed."); err != nil {
		t.Fatalf("ReviewReply: %v", err)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/pulls/42/comments/5000/replies",
		"-f", "body=Fixed.")
}

func TestReviewReplyReportsARefusal(t *testing.T) {
	c, _ := client(t, bad())
	if err := c.ReviewReply(context.Background(), testSlug(t), 42, 5000, "Fixed."); err == nil {
		t.Error("a refused reply reported success")
	}
}

// The two writes that warn rather than refuse when a marker body could not be
// filtered still publish, and say so.
func TestAWithheldMarkerWarnsOnTheWritesThatDegrade(t *testing.T) {
	var warned []string
	r := &recorder{}
	c := ghexec.New(r, withheld{}, ghexec.WithWarn(func(summary, _ string) {
		warned = append(warned, summary)
	}))
	body := "Finding.\n\n<!-- crossrev:f {\"id\":\"aaaa000000000001\"} -->"

	if _, err := c.ReviewCommentCreate(context.Background(), reviewComment(t, body)); err != nil {
		t.Fatalf("ReviewCommentCreate: %v", err)
	}
	if err := c.ReviewReply(context.Background(), testSlug(t), 42, 5000, body); err != nil {
		t.Fatalf("ReviewReply: %v", err)
	}

	if len(warned) != 2 {
		t.Fatalf("warnings = %v, want one per write", warned)
	}
	for _, w := range warned {
		if !strings.Contains(w, "could not filter a comment body") {
			t.Errorf("warning = %q", w)
		}
	}
	if got := strings.Join(r.specs[0].Args, " "); !strings.Contains(got, withheldNotice) {
		t.Errorf("argv = %q, want the notice published in place of the body", got)
	}
}
