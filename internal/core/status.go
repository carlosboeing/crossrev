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
// and that marker carries `verdict:"declined"` (lib/run.sh:1061).
const (
	VerdictConverged    Verdict = "converged"
	VerdictIssuesRemain Verdict = "issues-remain"
	VerdictBlocked      Verdict = "blocked"
	VerdictDeclined     Verdict = "declined"
)

// ErrVerdict is returned for a verdict a harness may not return: anything
// outside the findings schema's three.
var ErrVerdict = errors.New("a verdict is not one the findings schema lists")

// ErrMarkerVerdict is returned for a verdict no marker may carry: anything
// outside the schema's three plus `declined`. A separate sentinel from
// ErrVerdict because the two parse different sets, and `declined` is a value
// one accepts and the other refuses.
var ErrMarkerVerdict = errors.New("a marker verdict is not one a marker may carry")

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
	switch Verdict(s) {
	case VerdictConverged:
		return VerdictConverged, nil
	case VerdictIssuesRemain:
		return VerdictIssuesRemain, nil
	case VerdictBlocked:
		return VerdictBlocked, nil
	}
	return "", fmt.Errorf("%w: %q", ErrVerdict, s)
}

// ParseMarkerVerdict accepts the schema's three plus `declined`.
func ParseMarkerVerdict(s string) (Verdict, error) {
	switch Verdict(s) {
	case VerdictConverged:
		return VerdictConverged, nil
	case VerdictIssuesRemain:
		return VerdictIssuesRemain, nil
	case VerdictBlocked:
		return VerdictBlocked, nil
	case VerdictDeclined:
		return VerdictDeclined, nil
	}
	return "", fmt.Errorf("%w: %q", ErrMarkerVerdict, s)
}

// String renders the verdict as the marker holds it.
func (v Verdict) String() string { return string(v) }

// LoopState is the word `crossrev status` prints for the loop as a whole.
type LoopState string

// The five states, in the precedence order lib/run.sh:3119 reads them: a stop
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
var ErrLoopState = errors.New("that is not a loop state CrossRev prints")

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
	switch LoopState(s) {
	case LoopStopped:
		return LoopStopped, nil
	case LoopHalted:
		return LoopHalted, nil
	case LoopConverged:
		return LoopConverged, nil
	case LoopAwaitingResolution:
		return LoopAwaitingResolution, nil
	case LoopAwaitingReview:
		return LoopAwaitingReview, nil
	}
	return "", fmt.Errorf("%w: %q", ErrLoopState, s)
}

// String renders the state as `crossrev status` prints it.
func (l LoopState) String() string { return string(l) }
