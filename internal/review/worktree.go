package review

import (
	"context"
	"fmt"
	"os"
	"strings"

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
// call. An empty one creates a fresh detached worktree at the head by the
// resolve pattern — the ordered head fetch, then a fresh uniquely named
// worktree — and reports it created, so the caller removes it at leg end.
// The one worktree serves every call the pass makes, so quarantine and
// finding anchors resolve under it and never under the operator checkout.
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
	base, err := vcs.ReviewWorktreeDir(loaded.Repo, req.PR)
	if err != nil {
		return "", false, err
	}
	wt, err := l.selectWorktree(ctx, base, head)
	if err != nil {
		return "", false, err
	}
	// The worktree was just created at the head and proved registered to
	// this clone; the checked-out HEAD is proved once more before the
	// first call, so a swap in between still fails closed here.
	if have, err := l.VCS.HeadAt(ctx, wt); err != nil {
		return "", false, err
	} else if !have.Equal(head) {
		return "", false, newWorktreeHeadMismatch(wt, have, head)
	}
	return wt, true, nil
}

// maxWorktreeCandidates bounds the probe for a free worktree path: the
// canonical directory, then suffixed ones beside it.
const maxWorktreeCandidates = 100

// selectWorktree creates the fresh directory the pass works in.
//
// The leg never reuses a prior tree: every existing occupant — a clean tree
// at the head, a dirty one, an ignored leftover, a symlink, a file, a nested
// repository, another clone's worktree — is preserved, and the pass works in
// the first absent suffixed directory beside it. Reuse is what let dirty,
// ignored, symlinked, nested and foreign occupants reach the harness, and
// what a path swap between the check and its use could divert; a fresh `git
// worktree add` checks out only the head's tracked files, so ignored and
// untracked leftovers, submodule contents and nested repositories cannot
// arrive with it. The path is keyed on the repository and the pull request
// alone, so removing an occupant could take out uncommitted edits this run
// never proved it owns: the pass works beside it, never in it and never by
// removing it.
func (l *Leg) selectWorktree(ctx context.Context, base string, head core.Revision) (string, error) {
	if err := refuseSymlinkedParents(base); err != nil {
		return "", err
	}
	for n := 1; n <= maxWorktreeCandidates; n++ {
		candidate := base
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d", base, n)
		}
		// Lstat, not Stat: a symlink at the candidate is an occupant to
		// work beside, not a directory to follow into. Following it
		// handed the harness the checkout the link targets.
		if _, err := os.Lstat(candidate); err == nil {
			if l.Log != nil {
				l.Log.Event("worktree", "preserved "+candidate)
			}
			continue
		} else if !os.IsNotExist(err) {
			return "", err
		}
		l.VCS.PruneWorktrees(ctx)
		if err := l.VCS.AddWorktree(ctx, candidate, head); err != nil {
			// Another run may have won the race for this path between
			// the absence check and the add — git refuses an existing
			// path — in which case the next suffixed directory is
			// tried. Anything else also arrives here when the path is
			// still absent, and is reported rather than retried.
			if _, statErr := os.Lstat(candidate); statErr == nil {
				continue
			}
			return "", err
		}
		if err := l.proveFreshWorktree(ctx, candidate, head); err != nil {
			return "", err
		}
		if l.Log != nil {
			l.Log.Event("worktree", "created "+candidate)
		}
		return candidate, nil
	}
	return "", &ui.FatalError{
		Reason: fmt.Sprintf("could not find a free worktree path under %s", base),
		Action: "The preserved worktrees beside it were left by failed legs for debugging. Remove the ones no longer needed and re-run the leg.",
	}
}

// proveFreshWorktree proves the directory AddWorktree just created is still
// the worktree this run made: a real directory rather than a swap, checked
// out at the head, and registered to this clone rather than another one. A
// path replaced between the check and its use fails closed here, before any
// model call, and the directory is left for inspection rather than removed.
func (l *Leg) proveFreshWorktree(ctx context.Context, dir string, head core.Revision) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return &ui.FatalError{
			Reason: fmt.Sprintf("the worktree path %s is not a directory this run created", dir),
			Action: "Something replaced the fresh review worktree between its creation and its first use. Remove the unexpected entry and re-run the leg.",
		}
	}
	have, err := l.VCS.HeadAt(ctx, dir)
	if err != nil {
		return err
	}
	if !have.Equal(head) {
		return newWorktreeHeadMismatch(dir, have, head)
	}
	reusable, err := l.VCS.WorktreeReusable(ctx, dir, head)
	if err != nil {
		return err
	}
	if !reusable {
		return &ui.FatalError{
			Reason: fmt.Sprintf("the worktree at %s is not registered to this checkout", dir),
			Action: "Something replaced the fresh review worktree between its creation and its first use. Remove the unexpected entry and re-run the leg.",
		}
	}
	return nil
}

// refuseSymlinkedParents refuses a worktree base reached through a symlink:
// creating under one would check the head out inside whatever the link
// targets rather than the state directory. Every component of the shared
// parent chain below the state home is listed without following it, so a
// symlink anywhere on the path fails closed before anything is created. The
// state home itself is the operator's configured location and is trusted as
// found; absent components are fine, they are the leg's own to create.
func refuseSymlinkedParents(base string) error {
	const anchor = "/crossrev/worktrees/"
	i := strings.Index(base, anchor)
	if i < 0 {
		return &ui.FatalError{
			Reason: fmt.Sprintf("the review worktree path %s is not under a worktree directory", base),
			Action: "Re-run the leg: the worktree path is built internally, so an unexpected layout means something replaced it.",
		}
	}
	root := strings.TrimSuffix(base[:i], "/")
	// The whole chain below the state home, anchor included: the shared
	// parent of every candidate runs through it.
	rel := strings.Trim(base[i:], "/")
	parts := strings.Split(rel, "/")
	prefix := root
	if prefix == "" {
		prefix = "/"
	}
	// Every prefix short of the final component: the final component is
	// the candidate itself, whose symlinks the selection loop works
	// beside rather than refusing.
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." {
			continue
		}
		if prefix == "/" {
			prefix = "/" + part
		} else {
			prefix = prefix + "/" + part
		}
		fi, err := os.Lstat(prefix)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return &ui.FatalError{
				Reason: fmt.Sprintf("the worktree parent %s is a symlink", prefix),
				Action: "A fresh review worktree cannot be created under a symlink without checking the head out inside its target. Remove the link and re-run the leg.",
			}
		}
		if !fi.IsDir() {
			return &ui.FatalError{
				Reason: fmt.Sprintf("the worktree parent %s is not a directory", prefix),
				Action: "A fresh review worktree cannot be created under it. Remove the unexpected entry and re-run the leg.",
			}
		}
	}
	return nil
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
