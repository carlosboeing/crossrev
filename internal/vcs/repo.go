package vcs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
)

// Refusal is one fatal condition, in the two parts ui_die takes: what went
// wrong, and what to do about it (lib/ui.sh:113-121).
//
// The type is this package's own rather than a shared one, because a tier-2
// package imports no peer. It is deliberately the same shape as
// config.Refusal, so the tier-3 caller that renders both renders them the same
// way.
type Refusal struct {
	Message string
	Hint    string
}

func (r *Refusal) Error() string { return r.Message }

// Rendered is the two lines ui_die writes to stderr, with the colour escapes
// and the trailing blank line removed. It is the form
// tests/fixtures/parity/push_target.json records.
func (r *Refusal) Rendered() string {
	return "\nerror  " + r.Message + "\n       " + r.Hint
}

// Warning is one non-fatal condition, in the two parts ui_warn takes
// (lib/ui.sh:101-104).
//
// Returned rather than printed. This package starts processes and touches the
// filesystem; deciding what a terminal sees belongs to the caller, and a
// warning that printed itself could not be asserted against a frozen vector.
type Warning struct {
	Message string
	Hint    string
}

// Rendered is the two lines ui_warn writes to stderr, with the colour escapes
// and the trailing blank line removed.
func (w Warning) Rendered() string {
	return "\n⚠  " + w.Message + "\n   " + w.Hint
}

// Repository is one checkout, or one worktree of one.
type Repository struct {
	git *Git
	dir string
}

// Git is the command line this repository runs through.
func (r *Repository) Git() *Git { return r.git }

// Dir is the working directory every call runs in.
func (r *Repository) Dir() string { return r.dir }

// Run makes one git call in this repository.
func (r *Repository) Run(ctx context.Context, args ...string) (Output, error) {
	return r.git.Run(ctx, Call{Dir: r.dir, Args: args})
}

// RunWithEnv makes one git call with extra environment entries appended.
func (r *Repository) RunWithEnv(ctx context.Context, extraEnv []string, args ...string) (Output, error) {
	return r.git.Run(ctx, Call{Dir: r.dir, Args: args, ExtraEnv: extraEnv})
}

// RunCombined makes one git call whose two output streams arrive as one, in the
// order the child wrote them. Output.Stdout holds the whole of it.
//
// It is `$(git … 2>&1)`, which is how lib/github.sh:481 captures a commit,
// lib/github.sh:510 a push and lib/run.sh:1897 a worktree creation. Use it only
// where the output is a message for a person: a call whose stdout is read as
// data must never have git's diagnostics mixed into it.
func (r *Repository) RunCombined(ctx context.Context, args ...string) (Output, error) {
	return r.git.Run(ctx, Call{Dir: r.dir, Args: args, Streams: exec.StreamsCombined})
}

// ErrNotARepository is returned when git answers that the directory is not one.
//
// The shell spells this as `|| return 1` after a `git rev-parse` redirected to
// /dev/null (lib/run.sh:73-74), so the distinction between "not a repository"
// and "git is not installed" is lost there. It is kept here because the two
// need different messages and nothing downstream can recover it later.
var ErrNotARepository = errors.New("not inside a git repository")

// GitDir is `git rev-parse --git-dir`, which is this working tree's own
// directory and not the clone's shared one. It is what lib/run.sh:641 reads,
// because the index it copies is this tree's.
func (r *Repository) GitDir(ctx context.Context) (string, error) {
	return r.revParse(ctx, "--git-dir")
}

// TopLevel is `git rev-parse --show-toplevel` (lib/run.sh:672).
func (r *Repository) TopLevel(ctx context.Context) (string, error) {
	return r.revParse(ctx, "--show-toplevel")
}

