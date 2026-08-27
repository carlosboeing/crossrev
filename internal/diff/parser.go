package diff

import (
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// This file ports the state machine at lib/diff.sh:47-184. That awk program has
// one loop and three ways out of it, chosen by a `mode` variable. The Go port
// keeps the single loop and turns the three modes into three accessors over what
// the loop recorded: Numbered, Anchor and Excluded. Splitting the loop into three
// parsers would mean three places to keep the rule that a bare empty line inside
// a hunk is context, and the first one to drift silently renumbers everything
// after it.
//
// The awk ran under LC_ALL=C, so every offset, length and comparison counted
// bytes rather than characters. Every helper here works on bytes for the same
// reason: a path git escaped as octal is rebuilt one byte at a time, and a
// rune-oriented rebuild yields a two-byte character instead of the byte the path
// is made of.

// Diff is one parsed unified diff, together with the base and head it was
// produced from.
//
// Parse has no failure mode, which is why it returns no error. The awk it
// replaces refuses nothing: a hunk header it cannot read yields zero and the
// hunk renumbers from there, and a truncated hunk numbers the lines it actually
// has. Those answers are wrong in the sense that the diff was wrong, and
// reproducing them is the point of a parity port.
type Diff struct {
	revisions core.RevisionPair
	raw       []byte
	sections  []section
	lines     []line
	widest    int
	widestSet bool
}

// section is one file's worth of the diff: the lines from a `diff --git` header
// up to the next one, together with the two paths read off its `---` and `+++`
// lines.
//
// lib/diff.sh keeps those two paths in `path` and `bpath`, resets them on every
// `diff --git`, and reads them again when a body line is offered as an anchor
// candidate. Holding them per section is exactly equivalent, because the side
// lines are matched only outside a hunk (lib/diff.sh:159-160) and body lines
// only occur inside one, so no body line can see a different pair than the one
// its section ends with.
type section struct {
	pathA string
	pathB string
}

// line is one record of the diff. A header passes through bare; a body line
// carries the old and the new number, either of which may be absent.
type line struct {
	header  bool
	section int
	raw     string
	oldNo   int
	hasOld  bool
	newNo   int
	hasNew  bool
}

// Parse reads a unified diff once. The revision pair travels with the result
// rather than being re-read later: GitHub's diff endpoint answers for the moment
// of the call, so two reads of a moving pull request let a push between them
// validate lines from one revision and post comments against another.
func Parse(raw []byte, revisions core.RevisionPair) *Diff {
	d := &Diff{
		revisions: revisions,
		raw:       append([]byte(nil), raw...),
		sections:  []section{{}},
	}

	cur := 0
	inHunk := false
	oldNo, newNo := 0, 0

	// The cases below are in the awk program's own order, and each one ends the
	// record the way `next` does there. Reordering them changes the answer: the
	// `!inHunk` catch-all at lib/diff.sh:161 is what keeps a `\` line outside a
	// hunk from reaching the no-newline rule below it.
	for _, rec := range records(raw) {
		switch {
		// lib/diff.sh:145-149. The header only clears the previous file. Its own
		// paths are read off the two lines below it.
		case strings.HasPrefix(rec, "diff --git "):
			d.sections = append(d.sections, section{})
			cur = len(d.sections) - 1
			inHunk = false
			d.keepHeader(cur, rec)

		// lib/diff.sh:151-157. Hunk counts are omitted when they are 1, so only
		// the text up to the comma is read.
		case strings.HasPrefix(rec, "@@"):
			f := fields(rec)
			oldNo = hunkStart(field(f, 1))
			newNo = hunkStart(field(f, 2))
			inHunk = true
			d.keepHeader(cur, rec)

		// lib/diff.sh:158-160. `--- a/x` and `+++ b/x` are matched only outside a
		// hunk, so a deleted line whose own text starts with `--` is still read as
		// a deletion.
		case !inHunk && strings.HasPrefix(rec, "--- "):
			d.sections[cur].pathA = headerPath(rec[4:], "a/")
			d.keepHeader(cur, rec)

		case !inHunk && strings.HasPrefix(rec, "+++ "):
			d.sections[cur].pathB = headerPath(rec[4:], "b/")
			d.keepHeader(cur, rec)

		case !inHunk:
			d.keepHeader(cur, rec)

		// lib/diff.sh:163-164. "\ No newline at end of file" annotates the line
		// above and is not one.
		case strings.HasPrefix(rec, `\`):
			d.keepHeader(cur, rec)

		case strings.HasPrefix(rec, "+"):
			d.keepBody(cur, rec, 0, false, newNo, true)
			newNo++

		case strings.HasPrefix(rec, "-"):
			d.keepBody(cur, rec, oldNo, true, 0, false)
			oldNo++

		// lib/diff.sh:169-170. Context, including a blank line that lost its
		// leading space in transit.
		default:
			d.keepBody(cur, rec, oldNo, true, newNo, true)
			oldNo++
			newNo++
		}
	}

	return d
}

// Revisions is the base and head the diff was produced from.
func (d *Diff) Revisions() core.RevisionPair { return d.revisions }

func (d *Diff) keepHeader(sec int, raw string) {
	d.lines = append(d.lines, line{header: true, section: sec, raw: raw})
}

func (d *Diff) keepBody(sec int, raw string, oldNo int, hasOld bool, newNo int, hasNew bool) {
	d.lines = append(d.lines, line{
		section: sec, raw: raw,
		oldNo: oldNo, hasOld: hasOld,
		newNo: newNo, hasNew: hasNew,
	})
	d.widen(oldNo, hasOld)
	d.widen(newNo, hasNew)
}

// widen tracks the widest line number the gutter has to hold. lib/diff.sh:139-140
// starts from an unset variable, which awk reads as zero in a numeric compare, so
// a diff whose only numbers are zero leaves it unset.
func (d *Diff) widen(n int, has bool) {
	if !has {
		return
	}
	if !d.widestSet {
		if n > 0 {
			d.widest, d.widestSet = n, true
		}
		return
	}
	if n > d.widest {
		d.widest = n
	}
}

// records splits the input the way awk splits records. A file with no terminal
// newline still yields its last partial line, and a file that is empty yields
// nothing at all, which is the `[[ -s "$file" ]]` guard at lib/diff.sh:32.
func records(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	s := string(raw)
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// fields splits a record the way awk's default field separator does: on runs of
// blanks, with leading and trailing ones discarded.
func fields(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n'
	})
}

func field(f []string, i int) string {
	if i < len(f) {
		return f[i]
	}
	return ""
}

// hunkStart reads one side of a `@@` header. lib/diff.sh:153-154 drops the
// leading `-` or `+` and then everything from the first comma, because a count of
// 1 is omitted rather than written.
//
// The result is an integer because git writes integers. awk would carry a
// fractional value through, but no git-produced header holds one.
func hunkStart(f string) int {
	s := awkSubstr(f, 2, len(f))
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	return number(s)
}

// number is awk's `s + 0`: the longest numeric prefix, or zero when there is
// none. A header the parser cannot read therefore yields zero rather than an
// error, and the hunk renumbers from there.
func number(s string) int {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	n, digits := 0, 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int(s[i]-'0')
		i++
		digits++
	}
	if digits == 0 {
		return 0
	}
	if neg {
		return -n
	}
	return n
}

// awkSubstr is awk's substr: 1-based, byte-oriented, and empty rather than out
// of range when the arguments run past the string.
func awkSubstr(s string, start, length int) string {
	if length <= 0 {
		return ""
	}
	if start < 1 {
		length += start - 1
		start = 1
		if length <= 0 {
			return ""
		}
	}
	if start > len(s) {
		return ""
	}
	end := start - 1 + length
	if end > len(s) {
		end = len(s)
	}
	return s[start-1 : end]
}

// headerPath reads one path off a `---` or `+++` line, decoded and with its side
// prefix off (lib/diff.sh:88-96).
//
// The `diff --git` header carries two paths with no separator that cannot appear
// inside a name, so `diff --git a/my file.md b/my file.md` is genuinely
// ambiguous and reading fields off it makes `my` and `file.md` out of one name.
// A trailing tab is git marking a name it could not otherwise delimit, and never
// part of the name.
func headerPath(rest, prefix string) string {
	if i := strings.IndexByte(rest, '\t'); i >= 0 {
		rest = rest[:i]
	}
	p := unquote(rest)
	if p == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(p, prefix)
}

// unquote undoes git's C-style quoting (lib/diff.sh:61-84): the whole path
// wrapped in double quotes, with \\ and \" escaped, the usual control-character
// escapes, and \### octal for every byte outside printable ASCII. GitHub anchors
// a comment to the raw path, so the quoting is decoded rather than compared
// against.
//
// A string that opens with a quote loses its last byte whether or not that byte
// is the closing quote, because the awk takes substr(s, 2, length(s) - 2)
// without checking. That is reproduced rather than corrected.
func unquote(s string) string {
	if !strings.HasPrefix(s, `"`) {
		return s
	}
	s = awkSubstr(s, 2, len(s)-2)

	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out = append(out, s[i])
			continue
		}

		var e byte
		hasE := i+1 < len(s)
		if hasE {
			e = s[i+1]
		}

		if hasE && e >= '0' && e <= '7' {
			n := 0
			for j := 0; j < 3 && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '7'; j++ {
				n = n*8 + int(s[i+1]-'0')
				i++
			}
			// awk's sprintf("%c", n) under LC_ALL=C writes one byte. Three octal
			// digits can name 511, which git never emits and which truncates the
			// same way the C conversion does.
			out = append(out, byte(n))
			continue
		}

		i++
		switch {
		case !hasE:
			// A trailing backslash names nothing and appends nothing.
		case e == 'a':
			out = append(out, 7)
		case e == 'b':
			out = append(out, 8)
		case e == 't':
			out = append(out, 9)
		case e == 'n':
			out = append(out, 10)
		case e == 'v':
			out = append(out, 11)
		case e == 'f':
			out = append(out, 12)
		case e == 'r':
			out = append(out, 13)
		default:
			out = append(out, e)
		}
	}
	return string(out)
}
