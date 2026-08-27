package policy

import "github.com/carlosboeing/crossrev/internal/core"

// SeverityRank is the ordinal rank of a severity word (lib/legs.sh:60-67).
//
// Anything unrecognised ranks zero, which cannot meet any real threshold. That
// is the fail-closed half of ShouldFix, and it is why the rank exists as its own
// function rather than as a comparison inline.
func SeverityRank(s core.Severity) int {
	switch s {
	case core.SeverityHigh:
		return 3
	case core.SeverityMedium:
		return 2
	case core.SeverityLow:
		return 1
	default:
		return 0
	}
}

// ShouldFix reports whether the resolve leg may change code for one finding
// (lib/legs.sh:81-87).
//
// Two rules, in order. A pre-existing defect is never fixed here whatever its
// severity — that is the one guardrail deliberately not configurable, because a
// pull request that also fixes old bugs is one nobody can review. Otherwise the
// severity has to reach the threshold.
//
// An unrecognised threshold fixes nothing rather than guessing. The two ways to
// be wrong are not symmetrical: refusing to fix leaves a finding reported and a
// human to read it, while guessing leaves an unattended commit nobody asked for.
func ShouldFix(severity, minFixSeverity core.Severity, preExisting bool) bool {
	if preExisting {
		return false
	}
	bar := SeverityRank(minFixSeverity)
	if bar == 0 {
		return false
	}
	return SeverityRank(severity) >= bar
}
