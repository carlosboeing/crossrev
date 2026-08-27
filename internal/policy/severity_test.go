package policy_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
)

// TestSeverityRank transcribes lib/legs.sh:60-67. Anything unrecognised ranks
// zero, which cannot meet any real threshold.
func TestSeverityRank(t *testing.T) {
	cases := []struct {
		in   core.Severity
		want int
	}{
		{core.SeverityHigh, 3},
		{core.SeverityMedium, 2},
		{core.SeverityLow, 1},
		{core.Severity(""), 0},
		{core.Severity("critical"), 0},
		{core.Severity("HIGH"), 0},
	}
	for _, tc := range cases {
		if got := policy.SeverityRank(tc.in); got != tc.want {
			t.Errorf("SeverityRank(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestShouldFix transcribes lib/legs.sh:81-87 and the `fixes` block at
// tests/test-legs.sh:57-71, case for case and in the shell's order, plus one
// case the shell has no counterpart for: an empty threshold.
func TestShouldFix(t *testing.T) {
	cases := []struct {
		desc        string
		severity    core.Severity
		bar         core.Severity
		preExisting bool
		want        bool
	}{
		{"at the threshold, the resolver may act", core.SeverityMedium, core.SeverityMedium, false, true},
		{"above it too", core.SeverityHigh, core.SeverityMedium, false, true},
		{"below it, the finding is reported and left", core.SeverityLow, core.SeverityMedium, false, false},
		{"min_fix_severity low takes everything", core.SeverityLow, core.SeverityLow, false, true},
		{"min_fix_severity high takes only the top rung", core.SeverityMedium, core.SeverityHigh, false, false},
		{"a pre-existing finding is never fixed", core.SeverityHigh, core.SeverityLow, true, false},
		{"even with the threshold at its lowest", core.SeverityMedium, core.SeverityLow, true, false},
		{"an unrecognised severity meets no threshold", core.Severity("important"), core.SeverityMedium, false, false},
		{"an unrecognised threshold fixes nothing", core.SeverityHigh, core.Severity("urgent"), false, false},
		// No shell counterpart: the block above never passes an empty
		// min_fix_severity, though legs_should_fix ranks one 0 and refuses the
		// same way. An unset core.Severity is the Go zero value, so a caller
		// reaches this here by writing nothing at all.
		{"an empty threshold fixes nothing", core.SeverityHigh, core.Severity(""), false, false},
	}
	for _, tc := range cases {
		if got := policy.ShouldFix(tc.severity, tc.bar, tc.preExisting); got != tc.want {
			t.Errorf("%s: ShouldFix(%q, %q, %t) = %t, want %t",
				tc.desc, tc.severity, tc.bar, tc.preExisting, got, tc.want)
		}
	}
}
