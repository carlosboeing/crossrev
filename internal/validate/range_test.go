package validate_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// rangeExpectation is the two-file batch the supplied-range cases run
// against. a.go is read at the head with only two neighbourhoods shown —
// lines 1-3 and 8-10 of a ten-line file — while the base side was shown in
// full. old.go is a deletion: read at the base in full, with nothing shown
// on the head side.
func rangeExpectation(t *testing.T) validate.ReviewExpectations {
	t.Helper()
	base := mustReviewRevision(t, "1111111111111111111111111111111111111111")
	head := mustReviewRevision(t, "2222222222222222222222222222222222222222")
	span := func(start, end int) core.LineSpan { return core.LineSpan{Start: start, End: end} }
	return validate.ReviewExpectations{
		Base: base,
		Head: head,
		Units: []validate.UnitExpectation{
			{
				Path: "a.go", Revision: head, Lines: 10, Readable: true,
				Ranges: core.SuppliedRanges{
					Base: []core.LineSpan{span(1, 10)},
					Head: []core.LineSpan{span(1, 3), span(8, 10)},
				},
			},
			{
				Path: "old.go", Revision: base, Lines: 10, Readable: true,
				Ranges: core.SuppliedRanges{
					Base: []core.LineSpan{span(1, 10)},
				},
			},
		},
	}
}

// A span outside the supplied ranges is refused, even when it sits inside
// the whole file: the reviewer never saw lines 5-6 on the head side, so a
// judgement resting on them is not a judgement.
func TestSuppliedRangeRefusesSpanOutsideSuppliedRanges(t *testing.T) {
	expect := rangeExpectation(t)
	head := "2222222222222222222222222222222222222222"
	payload := reviewPayload(`[]`, reviewCoverage(
		reviewUnitNoIssue(1, "a.go", head, 5, 6),
		reviewUnitNoIssue(2, "old.go", "1111111111111111111111111111111111111111", 1, 10),
	))
	err := validate.Review([]byte(payload), expect)
	want := "coverage for unit 1 cites lines 5-6 outside the supplied head ranges (1-3, 8-10)"
	if got := reviewTestMessage(err); got != want {
		t.Fatalf("message:\n got %q\nwant %q", got, want)
	}
	if got := reviewTestCode(err); got != 2 {
		t.Fatalf("exit code: got %d, want 2", got)
	}
}

// A base-side span cited at the base is accepted, even when the head side
// never showed those lines: the ranges answer per side, not per file.
func TestSuppliedRangeAcceptsBaseSpanCitedAtBase(t *testing.T) {
	expect := rangeExpectation(t)
	payload := reviewPayload(`[]`, reviewCoverage(
		reviewUnitNoIssue(1, "a.go", "1111111111111111111111111111111111111111", 5, 6),
		reviewUnitNoIssue(2, "old.go", "1111111111111111111111111111111111111111", 1, 10),
	))
	if err := validate.Review([]byte(payload), expect); err != nil {
		t.Fatalf("wanted a base-side span cited at the base accepted, got %q", err)
	}
}

// A removed line cited at the base is accepted: a deletion reads at the
// base, so its lines live on the base side alone and the head side's empty
// ranges never answer for them.
func TestEvidenceRemovedLineCitedAtBaseAccepted(t *testing.T) {
	expect := rangeExpectation(t)
	payload := reviewPayload(`[]`, reviewCoverage(
		reviewUnitNoIssue(1, "a.go", "2222222222222222222222222222222222222222", 1, 3),
		reviewUnitNoIssue(2, "old.go", "1111111111111111111111111111111111111111", 5, 5),
	))
	if err := validate.Review([]byte(payload), expect); err != nil {
		t.Fatalf("wanted a removed line cited at the base accepted, got %q", err)
	}
}

// A head-side citation for a line only the base side showed is refused:
// the revision names the side, and the other side's ranges do not cover.
func TestSuppliedRangeRefusesHeadCitationForABaseOnlyLine(t *testing.T) {
	expect := rangeExpectation(t)
	head := "2222222222222222222222222222222222222222"
	payload := reviewPayload(`[]`, reviewCoverage(
		reviewUnitNoIssue(1, "a.go", head, 1, 3),
		reviewUnitNoIssue(2, "old.go", head, 5, 5),
	))
	err := validate.Review([]byte(payload), expect)
	want := "coverage for unit 2 cites lines 5-5 outside the supplied head ranges (none)"
	if got := reviewTestMessage(err); got != want {
		t.Fatalf("message:\n got %q\nwant %q", got, want)
	}
	if got := reviewTestCode(err); got != 2 {
		t.Fatalf("exit code: got %d, want 2", got)
	}
}
