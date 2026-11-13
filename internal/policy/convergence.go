package policy

// VerificationState is the required-check gate's verdict on one entry or on
// the whole list. Only passed and none_required let a route converge.
//
// It lives in this package rather than beside the evaluator because the
// convergence predicate reads it: tier 1 may not import the tier-2
// evaluator, so the vocabulary sits below both and the mapping sits above.
type VerificationState string

const (
	// VerificationNone means no checks are required, so the gate is open
	// by default.
	VerificationNone VerificationState = "none_required"
	// VerificationPassed means every required check reported a passing run.
	VerificationPassed VerificationState = "passed"
	// VerificationPending means a required check's newest run is still going.
	VerificationPending VerificationState = "pending"
	// VerificationFailed means a required check's newest run failed.
	VerificationFailed VerificationState = "failed"
	// VerificationMissing means no run carries a required check's name
	// and app.
	VerificationMissing VerificationState = "missing"
	// VerificationUnreadable means the enumeration itself could not be
	// trusted: a refused read or a partial list. It fails closed, never
	// open.
	VerificationUnreadable VerificationState = "unreadable"
)

// Convergence is the one input every convergence decision reads. It names
// the review obligation and what settled it: open fixable findings, whether
// the ledger is current, the required/covered/outstanding/could_not_review
// counts, whether the reviewer reported its scope, whether a repair delta
// needed and got its confirmation pair, and the required-check evidence.
type Convergence struct {
	// UnresolvedFixable counts findings at or above min_fix_severity with
	// no fix, skip, dispute, deferral or escalation settling them.
	UnresolvedFixable int
	// LedgerCurrent reports the current complete generation is at the
	// exact base, head and engine under review.
	LedgerCurrent bool
	// Required counts every required file at the current revision pair.
	Required int
	// Covered counts required files with an accepted verdict in the
	// current generation.
	Covered int
	// Outstanding counts required files with no accepted verdict.
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
	// Verification is the required-check evidence for the head. Empty
	// reads as none_required: no gate was configured, so there is nothing
	// to refuse on. Every leg with a configured gate supplies an explicit
	// state instead of leaving this unset.
	Verification VerificationState
}

// Converged is the one convergence predicate. It holds only when no
// unresolved fixable finding remains, the current complete generation
// exactly accounts for the required set, no outstanding or could_not_review
// record exists, the scope was reported, any repair delta has its matching
// accepted confirmation pair, and the required checks passed or none were
// required. Every other shape — a stale label, a stale marker, an
// outstanding record, an unexamined file, a missing scope report, an
// unconfirmed repair, a pending, missing, failed or unreadable check —
// refuses.
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
	switch c.Verification {
	case "", VerificationNone, VerificationPassed:
		return true
	}
	return false
}

// ConvergedExceptVerification is the predicate with the gate held open:
// whether the pass would converge if its required checks passed. The review
// leg waits on exactly this shape — anything else cannot converge however
// the checks report, so waiting would only spend the wait.
func ConvergedExceptVerification(c Convergence) bool {
	c.Verification = VerificationNone
	return Converged(c)
}
