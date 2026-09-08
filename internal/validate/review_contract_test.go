package validate_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// reviewContract_test.go — Task A4's named acceptance test: the review
// leg's semantic contract over the batch the orchestrator supplied.
//
// The current validator explicitly has no semantic half — findings.go says
// only shape is checked, because the review leg was handed a diff rather
// than a numbering to cover. So a payload that omits a numbered unit,
// answers one twice, or invents a number outside the batch passes today,
// and every semantic case below fails before the change. The shape half
// stays where it is: Findings remains the compatibility entry point for
// tests that do not have an input batch.
//
// File coverage only, verification deferred: no verification, intent-capsule,
// parser or structural-analysis duties enter here.
//
// TestReviewCoverageSemanticContract requires exact unit-number coverage —
// every batch position exactly once — valid finding references, valid
// evidence revisions and spans, evidence for not_affected, and
// failed-fallback reasons for could_not_review. Empty output is a shape
// error; malformed JSON is exit 1; semantic contradiction is exit 2.
func TestReviewCoverageSemanticContract(t *testing.T) {
	base := mustReviewRevision(t, "1111111111111111111111111111111111111111")
	head := mustReviewRevision(t, "2222222222222222222222222222222222222222")
	expect := validate.ReviewExpectations{
		Base: base,
		Head: head,
		Units: []validate.UnitExpectation{
			{Path: "a.go", Revision: head, Lines: 10, Readable: true},
			{Path: "b.go", Revision: head, Lines: 4, Readable: true},
		},
	}

	cases := []struct {
		name    string
		payload string
		want    string
		code    int
	}{
		{
			name: "a complete no-finding response passes",
			payload: reviewPayload(`[]`, reviewCoverage(
				reviewUnitNoIssue(1, "a.go", head.SHA(), 1, 10),
				reviewUnitNoIssue(2, "b.go", head.SHA(), 1, 4),
			)),
		},
		{
			name:    "empty output is a shape error, never clean coverage",
			payload: ``,
			want:    "the payload is empty, and empty output is never clean coverage",
			code:    1,
		},
		{
			name:    "whitespace-only output is a shape error for the same reason",
			payload: "   \n",
			want:    "the payload is empty, and empty output is never clean coverage",
			code:    1,
		},
		{
			name:    "malformed JSON is exit 1",
			payload: `not json`,
			want:    "the payload is not parseable JSON",
			code:    1,
		},
		{
			name:    "a missing top-level coverage array is exit 1, not a silent loss",
			payload: `{"verdict":"converged","findings":[],"examined_scope":"read both files","known_limits":[]}`,
			want:    "coverage is missing or not an array",
			code:    1,
		},
		{
			name:    "a missing examined_scope is exit 1",
			payload: `{"verdict":"converged","findings":[],"coverage":[],"known_limits":[]}`,
			want:    "examined_scope is missing",
			code:    1,
		},
		{
			name:    "a missing known_limits array is exit 1",
			payload: `{"verdict":"converged","findings":[],"coverage":[],"examined_scope":"read both files"}`,
			want:    "known_limits is missing or not an array",
			code:    1,
		},
		{
			name: "an omitted unit number is a semantic contradiction",
			payload: reviewPayload(`[]`, reviewCoverage(
				reviewUnitNoIssue(1, "a.go", head.SHA(), 1, 10),
			)),
			want: "coverage is missing unit number(s) 2 — 2 unit(s) were supplied, numbered 1 to 2",
			code: 2,
		},
		{
			name: "a duplicate unit number is a semantic contradiction",
			payload: reviewPayload(`[]`, reviewCoverage(
				reviewUnitNoIssue(1, "a.go", head.SHA(), 1, 10),
				reviewUnitNoIssue(1, "a.go", head.SHA(), 1, 10),
			)),
			want: "coverage is missing unit number(s) 2; names unit number(s) 1 more than once — 2 unit(s) were supplied, numbered 1 to 2",
			code: 2,
		},
		{
			name: "an invented unit number is a semantic contradiction",
			payload: reviewPayload(`[]`, reviewCoverage(
				reviewUnitNoIssue(1, "a.go", head.SHA(), 1, 10),
				reviewUnitNoIssue(9, "b.go", head.SHA(), 1, 4),
			)),
			want: "coverage is missing unit number(s) 2; names unknown unit number(s) 9 — 2 unit(s) were supplied, numbered 1 to 2",
			code: 2,
		},
		{
			name: "a finding number past the end is a semantic contradiction",
			payload: reviewPayload(`[]`, reviewCoverage(
				reviewUnitFinding(1, "a.go", head.SHA(), 1, 10, 7),
				reviewUnitNoIssue(2, "b.go", head.SHA(), 1, 4),
			)),
			want: "coverage for unit 1 names finding number 7, but 0 finding(s) were returned, numbered 1 to 0",
			code: 2,
		},
		{
			name: "finding with no finding number is a semantic contradiction",
			payload: reviewPayload(`[]`, reviewCoverage(
				`{"unit_number":1,"disposition":"finding","finding_numbers":[],"evidence":[`+reviewGitEvidence("a.go", head.SHA(), 1, 10)+`],"reason":null}`,
				reviewUnitNoIssue(2, "b.go", head.SHA(), 1, 4),
			)),
			want: "coverage for unit 1 says finding but names no finding number",
			code: 2,
		},
		{
			name: "evidence naming an unprovided path is a semantic contradiction",
			payload: reviewPayload(`[]`, reviewCoverage(
				reviewUnitNoIssue(1, "elsewhere.go", head.SHA(), 1, 10),
				reviewUnitNoIssue(2, "b.go", head.SHA(), 1, 4),
			)),
			want: `coverage for unit 1 cites evidence path "elsewhere.go", which the batch did not supply`,
			code: 2,
		},
		{
			name: "evidence naming an unprovided revision is a semantic contradiction",
			payload: reviewPayload(`[]`, reviewCoverage(
				reviewUnitNoIssue(1, "a.go", "3333333333333333333333333333333333333333", 1, 10),
				reviewUnitNoIssue(2, "b.go", head.SHA(), 1, 4),
			)),
			want: `coverage for unit 1 cites evidence revision "3333333333333333333333333333333333333333", which is neither the base nor the head`,
			code: 2,
		},
		{
			name: "an inverted span is a semantic contradiction",
			payload: reviewPayload(`[]`, reviewCoverage(
				reviewUnitNoIssue(1, "a.go", head.SHA(), 8, 3),
				reviewUnitNoIssue(2, "b.go", head.SHA(), 1, 4),
			)),
			want: "coverage for unit 1 cites lines 8-3, and a span cannot end before it starts",
			code: 2,
		},
		{
			name: "a span outside the supplied content is a semantic contradiction",
			payload: reviewPayload(`[]`, reviewCoverage(
				reviewUnitNoIssue(1, "a.go", head.SHA(), 1, 40),
				reviewUnitNoIssue(2, "b.go", head.SHA(), 1, 4),
			)),
			want: "coverage for unit 1 cites lines 1-40 outside the 10 readable line(s) supplied",
			code: 2,
		},
		{
			name: "not_affected without evidence and a reason is a semantic contradiction",
			payload: reviewPayload(`[]`, reviewCoverage(
				`{"unit_number":1,"disposition":"not_affected","finding_numbers":[],"evidence":[],"reason":null}`,
				reviewUnitNoIssue(2, "b.go", head.SHA(), 1, 4),
			)),
			want: "coverage for unit 1 says not_affected without evidence and a reason, and a changed file is not cleared by assertion",
			code: 2,
		},
		{
			name: "not_affected with evidence and a reason passes",
			payload: reviewPayload(`[]`, reviewCoverage(
				`{"unit_number":1,"disposition":"not_affected","finding_numbers":[],"evidence":[`+reviewGitEvidence("a.go", head.SHA(), 1, 10)+`],"reason":"generated file, matches its source"}`,
				reviewUnitNoIssue(2, "b.go", head.SHA(), 1, 4),
			)),
		},
		{
			name: "could_not_review without a reason is a semantic contradiction",
			payload: reviewPayload(`[]`, reviewCoverage(
				`{"unit_number":1,"disposition":"could_not_review","finding_numbers":[],"evidence":[],"reason":null}`,
				reviewUnitNoIssue(2, "b.go", head.SHA(), 1, 4),
			)),
			want: "coverage for unit 1 says could_not_review without the failed fallbacks in reason",
			code: 2,
		},
		{
			name: "could_not_review with the failed fallbacks in reason passes",
			payload: reviewPayload(`[]`, reviewCoverage(
				`{"unit_number":1,"disposition":"could_not_review","finding_numbers":[],"evidence":[],"reason":"submodule fetch failed, then LFS fetch failed"}`,
				reviewUnitNoIssue(2, "b.go", head.SHA(), 1, 4),
			)),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate.Review([]byte(tc.payload), expect)
			if got := reviewTestMessage(err); got != tc.want {
				t.Fatalf("message:\n got %q\nwant %q", got, tc.want)
			}
			if got := reviewTestCode(err); got != tc.code {
				t.Fatalf("exit code: got %d, want %d", got, tc.code)
			}
		})
	}
}

