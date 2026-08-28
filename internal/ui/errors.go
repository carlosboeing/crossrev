package ui

import "errors"

// FatalError is what a Bash `ui_die` becomes on this side.
//
// The Bash function prints and then calls `exit 1` (lib/ui.sh:110-118). A Go
// library must not: os.Exit would end the process from inside a package whose
// callers hold a lock on a pull request, an open run log and a working tree
// that may still need putting back, and it skips every deferred cleanup that
// would release them. It would also make this package untestable — a test that
// calls Die would take the suite with it.
//
// So Die prints what the Bash function prints and returns this instead. The
// caller returns it upward and exactly one place, the command's entry point,
// turns it into an exit status. That gives the two properties the shell gets
// for free and one it does not: the deferred cleanup runs, the reason is a
// value rather than a global, and a caller that wants to add context to it can.
//
// The Reason field replaces CROSSREV_DIE_REASON. The Bash version keeps the
// reason as well as printing it because a leg that dies has a claim marker open
// on the pull request and the EXIT trap writes this text into it
// (lib/run.sh:103 and :145) — otherwise the marker reads `started` forever and
// the cause survives only in the terminal that saw it. An error travelling up
// the stack carries the same text to the same place, and Reason reads it back
// out however deeply it was wrapped.
type FatalError struct {
	// Reason is what went wrong, and it is the text a pull request marker
	// carries when a leg dies.
	Reason string
	// Action is what to do about it — rule 4 of the output voice. It is
	// printed, not stored on the pull request.
	Action string
}

func (e *FatalError) Error() string { return e.Reason }

// Reason returns the die reason carried by err, or the empty string when err
// is not one and wraps none. This is the read the EXIT trap does.
func Reason(err error) string {
	var fatal *FatalError
	if errors.As(err, &fatal) {
		return fatal.Reason
	}
	return ""
}
