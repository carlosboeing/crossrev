package diff

import "github.com/carlosboeing/crossrev/internal/core"

// DefaultSnap is the default snap distance, in lines (lib/diff.sh:19-24).
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
// (lib/diff.sh:198-209).
//
// Not-found is a real answer rather than a failure. It means the finding has to
// go somewhere other than a line, and the caller has to say so.
//
// A path is matched against both sides of a section, so a rename is anchorable
// under either its old or its new name. An empty path matches a section whose
// side lines were absent or were /dev/null, which is what the shell does.
//
// bound has no default here, and a caller that wants the shell's must pass
// DefaultSnap. lib/diff.sh:207 applies it inside the boundary, with
// `bound="${5:-$CROSSREV_DIFF_SNAP}"`, and its one shipped caller omits the
// argument and relies on that. A Go caller that forgets it gets bound 0, which
// is exact-match-only: findings stop moving onto lines the diff does show, and
// nothing reports that they stopped.
//
// want is a line number, and the caller is expected to have one. A finding that
// reached here through the schema is bounded below by 1, but Anchor is exported
// and does not assume the schema ran, so a want anywhere in the int range is
// answered rather than refused — the distance to it saturates instead of
// wrapping, so an absurd want misses every line rather than matching all of them.
func (d *Diff) Anchor(path string, side core.Side, want, bound int) (int, bool) {
	best, bestDist := 0, 0
	found := false

	for _, l := range d.lines {
		// Redundant, and kept: a header carries neither side's number, so both
		// branches below would skip it anyway. It states what the awk states
		// structurally by calling offer() only from its three body rules
		// (lib/diff.sh:167-170), which one loop over every record cannot.
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

		dist := distance(v, want)

		// Ties go to the earlier line (lib/diff.sh:139-142). A finding names
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

// distance is |v - want|, saturating instead of wrapping.
//
// The awk subtracts in floating point (lib/diff.sh:138), where the gap between
// two line numbers cannot overflow. In an int it can, and the usual
// `if dist < 0 { dist = -dist }` makes that worse rather than better: negating
// the most negative int leaves it negative, and a negative distance then sits
// below every bound, so the widest possible miss reads as the closest possible
// hit. Saturating at maxInt keeps the comparison meaning what it says.
func distance(v, want int) int {
	if v >= want {
		if d := v - want; d >= 0 {
			return d
		}
		return maxInt
	}
	if d := want - v; d >= 0 {
		return d
	}
	return maxInt
}
