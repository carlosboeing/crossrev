package ghexec_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
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

// An answer this cannot decode is refused rather than carried.
//
// A dropped unmarshal error hands the caller a zero PullRequest that reports
// no error, and Number 0 then reaches anchoring and every write built from it.
// The same is true of each revision: a SHA gh reported that is not one is not
// a zero revision, and a message naming which of the two is wrong is the
// difference between a five-minute diagnosis and an hour of one.
func TestPullRequestRefusesAnAnswerItCannotRead(t *testing.T) {
	const head = "1111111111111111111111111111111111111111"
	const base = "2222222222222222222222222222222222222222"

	cases := []struct {
		name    string
		body    string
		mention string
	}{
		{
			name: "output that is not JSON",
			body: "gateway timeout",
		},
		{
			name:    "a head oid that is not a revision",
			body:    `{"number":42,"headRefOid":"not-a-sha","baseRefOid":"` + base + `"}`,
			mention: "head",
		},
		{
			name:    "a base oid that is not a revision",
			body:    `{"number":42,"headRefOid":"` + head + `","baseRefOid":"not-a-sha"}`,
			mention: "base",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := client(t, out(tt.body))
			pr, err := c.PullRequest(context.Background(), testSlug(t), 42)
			if err == nil {
				t.Fatalf("an unreadable answer was accepted as %+v", pr)
			}
			if pr.Number != 0 {
				t.Errorf("pull request = %+v, want nothing carried out of a refusal", pr)
			}
			if tt.mention != "" && !strings.Contains(err.Error(), tt.mention) {
				t.Errorf("error = %v, want it to name which revision", err)
			}
		})
	}
}

