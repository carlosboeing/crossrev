package ghexec_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/forge"
)

// gh not running at all is not a successful empty answer.
//
// answered reads two fields for one question, and only one of them is easy to
// remember. A non-zero exit is gh refusing an API call; Err is the runner
// saying no child produced a status — an unresolvable program, a killed
// process, a context that ended. Every one of those leaves ExitCode at zero,
// so a check that drops Err reads them as gh answering nothing successfully,
// on a write as readily as on a read.
func TestAnInvocationThatNeverRanIsNotAnAnswer(t *testing.T) {
	t.Run("a write reports it and says why", func(t *testing.T) {
		c, _ := client(t, unresolved())

		err := c.PullRequestLabelAdd(context.Background(), testSlug(t), 42, "crossrev/pass-1")
		if err == nil {
			t.Fatal("a label that never reached gh reported success")
		}
		if !errors.Is(err, errNoStatus) {
			t.Errorf("error = %v, want it to carry why gh never ran", err)
		}
		if strings.Contains(err.Error(), "exited 0") {
			t.Errorf("error = %v, which reports a zero exit for a child that produced none", err)
		}
	})

	t.Run("an anchor that never reached gh falls back", func(t *testing.T) {
		c, r := client(t, unresolved(), out("9001\n"))

		got, err := c.ReviewCommentCreate(context.Background(), reviewComment(t, "Finding."))
		if err != nil {
			t.Fatalf("ReviewCommentCreate: %v", err)
		}
		if got != forge.PlacementFallback {
			t.Errorf("placement = %q, want fallback; the inline comment was never posted", got)
		}
		if len(r.specs) != 2 {
			t.Errorf("gh was invoked %v, want the attempt and the fallback", r.argvs())
		}
	})

	t.Run("a cancelled read keeps nothing it captured", func(t *testing.T) {
		page := `[{"id":1,"body":"first","user":{"login":"carlosboeing"}}]`
		c, _ := client(t, cut(page))

		if got := c.IssueComments(context.Background(), testSlug(t), 42); len(got) != 0 {
			t.Errorf("comments = %+v, want none; the read never finished", got)
		}
	})

	t.Run("a page read reports it rather than answering empty", func(t *testing.T) {
		c, _ := client(t, unresolved())

		if _, err := c.RepoIssueComments(context.Background(), testSlug(t), time.Unix(0, 0), 1); err == nil {
			t.Fatal("a page that never reached gh answered as an empty page")
		}
	})
}
