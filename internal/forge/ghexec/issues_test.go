package ghexec_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge/ghexec"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

func findingID(t *testing.T, s string) core.FindingID {
	t.Helper()
	id, err := prstate.ParseFindingID(s)
	if err != nil {
		t.Fatalf("%q is not a finding id: %v", s, err)
	}
	return id
}

// Tier 1 dedupe: exact, against CrossRev's own issues.
func TestIssueByFindingArgv(t *testing.T) {
	body := `[{"number":31,"body":"tracked <!-- crossrev:f {\"id\":\"aaaa000000000001\"} -->"},
	          {"number":55,"body":"other <!-- crossrev:f {\"id\":\"bbbb000000000002\"} -->"}]`

	c, r := client(t, out(body))
	got, ok := c.IssueByFinding(context.Background(), testSlug(t), "crossrev-review", findingID(t, "bbbb000000000002"))

	r.wantArgs(t, 0, "api", "--paginate", "repos/acme/widget/issues?state=all&labels=crossrev-review&per_page=100")
	if !ok || got != 55 {
		t.Errorf("issue = %d, %v, want 55", got, ok)
	}
}

// A pull request is an issue in this API, and filing against one would be
// nonsense.
func TestIssueByFindingSkipsPullRequests(t *testing.T) {
	body := `[{"number":31,"pull_request":{"url":"x"},"body":"<!-- crossrev:f {\"id\":\"aaaa000000000001\"} -->"},
	          {"number":32,"body":"<!-- crossrev:f {\"id\":\"aaaa000000000001\"} -->"}]`

	c, _ := client(t, out(body))
	got, ok := c.IssueByFinding(context.Background(), testSlug(t), "crossrev-review", findingID(t, "aaaa000000000001"))
	if !ok || got != 32 {
		t.Errorf("issue = %d, %v, want 32", got, ok)
	}
}

func TestIssueByFindingAnswersNothingWhenThereIsNoMatch(t *testing.T) {
	for _, res := range []exec.Result{out("[]"), bad()} {
		c, _ := client(t, res)
		if got, ok := c.IssueByFinding(context.Background(), testSlug(t), "crossrev-review", findingID(t, "aaaa000000000001")); ok {
			t.Errorf("issue = %d, want none", got)
		}
	}
}

// Tier 2 dedupe: fuzzy, against every issue in the repository.
func TestIssueCandidatesArgv(t *testing.T) {
	body := `{"items":[{"number":31,"title":"Timing leak","state":"open","body":"a long body"}]}`

	c, r := client(t, out(body))
	got := c.IssueCandidates(context.Background(), testSlug(t), "src/deep/app.ts", "constant time")

	r.wantArgs(t, 0, "api", "-X", "GET", "search/issues",
		"--raw-field", "q=repo:acme/widget is:issue app.ts constant time",
		"--raw-field", "per_page=10")
	if len(got) != 1 || got[0].Number != 31 || got[0].Title != "Timing leak" || got[0].State != "open" {
		t.Errorf("candidates = %+v", got)
	}
}

func TestIssueCandidatesOmitsEmptyTerms(t *testing.T) {
	c, r := client(t, out(`{"items":[]}`))
	c.IssueCandidates(context.Background(), testSlug(t), "app.ts", "")
	r.wantArgs(t, 0, "api", "-X", "GET", "search/issues",
		"--raw-field", "q=repo:acme/widget is:issue app.ts",
		"--raw-field", "per_page=10")
}

// The body is cut to 500 characters the way jq cuts it, which is by character
// and not by byte.
func TestIssueCandidatesCutsTheBodyByCharacter(t *testing.T) {
	long := strings.Repeat("é", 600)
	c, _ := client(t, out(`{"items":[{"number":1,"title":"t","state":"open","body":"`+long+`"}]}`))

	got := c.IssueCandidates(context.Background(), testSlug(t), "app.ts", "")
	if len(got) != 1 {
		t.Fatalf("candidates = %+v", got)
	}
	if n := len([]rune(got[0].Body)); n != 500 {
		t.Errorf("body = %d characters, want 500", n)
	}
}

func TestIssueCandidatesAnswersEmptyOnARefusal(t *testing.T) {
	c, _ := client(t, bad())
	if got := c.IssueCandidates(context.Background(), testSlug(t), "app.ts", ""); len(got) != 0 {
		t.Errorf("candidates = %+v, want none", got)
	}
}

func TestIssueCreateArgv(t *testing.T) {
	c, r := client(t, out("31\n"))

	got, err := c.IssueCreate(context.Background(), testSlug(t), "Timing leak", "Body.",
		[]string{"crossrev-review", "bug"})
	if err != nil {
		t.Fatalf("IssueCreate: %v", err)
	}
	if got != 31 {
		t.Errorf("issue = %d, want 31", got)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/issues",
		"-f", "title=Timing leak", "-f", "body=Body.",
		"-f", "labels[]=crossrev-review", "-f", "labels[]=bug", "--jq", ".number")
}

func TestIssueCreateSkipsEmptyLabels(t *testing.T) {
	c, r := client(t, out("31\n"))
	if _, err := c.IssueCreate(context.Background(), testSlug(t), "t", "b", []string{"", "bug", ""}); err != nil {
		t.Fatalf("IssueCreate: %v", err)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/issues",
		"-f", "title=t", "-f", "body=b", "-f", "labels[]=bug", "--jq", ".number")
}

// The title is masked rather than filtered: an issue title is one line and the
// publish notice is a paragraph, so the note rides on the body.
func TestIssueCreateMasksTheTitleAndFiltersTheBody(t *testing.T) {
	r := &recorder{results: []exec.Result{out("31\n")}}
	c := ghexec.New(r, masking{})

	if _, err := c.IssueCreate(context.Background(), testSlug(t), "a title", "a body", nil); err != nil {
		t.Fatalf("IssueCreate: %v", err)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/issues",
		"-f", "title=masked-title", "-f", "body=masked", "--jq", ".number")
}

// Not fatal to the leg and deliberately loud: the caller must leave the thread
// open rather than resolve it against a write that did not land.
func TestIssueCreateReportsARefusal(t *testing.T) {
	for _, res := range []exec.Result{bad(), out("\n")} {
		c, _ := client(t, res)
		if _, err := c.IssueCreate(context.Background(), testSlug(t), "t", "b", nil); err == nil {
			t.Error("a refused filing answered with an issue number")
		}
	}
}

func TestIssueCommentCreateArgv(t *testing.T) {
	c, r := client(t)
	c.IssueCommentCreate(context.Background(), testSlug(t), 31, "Also raised on #42.")
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/issues/31/comments",
		"-f", "body=Also raised on #42.")
}
