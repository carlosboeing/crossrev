package review

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/ui"
)

func (l *Leg) applyPassLabels(ctx context.Context, req Request, loaded Context, pass int, next policy.PassLabelState) ([]string, error) {
	var msgs []string
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
	if warn != "" {
		msgs = append(msgs, warn)
	}
	if err != nil {
		return msgs, err
	}
	if next != "" {
		warn, err = l.addLabel(ctx, loaded, req.PR, next.Label())
		if warn != "" {
			msgs = append(msgs, warn)
		}
		if err != nil {
			return msgs, err
		}
	}
	return msgs, nil
}

func (l *Leg) addLabel(ctx context.Context, loaded Context, pr int, label string) (string, error) {
	err := l.Forge.PullRequestLabelAdd(ctx, loaded.Repo, pr, label)
	if err == nil {
		return "", nil
	}
	if loaded.Config != nil && loaded.Config.Get(".mode") == "automated" {
		return "", &ui.FatalError{
			Reason: fmt.Sprintf("could not apply the label '%s' to %s#%d", label, loaded.Repo, pr),
			Action: "The loop is label-driven, so this is fatal rather than cosmetic. Check the token's issues permission and GitHub's availability, then retry.",
		}
	}
	// The guidance is joined to the condition with ui.Warn's own newline and
	// three-space indent, because addLabel answers one string and ui.Warn takes
	// two. Bash calls `ui_warn "$1" "$2"` here (lib/run.sh:326-327) and prints
	// exactly these bytes. Nothing outside this package reads Result.Messages
	// yet, so when Phase 4 wires a consumer, split this back into a pair and
	// let ui.Warn do the joining. Until then a change to ui.Warn's indent
	// diverges this line silently.
	return fmt.Sprintf("could not apply the label '%s' to %s#%d\n   Locally that is cosmetic, because this process drives both legs itself. In automated mode it would stall the chain, which is what `crossrev init` creates the labels for.", label, loaded.Repo, pr), nil
}
