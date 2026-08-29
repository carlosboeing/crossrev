package vcs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// FileStatus is what a read found at a path.
//
// The three constants are declared in this order deliberately. config.FileStatus
// declares the same three in the same order, and the tier-3 wiring that hands
// Show to config.Load converts between them by value. The two packages cannot
// import one another — config imports no peer, which is what keeps the git
// adapter out of it — so the agreement is stated here rather than compiled.
type FileStatus int

const (
	// NotFound is nothing at the path.
	NotFound FileStatus = iota
	// IsFile is a regular file at the path, or a blob at the revision. The
	// content is its bytes, which may be empty.
	IsFile
	// IsOther is a path that exists but holds no file content: a directory in
	// the working tree, or a tree at a revision.
	IsOther
)

// Show reads one path, at a revision or from the working tree.
//
// # Policy comes from the base revision
//
// A read at a revision is `git show <revision>:<path>`, which lib/config.sh:95
// uses and lib/config.sh:87-93 explains: read from the pull request's head, a
// pull request could raise max_passes_per_cycle, repoint an endpoint at a
// server it controls and harvest every prompt, or ship a review instruction
// saying to return converged. So policy is read from the base revision and
// never the head, and a pull request that legitimately changes review policy
// takes effect when it merges — the new policy reviewed under the old one. That
// is a decision of record (ADR 0003) rather than a convention, which is why it
// is structural here: there is no argument on this method that could name the
// head.
//
// # The zero revision is the working tree
//
// A zero revision is a plain filesystem read, and the path it is given is not
// always repository-relative: the operator configuration layer lives at an
// absolute path outside the checkout, built by _cfg_operator_path
// (lib/config.sh:23) and read from the working tree at lib/config.sh:215-216.
// Resolving every path
// against the repository would drop that layer without a word.
//
// # Why a tree is found rather than skipped
//
// `[[ -f ]]` and `git show` disagree about a directory, and the shell keeps the
// disagreement. A working-tree read is guarded by `[[ -f ]]` (lib/config.sh:37)
// so a directory at the config path states no policy and the next path is
// tried. A revision read is `git show`, which succeeds on a tree and prints its
// listing, so the file is found and then refused for its shape. IsOther carries
// that distinction to the caller instead of resolving it here.
func (r *Repository) Show(ctx context.Context, revision core.Revision, path string) ([]byte, FileStatus, error) {
	if revision.IsZero() {
		return showWorkingTree(path)
	}
	return r.showRevision(ctx, revision, path)
}

// showWorkingTree is the `[[ -f ]]` read, on the same terms: a symlink to a
// regular file is a regular file, and anything else that exists is IsOther.
func showWorkingTree(path string) ([]byte, FileStatus, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, NotFound, nil
		}
		return nil, NotFound, err
	}
	if !info.Mode().IsRegular() {
		return nil, IsOther, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, NotFound, err
	}
	return content, IsFile, nil
}

// showRevision asks git what the object is before asking for its bytes.
//
// Two calls where the shell makes one, and the second call is what buys the
// blob-or-tree answer. `git show <rev>:<dir>` prints a listing headed by the
// object name, so reading its stdout alone cannot say whether the bytes are a
// file's content or git's rendering of a directory — and the caller has to
// know, because one is parsed and the other is refused.
func (r *Repository) showRevision(ctx context.Context, revision core.Revision, path string) ([]byte, FileStatus, error) {
	spec := revision.SHA() + ":" + path

	kind, err := r.Run(ctx, "cat-file", "-t", spec)
	if err != nil {
		return nil, NotFound, err
	}
	if !kind.OK() {
		// The path is not in the revision, or the revision is not there. Both
		// are `git show … || return 1` at lib/config.sh:95.
		return nil, NotFound, nil
	}
	if strings.TrimSpace(kind.Text()) != "blob" {
		return nil, IsOther, nil
	}

	content, err := r.Run(ctx, "cat-file", "blob", spec)
	if err != nil {
		return nil, NotFound, err
	}
	if !content.OK() {
		return nil, NotFound, nil
	}
	return []byte(content.Stdout), IsFile, nil
}
