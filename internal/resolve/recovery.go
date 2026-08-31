package resolve

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

func (l *Leg) reportFatal(ctx context.Context, s *session, marker prstate.Marker, reason string, workdir string, keep bool) {
	if marker.State != core.PassStarted {
		return
	}
	id := marker.CommentID()
	if id == 0 {
		return
	}
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
