package core

import (
	"errors"
	"fmt"
)

// Resolution is what the resolve leg did about one finding.
type Resolution string

// The five resolutions, from the `resolution` enum in
// schemas/resolve.schema.json.
const (
	ResolutionFixed     Resolution = "fixed"
	ResolutionSkipped   Resolution = "skipped"
	ResolutionDeferred  Resolution = "deferred"
	ResolutionDisputed  Resolution = "disputed"
	ResolutionEscalated Resolution = "escalated"
)

// ErrResolution is returned for a resolution outside the schema's enum.
var ErrResolution = errors.New("a resolution is not one the resolve schema lists")

// Resolutions lists the five resolutions in schema order.
func Resolutions() []Resolution {
	return []Resolution{
		ResolutionFixed,
		ResolutionSkipped,
		ResolutionDeferred,
		ResolutionDisputed,
		ResolutionEscalated,
	}
}

// ParseResolution accepts only the schema's five values.
//
// `rebutted` was the pre-migration spelling of `disputed` and is refused here.
// The migration belongs to the marker decoder, which applies it once at the
// single point every marker is read through (lib/state.sh:97-119); a value type
// that also accepted the old word would hide a marker the decoder had missed.
func ParseResolution(s string) (Resolution, error) {
	for _, r := range Resolutions() {
		if Resolution(s) == r {
			return r, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrResolution, s)
}

// String renders the resolution as the schema spells it.
func (r Resolution) String() string { return string(r) }

// Tracked is a deferred finding's `crossrev_tracked` field, in all three of the
// states the marker distinguishes.
//
// lib/legs.sh:174 asks whether any resolution is `deferred` with
// `crossrev_tracked == ""`, and jq answers that only for a key that is present
// and empty. Absent is a marker written before the field existed, or one whose
// filing has not been attempted; present and empty means the filing was
// attempted and produced nothing, which is what keeps the cycle alive for
// another pass. Collapsing the two would end the cycle over an unfiled
// deferral.
type Tracked struct {
	present bool
	value   string
}

// TrackedAbsent is the state of a marker that carries no `crossrev_tracked`
// key. It is also the zero value, so a field nobody set never claims an
// unfiled deferral.
func TrackedAbsent() Tracked { return Tracked{} }

// TrackedUnfiled is the present-and-empty state: a deferral nothing was filed
// for.
func TrackedUnfiled() Tracked { return Tracked{present: true} }

// NewTracked records a present `crossrev_tracked` value, empty or not.
func NewTracked(value string) Tracked { return Tracked{present: true, value: value} }

// Present reports whether the marker carried the key at all.
func (t Tracked) Present() bool { return t.present }

// Value is what the key held, or the empty string when it was absent.
func (t Tracked) Value() string { return t.value }

// Unfiled reports the present-and-empty state, which is the one the redrive
// predicate reads.
func (t Tracked) Unfiled() bool { return t.present && t.value == "" }
