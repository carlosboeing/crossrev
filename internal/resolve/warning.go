package resolve

import "fmt"

// warning joins the condition to its consequence with ui.Warn's newline and
// three-space indent. Result.Messages carries one string, so a renderer needs
// these bytes here rather than two parts it cannot render later.
func warning(condition, consequence string) string {
	return condition + "\n   " + consequence
}

func restoreFailure(harness, problem string) Result {
	return Result{
		Outcome: OutcomeRefused,
		Messages: []string{warning(
			"the rejected attempt's edits could not be put back",
			"They are still in the checkout, and a later run would capture them as its own baseline. Check `git status` before re-running the leg.",
		)},
		Err: &Refusal{
			Message: fmt.Sprintf("%s needs asking again, and the working tree it already edited could not be put back — %s", harness, problem),
			Hint:    "Retrying on top of a discarded attempt's edits would commit changes no accepted answer describes. Nothing has been written to the pull request; check `git status` in the checkout and re-run the leg.",
		},
	}
}
