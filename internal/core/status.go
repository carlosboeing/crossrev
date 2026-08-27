package core

import (
	"errors"
	"fmt"
)

// Verdict is what a review pass concluded.
type Verdict string

// The three verdicts a review leg may return, from the `verdict` enum in
// schemas/findings.schema.json, plus the one the orchestrator writes itself.
//
// VerdictDeclined is never returned by a harness. A pass a cap refused to start
// gets a marker so `crossrev status` has something to render the refusal from,
// and that marker carries `verdict:"declined"` (lib/run.sh:1055).
const (
	VerdictConverged    Verdict = "converged"
	VerdictIssuesRemain Verdict = "issues-remain"
	VerdictBlocked      Verdict = "blocked"
	VerdictDeclined     Verdict = "declined"
)

// ErrVerdict is returned for a verdict outside the set being parsed.
var ErrVerdict = errors.New("a verdict is not one the findings schema lists")

// Verdicts lists the three verdicts a harness may return.
func Verdicts() []Verdict { return []Verdict{VerdictConverged, VerdictIssuesRemain, VerdictBlocked} }

// MarkerVerdicts lists every verdict a marker may carry: the schema's three,
// plus `declined`.
func MarkerVerdicts() []Verdict {
	return []Verdict{VerdictConverged, VerdictIssuesRemain, VerdictBlocked, VerdictDeclined}
}

// ParseVerdict accepts only what a harness may return. Use ParseMarkerVerdict
// to read a verdict back off a marker.
func ParseVerdict(s string) (Verdict, error) {
	for _, v := range Verdicts() {
		if Verdict(s) == v {
			return v, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrVerdict, s)
}

// ParseMarkerVerdict accepts the schema's three plus `declined`.
func ParseMarkerVerdict(s string) (Verdict, error) {
	for _, v := range MarkerVerdicts() {
		if Verdict(s) == v {
			return v, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrVerdict, s)
}

// String renders the verdict as the marker holds it.
func (v Verdict) String() string { return string(v) }

// LoopState is the word `crossrev status` prints for the loop as a whole.
type LoopState string

// The five states, in the precedence order lib/run.sh:3112 reads them: a stop
// request outranks a halt, a halt outranks convergence, and convergence
// outranks whichever leg is owed.
//
// The two-word states carry a space. Their hyphenated spellings are label
// names, which are a separate vocabulary.
const (
	LoopStopped            LoopState = "stopped"
	LoopHalted             LoopState = "halted"
	LoopConverged          LoopState = "converged"
	LoopAwaitingResolution LoopState = "awaiting resolution"
	LoopAwaitingReview     LoopState = "awaiting review"
)

// ErrLoopState is returned for a word `crossrev status` never prints.
var ErrLoopState = errors.New("that is not a loop state crossrev prints")

// LoopStates lists the five states in precedence order.
func LoopStates() []LoopState {
	return []LoopState{
		LoopStopped,
		LoopHalted,
		LoopConverged,
		LoopAwaitingResolution,
		LoopAwaitingReview,
	}
}

// ParseLoopState accepts only the five printed words.
func ParseLoopState(s string) (LoopState, error) {
	for _, l := range LoopStates() {
		if LoopState(s) == l {
			return l, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrLoopState, s)
}

// String renders the state as `crossrev status` prints it.
func (l LoopState) String() string { return string(l) }
