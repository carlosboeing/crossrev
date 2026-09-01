package resolve

import (
	"fmt"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// restoreFailure is _run_retry_reset's death (lib/run.sh:684-685). Bash puts
// the causal problem in that message — the reason the answer was rejected, from
// the call sites at lib/run.sh:881 and :893 — not the restore error. The
// restore error is named after it rather than in its place, because losing why
// the harness needs asking again is the diagnostic a reader most needs here.
func restoreFailure(harness, problem, restoreErr string) Result {
	return Result{
		Outcome: OutcomeRefused,
		Messages: []string{ui.Warning(
			"the rejected attempt's edits could not be put back",
			"They are still in the checkout, and a later run would capture them as its own baseline. Check `git status` before re-running the leg.",
		)},
		Err: &Refusal{
			Message: fmt.Sprintf("%s needs asking again, and the working tree it already edited could not be put back — %s (the restore said: %s)", harness, problem, restoreErr),
			Hint:    "Retrying on top of a discarded attempt's edits would commit changes no accepted answer describes. Nothing has been written to the pull request; check `git status` in the checkout and re-run the leg.",
		},
	}
}
