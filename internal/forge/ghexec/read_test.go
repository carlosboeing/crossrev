package ghexec_test

import (
	"context"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
)

const prViewFields = "number,title,body,url,headRefName,headRefOid,baseRefName,baseRefOid," +
	"changedFiles,labels,isCrossRepository,isDraft,headRepositoryOwner,headRepository," +
	"maintainerCanModify,state"

func TestRepoSlugArgv(t *testing.T) {
	c, r := client(t, out("acme/widget\n"))

	got, err := c.RepoSlug(context.Background())
	if err != nil {
		t.Fatalf("RepoSlug: %v", err)
	}
	if got.String() != "acme/widget" {
		t.Errorf("slug = %q, want acme/widget", got)
	}
	r.wantArgs(t, 0, "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
}

func TestRepoSlugReportsARefusal(t *testing.T) {
	c, _ := client(t, bad())
	if _, err := c.RepoSlug(context.Background()); err == nil {
		t.Error("a refused read answered with a slug")
	}
}

// A slug gh could not parse is refused rather than carried, because it reaches
// a path under the state directory.
func TestRepoSlugRefusesAnUnreadableAnswer(t *testing.T) {
	c, _ := client(t, out("not a slug\n"))
	if _, err := c.RepoSlug(context.Background()); err == nil {
		t.Error("an unparseable answer was accepted")
	}
}

func TestDefaultBranchArgv(t *testing.T) {
	c, r := client(t, out("trunk\n"))

	if got := c.DefaultBranch(context.Background(), testSlug(t)); got != "trunk" {
		t.Errorf("branch = %q, want trunk", got)
	}
	r.wantArgs(t, 0, "repo", "view", "acme/widget", "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name")
}

func TestDefaultBranchFallsBackToMain(t *testing.T) {
	for _, res := range []exec.Result{bad(), out("")} {
		c, _ := client(t, res)
		if got := c.DefaultBranch(context.Background(), testSlug(t)); got != "main" {
			t.Errorf("branch = %q, want main", got)
		}
	}
}

func TestPullRequestArgvAndDecoding(t *testing.T) {
	head := "1111111111111111111111111111111111111111"
	base := "2222222222222222222222222222222222222222"
	body := `{"number":42,"title":"Add refresh","body":"Adds a refresh helper.",
	  "url":"https://github.com/x","headRefName":"feature","headRefOid":"` + head + `",
	  "baseRefName":"main","baseRefOid":"` + base + `","changedFiles":1,
	  "labels":[{"name":"crossrev/pass-1","color":"57606A","description":"loop state"}],
	  "isCrossRepository":false,"isDraft":true,"maintainerCanModify":false,
	  "headRepositoryOwner":{"login":"acme"},"headRepository":{"name":"widget"},"state":"OPEN"}`

	c, r := client(t, out(body))
	pr, err := c.PullRequest(context.Background(), testSlug(t), 42)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}

	r.wantArgs(t, 0, "pr", "view", "42", "--repo", "acme/widget", "--json", prViewFields)

	if pr.Number != 42 || pr.Title != "Add refresh" || pr.HeadRefName != "feature" {
		t.Errorf("pull request = %+v", pr)
	}
	if pr.HeadRefOid.SHA() != head || pr.BaseRefOid.SHA() != base {
		t.Errorf("revisions = %q/%q", pr.HeadRefOid.SHA(), pr.BaseRefOid.SHA())
	}
	if !pr.IsDraft || pr.State != "OPEN" || pr.ChangedFiles != 1 {
		t.Errorf("pull request = %+v", pr)
	}
	if pr.HeadRepositoryOwner != "acme" || pr.HeadRepository != "widget" {
		t.Errorf("head repository = %q/%q", pr.HeadRepositoryOwner, pr.HeadRepository)
	}
	if len(pr.Labels) != 1 || pr.Labels[0].Name != "crossrev/pass-1" || pr.Labels[0].Colour != "57606a" {
		t.Errorf("labels = %+v", pr.Labels)
	}
}

func TestPullRequestReportsARefusal(t *testing.T) {
	c, _ := client(t, bad())
	if _, err := c.PullRequest(context.Background(), testSlug(t), 42); err == nil {
		t.Error("a refused read answered with a pull request")
	}
}

func TestPullRequestDiffArgv(t *testing.T) {
	base, err := core.NewRevision("2222222222222222222222222222222222222222")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	head, err := core.NewRevision("1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("head: %v", err)
	}

	c, r := client(t, out("diff --git a/app.ts b/app.ts\n"))
	got, err := c.PullRequestDiff(context.Background(), testSlug(t), base, head)
	if err != nil {
		t.Fatalf("PullRequestDiff: %v", err)
	}
	if string(got) != "diff --git a/app.ts b/app.ts\n" {
		t.Errorf("diff = %q", got)
	}
	r.wantArgs(t, 0, "api", "-H", "Accept: application/vnd.github.diff",
		"repos/acme/widget/compare/"+base.SHA()+"..."+head.SHA())
}

