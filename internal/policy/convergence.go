package policy

// Convergence is the one input every convergence decision reads. It names
// the review obligation and what settled it: open fixable findings, whether
// the ledger is current, the required/covered/outstanding/could_not_review
// counts, whether the reviewer reported its scope, and whether a repair
// delta needed and got its confirmation pair. Verification is not an input:
// this release records verification.status not_implemented, and an
// unimplemented check is never evidence for convergence.
type Convergence struct {
	// UnresolvedFixable counts findings at or above min_fix_severity with
	// no fix, skip, dispute, deferral or escalation settling them.
	UnresolvedFixable int
	// LedgerCurrent reports the current complete generation is at the
	// exact base, head and engine under review.
	LedgerCurrent bool
	// Required counts every required file at the current revision pair.
	Required int
	// Covered counts required files with an accepted disposition in the
	// current generation.
	Covered int
	// Outstanding counts required files with no accepted disposition.
	Outstanding int
	// CouldNotReview counts records the reviewer could not examine.
	CouldNotReview int
	// ScopeReported reports the reviewer stated its examined scope and
	// known limits.
	ScopeReported bool
	// ConfirmationRequired reports a repair delta is under confirmation.
	ConfirmationRequired bool
	// ConfirmationComplete reports the repair delta carries its accepted
	// B/C confirmation pair.
	ConfirmationComplete bool
}

// Converged is the one convergence predicate. It holds only when no
// unresolved fixable finding remains, the current complete generation
// exactly accounts for the required set, no outstanding or could_not_review
// record exists, the scope was reported, and any repair delta has its
// matching accepted confirmation pair. Every other shape — a stale label,
// a stale marker, an outstanding record, an unexamined file, a missing
// scope report, an unconfirmed repair — refuses.
func Converged(c Convergence) bool {
	if c.UnresolvedFixable != 0 {
		return false
	}
	if !c.LedgerCurrent {
		return false
	}
	if c.Required <= 0 || c.Covered != c.Required {
		return false
	}
	if c.Outstanding != 0 {
		return false
	}
	if c.CouldNotReview != 0 {
		return false
	}
	if !c.ScopeReported {
		return false
	}
	if c.ConfirmationRequired && !c.ConfirmationComplete {
		return false
	}
	return true
}
