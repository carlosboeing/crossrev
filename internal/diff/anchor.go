package diff

import "github.com/carlosboeing/crossrev/internal/core"

// DefaultSnap is the default snap distance, in lines (lib/diff.sh:21-25).
//
// Three is the number of context lines git puts either side of a change, so it
// is exactly the margin a miscount lands in: the reviewer read the right hunk
// and fell off its end. Past that the reviewer meant somewhere else, and moving
// the comment would anchor it to code the finding never mentions, which is worse
// than not anchoring it at all.
const DefaultSnap = 3

// Anchor is the line to actually post on: the one asked for when the diff shows
// it, the nearest one it does show when that is within bound, and nothing at all
// when the file is absent from the diff or the miss is too wide to repair
// (lib/diff.sh:197-211).
//
// Not-found is a real answer rather than a failure. It means the finding has to
// go somewhere other than a line, and the caller has to say so.
//
// A path is matched against both sides of a section, so a rename is anchorable
// under either its old or its new name. An empty path matches a section whose
// side lines were absent or were /dev/null, which is what the shell does.
func (d *Diff) Anchor(path string, side core.Side, want, bound int) (int, bool) {
	best, bestDist := 0, 0
	found := false

	for _, l := range d.lines {
		if l.header {
			continue
		}
		s := d.sections[l.section]
		if s.pathA != path && s.pathB != path {
			continue
		}

		// The two sides count separately. A parser that pooled the columns would
		// accept either number on either side and anchor half of them to the
		// wrong line. Anything that is not LEFT reads the new side, as the shell's
		// ternary does.
		var v int
		if side == core.SideLeft {
			if !l.hasOld {
				continue
			}
			v = l.oldNo
		} else {
			if !l.hasNew {
				continue
			}
			v = l.newNo
		}

		dist := v - want
		if dist < 0 {
			dist = -dist
		}

		// Ties go to the earlier line (lib/diff.sh:126-128). A finding names
		// something at or after the number the reviewer gave more often than
		// before it, so on a tie the lower number is the better guess at what they
		// were looking at.
		if !found || dist < bestDist || (dist == bestDist && v < best) {
			best, bestDist, found = v, dist, true
		}
	}

	if found && bestDist <= bound {
		return best, true
	}
	return 0, false
}
