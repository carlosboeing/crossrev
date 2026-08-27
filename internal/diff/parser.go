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
//
// Bytes, but not byte for byte on every input. Three answers differ from the
// awk's, and each is noted where it arises: a hunk start written in hex or with
// an exponent or a fraction (hunkStart), a start too wide for an int (number),
// and a NUL byte in a body line. The awk reads a record up to its first NUL and
// prints the truncated line; Go keeps the whole line. Git never emits that,
// because a file holding a NUL is declared binary and gets no body lines at all,
// so that third one is unreachable through the shipped path.

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
	// record the way `next` does there. Two boundaries in that order are
	// load-bearing. The `!inHunk` catch-all (lib/diff.sh:163) sits above the
	// `+`, `-` and context cases, so a `--- ` or `+++ ` line outside a hunk is a
	// header rather than a deletion or an addition. The `\` case
	// (lib/diff.sh:165) sits above them too, so a no-newline annotation inside a
	// hunk does not number as a context line. The two side-line cases are not
	// such a pair: both keep a header with the same arguments, so swapping those
	// two changes nothing.
	for _, rec := range records(raw) {
		switch {
		// lib/diff.sh:145-151. The header only clears the previous file. Its own
		// paths are read off the two lines below it.
		case strings.HasPrefix(rec, "diff --git "):
			d.sections = append(d.sections, section{})
			cur = len(d.sections) - 1
			inHunk = false
			d.keepHeader(cur, rec)

		// lib/diff.sh:152-158. Hunk counts are omitted when they are 1, so only
		// the text up to the comma is read.
		case strings.HasPrefix(rec, "@@"):
			f := fields(rec)
			oldNo = hunkStart(field(f, 1))
			newNo = hunkStart(field(f, 2))
			inHunk = true
			d.keepHeader(cur, rec)

		// lib/diff.sh:159-162. `--- a/x` and `+++ b/x` are matched only outside a
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

		// lib/diff.sh:164-165. "\ No newline at end of file" annotates the line
		// above and is not one.
		case strings.HasPrefix(rec, `\`):
			d.keepHeader(cur, rec)

		case strings.HasPrefix(rec, "+"):
			d.keepBody(cur, rec, 0, false, newNo, true)
			newNo = incr(newNo)

		case strings.HasPrefix(rec, "-"):
			d.keepBody(cur, rec, oldNo, true, 0, false)
			oldNo = incr(oldNo)

		// lib/diff.sh:169-170. Context, including a blank line that lost its
		// leading space in transit.
		default:
			d.keepBody(cur, rec, oldNo, true, newNo, true)
			oldNo = incr(oldNo)
			newNo = incr(newNo)
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

// widen tracks the widest line number the gutter has to hold (lib/diff.sh:129-130).
//
// The awk starts from an unset variable, which it reads as zero in a numeric
// compare, so this starts from a zero that is never assigned either: no number
// below one can raise it, and a negative start (see number) cannot lower it. The
// two states the awk tells apart, unset and zero, both print in a column one
// character wide, so Numbered's floor of four swallows the difference. A
// widestSet flag beside this could therefore never change an answer, and there
// is none.
func (d *Diff) widen(n int, has bool) {
	if has && n > d.widest {
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

// hunkStart reads one side of a `@@` header. lib/diff.sh:154-155 drops the
// leading `-` or `+` and then everything from the first comma, because a count of
// 1 is omitted rather than written.
//
// The comma cut is belt and braces: number already stops at the first byte that
// is not a digit, so `number("6,6")` is 6 with or without it. It stays because
// the awk carries the same redundancy in its own `sub(/,.*/, "", o)`, and a
// parity port that quietly tidies one of a matched pair leaves two programs that
// have to be read side by side before either can be trusted.
//
// The result is an integer because git writes integers. Three forms the awk
// reads as numbers do not survive that, and none of them can arrive from git:
// `-1e3` is 1000 there and 1 here, `-1.9` is 1.9 there and 1 here, and `-0x10`
// is 16 there and 0 here. The first two are POSIX strtod, which any awk does.
// The hex is specific to the one-true-awk this ships against; gawk in POSIX mode
// reads it as 0, the way Go does.
func hunkStart(f string) int {
	s := awkSubstr(f, 2, len(f))
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	return number(s)
}

// maxInt is the largest value a line number can hold. The awk counts in floating
// point and so has no wrap to reproduce, which is what the saturation in number
// and in incr stands in for.
const maxInt = int(^uint(0) >> 1)

// incr is `++` with the overflow taken out. The awk's `oldno++` on a line number
// already past 2^53 adds nothing, because the double holding it has no room for
// the one. An int wraps instead, turning the second line of such a hunk into a
// negative line number and handing that to a GitHub comment call. Stopping at
// maxInt is one short of what the awk prints and on the same side of zero, which
// the wrap was not.
func incr(n int) int {
	if n == maxInt {
		return n
	}
	return n + 1
}

// number is awk's `s + 0`: the longest numeric prefix, or zero when there is
// none. A header the parser cannot read therefore yields zero rather than an
// error, and the hunk renumbers from there.
//
// A prefix too wide for an int saturates rather than wrapping. The awk holds it
// as a double and saturates in its own way: a twenty-digit start prints as
// 100000000000000000000 and repeats unchanged on every line of the hunk, because
// adding one to that double changes nothing. Go cannot print that from an int,
// so it prints maxInt and repeats that instead. Wrong either way, but wrong as a
// positive number of the right order rather than as the arbitrary
// 7766279631452241919 a wrap produces, and monotone enough that the gutter width
// and the anchor distances stay meaningful.
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
		d := int(s[i] - '0')
		if n > (maxInt-d)/10 {
			n = maxInt
		} else {
			n = n*10 + d
		}
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
//
// Both callers pass a start of 2, so there is no clamp here for a start below 1:
// that would be code no input can reach. The guard for a start past the end is
// reached, by a one-byte field — `@@ - + @@` offers `-` to hunkStart, where awk
// answers "" and a slice would panic.
func awkSubstr(s string, start, length int) string {
	if length <= 0 {
		return ""
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
// prefix off (lib/diff.sh:87-101).
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

// unquote undoes git's C-style quoting (lib/diff.sh:60-86): the whole path
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
