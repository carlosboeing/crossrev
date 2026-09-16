package vcs

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// ExactSearchLimit is the per-term search cap at the git layer: a fixed-string
// search returns no more than this many hits, and reports the term as too
// common when more exist. It names the same section 7.2 budget as
// intel.MaxSearchHits from the other side of the tier boundary — intel is tier
// 1 and cannot import this tier-2 package, so the two constants state the same
// number twice rather than sharing one.
const ExactSearchLimit = 200

// SearchHit is one path holding a fixed-string term at the searched revision.
type SearchHit struct {
	// Path is the repository-relative path git reported.
	Path string
}

// ExactSearch lists the paths holding term at revision as a fixed string.
//
// The call is `git grep -F --name-only -z -e <term> <revision> -- .`, so a
// term carrying pattern bytes or a leading dash still matches literally, and a
// path carrying a space arrives as one NUL-delimited field rather than two.
// Hits sort by path, so two runs over one revision answer identically.
//
// At most limit hits return. When more paths hold the term, the first limit by
// path order return with tooCommon set, and the caller records the cap rather
// than treating the truncated list as complete. A term with no holder is an
// empty list, because git answers that with exit 1 and there is no match to
// report.
func (r *Repository) ExactSearch(ctx context.Context, revision core.Revision, term string, limit int) ([]SearchHit, bool, error) {
	if revision.IsZero() {
		return nil, false, fmt.Errorf("vcs: exact search needs a revision")
	}
	if term == "" {
		return nil, false, fmt.Errorf("vcs: exact search needs a term")
	}
	if limit <= 0 {
		return nil, false, fmt.Errorf("vcs: exact search needs a limit above zero, got %d", limit)
	}
	output, err := r.Run(ctx, "grep", "-F", "--name-only", "-z", "-e", term, revision.SHA(), "--", ".")
	if err != nil {
		return nil, false, err
	}
	if !output.OK() {
		if output.ExitCode == 1 {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("git grep -F exited %d: %s", output.ExitCode, output.Stderr)
	}
	var hits []SearchHit
	prefix := revision.SHA() + ":"
	for _, field := range strings.Split(output.Stdout, "\x00") {
		if field == "" {
			continue
		}
		// A revision search prefixes every path with the revision as given,
		// "<sha>:<path>". The call above always passes the full SHA, so the
		// prefix is this exact string rather than something to guess at.
		hits = append(hits, SearchHit{Path: strings.TrimPrefix(field, prefix)})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Path < hits[j].Path })
	if len(hits) > limit {
		return hits[:limit], true, nil
	}
	return hits, false, nil
}
