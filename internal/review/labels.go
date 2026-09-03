package review

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/ui"
)

func (l *Leg) applyPassLabels(ctx context.Context, req Request, loaded Context, pass int, next policy.PassLabelState) ([]ui.Line, error) {
	var msgs []ui.Line
	for _, word := range []policy.PassLabelState{
		policy.PassAwaitingReview,
		policy.PassAwaitingResolution,
		policy.PassConverged,
		policy.PassHalted,
	} {
		if word == next {
			continue
		}
		l.Forge.PullRequestLabelRemove(ctx, loaded.Repo, req.PR, word.Label())
	}
	l.Forge.PullRequestLabelRemove(ctx, loaded.Repo, req.PR, policy.LabelWatchdogRetried)

	wantPass := policy.LabelPassPrefix + strconv.Itoa(pass)
	for _, label := range loaded.PR.Labels {
		name := label.Name
		if strings.HasPrefix(name, policy.LabelPassPrefix) && name != wantPass {
			l.Forge.PullRequestLabelRemove(ctx, loaded.Repo, req.PR, name)
		}
	}
	warn, err := l.addLabel(ctx, loaded, req.PR, wantPass)
	if warn.Text != "" {
		msgs = append(msgs, warn)
	}
	if err != nil {
		return msgs, err
	}
	if next != "" {
		warn, err = l.addLabel(ctx, loaded, req.PR, next.Label())
		if warn.Text != "" {
			msgs = append(msgs, warn)
		}
		if err != nil {
			return msgs, err
		}
	}
	return msgs, nil
}

func (l *Leg) addLabel(ctx context.Context, loaded Context, pr int, label string) (ui.Line, error) {
	err := l.Forge.PullRequestLabelAdd(ctx, loaded.Repo, pr, label)
	if err == nil {
		return ui.Line{}, nil
	}
	if loaded.Config != nil && loaded.Config.Get(".mode") == "automated" {
		return ui.Line{}, &ui.FatalError{
			Reason: fmt.Sprintf("could not apply the label '%s' to %s#%d", label, loaded.Repo, pr),
			Action: "The loop is label-driven, so this is fatal rather than cosmetic. Check the token's issues permission and GitHub's availability, then retry.",
		}
	}
	// The pair, kept apart, so ui.Warn does its own joining. Bash calls
	// `ui_warn "$1" "$2"` here (lib/run.sh:332-333).
	return ui.Warn(
		fmt.Sprintf("could not apply the label '%s' to %s#%d", label, loaded.Repo, pr),
		"Locally that is cosmetic, because this process drives both legs itself. In automated mode it would stall the chain, which is what `crossrev init` creates the labels for."), nil
}
