package ghexec

import (
	"errors"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// errNoFilter is what a write refuses with when the Client was built without a
// Publisher. See New.
var errNoFilter = errors.New("no publish filter is installed, so nothing was published")

// answered reports that gh ran and exited zero.
//
// The two halves are one question here. A non-zero exit is how gh reports a
// refused API call, and Result.Err is how the runner reports that no child
// produced a status at all — an unresolvable program, a killed process, a
// context that ended. lib/github.sh cannot tell the two apart either: `2>&1
// >/dev/null || ui_die` fires for both.
func answered(res exec.Result) bool { return res.Err == nil && res.ExitCode == 0 }

// failure turns a refused invocation into an error carrying the summary the
// shipped tool prints.
//
// The summary is the caller's, word for word from lib/github.sh, because the
// operator-facing text is part of what this port preserves. What is added is
// only how gh answered.
//
// Neither the arguments nor the captured streams are in it. The arguments carry
// comment bodies, and gh's own diagnostics are what `2>/dev/null` discards at
// every call site in lib/github.sh — so putting either in an error would print
// on a terminal and into a run log something the shell never printed.
//
// Typed distinctions — a missing label, a permission refusal, an API outage —
// are deliberately absent. Nothing in the shipped tool distinguishes them, and
// a second Forge implementation is what would say which distinctions are real.
func failure(summary string, res exec.Result) error {
	if res.Err != nil {
		return fmt.Errorf("%s: %w", summary, res.Err)
	}
	return fmt.Errorf("%s: gh exited %d", summary, res.ExitCode)
}
