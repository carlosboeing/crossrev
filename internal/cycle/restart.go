package cycle

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// Restart is `crossrev restart --pr N`: the halted-loop remedy as one command.
//
// The watchdog's halt comment and the halted-loop docs both told the operator
// to remove crossrev/halted (and crossrev/watchdog-retried when it stands
// beside it) and re-apply the halted leg's awaiting label by hand. This is
// that sequence, with the leg read off the markers rather than guessed. It is
// new with the Go binary — the shell had no counterpart — so nothing here is
// measured against bin/crossrev; the early refusals are status.go's, because
// the reads it mirrors are Status.Load's.
//
// crossrev/stop is refused rather than cleared: red is the human brake and no
// command removes it. A pull request that is not halted is refused too, with
// the command that applies instead, because restarting a loop that never
// stopped would rewrite labels it is already driving on.
type Restart struct {
	// Forge is every read and write it makes.
	Forge forge.Forge
	// Show reads the configuration from the pull request's base revision,
	// which is where policy is read from and never the head (ADR 0003). The
	// config is read for `.mode`, which keys the trusted author.
	Show config.ShowFile
	// Out is where the confirmation line goes.
	Out *ui.IO
	// AppSlug is the App whose markers are loop state in automated mode, in
	// the same hand-off Status takes (the composition root resolves it; empty
	// in automated mode is the refusal).
	AppSlug string
}

// Run reads the pull request, decides which leg halted, and puts its awaiting
// label back. The label writes are the whole of what it changes: the chain is
// label-driven, so the remove clears the halt and the add is the event the
// next leg listens for.
func (r *Restart) Run(ctx context.Context, repo core.Slug, pr int) error {
	if repo.Incomplete() {
		slug, err := r.Forge.RepoSlug(ctx)
		if err != nil {
			return &ui.FatalError{
				Reason: "could not work out which repository this is",
				Action: "Run crossrev from a checkout with a GitHub remote, or pass --repo owner/name.",
			}
		}
		repo = slug
	}
	pull, err := r.Forge.PullRequest(ctx, repo, pr)
	if err != nil {
		return &ui.FatalError{
			Reason: fmt.Sprintf("could not read %s#%d", repo, pr),
			Action: "Check the number, and that `gh auth status` passes for that repository.",
		}
	}
	if !strings.EqualFold(pull.State, "OPEN") {
		return &ui.FatalError{
			Reason: fmt.Sprintf("%s#%d is not open", repo, pr),
			Action: "crossrev only runs on open pull requests. Reopen it, or pick another number.",
		}
	}

	labels := statusLabelNames(pull.Labels)
	if statusHasLabel(labels, policy.LabelStop) {
		reason := fmt.Sprintf("%s#%d carries crossrev/stop", repo, pr)
		if statusHasLabel(labels, policy.LabelHalted) {
			reason = fmt.Sprintf("%s#%d is halted, but it also carries crossrev/stop", repo, pr)
		}
		return &ui.FatalError{
			Reason: reason,
			Action: fmt.Sprintf("crossrev/stop is the human brake, and no command clears it. Remove it by hand once the loop should run again: gh pr edit %d --remove-label crossrev/stop", pr),
		}
	}
	if !statusHasLabel(labels, policy.LabelHalted) {
		return restartNotHalted(repo, pr, labels)
	}

	cfg, err := config.Load(ctx, pull.BaseRefOid, r.Show)
	if err != nil {
		return err
	}
	author, err := statusTrustedAuthor(ctx, r.Forge, r.AppSlug, cfg.Get(".mode"), repo, pr)
	if err != nil {
		return err
	}
	markers := statusMarkers(r.Forge.IssueComments(ctx, repo, pr), author)
	leg := restartLeg(markers)

	// The add is the restart: the generated workflows listen for the awaiting
	// label going on, so it goes on first and a failed add is fatal with the
	// halt still in place — removing the halt first would leave a failed add
	// refusing the next restart as not halted. When the label is already on,
	// it comes off and back on, because an add against a present label fires
	// no labeled event.
	awaiting := policy.AwaitingLabel(leg)
	if statusHasLabel(labels, awaiting) {
		r.Forge.PullRequestLabelRemove(ctx, repo, pr, awaiting)
	}
	if err := r.Forge.PullRequestLabelAdd(ctx, repo, pr, awaiting); err != nil {
		return err
	}
	r.Forge.PullRequestLabelRemove(ctx, repo, pr, policy.LabelHalted)
	removed := []string{policy.LabelHalted}
	if statusHasLabel(labels, policy.LabelWatchdogRetried) {
		r.Forge.PullRequestLabelRemove(ctx, repo, pr, policy.LabelWatchdogRetried)
		removed = append(removed, policy.LabelWatchdogRetried)
	}

	r.Out.Say(fmt.Sprintf("%s#%d restarted: removed %s, applied %s",
		repo, pr, strings.Join(removed, " and "), awaiting))
	return nil
}

