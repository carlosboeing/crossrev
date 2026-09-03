package review

import (
	"fmt"

	"github.com/carlosboeing/crossrev/internal/ui"
)

type sandboxRestoreFailure struct {
	*ui.FatalError
}

// newSandboxRestoreFailure mirrors _run_retry_reset (lib/run.sh:684-685), where
// the interpolated value is the causal problem rather than the restore error.
func newSandboxRestoreFailure(harness, problem, restoreErr string) *sandboxRestoreFailure {
	return &sandboxRestoreFailure{&ui.FatalError{
		Reason: fmt.Sprintf("%s needs asking again, and the working tree it already edited could not be put back — %s (the restore said: %s)", harness, problem, restoreErr),
		Action: "Retrying on top of a discarded attempt's edits would commit changes no accepted answer describes. Nothing has been written to the pull request; check `git status` in the checkout and re-run the leg.",
	}}
}

func (sandboxRestoreFailure) Warning() ui.Line {
	return ui.Warn(
		"the rejected attempt's edits could not be put back",
		"They are still in the checkout, and a later run would capture them as its own baseline. Check `git status` before re-running the leg.",
	)
}
