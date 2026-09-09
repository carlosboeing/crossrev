package prstate

import (
	"github.com/carlosboeing/crossrev/internal/core"
)

// MarkerConverges reports whether one complete review marker may underwrite
// a convergence report: the state is complete, no stop is recorded, and the
// coverage promise is kept. A v1 marker predates the coverage obligation and
// is grandfathered; a v2 marker without a coverage manifest id promises
// coverage it never recorded, so it cannot underwrite green.
//
// Head freshness and the confirmation pair are checked by callers that hold
// the current head; this answers the marker's own half.
func MarkerConverges(m Marker) bool {
	if m.State != core.PassComplete {
		return false
	}
	if _, ok := m.CoverageStop.Get(); ok {
		return false
	}
	if m.Version >= 2 {
		if _, ok := m.CoverageManifestID.Get(); !ok {
			return false
		}
	}
	return true
}

// MarkerHeadCurrent reports whether the marker was written at the head under
// review. A green marker for another head is stale, never converged.
func MarkerHeadCurrent(m Marker, headSHA string) bool {
	head, ok := m.HeadSHA.Get()
	if !ok || head == "" {
		return false
	}
	return head == headSHA
}

// ConfirmationSettled reports whether a confirmation pair the marker carries
// settles the repair at this head: both SHAs present and the confirmed head
// the current one. No pair means no repair was under confirmation.
func ConfirmationSettled(m Marker, headSHA string) bool {
	base, ok := m.ConfirmationBaseSHA.Get()
	if !ok || base == "" {
		return true
	}
	confirmed, ok := m.ConfirmationHeadSHA.Get()
	if !ok || confirmed == "" {
		return false
	}
	return confirmed == headSHA
}