// The check below pins the retry prompt's quoting order: the rejected numbers
// arrive missing first, then duplicates, then unknown, each sorted low to
// high — so one retry names exactly what to fix.

func TestReviewNamesMissingDuplicateAndUnknownInRetryOrder(t *testing.T) {
	base := mustReviewRevision(t, "1111111111111111111111111111111111111111")
	head := mustReviewRevision(t, "2222222222222222222222222222222222222222")
	expect := validate.ReviewExpectations{
		Base: base,
		Head: head,
		Units: []validate.UnitExpectation{
			{Path: "a.go", Revision: head, Lines: 10, Readable: true},
			{Path: "b.go", Revision: head, Lines: 10, Readable: true},
			{Path: "c.go", Revision: head, Lines: 10, Readable: true},
		},
	}
	payload := reviewPayload(`[]`, reviewCoverage(
		reviewUnitNoIssue(3, "c.go", head.SHA(), 1, 10),
		reviewUnitNoIssue(3, "c.go", head.SHA(), 1, 10),
		reviewUnitNoIssue(9, "c.go", head.SHA(), 1, 10),
	))
	err := validate.Review([]byte(payload), expect)
	want := "coverage is missing unit number(s) 1, 2; names unit number(s) 3 more than once; " +
		"names unknown unit number(s) 9 — 3 unit(s) were supplied, numbered 1 to 3"
	if got := reviewTestMessage(err); got != want {
		t.Fatalf("message:\n got %q\nwant %q", got, want)
	}
	if got := reviewTestCode(err); got != 2 {
		t.Fatalf("exit code: got %d, want 2", got)
	}
}

