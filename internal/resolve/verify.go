package resolve

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/verify"
)

func (l *Leg) settlementEvidence(ctx context.Context, s *session) verify.Evidence {
	if len(s.settings.RequiredChecks) == 0 {
		return verify.Evidence{State: verify.None}
	}
	runs, err := l.Forge.CheckRuns(ctx, s.repo, s.pr.HeadRefOid)
	return verify.Evaluate(s.settings.RequiredChecks, runs, err)
}

// waitForSettlement shares the review's bounded wait and early-stop rules.
func (l *Leg) waitForSettlement(ctx context.Context, s *session, ev verify.Evidence) verify.Evidence {
	sleep := l.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	return verify.Wait(ctx, s.settings.CheckWait, ev, l.now, sleep,
		func() bool {
			return slices.Contains(l.Forge.PullRequestLabels(ctx, s.repo, s.req.PR), policy.LabelStop)
		},
		func() bool {
			pr, err := l.Forge.PullRequest(ctx, s.repo, s.req.PR)
			return err == nil && pr.HeadRefOid.SHA() != s.pr.HeadRefOid.SHA()
		},
		func() verify.Evidence { return l.settlementEvidence(ctx, s) })
}

func gateHaltLines(pr int, ev verify.Evidence) []ui.Line {
	return []ui.Line{
		ui.Say("Required checks block convergence: " + verify.Debt(ev) + "."),
		ui.Say(fmt.Sprintf("When they have reported, run `crossrev restart --pr %d` to drive the pass again.", pr)),
	}
}

// gateOnlySettle admits evidence-only recovery for an unchanged, settled pass.
func gateOnlySettle(s *session) bool {
	m := asPolicyResolve(s.redrive)
	if !policy.ResolveGateHeld(m) || s.redrive.HeadSHA.Value() != s.pr.HeadRefOid.SHA() {
		return false
	}
	m.Verification = ""
	return policy.ResolvePassLabel(m, otherEscalated(s.markers, s.pass)) == policy.PassConverged
}

// gateHeldAtMovedHead reports a gate-held settle whose head has moved since
// it was judged. Review admission reads a moved head as a new revision.
func gateHeldAtMovedHead(s *session) bool {
	return policy.ResolveGateHeld(asPolicyResolve(s.redrive)) && s.redrive.HeadSHA.Value() != s.pr.HeadRefOid.SHA()
}

// handBackMovedHead applies awaiting-review for a gate-held settle whose
// head moved, leaving the settled marker as it is.
func (l *Leg) handBackMovedHead(ctx context.Context, s *session) Result {
	got := Result{Outcome: OutcomeComplete, Pass: s.pass, Marker: s.redrive}
	if err := l.applyPassLabels(ctx, s, s.pass, policy.PassAwaitingReview); err != nil {
		got.Messages = append(got.Messages, ui.Say(err.Error()))
	}
	got.Messages = append(got.Messages, ui.Say(fmt.Sprintf(
		"The head moved since the required checks held pass %d, so the new revision goes back to the reviewer.", s.pass)))
	return got
}

func (l *Leg) redriveGate(ctx context.Context, s *session) Result {
	marker := s.redrive
	conv, ev, ok := l.resolveConvergenceEvidence(ctx, s)
	next := policy.PassConverged
	if !ok || policy.ConvergedExceptVerification(conv) {
		ev = l.waitForSettlement(ctx, s, ev)
		conv.Verification = ev.State
	}
	marker.Verification = prstate.Some(ev.MarkerRecord())
	if ok && !policy.ConvergedExceptVerification(conv) {
		next = policy.PassAwaitingReview
	}
	if frozenGateRefuses(ev) {
		next = policy.PassHalted
	}
	summary := resolveSummaryBody(marker.Resolutions, s.review.Findings, "", marker, s.repo.String(), s.req.PR, s.maxPasses, recurrenceCount(s.markers, s.pass, s.findings))
	encoded, err := marker.Encode()
	if err != nil {
		return wrapErr(err)
	}
	if err := prstate.FitMarkerComment(summary + encoded); err != nil {
		return wrapErr(err)
	}
	if err := l.Forge.CommentEdit(ctx, s.repo, marker.CommentID(), summary+encoded); err != nil {
		return wrapErr(err)
	}
	got := Result{Outcome: OutcomeComplete, Pass: s.pass, Marker: marker, Resolutions: marker.Resolutions}
	if err := l.applyPassLabels(ctx, s, s.pass, next); err != nil {
		got.Messages = append(got.Messages, ui.Say(err.Error()))
	}
	got.Messages = append(got.Messages, closingReport(marker, next, s.pass, s.req.PR)...)
	return got
}

// recordEmptySettlement creates or refreshes only the configured empty route's
// marker. Unconfigured routes return without writing any new bytes.
func (l *Leg) recordEmptySettlement(ctx context.Context, s *session, ev verify.Evidence, got Result) Result {
	marker := s.redrive
	if marker.CommentID() == 0 {
		marker = newClaim(s, l.now().Unix(), s.pr.HeadRefOid.SHA(), "")
	}
	marker.State = core.PassComplete
	marker.HeadSHA = prstate.Some(s.pr.HeadRefOid.SHA())
	marker.DoneTS = prstate.Some(l.now().Unix())
	marker.Verification = prstate.Some(ev.MarkerRecord())
	marker.Summary = prstate.Some("The review raised no findings to resolve.")
	summary := resolveSummaryBody(marker.Resolutions, s.review.Findings, "", marker, s.repo.String(), s.req.PR, s.maxPasses, 0)
	encoded, err := marker.Encode()
	if err != nil {
		return wrapErr(err)
	}
	if err := prstate.FitMarkerComment(summary + encoded); err != nil {
		return wrapErr(err)
	}
	if marker.CommentID() == 0 {
		id, err := l.Forge.CommentCreate(ctx, s.repo, s.req.PR, summary+encoded)
		if err != nil {
			return wrapErr(err)
		}
		if id == 0 {
			return wrapErr(fmt.Errorf("the settlement marker did not post"))
		}
		marker = withCommentID(marker, id)
	} else if err := l.Forge.CommentEdit(ctx, s.repo, marker.CommentID(), summary+encoded); err != nil {
		return wrapErr(err)
	}
	got.Marker = marker
	return got
}
