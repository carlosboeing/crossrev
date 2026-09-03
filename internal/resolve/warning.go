package resolve

import (
	"fmt"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// restoreFailure is _run_retry_reset's death (lib/run.sh:690-691). Bash puts
// the causal problem in that message — the reason the answer was rejected, from
// the call sites at lib/run.sh:887 and :893 — not the restore error. The
// restore error is named after it rather than in its place, because losing why
// the harness needs asking again is the diagnostic a reader most needs here.
func restoreFailure(harness, problem, restoreErr string) Result {
	return Result{
		Outcome: OutcomeRefused,
		Messages: []ui.Line{ui.Warn(
			"the rejected attempt's edits could not be put back",
			"They are still in the checkout, and a later run would capture them as its own baseline. Check `git status` before re-running the leg.",
		)},
		Err: &Refusal{
			Message: fmt.Sprintf("%s needs asking again, and the working tree it already edited could not be put back — %s (the restore said: %s)", harness, problem, restoreErr),
			Hint:    "Retrying on top of a discarded attempt's edits would commit changes no accepted answer describes. Nothing has been written to the pull request; check `git status` in the checkout and re-run the leg.",
		},
	}
}

// runFailureCause names why an attempt is being abandoned, for the message a
// failed restore prints.
//
// Four rungs, in the order a reader can act on. The runner's own error comes
// first because it means no process ran, which no envelope can describe. Then
// the harness's own words: every adapter answers Envelope{OK:false} with its
// diagnostic on a non-zero exit AND on a clean exit that reported failure —
// claude's is_error, agy's non-SUCCESS status, opencode's empty response. That
// rung outranks the exit code because "the model refused: no credit" beats "the
// harness exited 1". The exit code is what is left when the adapter had nothing
// to quote, and the neutral phrase fires only when the run itself looks fine.
func runFailureCause(env harness.Envelope, res exec.Result) string {
	if res.Err != nil {
		return res.Err.Error()
	}
	if !env.OK && env.Error != nil && *env.Error != "" {
		return *env.Error
	}
	if res.ExitCode != 0 {
		return fmt.Sprintf("the harness exited %d", res.ExitCode)
	}
	if !env.OK {
		return "the harness reported failure and named no reason"
	}
	return "the attempt finished and its answer was not read"
}
