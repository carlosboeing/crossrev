package diff

import (
	"strconv"
	"strings"
)

// ClipHunks returns the diff with context lines farther than radius lines
// from any changed line dropped. Changed lines always stay; a context line
// stays when it sits within radius lines of a changed line on either side.
// Every kept run is re-emitted under its own corrected `@@` header, so the
// result re-parses with the numbers the reviewer will quote: a stale header
// over a gapped hunk would number every line past the gap wrong.
//
// A `-W` diff widens each hunk to its enclosing function, and a file with
// no function pattern widens to the whole file. The clip is what bounds
// either back to the window around the change. Sections left with no kept
// body line drop whole, as does anything before the first `diff --git`,
// which belongs to no file. A hunk header the parser cannot read is kept
// verbatim rather than dropped: malformed input shows too much, never too
// little.
func (d *Diff) ClipHunks(radius int) []byte {
	var out []byte
	for si, s := range d.sections {
		if s.pathA == "" && s.pathB == "" {
			continue
		}
		pro, hunks := splitSection(d, si)
		kept := append([]string(nil), pro...)
		for _, h := range hunks {
			kept = append(kept, h.clip(radius)...)
		}
		if len(kept) == len(pro) {
			continue
		}
		for _, rec := range kept {
			out = append(out, rec...)
			out = append(out, '\n')
		}
	}
	return out
}

// HasChanges reports whether the diff carries any added or removed line.
// A pure rename, a mode-only change or an empty-file addition parses to
// headers alone, and header-only input shapes header-only output.
func (d *Diff) HasChanges() bool {
	for _, l := range d.lines {
		if l.header {
			continue
		}
		if l.hasOld != l.hasNew {
			return true
		}
	}
	return false
}

// Headers returns the diff's header lines alone: the `diff --git`, index,
// mode, rename and side lines, the binary notice, and any hunk headers.
// Body lines never survive, so a file with nothing to number still shows
// what changed about it.
func (d *Diff) Headers() []byte {
	var out []byte
	for _, l := range d.lines {
		if !l.header {
			continue
		}
		out = append(out, l.raw...)
		out = append(out, '\n')
	}
	return out
}

// splitSection divides one file's records into its header prologue —
// everything up to the first `@@` — and its hunks, each header-led. A `\`
// no-newline annotation travels with the body it annotates, because it
// sits under its hunk's header rather than above it.
func splitSection(d *Diff, si int) (pro []string, hunks []rawHunk) {
	var cur *rawHunk
	for _, l := range d.lines {
		if l.section != si {
			continue
		}
		if l.header && strings.HasPrefix(l.raw, "@@") {
			hunks = append(hunks, rawHunk{})
			cur = &hunks[len(hunks)-1]
			cur.header = l.raw
			continue
		}
		if cur == nil {
			pro = append(pro, l.raw)
			continue
		}
		cur.body = append(cur.body, l.raw)
	}
	return pro, hunks
}

// rawHunk is one hunk header with the body records under it.
type rawHunk struct {
	header string
	body   []string
}

// cline is one hunk body record with its numbers read: a `\` annotation
// carries neither side, an addition or deletion carries one, and context
// carries both.
type cline struct {
	raw    string
	note   bool
	oldNo  int
	hasOld bool
	newNo  int
	hasNew bool
}

