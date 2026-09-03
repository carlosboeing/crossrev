package cycle

import (
	"context"
	"strings"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// Waiting is one pull request the sweep was handed: its number, the labels it
// carries and its head revision.
//
// Bash builds this list itself, from one `gh api repos/<slug>/pulls?state=open`
// filtered to the pull requests carrying a `crossrev/awaiting-` label
// (lib/run.sh:3691-3693). forge.AwaitingPullRequests is that read, and the
// composition root performs it before calling Run, so the list arrives as an
// argument rather than through a forge call made here. Nothing else about the
// sweep changes: every decision below is made from these three fields and the
// markers on the pull request, which is what makes the watchdog answerable
// offline.
type Waiting struct {
	// PR is the pull request number.
	PR int
	// Labels is every label on it, including the awaiting label it is
	// waiting behind and any bookkeeping label a previous sweep applied.
	Labels []string
	// HeadSHA is the revision the pull request points at. It is printed
	// abbreviated on the retry line and read nowhere else.
	HeadSHA string
}

// Summary is the three counters the closing line reports
// (lib/run.sh:3689, :3733).
type Summary struct {
	// Checked is every pull request the sweep looked at, including the ones
	// it then skipped: lib/run.sh:3703 increments before lib/run.sh:3705
	// honours crossrev/stop.
	Checked int
	// Retried is how many legs were re-fired.
	Retried int
	// Halted is how many were given up on.
	Halted int
}

// Watchdog is the sweep that goes looking for a leg that never finished.
//
// Event-driven mode's failure mode is silence: a dropped label event and a
// converged pull request look identical from outside. So something has to go
// looking, and it retries once before giving up — a dropped event is fixed by
// re-firing it, and re-applying a label GitHub already holds fires nothing,
// which is why the retry removes it first (lib/run.sh:3660-3665).
//
// It decides from marker and label state alone. No event payload reaches it,
// and it reads no pull request body, diff or thread.
type Watchdog struct {
	// Forge is every read and write it makes.
	Forge forge.Forge
	// Now is the clock every age is measured against. Bash reads it once,
	// before the loop (lib/run.sh:3690), so one sweep uses one instant.
	Now func() time.Time
	// Out is where the page goes.
	Out *ui.IO
	// Timeout is how long a leg may be waiting before it counts as stuck.
	// Zero means the 1800 seconds lib/run.sh:3667 defaults the flag to.
	Timeout time.Duration
	// TimeoutRefusal is a `--timeout` the caller could not convert, held
	// rather than raised.
	//
	// Bash keeps the flag as a string (lib/run.sh:3671) and evaluates it only
	// at `(( age < timeout ))` (lib/run.sh:3719), so a sweep that reaches no
	// pull request never dies on a nonsense value, and one that reaches a
	// stopped or marker-less pull request does not either. Raising this where
	// the arithmetic is, rather than at the flag, is what reproduces that.
	TimeoutRefusal error
	// Author is whose markers are loop state. Bash resolves it per pull
	// request with `state_trusted_author automated` (lib/run.sh:3709); here
	// the caller resolves it once and hands it in.
	Author string
}

// watchdogDefaultTimeout is `timeout=1800` at lib/run.sh:3667.
const watchdogDefaultTimeout = 30 * time.Minute

// Run sweeps the pull requests it was handed and answers what it did
// (lib/run.sh:3689-3735).
//
// The error is the sweep stopping early, which is what `ui_die` does to the
// process: a label that did not land or a comment that did not post is fatal
// because the chain is label-driven (lib/state.sh:429-434, lib/github.sh:192).
// The counters returned alongside it are what had happened up to that point.
func (w *Watchdog) Run(ctx context.Context, repo core.Slug, waiting []Waiting) (Summary, error) {
	var summary Summary

	// With no trusted author every marker filters out, so a sweep that
	// carried on would read every pull request as never started and re-fire
	// the whole repository. Bash cannot reach that state: state_trusted_author
	// dies before it answers (lib/state.sh:37-39). The words are its own.
	if w.Author == "" {
		return summary, w.Out.Die(
			"cannot determine which App's markers to trust",
			"Automated mode reads markers only from the App that writes them. In a workflow, set CROSSREV_APP_SLUG from the token step's app-slug output. Locally, run: crossrev auth status")
	}

	timeout := w.Timeout
	if timeout <= 0 {
		timeout = watchdogDefaultTimeout
	}
	now := w.Now()

	w.Out.Section("CrossRev watchdog on " + repo.String())

	for _, pr := range waiting {
		summary.Checked++

		if watchdogHasLabel(pr.Labels, policy.LabelStop) {
			continue
		}

		leg := core.LegReview
		// Bash asks whether the space-joined label list CONTAINS the
		// awaiting-resolution name (lib/run.sh:3707), not whether one
		// label equals it, so the substring test is the parity one.
		// The stop test two lines up is different on purpose: lib/run.sh:3705
		// is `grep -qw`, a whole-label match, which is what watchdogHasLabel,
		// statusHasLabel (status.go) and hasStop (cycle.go) reproduce.
		if strings.Contains(strings.Join(pr.Labels, " "), policy.LabelAwaitingResolution) {
			leg = core.LegResolve
		}

		markers := statusMarkers(w.Forge.IssueComments(ctx, repo, pr.PR), w.Author)
		if len(markers) == 0 {
			// A label with nothing behind it: the event never arrived, so
			// the leg never wrote a marker at all (lib/run.sh:3712-3716).
			w.Out.No(watchdogNeverStartedLine(pr.PR, leg))
			if err := w.retry(ctx, repo, pr, leg, &summary); err != nil {
				return summary, err
			}
			continue
		}

		// `last`, with no leg filter (lib/run.sh:3711): the age comes off
		// the newest marker on the pull request whichever leg wrote it.
		// A marker with no `ts` reads as 0, which is `.ts // 0` at
		// lib/run.sh:3718 and makes the leg past any timeout.
		age := time.Duration(now.Unix()-markers[len(markers)-1].TS) * time.Second
		// The first read of the timeout, and so the first place a `--timeout`
		// that is not a number can stop anything (lib/run.sh:3719).
		if w.TimeoutRefusal != nil {
			return summary, w.TimeoutRefusal
		}
		if age < timeout {
			w.Out.Opt(watchdogInsideLine(pr.PR, leg, age, timeout))
			continue
		}

		w.Out.No(watchdogPastLine(pr.PR, leg, age, timeout))
		if err := w.retry(ctx, repo, pr, leg, &summary); err != nil {
			return summary, err
		}
	}

	w.Out.Gap()
	w.Out.Line(watchdogSummaryLine(summary))
	w.Out.End("A pass that never fired and a pass that converged look identical from outside, which is why this runs on a schedule.")
	return summary, nil
}

// retry re-fires the leg, or halts it because it has been re-fired once already
// (lib/run.sh:3738-3767). It counts what it did into the summary.
func (w *Watchdog) retry(ctx context.Context, repo core.Slug, pr Waiting, leg core.Leg, summary *Summary) error {
	label := policy.AwaitingLabel(leg)

	if watchdogHasLabel(pr.Labels, policy.LabelWatchdogRetried) {
		// One retry, then halt. A second failure is not a dropped event.
		// The awaiting label comes off and does not go back on, which is
		// what makes this terminal (lib/run.sh:3743-3746).
		w.Forge.PullRequestLabelRemove(ctx, repo, pr.PR, label)
		w.ensureLabel(ctx, repo, policy.LabelHalted)
		if err := w.Forge.PullRequestLabelAdd(ctx, repo, pr.PR, policy.LabelHalted); err != nil {
			return err
		}
		if _, err := w.Forge.CommentCreate(ctx, repo, pr.PR, watchdogHaltComment(pr.PR, leg, label)); err != nil {
			return err
		}
		w.Out.Line(watchdogHaltedLine())
		summary.Halted++
		return nil
	}

	// Re-applying a label GitHub already holds fires no event, so the retry
	// has to remove it first (lib/run.sh:3757-3764).
	w.ensureLabel(ctx, repo, policy.LabelWatchdogRetried)
	if err := w.Forge.PullRequestLabelAdd(ctx, repo, pr.PR, policy.LabelWatchdogRetried); err != nil {
		return err
	}
	w.Forge.PullRequestLabelRemove(ctx, repo, pr.PR, label)
	if err := w.Forge.PullRequestLabelAdd(ctx, repo, pr.PR, label); err != nil {
		return err
	}
	w.Out.Line(watchdogRetriedLine(label, pr.HeadSHA))
	summary.Retried++
	return nil
}

// ensureLabel declares the label at the colour and description the one map
// gives it. Bash sends the outcome to /dev/null and does not test it
// (lib/run.sh:3744, :3759): a label the sweep failed to declare is still
// applied on the next line, because GitHub's add-labels endpoint mints a
// missing one with default metadata.
func (w *Watchdog) ensureLabel(ctx context.Context, repo core.Slug, name string) {
	_, _ = w.Forge.LabelEnsure(ctx, repo, forge.Label{
		Name:        name,
		Colour:      policy.LabelColour(name),
		Description: policy.LabelDescription(name),
	})
}

// watchdogHasLabel is the `grep -qw` test at lib/run.sh:3705 and :3742. Bash's
// -w also matches the name inside a longer one, because `-` and `/` are not
// word characters; equality is what every other label reader in this tree uses,
// and no repository mints a label the two would disagree on.
func watchdogHasLabel(labels []string, want string) bool {
	for _, label := range labels {
		if label == want {
			return true
		}
	}
	return false
}
