package policy_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/policy"
)

// convergedInput is the all-clear convergence input: nothing fixable open,
// a current ledger, exact required/covered accounting, no outstanding or
// could_not_review records, a reported scope, and no repair awaiting
// confirmation.
func convergedInput() policy.Convergence {
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
	}
}

// TestConvergedRequiresEveryCoverageGuard pins the predicate field by field:
// the all-clear input converges, and weakening any single obligation refuses.
func TestConvergedRequiresEveryCoverageGuard(t *testing.T) {
	if !policy.Converged(convergedInput()) {
		t.Fatal("the all-clear input does not converge")
	}
	cases := map[string]func(*policy.Convergence){
		"unresolved fixable":   func(c *policy.Convergence) { c.UnresolvedFixable = 1 },
		"stale ledger":         func(c *policy.Convergence) { c.LedgerCurrent = false },
		"under-covered":        func(c *policy.Convergence) { c.Covered = 2 },
		"outstanding record":   func(c *policy.Convergence) { c.Outstanding = 1 },
		"could_not_review":     func(c *policy.Convergence) { c.CouldNotReview = 1 },
		"missing scope report": func(c *policy.Convergence) { c.ScopeReported = false },
		"unconfirmed repair":   func(c *policy.Convergence) { c.ConfirmationRequired = true },
		"over-covered":         func(c *policy.Convergence) { c.Covered = 4 },
		"empty required set":   func(c *policy.Convergence) { c.Required = 0; c.Covered = 0 },
	}
	for name, weaken := range cases {
		input := convergedInput()
		weaken(&input)
		if policy.Converged(input) {
			t.Errorf("%s still converges", name)
		}
	}
}
