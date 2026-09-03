package cycle

import (
	"encoding/json"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// TestCountActionableMatchesTheShellBar pins run_actionable (lib/run.sh:356-367)
// on the three shapes the driver actually meets: a pre-existing finding, one
// below the bar, and one at it.
//
// Measured against the shell, with lib/legs.sh sourced for legs_severity_rank:
//
//	$ CTX_MIN_FIX_SEVERITY=medium run_actionable '[{"severity":"high"}]'          -> 1
//	$ CTX_MIN_FIX_SEVERITY=medium run_actionable '[{"severity":"medium"}]'        -> 1
//	$ CTX_MIN_FIX_SEVERITY=medium run_actionable '[{"severity":"low"}]'           -> 0
//	$ CTX_MIN_FIX_SEVERITY=medium run_actionable '[{"severity":"high","pre_existing":true}]' -> 0
//	$ CTX_MIN_FIX_SEVERITY=medium run_actionable '[{"severity":"bogus"}]'         -> 0
//	$ CTX_MIN_FIX_SEVERITY=none   run_actionable '[{"severity":"high"}]'          -> 0
func TestCountActionableMatchesTheShellBar(t *testing.T) {
	cases := []struct {
		name     string
		findings string
		minFix   core.Severity
		want     int
	}{
		{"an empty array counts nothing", `[]`, core.SeverityMedium, 0},
		{"high is at or above a medium bar", `[{"severity":"high"}]`, core.SeverityMedium, 1},
		{"medium is at the bar", `[{"severity":"medium"}]`, core.SeverityMedium, 1},
		{"low is below it", `[{"severity":"low"}]`, core.SeverityMedium, 0},
		{"pre-existing is never actionable", `[{"severity":"high","pre_existing":true}]`, core.SeverityMedium, 0},
		{"an unranked severity is below every bar", `[{"severity":"bogus"}]`, core.SeverityMedium, 0},
		{"a bar of zero rank counts nothing", `[{"severity":"high"}]`, core.Severity("none"), 0},
		{"an empty bar counts nothing", `[{"severity":"high"}]`, core.Severity(""), 0},
		{
			"a mixed array counts only what clears the bar",
			`[{"severity":"high"},{"severity":"high","pre_existing":true},{"severity":"low"},{"severity":"medium"}]`,
			core.SeverityMedium, 2,
		},
		{"a low bar admits low", `[{"severity":"low"}]`, core.SeverityLow, 1},
		{"a high bar refuses medium", `[{"severity":"medium"}]`, core.SeverityHigh, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := actionableCount(json.RawMessage(tc.findings), tc.minFix); got != tc.want {
				t.Errorf("actionableCount(%s, %q) = %d, want %d", tc.findings, tc.minFix, got, tc.want)
			}
		})
	}
}

// TestCountActionableReadsPreExistingAsJqDoes pins the truthiness rule behind
// `select((.pre_existing // false) | not)` (lib/run.sh:364). jq calls everything
// except false and null true, so a zero and an empty string both mark a finding
// pre-existing. Measured:
//
//	$ CTX_MIN_FIX_SEVERITY=medium run_actionable '[{"severity":"high","pre_existing":false}]' -> 1
//	$ CTX_MIN_FIX_SEVERITY=medium run_actionable '[{"severity":"high","pre_existing":"true"}]' -> 0
func TestCountActionableReadsPreExistingAsJqDoes(t *testing.T) {
	cases := []struct {
		findings string
		want     int
	}{
		{`[{"severity":"high"}]`, 1},
		{`[{"severity":"high","pre_existing":null}]`, 1},
		{`[{"severity":"high","pre_existing":false}]`, 1},
		{`[{"severity":"high","pre_existing":true}]`, 0},
		{`[{"severity":"high","pre_existing":"true"}]`, 0},
		{`[{"severity":"high","pre_existing":"false"}]`, 0},
		{`[{"severity":"high","pre_existing":0}]`, 0},
		{`[{"severity":"high","pre_existing":""}]`, 0},
	}
	for _, tc := range cases {
		if got := actionableCount(json.RawMessage(tc.findings), core.SeverityMedium); got != tc.want {
			t.Errorf("actionableCount(%s) = %d, want %d", tc.findings, got, tc.want)
		}
	}
}

// TestCountActionableTreatsAMissingSeverityAsUnranked is the one place this
// count does not reproduce the shell byte for byte, so it is pinned rather than
// left to be rediscovered.
//
// `$r[.severity]` indexes an object with null when a finding carries no
// severity, which jq refuses. Measured:
//
//	$ CTX_MIN_FIX_SEVERITY=medium run_actionable '[{"title":"t"}]'
//	jq: error (at <stdin>:1): Cannot index object with null
//	rc=5, stdout empty
//
// cmd_cycle assigns that empty string to `actionable` and then tests
// `(( actionable == 0 ))` (lib/run.sh:2966), where bash reads an empty operand
// as zero — so the driver behaves as though nothing were actionable. Counting
// the finding as unranked gives the same answer for an array whose findings all
// lack a severity, and a different one for an array that mixes a valid finding
// with an invalid one. That mixture cannot reach a marker: the review payload
// is refused unless every finding names high, medium or low
// (internal/validate/findings.go:115).
func TestCountActionableTreatsAMissingSeverityAsUnranked(t *testing.T) {
	for _, findings := range []string{`[{"title":"t"}]`, `[{"severity":null}]`} {
		if got := actionableCount(json.RawMessage(findings), core.SeverityMedium); got != 0 {
			t.Errorf("actionableCount(%s) = %d, want 0", findings, got)
		}
	}
}

