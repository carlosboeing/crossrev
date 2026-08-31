package vcs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/carlosboeing/crossrev/internal/core"
)

// ErrWorktreePath is returned when a worktree path cannot be built.
var ErrWorktreePath = errors.New("a worktree path needs a repository slug and a state directory")

// WorktreeDir is where the resolve leg's dedicated worktree lives. It is
// _worktree_dir at lib/run.sh:58-62.
//
// Built by concatenation rather than by joining, because the shell builds it by
// concatenation: a trailing slash in XDG_STATE_HOME survives into the answer,
// and cleaning it here would make this and the shell disagree on a path that
// names the same directory. The frozen run-log vectors record the same double
// slash for the same reason.
//
// The zero slug is refused. core.Slug.PathKey renders it as `-`, which would
// give every repository that never resolved a slug the same reusable worktree —
// and the reuse test below cannot tell them apart, because two checkouts at one
// pull request hold the same commit by definition.
func WorktreeDir(slug core.Slug, pr int) (string, error) {
	if slug.Incomplete() {
		return "", fmt.Errorf("%w: the slug is %q", ErrWorktreePath, slug)
	}
	state := stateHome()
	if state == "" {
		return "", fmt.Errorf("%w: neither XDG_STATE_HOME nor HOME is set", ErrWorktreePath)
	}
	return state + "/crossrev/worktrees/" + slug.PathKey() + "/pr-" + strconv.Itoa(pr), nil
}

// stateHome is `${XDG_STATE_HOME:-$HOME/.local/state}`. The `:-` form falls
// back on an empty value as well as an unset one.
func stateHome() string {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return state
	}
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}
	return home + "/.local/state"
}

// AddWorktree creates a detached worktree of revision at dir, making the parent
// directory it needs. It is lib/run.sh:1889-1894.
//
// The failure carries git's own output, because there is nothing better to say
// about a refused `worktree add` than what git said about it
// (lib/run.sh:1891-1893).
func (r *Repository) AddWorktree(ctx context.Context, dir string, revision core.Revision) error {
	if revision.IsZero() {
		return &Refusal{
			Message: fmt.Sprintf("could not create worktree at %s: no revision was given", dir),
			Hint:    "A resolve leg checks out the pull request's head revision, and it was not resolved before this call.",
		}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	// `2>&1`, as at lib/run.sh:1891: the hint is git's own words about why it
	// refused, and there is nothing better to say than what git said.
	output, err := r.RunCombined(ctx, "worktree", "add", "--detach", dir, revision.SHA())
	if err != nil {
		return err
	}
	if !output.OK() {
		return &Refusal{
			Message: fmt.Sprintf("could not create worktree for revision '%s' at %s", revision.SHA(), dir),
			Hint:    output.Stdout,
		}
	}
	return nil
}

// PruneWorktrees is `git worktree prune`, which forgets the administrative
// records of worktrees whose directories are gone (lib/run.sh:1885). Its
// failure is ignored there and here: a prune that did not run leaves stale
// records and nothing else.
func (r *Repository) PruneWorktrees(ctx context.Context) {
	_, _ = r.Run(ctx, "worktree", "prune")
}

// WorktreeReusable reports whether an existing directory is this clone's own
// worktree, sitting at the revision the leg is about to work on. It is the test
// at lib/run.sh:1878-1886.
//
// Ownership is asked as well as revision, and lib/run.sh:64-70 says why: the
// path is keyed on the repository slug and the pull request number alone, so
// two checkouts of one repository collide on it, and the head revision cannot
// tell them apart — two checkouts at the same pull request hold the same commit
// by definition. Reusing the wrong one commits in a checkout the operator is
// not standing in, and pushes where that checkout's remote points.
func (r *Repository) WorktreeReusable(ctx context.Context, dir string, revision core.Revision) (bool, error) {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false, nil
	}

	candidate := r.git.At(dir)
	head, err := candidate.Head(ctx)
	if err != nil {
		// An unreadable HEAD is the empty string in the shell, which fails the
		// comparison below rather than the call.
		return false, nil
	}
	if head.IsZero() || !head.Equal(revision) {
		return false, nil
	}

	owner, err := candidate.CommonDir(ctx)
	if err != nil || owner == "" {
		return false, nil
	}
	mine, err := r.CommonDir(ctx)
	if err != nil || mine == "" {
		return false, nil
	}
	return owner == mine, nil
}

// RemoveWorktree takes the worktree away, and then the directory that held it.
//
// The order is the shell's (lib/run.sh:2450-2454). `git worktree remove
// --force` first, so git forgets its administrative record; a plain delete
// otherwise, because a directory git will not claim is still a directory in the
// way. Then one level of parent, and one only: the `-p` form of rmdir walks up
// removing every parent that becomes empty and stops at the first that is not,
// which on a machine where the state directory holds nothing else removes far
// more than CrossRev's own.
//
// Whether a failed leg reaches this at all is the caller's decision, not this
// function's. lib/run.sh:96-99 keeps the worktree on a non-zero exit and prints
// its path, because a tree nobody can look at is a leg nobody can debug.
func (r *Repository) RemoveWorktree(ctx context.Context, dir string) error {
	output, err := r.Run(ctx, "worktree", "remove", "--force", dir)
	if err != nil || !output.OK() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			return removeErr
		}
	}
	// Both failures are ignored: a parent that still holds another pull
	// request's worktree is supposed to survive.
	_ = os.Remove(filepath.Dir(dir))
	return nil
}
