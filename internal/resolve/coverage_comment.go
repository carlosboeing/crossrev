package resolve

import (
	"context"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// commentCoverage is the skip warning, the policy exclusion line and
// the coverage footnote for one rewrite of the review comment. on is false
// when the pass's generation cannot be read or is not current, and the
// rewrite then keeps the comment it has always written.
type commentCoverage struct {
	on       bool
	sha      string
	counts   intel.CoverageCounts
	skips    []intel.SkipNotice
	excluded []string
}

// reviewCommentCoverage reads the generation the review marker names, on
// the same terms as generationIfCurrent. A header skip's quote is read from
// the committed head blob, never from the working tree.
func (l *Leg) reviewCommentCoverage(ctx context.Context, s *session) commentCoverage {
	h, claimed, err := s.review.CoverageHandle()
	if err != nil || !claimed {
		return commentCoverage{}
	}
	gen, ok := l.generationIfCurrent(ctx, s, h)
	if !ok {
		return commentCoverage{}
	}
	sha, shaOK := s.review.HeadSHA.Get()
	if !shaOK || sha == "" {
		sha = gen.Revision.Head.SHA()
	}
	note := commentCoverage{
		on:  true,
		sha: sha,
		counts: intel.CoverageCounts{
			Required: len(gen.Paths),
			Excluded: len(gen.Excluded),
		},
	}
	for _, record := range gen.Records {
		if record.Type != prstate.CoverageRecordUnit {
			continue
		}
		disp, ok := record.Verdict.Get()
		if ok && disp != "" {
			note.counts.Covered++
		}
	}
	for _, exclusion := range gen.Excluded {
		signal, _, _, parsed := intel.ParseSkipReason(exclusion.Reason)
		if !parsed {
			note.excluded = append(note.excluded, exclusion.Path)
			continue
		}
		excerpt := ""
		if signal == intel.SignalHeader {
			excerpt = l.headExcerpt(ctx, s.pr.HeadRefOid, exclusion.Path)
		}
		note.skips = append(note.skips, intel.SkipNotice{
			Path:    exclusion.Path,
			Reason:  exclusion.Reason,
			Excerpt: excerpt,
		})
	}
	note.counts.Skipped = len(note.skips)
	return note
}

// headExcerpt quotes a header marker from the committed blob at head.
// A missing blob or a failed read leaves the quote empty; the warning
// still names the file from the recorded reason.
func (l *Leg) headExcerpt(ctx context.Context, head core.Revision, path string) string {
	body, err := l.blobAt(ctx, head, path)
	if err != nil {
		return ""
	}
	return intel.HeaderExcerpt(body)
}
