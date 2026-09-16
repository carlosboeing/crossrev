package core

import (
	"errors"
	"fmt"
)

// MarkerVersion is the `v` a marker this release writes opens with.
//
// Markers at `v:1` remain readable for findings, pass numbering and prior
// resolutions, but their coverage reference is always absent and cannot
// satisfy convergence. Readers refuse `v > 2`; writers never downgrade a
// pull request that already carries `v:2`.
const MarkerVersion = 2

// MarkerVersions lists the marker versions a reader accepts: the current
// one and the superseded one kept for context.
func MarkerVersions() []int { return []int{1, MarkerVersion} }

// ErrMarkerVersion is returned for a marker version no reader accepts.
var ErrMarkerVersion = errors.New("a marker version is 1 or 2")

// ParseMarkerVersion accepts only the versions a reader handles.
func ParseMarkerVersion(v int) (int, error) {
	switch v {
	case 1, MarkerVersion:
		return v, nil
	}
	return 0, fmt.Errorf("%w: %d", ErrMarkerVersion, v)
}

// Leg is the marker's name for one half of the loop.
type Leg string

// The two legs, spelled as the marker writers spell them: `leg:"review"` at
// lib/run.sh:1104 and `leg:"resolve"` at lib/run.sh:1966.
const (
	LegReview  Leg = "review"
	LegResolve Leg = "resolve"
)

// ErrLeg is returned for a leg name no marker carries.
var ErrLeg = errors.New("a leg is either review or resolve")

// Legs lists the two legs in the order the loop runs them.
func Legs() []Leg { return []Leg{LegReview, LegResolve} }

// ParseLeg accepts only the two written values.
func ParseLeg(s string) (Leg, error) {
	switch Leg(s) {
	case LegReview:
		return LegReview, nil
	case LegResolve:
		return LegResolve, nil
	}
	return "", fmt.Errorf("%w: %q", ErrLeg, s)
}

// String renders the leg as the marker holds it.
func (l Leg) String() string { return string(l) }

// LegRole is the configuration key for a leg's harness settings.
//
// The two vocabularies differ on purpose: the configuration names the actor
// and the marker names the act. lib/run.sh:524 converts one to the other
// before the descriptor check reads it.
type LegRole string

// The two configurable roles, from `.reviewer` and `.resolver` in
// `.github/crossrev.yml` (read at lib/run.sh:497).
const (
	RoleReviewer LegRole = "reviewer"
	RoleResolver LegRole = "resolver"
)

// ErrLegRole is returned for a role no configuration key names.
var ErrLegRole = errors.New("a leg role is either reviewer or resolver")

// Roles lists the two configurable roles.
func Roles() []LegRole { return []LegRole{RoleReviewer, RoleResolver} }

// Leg maps the configuration key onto the marker vocabulary.
//
// It returns an error rather than defaulting, and the branch at the call site
// is the price of that. A total function has to answer something for a role
// nobody declared, and the only two answers are a leg: returning LegResolve is
// returning the write-capable one, and returning LegReview quietly reviews
// under a role that was meant to resolve. Failing closed is the cheaper
// mistake.
func (r LegRole) Leg() (Leg, error) {
	switch r {
	case RoleReviewer:
		return LegReview, nil
	case RoleResolver:
		return LegResolve, nil
	}
	return "", fmt.Errorf("%w: %q", ErrLegRole, string(r))
}

// String renders the role as the configuration spells it.
func (r LegRole) String() string { return string(r) }

// PassState is a marker's `state` field.
type PassState string

// The four states a marker records.
//
//   - started:    an open claim, which recovery resumes from (lib/state.sh:313)
//   - complete:   the leg settled (lib/state.sh:290)
//   - declined:   a cap refused to start the pass, so it never ran at all
//     (lib/run.sh:1058, filtered back out at lib/state.sh:268)
//   - incomplete: the pass ran and halted before settling, so a re-drive may
//     resume the same revision rather than starting a new pass. It is not
//     declined, complete or converged.
const (
	PassStarted    PassState = "started"
	PassComplete   PassState = "complete"
	PassDeclined   PassState = "declined"
	PassIncomplete PassState = "incomplete"
)

// ErrPassState is returned for a state no marker writer produces.
var ErrPassState = errors.New("a pass state is started, complete, declined or incomplete")

// PassStates lists the four states in the order a pass moves through them.
func PassStates() []PassState {
	return []PassState{PassStarted, PassComplete, PassIncomplete, PassDeclined}
}

