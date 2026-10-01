package resolve

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// The rewrite renders the same visible set the review leg published: a
// rejected candidate stays out of the table and the counts, the
// rejection line survives, and the held count never mistakes a
// rejection for a hold.
func TestReviewSummaryRewriteKeepsTheCheckLines(t *testing.T) {
	findings := json.RawMessage(`[{` +
		`"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,"severity":"high","category":"correctness","pre_existing":false,"title":"real bug"},` +
		`{` +
		`"id":"bbbbbbbbbbbbbbbb","path":"a.go","line":2,"severity":"low","category":"maintainability","pre_existing":false,"title":"wrong nit","posted":false}]`)
	marker := prstate.Marker{
		Pass:        1,
		HeadSHA:     prstate.Some(testHeadSHA),
		Harness:     prstate.Some("claude"),
		Verdict:     prstate.Some(string(core.VerdictIssuesRemain)),
		Check:       prstate.Some(prstate.CheckRan),
		CheckedOut:  json.RawMessage(`[{"position":2,"id":"bbbbbbbbbbbbbbbb","path":"a.go","line":2,"title":"wrong nit","reason":"the type is exactly right","decision":"rejected"}]`),
	}
	got := reviewSummaryBody(findings, marker, mustSlug(t), 42, core.SeverityMedium, 3, commentCoverage{})
	if strings.Contains(got, "wrong nit") && strings.Contains(got, "| low") {
		t.Errorf("the rewrite lists the rejected finding:\n%s", got)
	}
	if !strings.Contains(got, "1 candidate rejected by the cross-model check:") {
		t.Errorf("the rewrite strips the rejection line:\n%s", got)
	}
	if !strings.Contains(got, "the type is exactly right") {
		t.Errorf("the rewrite strips the rejection reason:\n%s", got)
	}
	if strings.Contains(got, "recorded and not posted") {
		t.Errorf("the rewrite counts the rejection as held:\n%s", got)
	}
	// The rewrite's singular reads "need", as it always has; what
	// matters here is the count of one, not the rejected two.
	if !strings.Contains(got, "**1 finding need resolving.**") {
		t.Errorf("the rewrite counts the rejected candidate as resolving:\n%s", got)
	}
}

// A pass whose every candidate stayed off the pull request rewrites to
// the same no-findings lines the review leg wrote, not to a second
// agent sent after zero findings.
func TestReviewSummaryRewriteWithEveryCandidateCheckedOut(t *testing.T) {
	findings := json.RawMessage(`[{` +
		`"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,"severity":"high","category":"correctness","pre_existing":false,"title":"wrong bug","posted":false}]`)
	marker := prstate.Marker{
		Pass:       1,
		HeadSHA:    prstate.Some(testHeadSHA),
		Harness:    prstate.Some("claude"),
		Verdict:    prstate.Some(string(core.VerdictIssuesRemain)),
		Check:      prstate.Some(prstate.CheckRan),
		CheckedOut: json.RawMessage(`[{"position":1,"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,"title":"wrong bug","reason":"no such call","decision":"rejected"}]`),
	}
	got := reviewSummaryBody(findings, marker, mustSlug(t), 42, core.SeverityMedium, 3, commentCoverage{})
	if !strings.Contains(got, "**No findings need resolving.** Every candidate stayed off the pull request; the list below names them.") {
		t.Errorf("the rewrite sends a second agent after zero findings:\n%s", got)
	}
	if !strings.Contains(got, "No findings posted.") {
		t.Errorf("the rewrite claims an empty review:\n%s", got)
	}
}

// A degraded check's note survives the rewrite.
func TestReviewSummaryRewriteKeepsTheDegradeNote(t *testing.T) {
	findings := json.RawMessage(`[{` +
		`"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,"severity":"high","category":"correctness","pre_existing":false,"title":"real bug"}]`)
	marker := prstate.Marker{
		Pass:        1,
		HeadSHA:     prstate.Some(testHeadSHA),
		Harness:     prstate.Some("claude"),
		Verdict:     prstate.Some(string(core.VerdictIssuesRemain)),
		Check:       prstate.Some(prstate.CheckDegraded),
		CheckReason: prstate.Some("quota"),
	}
	got := reviewSummaryBody(findings, marker, mustSlug(t), 42, core.SeverityMedium, 3, commentCoverage{})
	if !strings.Contains(got, "check: degraded (quota): every candidate posted unchecked.") {
		t.Errorf("the rewrite strips the degrade note:\n%s", got)
	}
}

// The resolve summary never counts a checked-out entry as held: the
// rejection never reached the resolver.
func TestResolveSummaryExcludesCheckedOutFromHeld(t *testing.T) {
	findings := json.RawMessage(`[{` +
		`"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,"severity":"high","category":"correctness","pre_existing":false,"title":"real bug"},` +
		`{` +
		`"id":"bbbbbbbbbbbbbbbb","path":"a.go","line":2,"severity":"low","category":"maintainability","pre_existing":false,"title":"wrong nit","posted":false}]`)
	marker := prstate.Marker{
		Pass:       1,
		Harness:    prstate.Some("claude"),
		Summary:    prstate.Some("fixed the bug"),
		CheckedOut: json.RawMessage(`[{"position":2,"id":"bbbbbbbbbbbbbbbb","path":"a.go","line":2,"title":"wrong nit","reason":"r","decision":"rejected"}]`),
	}
	got := ResolveSummaryBody(json.RawMessage(`[{"id":"aaaaaaaaaaaaaaaa","resolution":"fixed"}]`), findings, "", marker, "acme/widget", 42, 3)
	if strings.Contains(got, "held finding recorded and not posted") {
		t.Errorf("the resolve summary counts the rejection as held:\n%s", got)
	}
}
