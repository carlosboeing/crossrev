package policy_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/verify"
)

// verificationInput is the all-clear convergence input with an explicit
// verification state. The package's shared builder stays untouched: an
// empty verification must keep converging exactly as it always has.
func verificationInput(state verify.State) policy.Convergence {
	return policy.Convergence{
		UnresolvedFixable:    0,
		LedgerCurrent:        true,
		Required:             3,
		Covered:              3,
		Outstanding:          0,
		CouldNotReview:       0,
		ScopeReported:        true,
		ConfirmationRequired: false,
		ConfirmationComplete: false,
		Verification:         state,
	}
}

// Only passed and none_required let a route converge; every other state —
// and an empty one, which reads as unconfigured — is judged beside the
// coverage guards rather than in place of them.
func TestConvergedGatesOnVerification(t *testing.T) {
	for _, state := range []verify.State{"", verify.None, verify.Passed} {
		if !policy.Converged(verificationInput(state)) {
			t.Errorf("verification %q refuses an otherwise clear convergence", state)
		}
	}
	for _, state := range []verify.State{verify.Pending, verify.Missing, verify.Failed, verify.Unreadable} {
		if policy.Converged(verificationInput(state)) {
			t.Errorf("verification %q still converges", state)
		}
	}
}

// A refused gate holds the review label the way an unmet coverage
// obligation does: open work stays owed, a quiet pass halts.
func TestPassLabelWithCoverageHoldsOnVerification(t *testing.T) {
	blocked := verificationInput(verify.Failed)
	if got := policy.PassLabelWithCoverage(core.VerdictConverged, 0, 0, blocked); got != policy.PassHalted {
		t.Errorf("failed gate maps to %q, want halted", got)
	}
}
