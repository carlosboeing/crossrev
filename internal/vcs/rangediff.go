package vcs

import (
	"context"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/core"
)

// RangeDiff produces the resolver-only two-dot delta between the reviewed
// head B and the repaired head C, without reading policy from either head.
//
// The review leg reads the full base-to-head scope for coverage; the repair
// delta travels ahead of it as required confirmation input, so the reviewer
// sees what the resolver changed before re-judging the whole. Two-dot
// rather than three-dot: B and C share the same base, and the confirmation
// input is exactly what moved between the two heads.
func (r *Repository) RangeDiff(ctx context.Context, base, head core.Revision) ([]byte, error) {
	output, err := r.Run(ctx, "diff", base.SHA()+".."+head.SHA())
	if err != nil {
		return nil, err
	}
	if !output.OK() {
		return nil, fmt.Errorf("git diff %s..%s exited %d: %s", base.SHA(), head.SHA(), output.ExitCode, output.Stderr)
	}
	return []byte(output.Stdout), nil
}
