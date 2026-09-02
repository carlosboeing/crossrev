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

// The id is past 2^31, which GitHub's comment ids already are. Read into 32
// bits it does not fit, and CommentCreate refuses a comment it had just
// posted — the marker lost on a write that landed.
const liveCommentID = 2946375510

func TestCommentCreateArgv(t *testing.T) {
	c, r := client(t, out("2946375510\n"))

	id, err := c.CommentCreate(context.Background(), testSlug(t), 42, "Summary.")
	if err != nil {
		t.Fatalf("CommentCreate: %v", err)
	}
	if id != liveCommentID {
		t.Errorf("id = %d, want %d", id, liveCommentID)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/issues/42/comments",
		"-f", "body=Summary.", "--jq", ".id")
}

func TestCommentCreateReportsARefusal(t *testing.T) {
	c, _ := client(t, bad())
	_, err := c.CommentCreate(context.Background(), testSlug(t), 42, "Summary.")
	if err == nil {
		t.Fatal("a refused post answered with an id")
	}
	want := "could not post a comment on acme/widget#42\n   Every pass records itself in a comment, so CrossRev stops rather than working without a record. Check the token has pull-requests write."
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to contain %q", err.Error(), want)
	}
}

// An id that cannot be read is carried as zero, not refused: gh_comment_create
// dies on `gh` failing and carries an unreadable id through as the empty string
// (lib/github.sh:187-195). The two callers that must edit the comment later —
// both claim sites — refuse on id == 0 for themselves; the callers that discard
// it, the watchdog halt and the inline-comment fallback, carry on the way the
// shell's `>/dev/null` does.
func TestCommentCreateCarriesAnUnreadableIDAsZero(t *testing.T) {
	c, _ := client(t, out("null\n"))
	id, err := c.CommentCreate(context.Background(), testSlug(t), 42, "Summary.")
	if err != nil {
		t.Errorf("an unreadable id was refused: %v", err)
	}
	if id != 0 {
		t.Errorf("id = %d, want 0", id)
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
		"-f", "body="+wantNotice, "--jq", ".id")
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

// A finding on a deleted line is a LEFT comment, and every other fixture here
// is on the right. The side travels twice — into the request and into the
// fallback body's location — so a constant in either place mis-anchors exactly
// the comment the fallback exists for.
func TestReviewCommentCreateSendsTheSideItWasGiven(t *testing.T) {
	comment := reviewComment(t, "Finding.")
	comment.Side = core.SideLeft

	c, r := client(t, bad(), out("9001\n"))
	got, err := c.ReviewCommentCreate(context.Background(), comment)
	if err != nil {
		t.Fatalf("ReviewCommentCreate: %v", err)
	}
	if got != forge.PlacementFallback {
		t.Fatalf("placement = %q, want fallback", got)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/pulls/42/comments",
		"-f", "body=Finding.",
		"-f", "commit_id=1111111111111111111111111111111111111111",
		"-f", "path=app.ts",
		"-F", "line=40",
		"-f", "side=LEFT")
	r.wantArgs(t, 1, "api", "--method", "POST", "repos/acme/widget/issues/42/comments",
		"-f", "body=**app.ts:40** (LEFT)\n\nFinding.", "--jq", ".id")
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
	if got := strings.Join(r.specs[0].Args, " "); !strings.Contains(got, wantNotice) {
		t.Errorf("argv = %q, want the notice published in place of the body", got)
	}
}

// A Publisher that reports failure and hands the original back must not get
// the original published. The contract is stated on the interface; this is
// what enforces it.
func TestAFailingFilterCannotPublishTheOriginalBody(t *testing.T) {
	const secret = "token sk-ant-api03-REAL"

	// The marker case refuses, and the no-marker case is the dangerous one:
	// nothing warns and nothing errors, so only the substituted body stops it.
	t.Run("no marker", func(t *testing.T) {
		r := &recorder{results: []exec.Result{out("9001\n")}}
		c := ghexec.New(r, rogue{})

		if _, err := c.CommentCreate(context.Background(), testSlug(t), 42, secret); err != nil {
			t.Fatalf("CommentCreate: %v", err)
		}
		argv := strings.Join(r.specs[0].Args, " ")
		if strings.Contains(argv, secret) {
			t.Errorf("argv = %q, want the body withheld", argv)
		}
		if !strings.Contains(argv, wantNotice) {
			t.Errorf("argv = %q, want the notice in its place", argv)
		}
	})

	t.Run("review reply", func(t *testing.T) {
		r := &recorder{}
		c := ghexec.New(r, rogue{})

		if err := c.ReviewReply(context.Background(), testSlug(t), 42, 5000, secret); err != nil {
			t.Fatalf("ReviewReply: %v", err)
		}
		if argv := strings.Join(r.specs[0].Args, " "); strings.Contains(argv, secret) {
			t.Errorf("argv = %q, want the body withheld", argv)
		}
	})

	t.Run("issue body", func(t *testing.T) {
		r := &recorder{results: []exec.Result{out("31\n")}}
		c := ghexec.New(r, rogue{})

		if _, err := c.IssueCreate(context.Background(), testSlug(t), "t", secret, nil); err != nil {
			t.Fatalf("IssueCreate: %v", err)
		}
		if argv := strings.Join(r.specs[0].Args, " "); strings.Contains(argv, secret) {
			t.Errorf("argv = %q, want the body withheld", argv)
		}
	})
}
