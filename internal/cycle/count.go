package cycle

import (
	"encoding/json"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// actionableCount is how many findings the resolve leg may change code for:
// at or above min_fix_severity, and not pre-existing (lib/run.sh:356-367).
//
// The driver reads this to decide whether a finished review pass has anything
// left to resolve, so it is the number that separates "converged" from another
// resolve leg. internal/review and internal/resolve each hold a sibling of it;
// the tier rule forbids importing either from here, so this package keeps its
// own and the rank table stays in internal/policy where all three read it.
//
// `pre_existing` is read under jq's truthiness rather than as a boolean,
// because the shell writes `select((.pre_existing // false) | not)`: everything
// except false, null and an absent key marks a finding pre-existing.
//
// A finding with no severity, or a null one, is counted as unranked. The shell
// crashes there instead — `$r[.severity]` cannot index an object with null, jq
// exits 5 and prints nothing, and cmd_cycle's `(( actionable == 0 ))` reads the
// empty string as zero. The two answers differ only for an array that mixes a
// valid finding with a severity-less one, and the review payload is refused
// before it reaches a marker unless every finding names high, medium or low.
func actionableCount(findings json.RawMessage, minFix core.Severity) int {
	if len(findings) == 0 {
		return 0
	}
	var parsed []harness.Node
	if err := json.Unmarshal(findings, &parsed); err != nil {
		return 0
	}
	count := 0
	for _, finding := range parsed {
		severity := core.Severity(finding.Member("severity").StringVal())
		if policy.ShouldFix(severity, minFix, finding.Member("pre_existing").Truthy()) {
			count++
		}
	}
	return count
}

// escalatedCount is how many findings across every resolve marker on the pull
// request were handed to a human (lib/run.sh:3198-3200).
//
// Which pass escalated one stops mattering once a newer pass runs: the halt it
// caused still stands until that pass is re-driven or the thread is settled by
// hand. A review marker never contributes, whatever it carries, because the
// shell filters on `.leg == "resolve"` first.
func escalatedCount(markers []prstate.Marker) int {
	count := 0
	for _, marker := range markers {
		if marker.Leg != core.LegResolve {
			continue
		}
		var records []struct {
			Resolution string `json:"resolution"`
		}
		if err := marker.DecodeResolutions(&records); err != nil {
			continue
		}
		for _, record := range records {
			if record.Resolution == string(core.ResolutionEscalated) {
				count++
			}
		}
	}
	return count
}
