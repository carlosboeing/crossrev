package core

import (
	"errors"
	"fmt"
)

// MarkerVersion is the `v` every marker opens with (lib/run.sh:1052 and
// lib/run.sh:1098).
const MarkerVersion = 1

// Leg is the marker's name for one half of the loop.
type Leg string

// The two legs, spelled as the marker writers spell them: `leg:"review"` at
// lib/run.sh:1098 and `leg:"resolve"` at lib/run.sh:1960.
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
// and the marker names the act. lib/run.sh:518 converts one to the other
// before the descriptor check reads it.
type LegRole string

// The two configurable roles, from `.reviewer` and `.resolver` in
// `.github/crossrev.yml` (read at lib/run.sh:491).
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

// The three states a marker records.
//
//   - started:  an open claim, which recovery resumes from (lib/state.sh:313)
//   - complete: the leg settled (lib/state.sh:290)
//   - declined: a cap refused to start the pass, so it never ran at all
//     (lib/run.sh:1052, filtered back out at lib/state.sh:268)
const (
	PassStarted  PassState = "started"
	PassComplete PassState = "complete"
	PassDeclined PassState = "declined"
)

// ErrPassState is returned for a state no marker writer produces.
var ErrPassState = errors.New("a pass state is started, complete or declined")

// PassStates lists the three states in the order a pass moves through them.
func PassStates() []PassState { return []PassState{PassStarted, PassComplete, PassDeclined} }

// ParsePassState accepts only the three written values.
func ParsePassState(s string) (PassState, error) {
	switch PassState(s) {
	case PassStarted:
		return PassStarted, nil
	case PassComplete:
		return PassComplete, nil
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