func (r *Repository) revParse(ctx context.Context, flag string) (string, error) {
	output, err := r.Run(ctx, "rev-parse", flag)
	if err != nil {
		return "", err
	}
	if !output.OK() {
		return "", fmt.Errorf("%w: %s", ErrNotARepository, r.displayDir())
	}
	value := output.Text()
	if value == "" {
		return "", fmt.Errorf("%w: %s", ErrNotARepository, r.displayDir())
	}
	return value, nil
}

// CommonDir is the clone's shared git directory, as an absolute path with every
// symlink resolved.
//
// It is `git rev-parse --git-common-dir` put through `cd … && pwd -P`, which
// lib/run.sh:71-78 and lib/run.sh:192-193 both do and for the same reason:
// every working tree of a clone must resolve to one directory, so that two
// worktrees find the same run lock and so that a reusable worktree can be
// tested for belonging to this clone rather than another checkout of it.
//
// The resolution is not decoration. git answers this relative to the current
// directory — "../.git" from a subdirectory — and both call sites build a path
// by concatenating onto it.
func (r *Repository) CommonDir(ctx context.Context) (string, error) {
	common, err := r.revParse(ctx, "--git-common-dir")
	if err != nil {
		return "", err
	}
	// `cd "$dir" && cd "$common"`: a relative answer is relative to the
	// directory git ran in, and an absolute one ignores it.
	if !filepath.IsAbs(common) {
		base := r.dir
		if base == "" {
			base = "."
		}
		common = filepath.Join(base, common)
	}
	absolute, err := filepath.Abs(common)
	if err != nil {
		return "", err
	}
	// `pwd -P` resolves every component, and it fails when the directory is not
	// there — which is the `|| return 0` at lib/run.sh:193.
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return physical, nil
}

// Head is the revision HEAD names.
//
// The shell reads this as `git rev-parse HEAD 2>/dev/null || true` at
// lib/run.sh:1908 and lib/run.sh:1885, so an unborn HEAD yields an empty string
// there and the zero Revision here.
func (r *Repository) Head(ctx context.Context) (core.Revision, error) {
	output, err := r.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return core.Revision{}, err
	}
	if !output.OK() {
		return core.Revision{}, nil
	}
	text := output.Text()
	if text == "" {
		return core.Revision{}, nil
	}
	return core.NewRevision(text)
}

// HasCommit reports whether the object database holds this revision as a
// commit. It is `git cat-file -e "<sha>^{commit}"` (lib/run.sh:1873), whose
// exit status is the whole answer.
func (r *Repository) HasCommit(ctx context.Context, revision core.Revision) (bool, error) {
	if revision.IsZero() {
		return false, nil
	}
	output, err := r.Run(ctx, "cat-file", "-e", revision.SHA()+"^{commit}")
	if err != nil {
		return false, err
	}
	return output.OK(), nil
}

// ConfigGet is `git config --get <key>`, empty when the key is unset.
//
// Every caller in lib/ reads it as `$(git config … 2>/dev/null || true)`
// (lib/run.sh:1851-1857), so a missing key and an empty value are one answer.
func (r *Repository) ConfigGet(ctx context.Context, key string) (string, error) {
	output, err := r.Run(ctx, "config", "--get", key)
	if err != nil {
		return "", err
	}
	if !output.OK() {
		return "", nil
	}
	return output.Text(), nil
}

// ConfigGetAll is `git config --get-all <key>`, one entry per configured value.
//
// The plural form is the point: a key set more than once carries every value,
// and lib/legs.sh:369-372 turns on exactly that — `git remote get-url --push`
// returns only the first pushurl, so a second entry pointing somewhere else is
// invisible to it.
func (r *Repository) ConfigGetAll(ctx context.Context, key string) ([]string, error) {
	output, err := r.Run(ctx, "config", "--get-all", key)
	if err != nil {
		return nil, err
	}
	if !output.OK() {
		return nil, nil
	}
	return output.Lines(), nil
}

// displayDir names the directory a message is about.
func (r *Repository) displayDir() string {
	if r.dir == "" {
		return "the current directory"
	}
	return r.dir
}
