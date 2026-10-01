package ghexec_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
)

func checkHead(t *testing.T) core.Revision {
	t.Helper()
	head, err := core.NewRevision("1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("NewRevision: %v", err)
	}
	return head
}

// One page of two runs decodes with the fields the evaluator matches on:
// the name, the app slug, the status, the conclusion and the run URL.
func TestCheckRunsDecodesOnePage(t *testing.T) {
	c, r := client(t, out(`{"total_count":2,"check_runs":[`+
		`{"id":11,"name":"build","status":"completed","conclusion":"success",`+
		`"html_url":"https://github.com/acme/widget/runs/11","app":{"slug":"github-actions"}},`+
		`{"id":12,"name":"lint","status":"in_progress","conclusion":null,`+
		`"html_url":"https://github.com/acme/widget/runs/12","app":{"slug":"github-actions"}}]}`))

	got, err := c.CheckRuns(context.Background(), testSlug(t), checkHead(t))
	if err != nil {
		t.Fatalf("CheckRuns: %v", err)
	}
	if got.Truncated {
		t.Error("a complete enumeration reads as truncated")
	}
	if len(got.Runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(got.Runs))
	}
	build := got.Runs[0]
	if build.ID != 11 || build.Name != "build" || build.App != "github-actions" ||
		build.Status != "completed" || build.Conclusion != "success" ||
		build.URL != "https://github.com/acme/widget/runs/11" {
		t.Errorf("build = %+v", build)
	}
	lint := got.Runs[1]
	if lint.ID != 12 || lint.Status != "in_progress" || lint.Conclusion != "" {
		t.Errorf("lint = %+v, want the null conclusion as empty", lint)
	}
	r.wantArgs(t, 0, "api", "repos/acme/widget/commits/1111111111111111111111111111111111111111/check-runs",
		"-F", "per_page=100", "-F", "page=1")
}

// A full first page is followed to the second, and the runs concatenate in
// page order.
func TestCheckRunsPaginatesToCompletion(t *testing.T) {
	first := `{"total_count":101,"check_runs":[` + strings.Repeat(`{"id":1,"name":"a","status":"completed","conclusion":"success","html_url":"u","app":{"slug":"github-actions"}},`, 99) +
		`{"id":100,"name":"a","status":"completed","conclusion":"success","html_url":"u","app":{"slug":"github-actions"}}]}`
	second := `{"total_count":101,"check_runs":[{"id":101,"name":"b","status":"completed","conclusion":"success","html_url":"u","app":{"slug":"github-actions"}}]}`
	c, r := client(t, out(first), out(second))

	got, err := c.CheckRuns(context.Background(), testSlug(t), checkHead(t))
	if err != nil {
		t.Fatalf("CheckRuns: %v", err)
	}
	if len(got.Runs) != 101 {
		t.Fatalf("runs = %d, want 101", len(got.Runs))
	}
	if got.Runs[100].ID != 101 {
		t.Errorf("last run = %+v, want the second page's", got.Runs[100])
	}
	if got.Truncated {
		t.Error("an enumeration that reached its total reads as truncated")
	}
	if len(r.specs) != 2 {
		t.Fatalf("gh calls = %d, want 2", len(r.specs))
	}
	r.wantArgs(t, 1, "api", "repos/acme/widget/commits/1111111111111111111111111111111111111111/check-runs",
		"-F", "per_page=100", "-F", "page=2")
}

// A response whose total exceeds the runs it lists is truncated, and the
// evaluator fails closed on that rather than judging a partial list.
func TestCheckRunsReportsTruncation(t *testing.T) {
	c, _ := client(t, out(`{"total_count":3,"check_runs":[`+
		`{"id":11,"name":"build","status":"completed","conclusion":"success","html_url":"u","app":{"slug":"github-actions"}}]}`))

	got, err := c.CheckRuns(context.Background(), testSlug(t), checkHead(t))
	if err != nil {
		t.Fatalf("CheckRuns: %v", err)
	}
	if !got.Truncated {
		t.Error("a total above the listed runs reads as complete")
	}
	if len(got.Runs) != 1 {
		t.Errorf("runs = %d, want the one listed run beside the flag", len(got.Runs))
	}
}

// A 403 names the missing permission: the token may not read check runs,
// which is the App's checks: read. A 404 is the same refusal — GitHub
// answers 404 where it will not admit 403.
func TestCheckRunsDenialNamesThePermission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		status int
	}{
		{"forbidden", "gh: Resource not accessible by integration (HTTP 403)\n", 403},
		{"not found", "gh: Not Found (HTTP 404)\n", 404},
	} {
		c, _ := client(t, exec.Result{ExitCode: 1, Stderr: []byte(tc.output)})
		_, err := c.CheckRuns(context.Background(), testSlug(t), checkHead(t))
		var denied *forge.CheckRunsDenied
		if !errors.As(err, &denied) {
			t.Errorf("%s: error = %v, want a *CheckRunsDenied", tc.name, err)
			continue
		}
		if denied.Status != tc.status {
			t.Errorf("%s: status = %d, want %d", tc.name, denied.Status, tc.status)
		}
		if !strings.Contains(err.Error(), "checks") {
			t.Errorf("%s: error %q names no permission", tc.name, err)
		}
	}
}

// Against the offline suite's stub the client issues the same call the
// shell suites would see in the log, and reads the routed enumeration.
func TestCheckRunsAgainstTheStub(t *testing.T) {
	routes := "api repos/acme/widget/commits/1111111111111111111111111111111111111111/check-runs*\t" +
		"{\"total_count\":1,\"check_runs\":[{\"id\":11,\"name\":\"build\",\"status\":\"completed\"," +
		"\"conclusion\":\"success\",\"html_url\":\"https://github.com/acme/widget/runs/11\"," +
		"\"app\":{\"slug\":\"github-actions\"}}]}\n"
	client, calls := stubClient(t, routes)

	got, err := client.CheckRuns(context.Background(), testSlug(t), checkHead(t))
	if err != nil {
		t.Fatalf("CheckRuns: %v", err)
	}
	if len(got.Runs) != 1 || got.Runs[0].Name != "build" || got.Runs[0].Conclusion != "success" {
		t.Fatalf("runs = %+v", got.Runs)
	}
	want := "api repos/acme/widget/commits/1111111111111111111111111111111111111111/check-runs -F per_page=100 -F page=1"
	if lines := calls(); len(lines) != 1 || lines[0] != want {
		t.Errorf("calls = %q, want %q", lines, want)
	}
}

// Anything else — a refused invocation with no status, a child that never
// ran, a page that will not parse — is an ordinary error, which the
// evaluator reads as unreadable with the error.
func TestCheckRunsOtherFailuresAreOrdinaryErrors(t *testing.T) {
	c, _ := client(t, bad())
	if _, err := c.CheckRuns(context.Background(), testSlug(t), checkHead(t)); err == nil {
		t.Error("a refused read returned no error")
	} else {
		var denied *forge.CheckRunsDenied
		if errors.As(err, &denied) {
			t.Errorf("a statusless refusal reads as denied: %v", err)
		}
	}

	c, _ = client(t, unresolved())
	if _, err := c.CheckRuns(context.Background(), testSlug(t), checkHead(t)); err == nil {
		t.Error("an unstarted child returned no error")
	}

	c, _ = client(t, out(`not json`))
	if _, err := c.CheckRuns(context.Background(), testSlug(t), checkHead(t)); err == nil {
		t.Error("an unparsable page returned no error")
	}
}
