package policy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Marker represents minimal fields needed for state policy evaluation.
type Marker struct {
	Leg         string       `json:"leg"`
	Pass        int          `json:"pass"`
	State       string       `json:"state"`
	Verdict     string       `json:"verdict,omitempty"`
	Blocked     *bool        `json:"blocked,omitempty"`
	CommitSHA   *string      `json:"commit_sha,omitempty"`
	Resolutions []Resolution `json:"resolutions,omitempty"`
}

// Resolution represents a finding resolution in a resolve marker.
type Resolution struct {
	Resolution      string  `json:"resolution"`
	CrossRevTracked *string `json:"crossrev_tracked,omitempty"`
}

// Pass returns the next pass number given a JSON array of markers.
// Refused/declined passes do not count.
func Pass(markersJSON string) int {
	var markers []Marker
	if err := json.Unmarshal([]byte(markersJSON), &markers); err != nil {
		return 1
	}
	maxReviewPass := 0
	for _, m := range markers {
		if m.State != "declined" && m.Leg == "review" {
			if m.Pass > maxReviewPass {
				maxReviewPass = m.Pass
			}
		}
	}
	return maxReviewPass + 1
}

// MaxPass returns the highest pass number among all markers, declined included.
func MaxPass(markersJSON string) int {
	var markers []Marker
	if err := json.Unmarshal([]byte(markersJSON), &markers); err != nil {
		return 0
	}
	maxP := 0
	for _, m := range markers {
		if m.Pass > maxP {
			maxP = m.Pass
		}
	}
	return maxP
}

// CurrentReviewPass returns the newest review pass number among non-declined markers.
func CurrentReviewPass(markersJSON string) int {
	var markers []Marker
	if err := json.Unmarshal([]byte(markersJSON), &markers); err != nil {
		return 0
	}
	maxReviewPass := 0
	for _, m := range markers {
		if m.State != "declined" && m.Leg == "review" {
			if m.Pass > maxReviewPass {
				maxReviewPass = m.Pass
			}
		}
	}
	return maxReviewPass
}

// Decision represents the outcome of ShouldContinue.
type Decision struct {
	Action string // "continue", "converged", "halt"
	Reason string
}

func (d Decision) String() string {
	if d.Reason == "" {
		return d.Action
	}
	return fmt.Sprintf("%s %s", d.Action, d.Reason)
}

// ShouldContinue evaluates the termination policy for the review/resolve loop.
func ShouldContinue(
	verdict string,
	pass int,
	maxPassesPerCycle int,
	stop bool,
	blocked bool,
	otherPRsToday int,
	maxPRsPerDay int,
	files int,
	maxFilesChangedPerPR int,
) Decision {
	if stop {
		return Decision{Action: "halt", Reason: "a human applied crossrev/stop"}
	}
	if blocked {
		return Decision{Action: "halt", Reason: "the resolver reported blocked"}
	}
	if verdict == "converged" {
		return Decision{Action: "converged", Reason: "nothing at or above min_fix_severity remains"}
	}
	if maxPassesPerCycle > 0 && pass >= maxPassesPerCycle {
		return Decision{Action: "halt", Reason: fmt.Sprintf("reached max_passes_per_cycle (%d)", maxPassesPerCycle)}
	}
	if maxPRsPerDay > 0 && otherPRsToday >= maxPRsPerDay {
		return Decision{
			Action: "halt",
			Reason: fmt.Sprintf("reached max_prs_per_day (%d) — %d other pull requests were already reviewed in the last 24 hours", maxPRsPerDay, otherPRsToday),
		}
	}
	if maxFilesChangedPerPR > 0 && files > maxFilesChangedPerPR {
		return Decision{
			Action: "halt",
			Reason: fmt.Sprintf("%d files changed, above max_files_changed_per_pr (%d)", files, maxFilesChangedPerPR),
		}
	}
	return Decision{Action: "continue", Reason: "issues remain and no cap is reached"}
}

// ResolveRedrivable determines if a completed resolve pass marker can be redriven.
func ResolveRedrivable(markerJSON string) bool {
	if strings.TrimSpace(markerJSON) == "" {
		return false
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(markerJSON), &raw); err != nil {
		return false
	}

	if b, ok := raw["blocked"].(bool); ok && b {
		return true
	}

	var hasEscalated bool
	var hasFixed bool
	var hasUnpersistedDeferral bool
	var resolutionCount int

	if resRaw, ok := raw["resolutions"]; ok && resRaw != nil {
		if resList, ok := resRaw.([]interface{}); ok {
			resolutionCount = len(resList)
			for _, item := range resList {
				if rMap, ok := item.(map[string]interface{}); ok {
					rType, _ := rMap["resolution"].(string)
					if rType == "escalated" {
						hasEscalated = true
					}
					if rType == "fixed" {
						hasFixed = true
					}
					if rType == "deferred" {
						if tracked, ok := rMap["crossrev_tracked"]; ok {
							if s, ok := tracked.(string); ok && s == "" {
								hasUnpersistedDeferral = true
							}
						}
					}
				}
			}
		}
	}

	if hasEscalated {
		return true
	}

	commitSHA, hasCommitSHA := raw["commit_sha"].(string)
	if !hasCommitSHA || commitSHA == "" {
		if hasFixed {
			return true
		}
		if resolutionCount == 0 {
			return true
		}
	}

	if hasUnpersistedDeferral {
		return true
	}

	return false
}

// ReviewRedrivable determines if a completed review pass marker can be redriven.
func ReviewRedrivable(markerJSON string) bool {
	if strings.TrimSpace(markerJSON) == "" {
		return false
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(markerJSON), &raw); err != nil {
		return false
	}
	state, _ := raw["state"].(string)
	if state != "complete" {
		return false
	}
	verdict, _ := raw["verdict"].(string)
	return verdict == "blocked"
}
