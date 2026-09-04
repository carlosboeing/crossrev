package vcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrTreeCapture is returned when the working tree could not be captured or put
// back.
var ErrTreeCapture = errors.New("the working tree could not be captured")

// CaptureTree records everything in the working tree as a git tree object,
// through a temporary index of its own. It is _run_tree_capture at
// lib/run.sh:640-646.
//
// # Why a capture exists at all
//
// The resolve harness edits files before it returns its answer, and a rejected
// answer is thrown away — its edits are not. A retry that reuses the tree reads
// one the discarded attempt already changed: it applies a non-idempotent fix
// twice, or finds a finding already fixed and calls it skipped, and the commit
// takes whatever is sitting there either way. The resolutions then describe a
// tree nobody produced.
//
// # Why a temporary index and not a stash
//
// `git stash` rewrites the real index and pushes onto the operator's stash
// list, and CrossRev is routinely run in a checkout somebody else is also
// working in. The temporary index is seeded from the real one so the stat cache
// still applies, which makes this cost a status rather than a rehash of every
// file in the repository — and the seeding failure is ignored for the same
// reason, because a cold cache is slower and not wrong.
//
// indexPath must be absolute: git resolves GIT_INDEX_FILE against the directory
// each call runs in, and the calls here do not all run in the same one.
//
// # An ordering this package cannot enforce
//
// The quarantine must be restored before this runs, or the capture holds the
// quarantine and the commit carries it. sandbox.Restore is the other half and
// neither function can see the other, so the sequence belongs to the leg that
// drives both — and so does the test for it.
func (r *Repository) CaptureTree(ctx context.Context, indexPath string) (string, error) {
	gitDir, err := r.GitDir(ctx)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(r.baseDir(), gitDir)
	}

	if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	// `cp … || true`. A seed that did not happen costs the stat cache and
	// nothing else.
	_ = copyFile(filepath.Join(gitDir, "index"), indexPath)

	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if output, err := r.RunWithEnv(ctx, env, "add", "-A"); err != nil {
		return "", err
	} else if !output.OK() {
		return "", fmt.Errorf("%w: git add -A: %s", ErrTreeCapture, diagnostic(output))
	}

	// Separate streams, deliberately: this call's stdout is the tree object
	// name, and a git warning merged into it would be returned as one.
	output, err := r.RunWithEnv(ctx, env, "write-tree")
	if err != nil {
		return "", err
	}
	if !output.OK() {
		return "", fmt.Errorf("%w: git write-tree: %s", ErrTreeCapture, diagnostic(output))
	}
	return output.Text(), nil
}

// RestoreTree puts the working tree back to a captured state, or fails. It is
// _run_tree_restore at lib/run.sh:660-669.
//
// # Read into the capture's own index
//
// That is what keeps someone's staging area out of this. The capture is a tree
// of everything that was there, staged and unstaged alike, so resetting the
// real index to it would stage every unstaged change the run happened to find —
// and a pass that then fixes nothing hands the checkout back with a staging
// area CrossRev invented.
//
// The files still get rewritten: the capture's stat cache was recorded before
// the attempt ran, so anything the attempt touched fails the stat check and is
// checked out again.
//
// # Why the leftovers are read from the capture
//
// Anything untracked afterwards was written by the attempt being discarded: the
// capture indexed every file that was there before it ran, so read-tree
// restores those and only the leftovers are left over. Asking the real index
// instead would delete a file that was untracked before CrossRev started, as
// though the attempt had written it. Ignored files are neither captured nor
// removed, which is right — they are not committed either.
func (r *Repository) RestoreTree(ctx context.Context, indexPath, tree string) error {
	if tree == "" {
		return fmt.Errorf("%w: no tree was captured", ErrTreeCapture)
	}
	if info, err := os.Stat(indexPath); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: there is no capture at %s", ErrTreeCapture, indexPath)
	}

	top, err := r.TopLevel(ctx)
	if err != nil {
		return err
	}

	env := []string{"GIT_INDEX_FILE=" + indexPath}
	output, err := r.RunWithEnv(ctx, env, "read-tree", "--reset", "-u", tree)
	if err != nil {
		return err
	}
	if !output.OK() {
		return fmt.Errorf("%w: git read-tree: %s", ErrTreeCapture, diagnostic(output))
	}

	// Separate streams again, and here it decides what gets deleted: every line
	// of this stdout is joined onto the top level and handed to os.RemoveAll,
	// so a git diagnostic merged into it would become a path.
	leftovers, err := r.git.Run(ctx, Call{
		Dir:      top,
		ExtraEnv: env,
		Args:     []string{"ls-files", "--others", "--exclude-standard"},
	})
	if err != nil {
		return err
	}
	if !leftovers.OK() {
		return fmt.Errorf("%w: git ls-files: %s", ErrTreeCapture, diagnostic(leftovers))
	}

	for _, path := range leftovers.Lines() {
		if !RemovableUnder(top, path) {
			// Unreachable for anything git emits: ls-files answers
			// repository-relative paths and never an absolute one. The check is
			// here because the alternative is trusting a subprocess's stdout
			// with a recursive delete, and no reading of the shell makes that
			// trust explicit.
			continue
		}
		if err := os.RemoveAll(filepath.Join(top, path)); err != nil {
			return err
		}
	}
	return nil
}

// RemovableUnder reports whether a repository-relative path stays inside root
// once resolved.
//
// Lexical rather than symlink-resolving, and deliberately so: the paths it
// judges come from `git ls-files`, which names entries in the checkout, and a
// resolving check would refuse a legitimate file whose parent directory is a
// symlink into the same tree.
func RemovableUnder(root, path string) bool {
	if path == "" || filepath.IsAbs(path) {
		return false
	}
	cleanRoot := filepath.Clean(root)
	target := filepath.Join(cleanRoot, path)
	if target == cleanRoot {
		return false
	}
	return strings.HasPrefix(target, cleanRoot+string(filepath.Separator))
}

// baseDir is the directory a relative git answer is relative to.
func (r *Repository) baseDir() string {
	if r.dir == "" {
		return "."
	}
	return r.dir
}

// copyFile copies source over destination, creating it.
func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// diagnostic is what git said about a call whose streams were kept apart.
//
// stderr first, because that is where git puts a refusal, and stdout only when
// stderr said nothing. These calls are not ported captures — the shell throws
// their output away entirely (lib/run.sh:644, :667) — so there is no `2>&1` to
// match and no order to preserve.
func diagnostic(output Output) string {
	if message := strings.TrimSpace(output.Stderr); message != "" {
		return message
	}
	return strings.TrimSpace(output.Stdout)
}
