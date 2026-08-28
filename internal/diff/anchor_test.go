package diff

import (
	"math"
	"strconv"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
)

func anchorString(d *Diff, path string, side core.Side, line, bound int) string {
	n, ok := d.Anchor(path, side, line, bound)
	if !ok {
		return ""
	}
	return strconv.Itoa(n)
}

// Every anchor query the Bash oracle recorded, including the three shapes
// lib/diff.sh singles out in its own comments (tests/capture-parity.sh:309-343).
func TestAnchorMatchesTheFrozenQueries(t *testing.T) {
	v := loadViews(t)
	d := Parse([]byte(v.Corpus), testRevisions(t))
	if len(v.Anchors) == 0 {
		t.Fatalf("the frozen oracle carries no anchor queries")
	}
	for _, c := range v.Anchors {
		t.Run(c.Name, func(t *testing.T) {
			side, err := core.ParseSide(c.Side)
			if err != nil {
				t.Fatalf("side %q: %v", c.Side, err)
			}
			if got := anchorString(d, c.Path, side, c.Line, c.Bound); got != c.Result {
				t.Fatalf("anchor(%q, %s, %d, %d) = %q, want %q",
					c.Path, c.Side, c.Line, c.Bound, got, c.Result)
			}
		})
	}
}

// lib/diff.sh:19-24. Three is the number of context lines git puts either side
// of a change, so it is exactly the margin a miscount lands in.
func TestDefaultSnapIsThreeLines(t *testing.T) {
	if DefaultSnap != 3 {
		t.Fatalf("DefaultSnap = %d, want 3", DefaultSnap)
	}
	d := parseCorpus(t)
	const path = "tools/crossrev/CHANGELOG.md"
	if got := anchorString(d, path, core.SideRight, 16, DefaultSnap); got != "13" {
		t.Fatalf("three lines away = %q, want 13", got)
	}
	if got := anchorString(d, path, core.SideRight, 17, DefaultSnap); got != "" {
		t.Fatalf("four lines away = %q, want nothing", got)
	}
}

// lib/diff.sh:139-142. Ties go to the earlier line: a finding names something at
// or after the number the reviewer gave more often than before it.
//
// The hunks are out of order on purpose. On an in-order diff the scan meets the
// lower number first and `dist < bestDist` alone gives the same answer, so the
// tie clause only bites when the higher number is seen first. The shell answers
// 10 for this diff too.
func TestAnchorTieGoesToTheEarlierLine(t *testing.T) {
	const in = "diff --git a/x b/x\n--- a/x\n+++ b/x\n" +
		"@@ -20,1 +20,1 @@\n twenty\n" +
		"@@ -10,1 +10,1 @@\n ten\n"
	d := Parse([]byte(in), testRevisions(t))
	if got := anchorString(d, "x", core.SideRight, 15, 10); got != "10" {
		t.Fatalf("an equidistant line anchored to %q, want 10", got)
	}
	if got := anchorString(parseCorpus(t), "tools/crossrev/CHANGELOG.md", core.SideRight, 24, 11); got != "13" {
		t.Fatalf("an equidistant line in the frozen corpus anchored to %q, want 13", got)
	}
}

// A line the asked-for side does not carry is not a candidate at all, however
// close its other side's number sits. An added line has no old number and a
// deleted line has no new one, and without those two guards a LEFT comment
// anchors onto a line that exists only on the right.
func TestAnchorSkipsALineTheSideDoesNotCarry(t *testing.T) {
	rev := testRevisions(t)
	created := Parse([]byte("diff --git a/c b/c\n--- /dev/null\n+++ b/c\n@@ -0,0 +1,2 @@\n+one\n+two\n"), rev)
	deleted := Parse([]byte("diff --git a/d b/d\n--- a/d\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-one\n-two\n"), rev)

	// Both added lines carry oldNo 0, so a LEFT query for 0 would match exactly
	// were the guard gone. The shell answers nothing.
	if got := anchorString(created, "c", core.SideLeft, 0, 3); got != "" {
		t.Fatalf("a created file anchored a left-side comment to %q, want nothing", got)
	}
	if got := anchorString(created, "c", core.SideRight, 1, 3); got != "1" {
		t.Fatalf("a created file anchored a right-side comment to %q, want 1", got)
	}
	// The mirror: both deleted lines carry newNo 0.
	if got := anchorString(deleted, "d", core.SideRight, 0, 3); got != "" {
		t.Fatalf("a deleted file anchored a right-side comment to %q, want nothing", got)
	}
	if got := anchorString(deleted, "d", core.SideLeft, 1, 3); got != "1" {
		t.Fatalf("a deleted file anchored a left-side comment to %q, want 1", got)
	}
}