// A closed pull request whose head branch has been deleted reports an empty
// oid, and that is an absent revision rather than a malformed one. Refusing it
// would make every such pull request unreadable.
func TestPullRequestReadsAPullRequestWhoseHeadBranchIsGone(t *testing.T) {
	body := `{"number":42,"state":"CLOSED","headRefName":"feature","headRefOid":"",
	  "baseRefName":"main","baseRefOid":""}`

	c, _ := client(t, out(body))
	pr, err := c.PullRequest(context.Background(), testSlug(t), 42)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if pr.Number != 42 || pr.State != "CLOSED" {
		t.Errorf("pull request = %+v", pr)
	}
	if pr.HeadRefOid.SHA() != "" || pr.BaseRefOid.SHA() != "" {
		t.Errorf("revisions = %q/%q, want the zero revision", pr.HeadRefOid.SHA(), pr.BaseRefOid.SHA())
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

func TestPullRequestDiffReportsAFailedFetch(t *testing.T) {
	base, err := core.NewRevision("2222222222222222222222222222222222222222")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	head, err := core.NewRevision("1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("head: %v", err)
	}

	c, _ := client(t, bad())
	_, err = c.PullRequestDiff(context.Background(), testSlug(t), base, head)
	if err == nil {
		t.Fatal("a refused diff fetch reported success")
	}
	want := "could not fetch the diff for acme/widget at 1111111111111111111111111111111111111111\n   The review leg has nothing to reason about without it. Check network access and `gh auth status`."
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to contain %q", err.Error(), want)
	}
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

// The page size this client asks GitHub for and the size the count treats as a
// full page are one number written in two packages, and nothing pairs them.
//
// The count reads another page whenever a page comes back full. Ask for fewer
// than it expects and every page reads as short, so the count stops at the
// first one and reports a number it believes is exact. Both halves are read
// here rather than restated: the size comes off the argv, and what the count
// does with a page of exactly that many comments is measured.
func TestThePageSizeAskedForIsThePageSizeTheCountExpects(t *testing.T) {
	c, r := client(t, out("[]"))
	if _, err := c.RepoIssueComments(context.Background(), testSlug(t), time.Unix(0, 0), 1); err != nil {
		t.Fatalf("RepoIssueComments: %v", err)
	}
	asked := 0
	for _, arg := range r.specs[0].Args {
		if size, ok := strings.CutPrefix(arg, "per_page="); ok {
			n, err := strconv.Atoi(size)
			if err != nil {
				t.Fatalf("per_page = %q, which is not a number", size)
			}
			asked = n
		}
	}
	if asked <= 1 {
		t.Fatalf("the read asked for per_page=%d; the argv is %q", asked, r.specs[0].Args)
	}

	count := func(t *testing.T, comments int) *recorder {
		t.Helper()
		full, rec := client(t, out(commentPage(comments)), out("[]"))
		_, err := forge.PRsReviewedToday(context.Background(), full, forge.DailyCount{
			Repo: testSlug(t), Author: "crossrev-acme[bot]", CurrentPR: 42,
		})
		if err != nil {
			t.Fatalf("PRsReviewedToday: %v", err)
		}
		return rec
	}

	if got := len(count(t, asked).specs); got != 2 {
		t.Errorf("a page of %d comments ended the read after %d page(s); the count treats a different size as full",
			asked, got)
	}
	if got := len(count(t, asked-1).specs); got != 1 {
		t.Errorf("a page of %d comments was read as full and cost %d page(s)", asked-1, got)
	}
}

// commentPage is n issue comments carrying nothing the count reads, so only
// the page's length matters.
func commentPage(n int) string {
	entries := make([]string, 0, n)
	for i := range n {
		entries = append(entries, fmt.Sprintf(
			`{"id":%d,"body":"no marker","issue_url":"https://api.github.com/repos/acme/widget/issues/%d","user":{"login":"someone"}}`,
			i+1, i+1))
	}
	return "[" + strings.Join(entries, ",") + "]"
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

// The shell loses a trailing newline through the command substitution
// gh_pr_diff captures the diff in; these are the bytes gh printed.
func TestPullRequestDiffKeepsTheTrailingNewline(t *testing.T) {
	base, err := core.NewRevision("2222222222222222222222222222222222222222")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	head, err := core.NewRevision("1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("head: %v", err)
	}

	c, _ := client(t, out("+b\n"))
	got, err := c.PullRequestDiff(context.Background(), testSlug(t), base, head)
	if err != nil {
		t.Fatalf("PullRequestDiff: %v", err)
	}
	if string(got) != "+b\n" {
		t.Errorf("diff = %q, want the byte gh printed last", got)
	}
}

// gh's stdout is the API's answer, and this is the only route from it into
// text an operator reads.
//
// The escape sits inside the excerpt rather than past it, which is what makes
// this measure the scrub: an escape at offset 4000 is cut away by the bound
// and says nothing about what happens to one that is quoted. What the scrub
// leaves in its place is the replacement character, and the assertion is on
// that rather than on the absence of a raw byte, because %q would render a
// surviving escape as four printable characters and pass either way.
func TestRepoSlugBoundsWhatItQuotesBackFromGh(t *testing.T) {
	page := "<html>\x1b[2J\x07" + strings.Repeat("A", 4000) + "</html>"

	c, _ := client(t, out(page))
	_, err := c.RepoSlug(context.Background())
	if err == nil {
		t.Fatal("a page of HTML was accepted as a repository slug")
	}

	// The refusal names the length and quotes a bounded excerpt, so it is
	// short whatever gh printed. The page here is four thousand characters.
	message := err.Error()
	if len(message) > 300 {
		t.Errorf("the refusal is %d bytes long for a %d-byte answer; gh's output is not bounded",
			len(message), len(page))
	}
	if strings.ContainsAny(message, "\x1b\x07") {
		t.Errorf("the refusal carries a terminal escape sequence: %q", message)
	}
	if !strings.Contains(message, "\uFFFD") {
		t.Errorf("the escape was not replaced, so nothing scrubbed it: %q", message)
	}
	if strings.Contains(message, `\x1b`) || strings.Contains(message, `\a`) {
		t.Errorf("the escape reached the message and only %%q kept it off the terminal: %q", message)
	}
	if !errors.Is(err, core.ErrSlug) {
		t.Errorf("error = %v, want it to carry core.ErrSlug", err)
	}
}

// The space is what makes the scrub's test one condition rather than two:
// unicode.IsGraphic already keeps it, and the tab and newline beside it are
// replaced.
func TestRepoSlugKeepsTheSpacesInWhatItQuotes(t *testing.T) {
	c, _ := client(t, out("not a slug\tafter a tab"))
	_, err := c.RepoSlug(context.Background())
	if err == nil {
		t.Fatal("an unparseable answer was accepted")
	}
	message := err.Error()
	if !strings.Contains(message, "not a slug") {
		t.Errorf("the spaces were replaced: %q", message)
	}
	if !strings.Contains(message, "\uFFFD") {
		t.Errorf("the tab was not replaced: %q", message)
	}
}

// A payload with no isCrossRepository reads as a fork, not as this repository's
// own branch.
//
// lib/run.sh:284 records absent provenance as `unknown`, and every reader tests
// for an explicit `false`: the automatic-trigger fork refusal at
// lib/run.sh:249, the head-repository branch at lib/run.sh:285 and the
// maintainer-edit guard at lib/legs.sh:478. A Go bool defaulting to false
// collapsed `unknown` onto the one value that means "safe to push", so an
// unreadable payload inherited the permission an upstream branch gets.
func TestPullRequestTreatsAbsentProvenanceAsAFork(t *testing.T) {
	head := "1111111111111111111111111111111111111111"
	base := "2222222222222222222222222222222222222222"
	body := `{"number":42,"title":"t","headRefName":"feature","headRefOid":"` + head + `",
	  "baseRefName":"main","baseRefOid":"` + base + `","state":"OPEN"}`

	c, _ := client(t, out(body))
	pr, err := c.PullRequest(context.Background(), testSlug(t), 42)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}
	if !pr.IsCrossRepository {
		t.Error("an absent isCrossRepository read as this repository's own branch (lib/run.sh:284)")
	}
	if pr.MaintainerCanModify {
		t.Error("an absent maintainerCanModify read as permission granted (lib/run.sh:290)")
	}
}
