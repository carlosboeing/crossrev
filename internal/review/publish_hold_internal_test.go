package review

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
)

// The hold predicate behind the pass-2 posting filter: passes after the
// first hold findings whose severity ranks below min_fix_severity, whatever
// their pre-existing flag. Pass 1 posts everything, and anything at or above
// the threshold — pre-existing included — still posts.
func TestPublishHoldPredicatePinsEverySliceCase(t *testing.T) {
	held := func(severity, minFix string, pass int, pre bool) bool {
		return holdBelowThreshold(Finding{Severity: severity, PreExisting: pre}, minFix, pass)
	}
	cases := []struct {
		name     string
		severity string
		minFix   string
		pass     int
		pre      bool
		want     bool
	}{
		{"low after the first pass is held", "low", "medium", 2, false, true},
		{"low on pass 1 posts", "low", "medium", 1, false, false},
		{"at the threshold posts", "medium", "medium", 2, false, false},
		{"above the threshold posts", "high", "medium", 2, false, false},
		{"pre-existing at the threshold posts", "medium", "medium", 2, true, false},
		{"pre-existing above the threshold posts", "high", "medium", 2, true, false},
		{"pre-existing below the threshold is held", "low", "medium", 2, true, true},
		{"re-raised at a higher severity posts", "medium", "medium", 3, false, false},
		{"high threshold holds medium", "medium", "high", 2, false, true},
		{"low threshold holds nothing new", "low", "low", 2, false, false},
		{"an unknown severity cannot post", "nonsense", "medium", 2, false, true},
		{"an unknown threshold holds nothing", "low", "nonsense", 2, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := held(tc.severity, tc.minFix, tc.pass, tc.pre); got != tc.want {
				t.Errorf("holdBelowThreshold(%q, %q, pass %d, pre %v) = %v, want %v",
					tc.severity, tc.minFix, tc.pass, tc.pre, got, tc.want)
			}
		})
	}
}

// holdBelowThreshold is a severity comparison, not ShouldFix: pre-existing
// findings never fix but still post at or above the threshold, so the two
// must disagree exactly there.
func TestPublishHoldPredicateDisagreesWithShouldFixOnPreExisting(t *testing.T) {
	f := Finding{Severity: "high", PreExisting: true}
	if policy.ShouldFix(core.SeverityHigh, core.SeverityMedium, true) {
		t.Fatal("ShouldFix fixes a pre-existing finding")
	}
	if holdBelowThreshold(f, "medium", 2) {
		t.Error("the hold swallowed a pre-existing finding at the threshold")
	}
}

// Absent posted reads as posted, keeping markers written before the field
// existed readable: only an explicit false holds a finding back.
func TestPostedAbsentReadsAsPosted(t *testing.T) {
	if !(Finding{}).IsPosted() {
		t.Error("a finding without posted reads as held")
	}
	if !(Finding{Posted: boolPtr(true)}).IsPosted() {
		t.Error("a finding with posted:true reads as held")
	}
	if (Finding{Posted: boolPtr(false)}).IsPosted() {
		t.Error("a finding with posted:false reads as posted")
	}
}

func boolPtr(b bool) *bool { return &b }