// A want far outside the range of any line number must miss every line rather
// than match all of them. The subtraction overflows, and a wrapped difference
// reads as negative and so sits below every bound.
func TestAnchorOnAnAbsurdWant(t *testing.T) {
	rev := testRevisions(t)
	// An unreadable header numbers from zero, so `0 - math.MinInt64` is the
	// subtraction that wraps.
	zero := Parse([]byte("diff --git a/x.txt b/x.txt\n--- a/x.txt\n+++ b/x.txt\n@@ garbage @@\n a\n b\n"), rev)
	if got := anchorString(zero, "x.txt", core.SideRight, math.MinInt64, 3); got != "" {
		t.Fatalf("the most negative want anchored to %q, want nothing", got)
	}
	one := Parse([]byte("diff --git a/x.txt b/x.txt\n--- a/x.txt\n+++ b/x.txt\n@@ -1,2 +1,2 @@\n a\n b\n"), rev)
	if got := anchorString(one, "x.txt", core.SideRight, math.MinInt64+1, 3); got != "" {
		t.Fatalf("a want one above the floor anchored to %q, want nothing", got)
	}
	// The other end: a hunk start too wide for an int saturates, and the
	// saturated line number must still be a whole snap away from line zero.
	wide := Parse([]byte("diff --git a/x.txt b/x.txt\n--- a/x.txt\n+++ b/x.txt\n@@ -9223372036854775807,2 +1,2 @@\n a\n b\n"), rev)
	if got := anchorString(wide, "x.txt", core.SideLeft, 0, 0); got != "" {
		t.Fatalf("a saturated line number anchored to %q, want nothing", got)
	}
}

// lib/diff.sh:136-137. The two sides count separately, and a line the asked-for
// side does not carry is not a candidate at all.
func TestAnchorSidesCountSeparately(t *testing.T) {
	d := parseCorpus(t)
	const path = "tools/crossrev/CHANGELOG.md"
	cases := []struct {
		name string
		side core.Side
		line int
		want string
	}{
		{"a deleted line is anchorable on the left", core.SideLeft, 34, "34"},
		{"an added line's number is not a left-side line", core.SideLeft, 37, "35"},
		{"a deleted line's number is not a right-side line", core.SideRight, 33, "35"},
		{"the left side counts in the old file's numbering", core.SideLeft, 11, "11"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := anchorString(d, path, c.side, c.line, DefaultSnap); got != c.want {
				t.Fatalf("anchor = %q, want %q", got, c.want)
			}
		})
	}
}

// Every malformed anchor query the Bash oracle recorded, replayed against the
// malformed corpus it was recorded on.
func TestAnchorMatchesTheFrozenMalformedQueries(t *testing.T) {
	v := loadViews(t)
	d := parseMalformed(t)
	if len(v.Malformed.Anchors) == 0 {
		t.Fatalf("the frozen oracle carries no malformed anchor queries")
	}
	for _, c := range v.Malformed.Anchors {
		t.Run(c.Name, func(t *testing.T) {
			side, err := core.ParseSide(c.Side)
			if err != nil {
				t.Fatalf("side %q: %v", c.Side, err)
			}
			if got := anchorString(d, c.Path, side, c.Line, c.Bound); got != c.Result {
				t.Fatalf("anchor(%q, %s, %d, %d) = %q, want %q",
					c.Path, c.Side, c.Line, c.Bound, got, c.Result)
			}
		})
	}
}

// Shapes the frozen malformed corpus does not hold: it opens every section with
// a `diff --git` line, its one section without side lines carries no hunk, it
// has no /dev/null pair, no quoted path, and is never empty. Its unreadable
// header is queried only on the right. Every expectation here is still the
// shipped Bash answer rather than the answer that would be more useful.
func TestAnchorOnShapesTheFrozenCorpusDoesNotHold(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		path  string
		side  core.Side
		line  int
		bound int
		want  string
	}{
		{
			name: "an unreadable header anchors line zero on the left too",
			in:   "diff --git a/m.txt b/m.txt\n--- a/m.txt\n+++ b/m.txt\n@@ garbage @@\n one\n+two\n-three\n",
			path: "m.txt", side: core.SideLeft, line: 0, bound: 3, want: "0",
		},
		{
			name: "a diff with no `diff --git` header still anchors",
			in:   "--- a/h.txt\n+++ b/h.txt\n@@ -1,1 +1,1 @@\n one\n",
			path: "h.txt", side: core.SideRight, line: 1, bound: 3, want: "1",
		},
		{
			name: "a hunk with no side lines matches the empty path",
			in:   "diff --git a/z.txt b/z.txt\n@@ -1,1 +1,1 @@\n one\n",
			path: "", side: core.SideRight, line: 1, bound: 3, want: "1",
		},
		{
			name: "and so does a file that is /dev/null on both sides",
			in:   "diff --git a/d.txt b/d.txt\n--- /dev/null\n+++ /dev/null\n@@ -0,0 +1,1 @@\n+x\n",
			path: "", side: core.SideRight, line: 1, bound: 3, want: "1",
		},
		{
			name: "an empty diff anchors nothing",
			in:   "",
			path: "t.txt", side: core.SideRight, line: 1, bound: 3, want: "",
		},
		{
			name: "a quoted path's escapes are decoded before it is matched",
			in:   "diff --git a/q b/q\n--- \"a/we\\\"ird\\tname\"\n+++ \"b/we\\\"ird\\tname\"\n@@ -1,1 +1,1 @@\n x\n",
			path: "we\"ird\tname", side: core.SideRight, line: 1, bound: 3, want: "1",
		},
		{
			name: "and a trailing escaped backslash survives decoding",
			in:   "diff --git a/r b/r\n--- \"a/trail\\\\\"\n+++ \"b/trail\\\\\"\n@@ -1,1 +1,1 @@\n x\n",
			path: "trail\\", side: core.SideRight, line: 1, bound: 3, want: "1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Parse([]byte(c.in), testRevisions(t))
			if got := anchorString(d, c.path, c.side, c.line, c.bound); got != c.want {
				t.Fatalf("anchor = %q, want %q", got, c.want)
			}
		})
	}
}
