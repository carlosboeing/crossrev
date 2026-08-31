package resolve

import (
	"context"

	"github.com/carlosboeing/crossrev/internal/vcs"
)

// pushRemoteWarnings collects pushInsteadOf rewrites. They stay warnings.
func pushRemoteWarnings(target vcs.PushTarget) []string {
	var out []string
	for _, w := range target.Warnings {
		out = append(out, w.Message)
	}
	return out
}

func (l *Leg) resolveRemote(ctx context.Context, branch string) (string, vcs.PushTarget, error) {
	remote, err := l.pushRemote(ctx, branch)
	if err != nil {
		return "", vcs.PushTarget{}, err
	}
	target, err := l.Git.ResolvePushRepo(ctx, remote)
	return remote, target, err
}
