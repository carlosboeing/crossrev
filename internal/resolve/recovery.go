package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// reportFatal records a leg that died on the pull request it had claimed
// (_run_report_fatal at lib/run.sh:131-146, through
// _run_report_invoke_failure at lib/run.sh:725-769).
//
// Guarded on the claim still reading `started`: a pass that finished has
// written `complete`, and rewriting it as blocked because something after it
// failed would replace an accurate record with a wrong one (lib/run.sh:127-129).
//
// `reported` is CROSSREV_LEG_REPORTED (lib/run.sh:731), set before the writes
// rather than after, so a second pass over the same leg cannot post the summary
// twice when one of the writes below fails.
func (l *Leg) reportFatal(ctx context.Context, s *session, marker prstate.Marker, reason string, workdir string, keep bool) {
	if marker.State != core.PassStarted {
		return
	}
	id := marker.CommentID()
	if id == 0 {
		return
	}
	l.reported = true
	now := l.now().Unix()
	marker.State = core.PassComplete
	marker.DoneTS = prstate.Some(now)
	marker.Blocked = prstate.Some(true)
	marker.BlockedReason = prstate.Some(reason)

	body := ResolveSummaryBody(marker.Resolutions, json.RawMessage("[]"), "", marker, s.repo.String(), s.req.PR, s.maxPasses)
	encoded, err := marker.Encode()
	if err == nil {
		_ = l.Forge.CommentEdit(ctx, s.repo, id, body+encoded)
	}

	l.Forge.PullRequestLabelRemove(ctx, s.repo, s.req.PR, policy.LabelAwaitingReview)
	l.Forge.PullRequestLabelRemove(ctx, s.repo, s.req.PR, policy.LabelAwaitingResolution)
	l.Forge.PullRequestLabelRemove(ctx, s.repo, s.req.PR, policy.LabelConverged)
	l.Forge.PullRequestLabelRemove(ctx, s.repo, s.req.PR, policy.LabelWatchdogRetried)
	for _, lab := range s.pr.Labels {
		if stringsHasPassPrefix(lab.Name) && lab.Name != policy.PassLabelName(mustPass(marker.Pass)) {
			l.Forge.PullRequestLabelRemove(ctx, s.repo, s.req.PR, lab.Name)
		}
	}
	_ = l.Forge.PullRequestLabelAdd(ctx, s.repo, s.req.PR, policy.LabelHalted)
	_ = l.Forge.PullRequestLabelAdd(ctx, s.repo, s.req.PR, policy.PassLabelName(mustPass(marker.Pass)))

	if keep {
		return
	}
	_ = workdir
}

func stringsHasPassPrefix(name string) bool {
	return len(name) > len(policy.LabelPassPrefix) && name[:len(policy.LabelPassPrefix)] == policy.LabelPassPrefix
}

func (l *Leg) encodeClaim(marker prstate.Marker, heading, lead string) (string, error) {
	encoded, err := marker.Encode()
	if err != nil {
		return "", err
	}
	return heading + "\n\n" + lead + encoded, nil
}

func passHeading(s *session) string {
	return "**crossrev — resolving " + passLabel(s.pass, s.maxPasses) + "**"
}

func optIntJSON(n int) json.RawMessage {
	return json.RawMessage(strconv.Itoa(n))
}

// refusalReason is the first half of whatever refusal ended the leg, which is
// what ui_die puts in CROSSREV_DIE_REASON.
func refusalReason(err error) string {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal.Message
	}
	var harnessRefusal *harness.Refusal
	if errors.As(err, &harnessRefusal) {
		return harnessRefusal.Reason
	}
	var fatal *ui.FatalError
	if errors.As(err, &fatal) {
		return fatal.Reason
	}
	return err.Error()
}
