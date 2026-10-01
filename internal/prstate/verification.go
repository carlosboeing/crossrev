package prstate

// MarkerVerification is the required-check evidence the pass judged: the
// overall state and the per-check detail. It rides on the review and
// resolve markers whose convergence it gated, so the halt a failed check
// caused names its evidence on the record.
//
// It is not the Verification envelope in coverage.go. That one reserves the
// generation's verification record; this one is the pass's gate evidence,
// and the names stay apart so neither reader mistakes one for the other.
type MarkerVerification struct {
	// State is the overall gate verdict: passed, pending, failed, missing
	// or unreadable. A pass with no required checks carries no field at
	// all rather than none_required.
	State string `json:"state"`
	// Reason explains an unreadable enumeration: the missing permission,
	// the truncation, or the transport error. Empty otherwise.
	Reason string `json:"reason"`
	Checks []MarkerVerificationCheck `json:"checks"`
}

// MarkerVerificationCheck is one required entry's evidence: the entry, its
// state, and the selected run's conclusion and URL.
type MarkerVerificationCheck struct {
	Name string `json:"name"`
	App  string `json:"app"`
	// State is passed, pending, failed, missing — or unreadable when the
	// enumeration could not be trusted and no entry was evaluated.
	State      string `json:"state"`
	Conclusion string `json:"conclusion"`
	// URL is the selected run's page, empty for a missing run.
	URL string `json:"url"`
	// Note qualifies the state: `skipped` or `neutral` beside a pass, or
	// the raw value beside an unknown one.
	Note string `json:"note"`
}