// ParsePassState accepts only the four written values.
func ParsePassState(s string) (PassState, error) {
	switch PassState(s) {
	case PassStarted:
		return PassStarted, nil
	case PassComplete:
		return PassComplete, nil
	case PassIncomplete:
		return PassIncomplete, nil
	case PassDeclined:
		return PassDeclined, nil
	}
	return "", fmt.Errorf("%w: %q", ErrPassState, s)
}

// Declined reports whether this marker records a pass that did not happen.
//
// Three readers depend on the answer: pass numbering, revision detection and
// the daily cap all skip a declined marker, because counting it would report a
// refused pass as one that ran (lib/state.sh:260-268).
func (s PassState) Declined() bool { return s == PassDeclined }

// String renders the state as the marker holds it.
func (s PassState) String() string { return string(s) }

// HaltReason is a `blocked_reason` word that says why a pass halted before
// settling. The set is closed: only these three halt a review pass.
type HaltReason string

// The three closed halt reasons: coverage left outstanding, the ledger could
// not publish, and one file that cannot fit the input budget alone.
const (
	HaltCoverageIncomplete HaltReason = "coverage_incomplete"
	HaltLedgerExhausted    HaltReason = "ledger_exhausted"
	HaltInputExceedsBudget HaltReason = "input_exceeds_budget"
)

// ErrHaltReason is returned for a halt reason no marker writer produces.
var ErrHaltReason = errors.New("a halt reason is coverage_incomplete, ledger_exhausted or input_exceeds_budget")

// HaltReasons lists the three halt reasons.
func HaltReasons() []HaltReason {
	return []HaltReason{HaltCoverageIncomplete, HaltLedgerExhausted, HaltInputExceedsBudget}
}

// ParseHaltReason accepts only the three written values.
func ParseHaltReason(s string) (HaltReason, error) {
	switch HaltReason(s) {
	case HaltCoverageIncomplete:
		return HaltCoverageIncomplete, nil
	case HaltLedgerExhausted:
		return HaltLedgerExhausted, nil
	case HaltInputExceedsBudget:
		return HaltInputExceedsBudget, nil
	}
	return "", fmt.Errorf("%w: %q", ErrHaltReason, s)
}

// String renders the halt reason as the marker holds it.
func (h HaltReason) String() string { return string(h) }

// LimitReason is a recorded limit word: work deferred by a budget rather
// than halted by one. The set is closed: only these two are recorded.
type LimitReason string

// The two recorded limit reasons: files carried past the pass budget, and a
// search term capped for being too common.
const (
	LimitReviewBudgetReached LimitReason = "review_budget_reached"
	LimitTooCommon           LimitReason = "too_common"
)

// ErrLimitReason is returned for a limit reason no writer records.
var ErrLimitReason = errors.New("a limit reason is review_budget_reached or too_common")

// LimitReasons lists the two recorded limit reasons.
func LimitReasons() []LimitReason { return []LimitReason{LimitReviewBudgetReached, LimitTooCommon} }

// ParseLimitReason accepts only the two recorded values.
func ParseLimitReason(s string) (LimitReason, error) {
	switch LimitReason(s) {
	case LimitReviewBudgetReached:
		return LimitReviewBudgetReached, nil
	case LimitTooCommon:
		return LimitTooCommon, nil
	}
	return "", fmt.Errorf("%w: %q", ErrLimitReason, s)
}

// String renders the limit reason as it is recorded.
func (l LimitReason) String() string { return string(l) }

// PassNumber is a one-based pass counter.
type PassNumber int

// ErrPassNumber is returned for a pass number below one.
var ErrPassNumber = errors.New("a pass number starts at 1")

// NewPassNumber validates that a pass number is one or greater. No trusted
// marker means pass 1 (lib/state.sh:273), so zero is the absence of a pass
// rather than a pass.
func NewPassNumber(n int) (PassNumber, error) {
	if n < 1 {
		return 0, fmt.Errorf("%w: %d", ErrPassNumber, n)
	}
	return PassNumber(n), nil
}

// Valid reports whether this is a pass number rather than the absence of one.
//
// Go cannot block the conversion, so `PassNumber(0)` is constructible whatever
// NewPassNumber refuses; this is what a reader that did not build the value
// itself checks. Zero is the absence of a pass, not a pass: no trusted marker
// means pass 1 (lib/state.sh:273).
func (p PassNumber) Valid() bool { return p >= 1 }

// Int is the pass number as an ordinary integer.
func (p PassNumber) Int() int { return int(p) }
