package cycle

import (
	"fmt"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
)

// The lines the sweep prints, kept apart from the decisions that choose them so
// each is one measurable string. Every minute figure is integer division, the
// way Bash's `$(( age / 60 ))` is: 599 seconds under a 600-second timeout reads
// as 9 minutes in, not as 10.

// watchdogMinutes is `$(( seconds / 60 ))`.
func watchdogMinutes(d time.Duration) int64 {
	return int64(d / time.Minute)
}

// watchdogNeverStartedLine is lib/run.sh:3741, printed with ui_no.
func watchdogNeverStartedLine(pr int, leg core.Leg) string {
	return fmt.Sprintf("#%d — waiting on the %s leg with no marker at all, so it never started", pr, leg)
}

// watchdogInsideLine is lib/run.sh:3748, printed with ui_opt.
func watchdogInsideLine(pr int, leg core.Leg, age, timeout time.Duration) string {
	return fmt.Sprintf("#%d — waiting on the %s leg, %d minute(s) in, inside the %d-minute timeout",
		pr, leg, watchdogMinutes(age), watchdogMinutes(timeout))
}

// watchdogPastLine is lib/run.sh:3752, printed with ui_no.
func watchdogPastLine(pr int, leg core.Leg, age, timeout time.Duration) string {
	return fmt.Sprintf("#%d — waiting on the %s leg for %d minutes, past the %d-minute timeout",
		pr, leg, watchdogMinutes(age), watchdogMinutes(timeout))
}

// watchdogRetriedLine is lib/run.sh:3793. The three leading spaces are the
// indent under the pull request's own line, and the revision is the seven bytes
// `${head:0:7}` takes.
func watchdogRetriedLine(label, head string) string {
	return "   retried by re-firing " + label + " at " + statusAbbreviate(head)
}

// watchdogDraftLine is lib/run.sh:3759, printed with ui_opt.
func watchdogDraftLine(pr int, leg core.Leg) string {
	return fmt.Sprintf("#%d — a draft pull request, so no automatic %s leg runs on it", pr, leg)
}

// watchdogDraftRecoveryLine is lib/run.sh:3760. The three leading spaces are
// the indent under the pull request's own line, and the backticks are the
// shell's own: the command is code, so it stays lowercase.
func watchdogDraftRecoveryLine(pr int, leg core.Leg) string {
	return fmt.Sprintf("   mark it ready for review, or run `crossrev %s --pr %d` yourself", leg, pr)
}

// watchdogHaltedLine is lib/run.sh:3781.
func watchdogHaltedLine() string {
	return "   halted — it had already been retried once"
}

// watchdogSummaryLine is lib/run.sh:3789.
func watchdogSummaryLine(s Summary) string {
	return fmt.Sprintf("checked %d pull request(s) waiting on a leg — retried %d, halted %d, %d in draft",
		s.Checked, s.Retried, s.Halted, s.Drafts)
}

// watchdogHaltComment is the comment the halt posts (lib/run.sh:3775-3780).
//
// It names the leg, refuses to read as a verdict on the code, and gives both
// the command to look at the pull request and the labels to remove to restart
// it. The lowercase `crossrev` in the first line, in the command and in the
// three label names is the shell's own: two of the three are matched literally
// by the workflows, and the string is copied rather than rewritten.
func watchdogHaltComment(pr int, leg core.Leg, label string) string {
	return fmt.Sprintf(
		"**crossrev halted** — the %s leg was already retried once and is still not finishing.\n"+
			"\n"+
			"The last marker on this pull request records how far it got. Nothing here is a judgement about the code: the loop stopped, it did not converge.\n"+
			"\n"+
			"To look yourself: `crossrev status --pr %d`. To restart it, remove `crossrev/halted` and `crossrev/watchdog-retried`, then apply `%s`.",
		leg, pr, label)
}
