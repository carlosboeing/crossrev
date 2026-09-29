package diff

import (
	"github.com/carlosboeing/crossrev/internal/core"
)

// LineRanges answers the contiguous supplied spans each side of the diff
// shows, numbered as the gutter shows them: old-side numbers on the base
// side, new-side numbers on the head side. A removed line numbers only on
// the base side, an added line only on the head side, and a context line on
// both. Headers number nothing. Each side's numbers fold into contiguous
// runs, so a hunk reading lines 8 through 12 answers one span, and lines
// either side of an addition still read as one run when their numbers meet.
//
// The validator holds evidence spans inside these ranges; the ledger
// records them beside the supplied digest, so coverage counts the bytes the
// reviewer was actually shown rather than the whole file.
func (d *Diff) LineRanges() (base, head []core.LineSpan) {
	var oldNums, newNums []core.LineSpan
	for _, l := range d.lines {
		if l.header {
			continue
		}
		if l.hasOld {
			oldNums = append(oldNums, core.LineSpan{Start: l.oldNo, End: l.oldNo})
		}
		if l.hasNew {
			newNums = append(newNums, core.LineSpan{Start: l.newNo, End: l.newNo})
		}
	}
	return core.MergeLineSpans(oldNums), core.MergeLineSpans(newNums)
}