// TestCountActionableReadsAnAbsentFindingsPayload covers the marker whose
// `findings` key is absent. cmd_cycle reads it as `jq -c '.findings // []'`
// (lib/run.sh:2960), so absent is the empty array.
func TestCountActionableReadsAnAbsentFindingsPayload(t *testing.T) {
	if got := actionableCount(nil, core.SeverityMedium); got != 0 {
		t.Errorf("actionableCount(nil) = %d, want 0", got)
	}
	if got := actionableCount(json.RawMessage(`null`), core.SeverityMedium); got != 0 {
		t.Errorf("actionableCount(null) = %d, want 0", got)
	}
}

// TestCountActionableReadsMalformedJSONAsZero pins the jq failure path of
// run_actionable (lib/run.sh:363-366). Measured:
//
//	$ run_actionable "{"   -> (no output), rc=5
//
// and `(( actionable == 0 ))` in cmd_cycle reads that empty output as zero.
func TestCountActionableReadsMalformedJSONAsZero(t *testing.T) {
	for _, raw := range []string{`{`, `[{"severity":`, `"x"`, `7`} {
		if got := actionableCount(json.RawMessage(raw), core.SeverityMedium); got != 0 {
			t.Errorf("actionableCount(%s) = %d, want 0", raw, got)
		}
	}
}

// TestCountEscalatedReadsAnUndecodableResolutionsAsZero pins the jq failure
// path of _markers_escalated (lib/run.sh:3199) for a `resolutions` value that
// is not iterable. Measured:
//
//	$ _markers_escalated '[{"leg":"resolve","resolutions":"escalated"}]' -> (no output), rc=5
//	$ _markers_escalated '[{"leg":"resolve","resolutions":7}]'           -> (no output), rc=5
//
// and every caller reads the empty output as zero. An object is different: jq
// iterates its values, so `{"a":{"resolution":"escalated"}}` counts one in the
// shell where this port skips the marker. No writer produces that shape, and
// the difference is recorded rather than reproduced.
func TestCountEscalatedReadsAnUndecodableResolutionsAsZero(t *testing.T) {
	markers := parseMarkers(t, `[{"v":1,"leg":"resolve","pass":1,"state":"complete","resolutions":"escalated"},
		{"v":1,"leg":"resolve","pass":2,"state":"complete","resolutions":7}]`)
	if got := escalatedCount(markers); got != 0 {
		t.Errorf("escalatedCount = %d, want 0", got)
	}
}

// TestCountEscalatedSkipsReviewMarkers pins _markers_escalated
// (lib/run.sh:3198-3200): the filter is `select(.leg == "resolve")`, so an
// escalation recorded on a review marker is not counted. Measured:
//
//	$ _markers_escalated '[{"leg":"review","resolutions":[{"resolution":"escalated"}]}]' -> 0
func TestCountEscalatedSkipsReviewMarkers(t *testing.T) {
	markers := parseMarkers(t, `[{"v":1,"leg":"review","pass":1,"state":"complete",
		"resolutions":[{"resolution":"escalated"}]}]`)
	if got := escalatedCount(markers); got != 0 {
		t.Errorf("escalatedCount = %d, want 0", got)
	}
}

// TestCountEscalatedSumsAcrossResolveMarkers pins the other half: every resolve
// marker on the pull request contributes, whichever pass it belongs to.
// Measured:
//
//	$ _markers_escalated '[{"leg":"resolve","resolutions":[{"resolution":"escalated"},
//	    {"resolution":"fixed"}]},{"leg":"resolve","resolutions":[{"resolution":"escalated"}]}]' -> 2
func TestCountEscalatedSumsAcrossResolveMarkers(t *testing.T) {
	markers := parseMarkers(t, `[
		{"v":1,"leg":"resolve","pass":1,"state":"complete",
		 "resolutions":[{"resolution":"escalated"},{"resolution":"fixed"}]},
		{"v":1,"leg":"resolve","pass":2,"state":"complete",
		 "resolutions":[{"resolution":"escalated"}]}]`)
	if got := escalatedCount(markers); got != 2 {
		t.Errorf("escalatedCount = %d, want 2", got)
	}
}

// TestCountEscalatedReadsAnAbsentResolutionsArray pins `(.resolutions // [])`
// (lib/run.sh:3199). Measured: both `[{"leg":"resolve"}]` and
// `[{"leg":"resolve","resolutions":null}]` answer 0.
func TestCountEscalatedReadsAnAbsentResolutionsArray(t *testing.T) {
	markers := parseMarkers(t, `[{"v":1,"leg":"resolve","pass":1,"state":"complete"},
		{"v":1,"leg":"resolve","pass":2,"state":"complete","resolutions":null}]`)
	if got := escalatedCount(markers); got != 0 {
		t.Errorf("escalatedCount = %d, want 0", got)
	}
	if got := escalatedCount(nil); got != 0 {
		t.Errorf("escalatedCount(nil) = %d, want 0", got)
	}
}

func parseMarkers(t *testing.T, raw string) []prstate.Marker {
	t.Helper()
	markers, err := prstate.ParseMarkers([]byte(raw))
	if err != nil {
		t.Fatalf("ParseMarkers: %v", err)
	}
	return markers
}