// Set equality is against the prompt's own numbering, not discovery order:
// the same two paths in the opposite positions accept the opposite numbers.
func TestReviewNumberingFollowsThePromptNotDiscoveryOrder(t *testing.T) {
	base := mustReviewRevision(t, "1111111111111111111111111111111111111111")
	head := mustReviewRevision(t, "2222222222222222222222222222222222222222")
	expect := validate.ReviewExpectations{
		Base: base,
		Head: head,
		Units: []validate.UnitExpectation{
			{Path: "b.go", Revision: head, Lines: 10, Readable: true},
			{Path: "a.go", Revision: head, Lines: 10, Readable: true},
		},
	}
	payload := reviewPayload(`[]`, reviewCoverage(
		reviewUnitNoIssue(1, "b.go", head.SHA(), 1, 10),
		reviewUnitNoIssue(2, "a.go", head.SHA(), 1, 10),
	))
	if err := validate.Review([]byte(payload), expect); err != nil {
		t.Fatalf("wanted prompt-order numbers accepted, got %q", err)
	}
}

func mustReviewRevision(t *testing.T, sha string) core.Revision {
	t.Helper()
	rev, err := core.NewRevision(sha)
	if err != nil {
		t.Fatalf("revision %q: %v", sha, err)
	}
	return rev
}

// reviewPayload wraps findings and coverage entries in the shape the reviewer
// contract requires: a verdict, both arrays, and the scope report that stays
// even when nothing was found.
func reviewPayload(findings, coverage string) string {
	return `{"verdict":"converged","findings":` + findings +
		`,"coverage":` + coverage +
		`,"examined_scope":"read the two batch files","known_limits":[]}`
}

// reviewCoverage joins coverage entries into the array the payload carries.
func reviewCoverage(entries ...string) string {
	return "[" + strings.Join(entries, ",") + "]"
}

// reviewGitEvidence is one git-sourced evidence item over the named path and
// revision with a whole-file line span.
func reviewGitEvidence(path, revision string, start, end int) string {
	return `{"path":` + reviewQuoteString(path) +
		`,"revision":` + reviewQuoteString(revision) +
		`,"start_line":` + reviewItoa(start) +
		`,"end_line":` + reviewItoa(end) +
		`,"source":"git","note":null}`
}

// reviewUnitNoIssue is a clean disposition over one evidence item.
func reviewUnitNoIssue(number int, path, revision string, start, end int) string {
	return `{"unit_number":` + reviewItoa(number) +
		`,"disposition":"no_issue","finding_numbers":[]` +
		`,"evidence":[` + reviewGitEvidence(path, revision, start, end) + `]` +
		`,"reason":null}`
}

// reviewUnitFinding is a finding disposition naming one finding number.
func reviewUnitFinding(number int, path, revision string, start, end, finding int) string {
	return `{"unit_number":` + reviewItoa(number) +
		`,"disposition":"finding","finding_numbers":[` + reviewItoa(finding) + `]` +
		`,"evidence":[` + reviewGitEvidence(path, revision, start, end) + `]` +
		`,"reason":null}`
}

func reviewQuoteString(s string) string {
	return `"` + s + `"`
}

func reviewItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func reviewTestMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func reviewTestCode(err error) int {
	if err == nil {
		return 0
	}
	return code(err)
}