// clip answers the hunk's kept records: each kept run under its own
// corrected header, or the hunk verbatim when its header does not parse.
func (h rawHunk) clip(radius int) []string {
	oldStart, _, newStart, _, suffix, ok := splitHunkHeader(h.header)
	if !ok {
		out := make([]string, 0, len(h.body)+1)
		return append(append(out, h.header), h.body...)
	}
	lines := make([]cline, 0, len(h.body))
	o, n := oldStart, newStart
	for _, rec := range h.body {
		switch {
		case strings.HasPrefix(rec, `\`):
			lines = append(lines, cline{raw: rec, note: true})
		case strings.HasPrefix(rec, "+"):
			lines = append(lines, cline{raw: rec, newNo: n, hasNew: true})
			n = incr(n)
		case strings.HasPrefix(rec, "-"):
			lines = append(lines, cline{raw: rec, oldNo: o, hasOld: true})
			o = incr(o)
		default:
			lines = append(lines, cline{raw: rec, oldNo: o, hasOld: true, newNo: n, hasNew: true})
			o = incr(o)
			n = incr(n)
		}
	}

	keep := make([]bool, len(lines))
	for i, l := range lines {
		// An annotation stands or falls with the line it annotates: kept
		// when the previous record is kept, dropped with it otherwise, so
		// no `\` ever dangles under a dropped line.
		if l.note {
			keep[i] = i > 0 && keep[i-1]
			continue
		}
		if l.hasOld != l.hasNew {
			keep[i] = true
			continue
		}
		if !l.hasOld {
			continue
		}
		for _, m := range lines {
			if m.hasOld == m.hasNew {
				continue
			}
			if m.hasOld && distance(l.oldNo, m.oldNo) <= radius {
				keep[i] = true
				break
			}
			if m.hasNew && distance(l.newNo, m.newNo) <= radius {
				keep[i] = true
				break
			}
		}
	}

	// Emit maximal kept runs. The counters walk every line, kept or not,
	// so a run's header starts where the file actually is rather than
	// where the kept lines resume.
	var out []string
	o, n = oldStart, newStart
	var run []cline
	runO, runN := o, n
	runConsumedOld, runConsumedNew := false, false
	emit := func() {
		if len(run) == 0 {
			return
		}
		out = append(out, runHeader(run, runO, runN, oldStart, newStart, runConsumedOld, runConsumedNew, suffix))
		for _, l := range run {
			out = append(out, l.raw)
		}
		run = nil
	}
	for i, l := range lines {
		if !keep[i] {
			emit()
			advance(&o, &n, l)
			continue
		}
		if len(run) == 0 {
			runO, runN = o, n
			runConsumedOld = o != oldStart
			runConsumedNew = n != newStart
		}
		run = append(run, l)
		advance(&o, &n, l)
	}
	emit()
	return out
}

// advance moves the file counters past one body record. Notes annotate
// rather than number, so they move nothing.
func advance(o, n *int, l cline) {
	if l.note {
		return
	}
	if l.hasOld {
		*o = incr(*o)
	}
	if l.hasNew {
		*n = incr(*n)
	}
}

// runHeader writes one kept run's `@@` header with corrected starts and
// counts, carrying the original hunk's function suffix so the enclosing
// function stays named on every run. A run opening on the added side starts
// its old range on the last consumed old line — the line the insertion
// follows — or on the hunk's own old start when nothing was consumed yet,
// which is the new-file zero and the hunk-start insertion alike.
func runHeader(run []cline, runO, runN, oldStart, newStart int, consumedOld, consumedNew bool, suffix string) string {
	oldS := oldStart
	if first, ok := firstOld(run); ok {
		oldS = first
	} else if consumedOld {
		oldS = runO - 1
	}
	newS := newStart
	if first, ok := firstNew(run); ok {
		newS = first
	} else if consumedNew {
		newS = runN - 1
	}
	oldLen, newLen := 0, 0
	for _, l := range run {
		if l.hasOld {
			oldLen++
		}
		if l.hasNew {
			newLen++
		}
	}
	return "@@ -" + hunkCount(oldS, oldLen) + " +" + hunkCount(newS, newLen) + " @@" + suffix
}

func firstOld(run []cline) (int, bool) {
	for _, l := range run {
		if l.hasOld {
			return l.oldNo, true
		}
	}
	return 0, false
}

func firstNew(run []cline) (int, bool) {
	for _, l := range run {
		if l.hasNew {
			return l.newNo, true
		}
	}
	return 0, false
}

// splitHunkHeader reads `@@ -o[,ol] +n[,nl] @@ suffix`. A count of 1 is
// omitted rather than written, the way git writes it.
func splitHunkHeader(rec string) (oldStart, oldLen, newStart, newLen int, suffix string, ok bool) {
	if !strings.HasPrefix(rec, "@@") {
		return 0, 0, 0, 0, "", false
	}
	rest := rec[2:]
	end := strings.Index(rest, "@@")
	if end < 0 {
		return 0, 0, 0, 0, "", false
	}
	suffix = rest[end+2:]
	fields := strings.Fields(rest[:end])
	if len(fields) < 2 {
		return 0, 0, 0, 0, "", false
	}
	oldStart, oldLen, ok = hunkRange(fields[0], '-')
	if !ok {
		return 0, 0, 0, 0, "", false
	}
	newStart, newLen, ok = hunkRange(fields[1], '+')
	if !ok {
		return 0, 0, 0, 0, "", false
	}
	return oldStart, oldLen, newStart, newLen, suffix, true
}

// hunkRange reads one side of a hunk header: the sign, the start, and the
// count after the comma, defaulting to 1 when git omitted it.
func hunkRange(field string, sign byte) (start, length int, ok bool) {
	if len(field) < 1 || field[0] != sign {
		return 0, 0, false
	}
	s := field[1:]
	length = 1
	if i := strings.IndexByte(s, ','); i >= 0 {
		length = number(s[i+1:])
		s = s[:i]
	}
	return number(s), length, true
}

// hunkCount writes a hunk range the way git does: the count omitted when
// it is 1.
func hunkCount(start, length int) string {
	if length == 1 {
		return strconv.Itoa(start)
	}
	return strconv.Itoa(start) + "," + strconv.Itoa(length)
}
