package review

import (
	"context"
	"fmt"
	"os"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// WorktreeHeadMismatch refuses an explicit workdir override whose checked-out
// HEAD is not the pull request head. Findings anchor against the worktree's
// files, so running the harness anywhere else would mis-anchor them; the leg
// stops before any model call instead.
//
// It unwraps to the *ui.FatalError the terminal prints, so the composition
// root renders it like every other refusal.
type WorktreeHeadMismatch struct {
	*ui.FatalError
	Dir  string
	Have string
	Want string
}

// Unwrap answers the printable refusal.
func (e *WorktreeHeadMismatch) Unwrap() error { return e.FatalError }

func newWorktreeHeadMismatch(dir string, have, want core.Revision) *WorktreeHeadMismatch {
	return &WorktreeHeadMismatch{
		FatalError: &ui.FatalError{
			Reason: fmt.Sprintf("the checkout at %s is at %s, not the pull request head %s", dir, have.Short(), want.Short()),
			Action: "Check out the pull request head in that directory, or run without a workdir override so the leg pins a worktree at the head itself.",
		},
		Dir:  dir,
		Have: have.SHA(),
		Want: want.SHA(),
	}
}

// prepareWorktree answers the directory the harness runs in.
//
// An explicit req.Workdir stays as the override it is: its HEAD is proved
// against the pull request head and a mismatch is refused before any model
// call. An empty one creates a clean detached worktree at the head by the
// resolve pattern — the ordered head fetch, then reuse or create — and
// reports it created, so the caller removes it at leg end. The one
// worktree serves every call the pass makes, so quarantine and finding
// anchors resolve under it and never under the operator checkout.
func (l *Leg) prepareWorktree(ctx context.Context, req Request, loaded Context) (workdir string, created bool, err error) {
	head := loaded.PR.HeadRefOid
	if req.Workdir != "" {
		have, err := l.VCS.HeadAt(ctx, req.Workdir)
		if err != nil {
			return "", false, err
		}
		if !have.Equal(head) {
			return "", false, newWorktreeHeadMismatch(req.Workdir, have, head)
		}
		return req.Workdir, false, nil
	}
	if err := l.ensureHeadPresent(ctx, req, loaded); err != nil {
		return "", false, err
	}
	wt, err := vcs.WorktreeDir(loaded.Repo, req.PR)
	if err != nil {
		return "", false, err
	}
	reusable, err := l.VCS.WorktreeReusable(ctx, wt, head)
	if err != nil {
		return "", false, err
	}
	if !reusable {
		_ = os.RemoveAll(wt)
		l.VCS.PruneWorktrees(ctx)
		if err := l.VCS.AddWorktree(ctx, wt, head); err != nil {
			return "", false, err
		}
		if l.Log != nil {
			l.Log.Event("worktree", "created "+wt)
		}
	}
	// The worktree was just created at the head or proved reusable at it;
	// either way the checked-out HEAD is proved before the first call.
	if have, err := l.VCS.HeadAt(ctx, wt); err != nil {
		return "", false, err
	} else if !have.Equal(head) {
		return "", false, newWorktreeHeadMismatch(wt, have, head)
	}
	return wt, true, nil
}

// ensureHeadPresent fetches a missing head through the resolve pattern's
// ordered fallbacks: the head SHA itself, the pull request ref, the branch,
// then everything the remote holds. A head the clone already holds costs no
// fetch. It is internal/resolve/invoke.go:47-70, whose order is the point:
// each fallback reaches a smaller set of repositories than the next.
func (l *Leg) ensureHeadPresent(ctx context.Context, req Request, loaded Context) error {
	head := loaded.PR.HeadRefOid
	if ok, err := l.VCS.HasCommit(ctx, head); err != nil {
		return err
	} else if ok {
		return nil
	}
	remote, err := l.pushRemote(ctx, loaded.PR.HeadRefName)
	if err != nil {
		return err
	}
	_ = l.VCS.Fetch(ctx, remote, head.SHA())
	if ok, _ := l.VCS.HasCommit(ctx, head); !ok {
		_ = l.VCS.Fetch(ctx, remote, fmt.Sprintf("refs/pull/%d/head", req.PR))
	}
	if ok, _ := l.VCS.HasCommit(ctx, head); !ok {
		_ = l.VCS.Fetch(ctx, remote, loaded.PR.HeadRefName)
	}
	if ok, _ := l.VCS.HasCommit(ctx, head); !ok {
		_ = l.VCS.Fetch(ctx, remote, "")
	}
	if ok, _ := l.VCS.HasCommit(ctx, head); !ok {
		return &ui.FatalError{
			Reason: fmt.Sprintf("could not find revision '%s' for %s#%d", head.SHA(), loaded.Repo, req.PR),
			Action: fmt.Sprintf("Fetching from remote '%s' did not reach the pull request's head revision.", remote),
		}
	}
	return nil
}

// pushRemote is internal/resolve/invoke.go:142-157: the remote the head
// branch pushes to, read off the branch and push-default keys.
func (l *Leg) pushRemote(ctx context.Context, branch string) (string, error) {
	for _, key := range []string{
		"branch." + branch + ".pushRemote",
		"branch." + branch + ".remote",
		"remote.pushDefault",
	} {
		val, err := l.VCS.ConfigGet(ctx, key)
		if err != nil {
			return "", err
		}
		if val != "" {
			return val, nil
		}
	}
	return "origin", nil
}
