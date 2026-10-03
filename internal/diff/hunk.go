package diff

import (
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// Hunk answers the hunk of path's diff holding line on side: its `@@`
// header with the body lines under it. When no hunk holds the line, the
// nearest hunk of the same file answers instead, so a file-level anchor
// still reads its closest change. False when the file carries no hunk
// with a number on that side at all.
//
// The cross-model check reads its in-scope excerpts through this: the
// checker judges the hunk the finding faults rather than the whole file.
func (d *Diff) Hunk(path string, side core.Side, line int) ([]byte, bool) {
	type hunk struct {
		header string
		body   []string
		lo, hi int
		has    bool
	}
	var hunks []hunk
	for _, l := range d.lines {
		s := d.sections[l.section]
		if s.pathA != path && s.pathB != path {
			continue
		}
		if l.header {
			// A hunk starts at its `@@` header the way splitSection
			// groups it; any other header under it annotates the body
			// it follows.
			if strings.HasPrefix(l.raw, "@@") {
				hunks = append(hunks, hunk{header: l.raw})
			} else if len(hunks) > 0 {
				last := &hunks[len(hunks)-1]
				last.body = append(last.body, l.raw)
			}
			continue
		}
		if len(hunks) == 0 {
			continue
		}
		last := &hunks[len(hunks)-1]
		last.body = append(last.body, l.raw)
		no, has := l.oldNo, l.hasOld
		if side == core.SideRight {
			no, has = l.newNo, l.hasNew
		}
		if !has {
			continue
		}
		if !last.has || no < last.lo {
			last.lo = no
		}
		if !last.has || no > last.hi {
			last.hi = no
		}
		last.has = true
	}
	best := -1
	bestDist := 0
	for i, h := range hunks {
		if !h.has {
			continue
		}
		if line >= h.lo && line <= h.hi {
			best = i
			break
		}
		dist := h.lo - line
		if line > h.hi {
			dist = line - h.hi
		}
		if best == -1 || dist < bestDist {
			best, bestDist = i, dist
		}
	}
	if best == -1 {
		return nil, false
	}
	winner := hunks[best]
	out := []byte(winner.header + "\n")
	for _, rec := range winner.body {
		out = append(out, rec...)
		out = append(out, '\n')
	}
	return out, true
}
