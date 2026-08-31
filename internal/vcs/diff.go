package vcs

import "context"

// HasStagedChanges reports whether the index differs from HEAD.
//
// It is `git diff --cached --quiet` read for its status, which lib/github.sh:462
// uses to decide whether there is a commit to make at all: a resolve leg that
// changed no file must not produce an empty commit, and must not report a push
// it never made.
//
// Any non-zero status means "there is something", which is how the shell reads
// it. git answers 1 for a difference and something larger for a failure, and
// treating a failure as "nothing to commit" would silently drop the resolver's
// work.
func (r *Repository) HasStagedChanges(ctx context.Context) (bool, error) {
	output, err := r.Run(ctx, "diff", "--cached", "--quiet")
	if err != nil {
		return false, err
	}
	return !output.OK(), nil
}
