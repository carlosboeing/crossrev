package diff

import (
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
// lib/diff.sh singles out in its own comments (tests/capture-parity.sh:326-338).
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

// lib/diff.sh:21-25. Three is the number of context lines git puts either side
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

// lib/diff.sh:126-128. Ties go to the earlier line: a finding names something
// at or after the number the reviewer gave more often than before it.
func TestAnchorTieGoesToTheEarlierLine(t *testing.T) {
	d := parseCorpus(t)
	if got := anchorString(d, "tools/crossrev/CHANGELOG.md", core.SideRight, 24, 11); got != "13" {
		t.Fatalf("an equidistant line anchored to %q, want 13", got)
	}
}

// lib/diff.sh:120-122. The two sides count separately, and a line the asked-for
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

// Malformed and truncated input anchors to whatever the state machine counted.
// Every expectation is the shipped Bash answer, captured from lib/diff.sh's own
// functions, rather than the answer that would be more useful.
func TestAnchorOnMalformedAndTruncatedHunks(t *testing.T) {
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
			name: "a truncated hunk anchors the line it has",
			in:   "diff --git a/t.txt b/t.txt\n--- a/t.txt\n+++ b/t.txt\n@@ -1,3 +1,3 @@\n one\n",
			path: "t.txt", side: core.SideRight, line: 1, bound: 3, want: "1",
		},
		{
			name: "and the counts it promised do not extend the snap",
			in:   "diff --git a/t.txt b/t.txt\n--- a/t.txt\n+++ b/t.txt\n@@ -1,3 +1,3 @@\n one\n",
			path: "t.txt", side: core.SideRight, line: 5, bound: 3, want: "",
		},
		{
			name: "an unreadable header anchors line zero on the right",
			in:   "diff --git a/m.txt b/m.txt\n--- a/m.txt\n+++ b/m.txt\n@@ garbage @@\n one\n+two\n-three\n",
			path: "m.txt", side: core.SideRight, line: 0, bound: 3, want: "0",
		},
		{
			name: "and on the left",
			in:   "diff --git a/m.txt b/m.txt\n--- a/m.txt\n+++ b/m.txt\n@@ garbage @@\n one\n+two\n-three\n",
			path: "m.txt", side: core.SideLeft, line: 0, bound: 3, want: "0",
		},
		{
			name: "a header with no blank after @@ still anchors",
			in:   "diff --git a/n.txt b/n.txt\n--- a/n.txt\n+++ b/n.txt\n@@-1,2 +1,2 @@\n one\n+two\n",
			path: "n.txt", side: core.SideRight, line: 1, bound: 3, want: "1",
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
