package ghexec_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	crexec "github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/forge/ghexec"
)

// The offline suite's fake `gh` logs every invocation as one joined argv line,
// and the shell suites assert on that log. Running the Go client against the
// same stub is what proves the two produce the same calls, rather than the
// recorder tests proving the Go client agrees with itself.
func stubClient(t *testing.T, routes string) (*ghexec.Client, func() []string) {
	t.Helper()

	stub, err := filepath.Abs("../../../tests/stub")
	if err != nil {
		t.Fatalf("locating the stub: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stub, "gh")); err != nil {
		t.Skipf("the stub gh is not in this checkout: %v", err)
	}
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed, and the stub needs it", tool)
		}
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "gh.log")
	routesPath := filepath.Join(dir, "routes")
	if err := os.WriteFile(routesPath, []byte(routes), 0o600); err != nil {
		t.Fatalf("writing the routes: %v", err)
	}
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatalf("writing the log: %v", err)
	}

	// os/exec resolves a bare program name on the PATH of the calling process,
	// so the stub has to be ahead on this one as well as in the child's.
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))

	env := append(crexec.Inherit([]string{"PATH", "HOME"}),
		"CROSSREV_GH_LOG="+logPath, "CROSSREV_GH_ROUTES="+routesPath)

	client := ghexec.New(crexec.NewOrchestratorRunner(), passthrough{}, ghexec.WithEnv(env))
	return client, func() []string {
		raw, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("reading the call log: %v", err)
		}
		var lines []string
		for line := range strings.SplitSeq(strings.TrimSuffix(string(raw), "\n"), "\n") {
			if line != "" {
				lines = append(lines, line)
			}
		}
		return lines
	}
}

func TestAgainstTheStub(t *testing.T) {
	routes := strings.Join([]string{
		"repo view --json nameWithOwner*\t{\"nameWithOwner\":\"acme/widget\"}",
		"api user*\t{\"login\":\"carlosboeing\"}",
		"api repos/*/labels/bug\t{\"color\":\"D73A4A\"}",
		"api --method POST repos/*/issues/42/comments*\t{\"id\":9001}",
		"api --paginate repos/*/issues/42/comments*\t[{\"id\":9001,\"body\":\"Summary.\",\"user\":{\"login\":\"carlosboeing\"}}]",
	}, "\n") + "\n"

	client, calls := stubClient(t, routes)
	ctx := context.Background()

	repo, err := client.RepoSlug(ctx)
	if err != nil {
		t.Fatalf("RepoSlug: %v", err)
	}
	if repo.String() != "acme/widget" {
		t.Fatalf("slug = %q", repo)
	}

	login, err := client.ViewerLogin(ctx)
	if err != nil {
		t.Fatalf("ViewerLogin: %v", err)
	}
	if login != "carlosboeing" {
		t.Errorf("login = %q", login)
	}

	if got := client.LabelColour(ctx, repo, "bug"); got != "d73a4a" {
		t.Errorf("colour = %q", got)
	}

	id, err := client.CommentCreate(ctx, repo, 42, "Summary.")
	if err != nil {
		t.Fatalf("CommentCreate: %v", err)
	}
	if id != 9001 {
		t.Errorf("comment id = %d", id)
	}

	comments := client.IssueComments(ctx, repo, 42)
	if len(comments) != 1 || comments[0].AuthorLogin != "carlosboeing" {
		t.Errorf("comments = %+v", comments)
	}

	want := []string{
		"repo view --json nameWithOwner --jq .nameWithOwner",
		"api user --jq .login",
		"api repos/acme/widget/labels/bug",
		"api --method POST repos/acme/widget/issues/42/comments -f body=Summary. --jq .id",
		"api --paginate repos/acme/widget/issues/42/comments",
	}
	got := calls()
	if len(got) != len(want) {
		t.Fatalf("the stub logged\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

// An inline comment the stub refuses falls back to a top-level one, and the
// log shows both calls.
func TestAgainstTheStubFallsBackWhenTheAnchorIsRefused(t *testing.T) {
	routes := strings.Join([]string{
		"api --method POST repos/*/pulls/42/comments -f body=*\t!fail",
		"api --method POST repos/*/issues/42/comments*\t{\"id\":9002}",
	}, "\n") + "\n"

	client, calls := stubClient(t, routes)
	got, err := client.ReviewCommentCreate(context.Background(), reviewComment(t, "Finding."))
	if err != nil {
		t.Fatalf("ReviewCommentCreate: %v", err)
	}
	if got != forge.PlacementFallback {
		t.Errorf("placement = %q, want fallback", got)
	}

	// The fallback body carries a newline, so its log entry spans two lines —
	// which is what the shell writes there too.
	lines := calls()
	if len(lines) != 3 {
		t.Fatalf("the stub logged\n%s", strings.Join(lines, "\n"))
	}
	if lines[0] != "api --method POST repos/acme/widget/pulls/42/comments -f body=Finding. "+
		"-f commit_id=1111111111111111111111111111111111111111 -f path=app.ts -F line=40 -f side=RIGHT" {
		t.Errorf("inline call = %q", lines[0])
	}
	if lines[1] != "api --method POST repos/acme/widget/issues/42/comments -f body=**app.ts:40** (RIGHT)" ||
		lines[2] != "Finding. --jq .id" {
		t.Errorf("fallback call = %q / %q", lines[1], lines[2])
	}
}

// The stub's own route table is what the shell suites drive, so the daily
// count runs against the route tests/harness.sh declares for it.
func TestAgainstTheStubCountsThroughTheRepositoryCommentsRoute(t *testing.T) {
	routes := "api --method GET repos/*/issues/comments -f since=* -F per_page=100 -F page=*\t[]\n"

	client, calls := stubClient(t, routes)

	got, err := forge.PRsReviewedToday(context.Background(), client, forge.DailyCount{
		Repo: testSlug(t), Author: "crossrev-acme[bot]", Cap: 3, CurrentPR: 42,
	})
	if err != nil {
		t.Fatalf("PRsReviewedToday: %v", err)
	}
	if got != 0 {
		t.Errorf("count = %d, want 0", got)
	}

	lines := calls()
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "api --method GET repos/acme/widget/issues/comments -f since=") ||
		!strings.HasSuffix(last, "-F per_page=100 -F page=1") {
		t.Errorf("page call = %q", last)
	}
}
