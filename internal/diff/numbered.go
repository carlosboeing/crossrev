package diff

import (
	"fmt"
	"strconv"
)

// gutterFloor is the narrowest the two number columns are ever printed
// (lib/diff.sh:179). Four holds a four-digit line number, which is more than
// most files reach, so the gutter reads as a column rather than as ragged text.
const gutterFloor = 4

// Numbered is the diff back with every hunk line prefixed by its old and its new
// line number, a dash standing in for the side that does not have one. Headers
// pass through bare so the gutter reads as a gutter rather than as part of the
// diff.
//
// This is what the review leg is given instead of the raw diff (lib/diff.sh:187-195).
// The number a finding must carry is now on the line the model is looking at,
// rather than something it has to derive by counting under a `@@` header, and
// the dash says which side that line can take a comment on.
//
// Every line ends with a newline, including the last one, because awk's `print`
// and `printf` wrote one there. An empty diff numbers to nothing at all rather
// than to a blank line.
func (d *Diff) Numbered() []byte {
	if len(d.lines) == 0 {
		return nil
	}

	w := gutterFloor
	if d.widestSet {
		if n := len(strconv.Itoa(d.widest)); n > w {
			w = n
		}
	}

	var out []byte
	for _, l := range d.lines {
		if l.header {
			out = append(out, l.raw...)
			out = append(out, '\n')
			continue
		}
		out = append(out, fmt.Sprintf("%*s %*s |%s\n",
			w, gutter(l.oldNo, l.hasOld),
			w, gutter(l.newNo, l.hasNew),
			l.raw)...)
	}
	return out
}

// gutter is one column of the number pair. A dash means the line does not exist
// on that side, so a comment cannot be anchored to it there.
func gutter(n int, has bool) string {
	if !has {
		return "-"
	}
	return strconv.Itoa(n)
}