func TestIssueCommentsArgvAndOrder(t *testing.T) {
	page1 := `[{"id":1,"body":"first","created_at":"2026-01-01T00:00:00Z","user":{"login":"carlosboeing"}}]`
	page2 := `[{"id":2,"body":"second","created_at":"2026-01-02T00:00:00Z","user":{"login":"other"}}]`

	c, r := client(t, out(page1+page2))
	got := c.IssueComments(context.Background(), testSlug(t), 42)

	r.wantArgs(t, 0, "api", "--paginate", "repos/acme/widget/issues/42/comments")
	if len(got) != 2 {
		t.Fatalf("comments = %+v, want two", got)
	}
	if got[0].ID != 1 || got[1].ID != 2 {
		t.Errorf("order = %d,%d, want the pages concatenated oldest first", got[0].ID, got[1].ID)
	}
	if got[0].AuthorLogin != "carlosboeing" || got[1].AuthorLogin != "other" {
		t.Errorf("authors = %q,%q", got[0].AuthorLogin, got[1].AuthorLogin)
	}
	if got[0].CreatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("created_at = %q", got[0].CreatedAt)
	}
}

func TestIssueCommentsAnswersEmptyOnARefusal(t *testing.T) {
	c, _ := client(t, bad())
	if got := c.IssueComments(context.Background(), testSlug(t), 42); len(got) != 0 {
		t.Errorf("comments = %+v, want none", got)
	}
}

func TestReviewCommentsArgv(t *testing.T) {
	c, r := client(t, out(`[{"id":5000,"body":"finding","user":{"login":"carlosboeing"}}]`))
	got := c.ReviewComments(context.Background(), testSlug(t), 42)

	r.wantArgs(t, 0, "api", "--paginate", "repos/acme/widget/pulls/42/comments")
	if len(got) != 1 || got[0].ID != 5000 {
		t.Errorf("comments = %+v", got)
	}
}

func TestRepoIssueCommentsArgv(t *testing.T) {
	c, r := client(t, out(`[{"id":7,"body":"b","issue_url":"https://api.github.com/repos/acme/widget/issues/7","user":{"login":"bot"}}]`))

	got, err := c.RepoIssueComments(context.Background(), testSlug(t), time.Unix(1_700_000_000, 0).UTC(), 3)
	if err != nil {
		t.Fatalf("RepoIssueComments: %v", err)
	}
	r.wantArgs(t, 0, "api", "--method", "GET", "repos/acme/widget/issues/comments",
		"-f", "since=2023-11-14T22:13:20Z", "-F", "per_page=100", "-F", "page=3")
	if len(got) != 1 || got[0].IssueURL != "https://api.github.com/repos/acme/widget/issues/7" {
		t.Errorf("comments = %+v", got)
	}
}

// This read reports its failure, because the count above it distinguishes an
// empty page from an unreadable one.
func TestRepoIssueCommentsReportsARefusal(t *testing.T) {
	c, _ := client(t, bad())
	if _, err := c.RepoIssueComments(context.Background(), testSlug(t), time.Unix(0, 0), 1); err == nil {
		t.Error("a refused page read answered as an empty page")
	}
}

// A cutoff is sent in UTC whatever zone the caller holds it in.
func TestRepoIssueCommentsSendsUTC(t *testing.T) {
	zone := time.FixedZone("AEST", 10*60*60)
	c, r := client(t, out("[]"))
	if _, err := c.RepoIssueComments(context.Background(), testSlug(t), time.Unix(1_700_000_000, 0).In(zone), 1); err != nil {
		t.Fatalf("RepoIssueComments: %v", err)
	}
	if got := r.specs[0].Args[5]; got != "since=2023-11-14T22:13:20Z" {
		t.Errorf("since = %q, want the UTC stamp", got)
	}
}

func TestViewerLoginArgv(t *testing.T) {
	c, r := client(t, out("carlosboeing\n"))
	got, err := c.ViewerLogin(context.Background())
	if err != nil {
		t.Fatalf("ViewerLogin: %v", err)
	}
	if got != "carlosboeing" {
		t.Errorf("login = %q", got)
	}
	r.wantArgs(t, 0, "api", "user", "--jq", ".login")
}

func TestViewerLoginReportsARefusal(t *testing.T) {
	for _, res := range []exec.Result{bad(), out("\n")} {
		c, _ := client(t, res)
		if _, err := c.ViewerLogin(context.Background()); err == nil {
			t.Error("a refused identity read answered with a login")
		}
	}
}

func TestPullRequestLabelsArgv(t *testing.T) {
	c, r := client(t, out("crossrev/pass-1\ncrossrev/awaiting-resolution\n"))
	got := c.PullRequestLabels(context.Background(), testSlug(t), 42)

	r.wantArgs(t, 0, "api", "repos/acme/widget/issues/42/labels", "--jq", ".[].name")
	if len(got) != 2 || got[0] != "crossrev/pass-1" || got[1] != "crossrev/awaiting-resolution" {
		t.Errorf("labels = %q", got)
	}
}

func TestPullRequestLabelsAnswersEmptyOnARefusal(t *testing.T) {
	c, _ := client(t, bad())
	if got := c.PullRequestLabels(context.Background(), testSlug(t), 42); len(got) != 0 {
		t.Errorf("labels = %q, want none", got)
	}
}
