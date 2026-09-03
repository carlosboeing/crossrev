package cli

import (
	"context"
	"errors"
)

// The three statuses the process may end with.
//
// The shell has no fourth. `ui_die` prints and exits 1 (lib/ui.sh:113-119),
// `run_checkpoint` exits 130 when a leg was interrupted between writes
// (lib/run.sh:178), `doctor` returns 1 when something is missing
// (bin/crossrev:179), and every other path returns 0. A command that answered
// anything else would be reporting a status no caller of this tool has ever
// had to read, so Run folds it into a failure rather than passing it on.
const (
	ExitOK          = 0
	ExitFailure     = 1
	ExitInterrupted = 130
)

// ErrInterrupted is Ctrl-C: the leg stopped at a checkpoint, after the last
// completed write and before the next one (lib/run.sh:172-179).
//
// It is not a failure. The marker on the pull request records how far the leg
// got, the claim is deliberately resumable, and `_run_report_fatal` returns
// early on 130 rather than marking the pull request halted (lib/run.sh:142).
var ErrInterrupted = errors.New("interrupted after the last completed write")

// errNoValue is a flag with nothing after it.
//
// The Bash argument loops read the value with `${2:-}` and then `shift 2`.
// With one argument left, `shift` fails, `set -euo pipefail` ends the process,
// and nothing is printed at all: an empty terminal and status 1
// (bin/crossrev:12, lib/run.sh:923). Reproduced rather than improved on,
// because a reader can see the difference. It is a defect of the shell, not a
// design.
//
// The commands whose loops use `${2:?…}` instead — `init` and every `auth`
// sub-command — say what is missing, and go through IO.Die rather than this.
var errNoValue = errors.New("a flag is missing its value")

// exitFor folds what a command answered into one of the three statuses.
func exitFor(code int, err error) int {
	if errors.Is(err, ErrInterrupted) || errors.Is(err, context.Canceled) {
		return ExitInterrupted
	}
	if err != nil {
		return ExitFailure
	}
	switch code {
	case ExitOK, ExitInterrupted:
		return code
	}
	return ExitFailure
}
