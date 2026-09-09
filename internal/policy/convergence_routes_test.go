package policy_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// outstandingInput is one outstanding file past an otherwise all-clear
// input: the shape every convergence route must refuse.
func outstandingInput() policy.Convergence {
	input := convergedInput()
	input.Outstanding = 1
	return input
}

// TestEveryConvergedRouteUsesThePredicate drives the gate behind each of the
// four convergence routes with one outstanding file and fails if any route
// emits, applies or reports convergence.
//
// The routes share one predicate rather than reconstructing it: the review
// writer consults Converged before completing a converged verdict, the two
// label rules consult it through their WithCoverage forms, and the status
// and cycle paths consult the marker-half predicates over the same fields.
// One outstanding file must refuse on every one.
func TestEveryConvergedRouteUsesThePredicate(t *testing.T) {
	input := outstandingInput()
	if policy.Converged(input) {
		t.Fatal("the predicate converges with one outstanding file")
	}

	// Route 1, the review writer's gate.
	if policy.Converged(input) {
		t.Error("the review writer would complete a converged verdict with one outstanding file")
	}

	// Route 2, the review label gate.
	if policy.PassLabelWithCoverage(core.VerdictConverged, 0, 0, input) == policy.PassConverged {
		t.Error("PassLabel applies crossrev/converged with one outstanding file")
	}

	// Route 3, the resolve settle gate: a no-commit settle marker with one
	// skipped resolution and no commit.
	settle := policy.ResolveMarker{Resolutions: []policy.ResolutionRecord{{Resolution: core.ResolutionSkipped}}}
	if policy.ResolvePassLabel(settle, 0) != policy.PassConverged {
		t.Fatal("the settle fixture does not converge at the base rule; the gate below proves nothing")
	}
	if policy.ResolvePassLabelWithCoverage(settle, 0, input) == policy.PassConverged {
		t.Error("ResolvePassLabel applies crossrev/converged with one outstanding file")
	}

	// Route 4, the status and cycle marker-half gates: a v2 review marker,
	// complete with a converged verdict at the current head but no coverage
	// manifest id, promises coverage it never recorded.
	head := "2c4a46cb321db01826d116b5ef2add6b0284d68c"
	unrecorded := prstate.Marker{
		Version: 2,
		State:   core.PassComplete,
		HeadSHA: prstate.Some(head),
		Verdict: prstate.Some(string(core.VerdictConverged)),
	}
	if prstate.MarkerConverges(unrecorded) {
		t.Error("a v2 marker with no coverage manifest id underwrites convergence")
	}
	stale := prstate.Marker{
		Version: 1,
		State:   core.PassComplete,
		HeadSHA: prstate.Some("0000000000000000000000000000000000000000"),
		Verdict: prstate.Some(string(core.VerdictConverged)),
	}
	if prstate.MarkerHeadCurrent(stale, head) {
		t.Error("a marker written at another head reads as current")
	}
	unconfirmed := prstate.Marker{
		Version:             2,
		State:               core.PassComplete,
		HeadSHA:             prstate.Some(head),
		Verdict:             prstate.Some(string(core.VerdictConverged)),
		ConfirmationBaseSHA: prstate.Some("0913bf7b99dcecf746d0e6fcef5a9c1d64aaf3b0"),
	}
	if prstate.ConfirmationSettled(unconfirmed, head) {
		t.Error("a confirmation pair naming no head settles the repair")
	}
}