// restartNotHalted is the refusal for a pull request carrying no halt: which
// command applies instead is read off the labels, so a reader who mistook the
// state is sent to the leg that is actually owed.
func restartNotHalted(repo core.Slug, pr int, labels []string) error {
	prNumber := strconv.Itoa(pr)
	switch {
	case statusHasLabel(labels, policy.LabelAwaitingReview):
		return &ui.FatalError{
			Reason: fmt.Sprintf("%s#%d is not halted — it is awaiting review", repo, pr),
			Action: "Run: crossrev review --pr " + prNumber,
		}
	case statusHasLabel(labels, policy.LabelAwaitingResolution):
		return &ui.FatalError{
			Reason: fmt.Sprintf("%s#%d is not halted — it is awaiting resolution", repo, pr),
			Action: "Run: crossrev resolve --pr " + prNumber,
		}
	case statusHasLabel(labels, policy.LabelConverged):
		return &ui.FatalError{
			Reason: fmt.Sprintf("%s#%d is not halted — it has converged", repo, pr),
			Action: "There is nothing to restart. Run `crossrev status --pr " + prNumber + "` to see its state.",
		}
	}
	return &ui.FatalError{
		Reason: fmt.Sprintf("%s#%d is not halted — it has no CrossRev loop state yet", repo, pr),
		Action: "Run: crossrev status --pr " + prNumber,
	}
}

// restartLeg decides which leg a halt belongs to. The chain mirrors
// statusNextHalted — which reads the same markers to name the remedy — plus
// the one shape it has no case for: an open claim is not an outcome, so
// `status` says "needs a human" and this answers the leg the claim itself
// records. First match wins:
//
//  1. A declined review marker one pass ahead is a cap halt, and the review
//     leg is what raising it re-runs.
//  2. An escalated finding halts for a human decision, and the review leg is
//     what verifies the settlement.
//  3. A completed resolve pass whose own label rule says halted — blocked, an
//     unpushed fix, an unrecorded pass, an unfiled deferral — is re-driven by
//     the resolve leg, which re-runs rather than refusing exactly these
//     endings (policy.ResolveRedrivable).
//  4. A completed review pass with a blocked verdict is the review leg's halt.
//  5. A completed review pass with no resolve marker beside it and a verdict
//     that is neither converged nor blocked landed findings the resolve leg
//     never answered, so the resolve leg is owed (statusStateFromMarkers
//     reads the same shape as awaiting-resolution).
//  6. The newest marker reading started or incomplete is the watchdog-halted
//     mid-leg case: the watchdog removed the awaiting label when it halted,
//     so the open claim is the only record of which leg was retried. A
//     coverage-halted review pass is `incomplete` and is this case too.
//  7. Anything else — zero markers included, which is a watchdog halt on a leg
//     that never started — is the review leg: every loop begins with one.
func restartLeg(markers []prstate.Marker) core.Leg {
	pass := prstate.CurrentReviewPass(markers)
	if next, ok := prstate.MarkerFor(markers, pass+1, core.LegReview); ok && next.State.Declined() {
		return core.LegReview
	}
	escalated := statusMarkersEscalated(markers)
	if escalated > 0 {
		return core.LegReview
	}
	if m, ok := prstate.MarkerFor(markers, pass, core.LegResolve); ok &&
		m.State == core.PassComplete &&
		policy.ResolvePassLabel(statusResolveMarker(m), escalated) == policy.PassHalted {
		return core.LegResolve
	}
	if review, ok := prstate.MarkerFor(markers, pass, core.LegReview); ok && review.State == core.PassComplete {
		switch core.Verdict(review.Verdict.Value()) {
		case core.VerdictBlocked:
			return core.LegReview
		case core.VerdictConverged:
			// A converged review owes no resolve leg; fall through.
		default:
			if _, hasResolve := prstate.MarkerFor(markers, pass, core.LegResolve); !hasResolve {
				return core.LegResolve
			}
		}
	}
	if len(markers) > 0 {
		newest := markers[len(markers)-1]
		if newest.State == core.PassStarted || newest.State == core.PassIncomplete {
			return newest.Leg
		}
	}
	return core.LegReview
}
