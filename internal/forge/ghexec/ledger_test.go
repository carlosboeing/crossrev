package ghexec_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge/ghexec"
)

// The ledger reads follow every page and report failure, where the
// parity-era reads collapse it to absence. A comment list that never
// arrived is not an empty ledger, and a shard that never arrived is not
// missing coverage.
func TestCoverageCommentsArgvAndOrder(t *testing.T) {
	page1 := `[{"id":9001,"body":"first","created_at":"2026-01-01T00:00:00Z","user":{"login":"carlosboeing"}}]`
	page2 := `[{"id":9002,"body":"second","created_at":"2026-01-02T00:00:00Z","user":{"login":"other"}}]`

	c, r := client(t, out(page1+page2))
	got, err := c.CoverageComments(context.Background(), testSlug(t), 42)
	if err != nil {
		t.Fatalf("CoverageComments: %v", err)
	}

	r.wantArgs(t, 0, "api", "--paginate", "repos/acme/widget/issues/42/comments")
	if len(got) != 2 {
		t.Fatalf("comments = %+v, want two", got)
	}
	if got[0].ID != 9001 || got[1].ID != 9002 {
		t.Errorf("order = %d,%d, want the pages concatenated oldest first", got[0].ID, got[1].ID)
	}
	if got[0].Author != "carlosboeing" || got[1].Author != "other" {
		t.Errorf("authors = %q,%q", got[0].Author, got[1].Author)
	}
}

// An unreadable comment list refuses rather than answering empty: absent
// coverage is not covered code.
func TestCoverageCommentsReportsARefusal(t *testing.T) {
	for _, res := range []exec.Result{bad(), unresolved(), out("not json")} {
		c, _ := client(t, res)
		if _, err := c.CoverageComments(context.Background(), testSlug(t), 42); err == nil {
			t.Errorf("result %+v answered as an empty list", res)
		}
	}
}

// A truncated page refuses rather than selecting out of half a list.
func TestCoverageCommentsReportsATruncatedPage(t *testing.T) {
	page1 := `[{"id":9001,"body":"first","user":{"login":"carlosboeing"}}]`
	c, _ := client(t, out(page1+`[{"id":`))
	if _, err := c.CoverageComments(context.Background(), testSlug(t), 42); err == nil {
		t.Error("a truncated page answered as the prefix before it")
	}
}

func TestCoverageCommentArgv(t *testing.T) {
	c, r := client(t, out(`{"id":9001,"body":"shard","user":{"login":"carlosboeing"}}`))

	got, err := c.CoverageComment(context.Background(), testSlug(t), 9001)
	if err != nil {
		t.Fatalf("CoverageComment: %v", err)
	}
	r.wantArgs(t, 0, "api", "repos/acme/widget/issues/comments/9001")
	if got.ID != 9001 || got.Author != "carlosboeing" || got.Body != "shard" {
		t.Errorf("comment = %+v", got)
	}
}

func TestCoverageCommentReportsARefusal(t *testing.T) {
	for _, res := range []exec.Result{bad(), unresolved(), out("not json")} {
		c, _ := client(t, res)
		if _, err := c.CoverageComment(context.Background(), testSlug(t), 9001); err == nil {
			t.Errorf("result %+v answered with a comment", res)
		}
	}
}

func TestCreateCoverageCommentArgv(t *testing.T) {
	c, r := client(t, out("9001\n"))

	id, err := c.CreateCoverageComment(context.Background(), testSlug(t), 42, "Summary.")
	if err != nil {
		t.Fatalf("CreateCoverageComment: %v", err)
	}
	if id != 9001 {
		t.Errorf("id = %d, want 9001", id)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/issues/42/comments",
		"-f", "body=Summary.", "--jq", ".id")
}

func TestCreateCoverageCommentReportsARefusal(t *testing.T) {
	c, _ := client(t, bad())
	if _, err := c.CreateCoverageComment(context.Background(), testSlug(t), 42, "Summary."); err == nil {
		t.Fatal("a refused post answered with an id")
	}
}

// An unreadable id refuses rather than answering zero: CommentCreate
// carries zero for callers that discard it, but a ledger shard the
// manifest cannot name is a generation that can never verify.
func TestCreateCoverageCommentRefusesAnUnreadableID(t *testing.T) {
	c, _ := client(t, out("null\n"))
	if _, err := c.CreateCoverageComment(context.Background(), testSlug(t), 42, "Summary."); err == nil {
		t.Error("an unreadable id was carried as zero")
	}
}

// A coverage body the filter could not process stops the write: the
// notice standing in for the bytes loses the generation rather than
// masking it.
func TestCreateCoverageCommentRefusesWhenTheFilterFails(t *testing.T) {
	r := &recorder{}
	c := ghexec.New(r, withheld{})
	body := "Shard.\n\n<!-- crossrev:c {\"v\":1,\"kind\":\"shard\"} -->"

	if _, err := c.CreateCoverageComment(context.Background(), testSlug(t), 42, body); err == nil {
		t.Error("a coverage body was published unfiltered")
	}
	if len(r.specs) != 0 {
		t.Errorf("gh was invoked %v, want not at all", r.argvs())
	}
}

// A body with no marker in it publishes the notice, which is what the
// shell does: only the marker case refuses.
func TestCreateCoverageCommentPublishesTheNoticeWithoutAMarker(t *testing.T) {
	r := &recorder{results: []exec.Result{out("9001\n")}}
	c := ghexec.New(r, withheld{})

	if _, err := c.CreateCoverageComment(context.Background(), testSlug(t), 42, "plain text"); err != nil {
		t.Fatalf("CreateCoverageComment: %v", err)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/issues/42/comments",
		"-f", "body="+wantNotice, "--jq", ".id")
}

// The filtered body is what reaches gh, not the body the caller handed over.
func TestCreateCoverageCommentPublishesTheFilteredBody(t *testing.T) {
	r := &recorder{results: []exec.Result{out("9001\n")}}
	c := ghexec.New(r, masking{})

	if _, err := c.CreateCoverageComment(context.Background(), testSlug(t), 42, "sk-ant-secret"); err != nil {
		t.Fatalf("CreateCoverageComment: %v", err)
	}
	if got := strings.Join(r.specs[0].Args, " "); strings.Contains(got, "sk-ant-secret") {
		t.Errorf("argv = %q, want the filtered body", got)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/issues/42/comments",
		"-f", "body=masked", "--jq", ".id")
}
