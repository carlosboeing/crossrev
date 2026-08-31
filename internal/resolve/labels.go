package resolve

import (
	"context"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
)

func (l *Leg) applyPassLabels(ctx context.Context, s *session, pass int, next policy.PassLabelState) error {
	current := s.pr.Labels
	names := make([]string, 0, len(current))
	for _, lab := range current {
		names = append(names, lab.Name)
	}

	for _, word := range []policy.PassLabelState{
		policy.PassAwaitingReview, policy.PassAwaitingResolution, policy.PassConverged, policy.PassHalted,
	} {
		if word == next {
			continue
		}
		l.Forge.PullRequestLabelRemove(ctx, s.repo, s.req.PR, word.Label())
	}
	l.Forge.PullRequestLabelRemove(ctx, s.repo, s.req.PR, policy.LabelWatchdogRetried)
	wantPass := policy.PassLabelName(mustPass(pass))
	for _, name := range names {
		if strings.HasPrefix(name, policy.LabelPassPrefix) && name != wantPass {
			l.Forge.PullRequestLabelRemove(ctx, s.repo, s.req.PR, name)
		}
	}
	if err := l.addLabel(ctx, s, wantPass); err != nil {
		return err
	}
	if next != "" {
		if err := l.addLabel(ctx, s, next.Label()); err != nil {
			return err
		}
	}
	return nil
}

func (l *Leg) addLabel(ctx context.Context, s *session, label string) error {
	err := l.Forge.PullRequestLabelAdd(ctx, s.repo, s.req.PR, label)
	if err == nil {
		return nil
	}
	if s.mode == "automated" {
		return &Refusal{
			Message: "could not apply the label '" + label + "' to " + s.repo.String() + "#" + strconv.Itoa(s.req.PR),
			Hint:    "The loop is label-driven, so this is fatal rather than cosmetic. Check the token's issues permission and GitHub's availability, then retry.",
		}
	}
	return nil
}

func mustPass(n int) core.PassNumber {
	p, err := core.NewPassNumber(n)
	if err != nil {
		p, _ = core.NewPassNumber(1)
	}
	return p
}
