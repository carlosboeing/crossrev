package diff

import (
	"strings"
	"testing"
)

// assertFrozenNumbered compares a numbered view against a frozen one.
// tests/capture-parity.sh:447 and :445 captured `diff_number` through a command
// substitution, which strips every terminal newline, so the fixture carries each
// view without its final one. The Go view keeps it, because awk's `print` ends
// every line (lib/diff.sh:181-183) and the prompt is assembled from those bytes.
func assertFrozenNumbered(t *testing.T, got, want string) {
	t.Helper()
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("the numbered view does not end with a newline")
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Fatalf("the numbered view ends with more than one newline")
	}
	trimmed := strings.TrimSuffix(got, "\n")
	if trimmed == want {
		return
	}
	gl := strings.Split(trimmed, "\n")
	wl := strings.Split(want, "\n")
	for i := 0; i < len(gl) || i < len(wl); i++ {
		var g, w string
		if i < len(gl) {
			g = gl[i]
		}
		if i < len(wl) {
			w = wl[i]
		}
		if g != w {
			t.Fatalf("line %d:\n got %q\nwant %q", i+1, g, w)
		}
	}
	t.Fatalf("numbered view differs from the frozen one")
}

func TestNumberedMatchesTheFrozenView(t *testing.T) {
	v := loadViews(t)
	assertFrozenNumbered(t, string(Parse([]byte(v.Corpus), testRevisions(t)).Numbered()), v.DiffNumber)
}

// The malformed corpus, numbered. A truncated hunk, an unreadable header, a
// header with no space after the at-at, a bare `@@`, a section with no side
// lines, and a `---` line inside a hunk. Every expectation is the shipped Bash
// answer because it was captured from it, not written from a reading of it.
func TestNumberedMatchesTheFrozenMalformedView(t *testing.T) {
	v := loadViews(t)
	assertFrozenNumbered(t, string(parseMalformed(t).Numbered()), v.Malformed.DiffNumber)
}

// The gutter is what the review leg reads instead of counting under a `@@`
// header. lib/diff.sh:178-179 pads both columns to the widest line number, and
// never below four.
func TestNumberedGutterWidth(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "four columns is the floor",
			in:   "diff --git a/t.txt b/t.txt\n--- a/t.txt\n+++ b/t.txt\n@@ -1,1 +1,1 @@\n a\n",
			want: "diff --git a/t.txt b/t.txt\n--- a/t.txt\n+++ b/t.txt\n@@ -1,1 +1,1 @@\n   1    1 | a\n",
		},
		{
			// The hunk crosses from four digits to five, so the first two lines
			// are padded to a width only the third one needs. Numbers of one
			// width would leave this green with the widening deleted, because
			// `%*s` never truncates.
			name: "and it widens to the widest number",
			in:   "diff --git a/w.txt b/w.txt\n--- a/w.txt\n+++ b/w.txt\n@@ -9998,3 +9998,3 @@\n a\n b\n c\n",
			want: "diff --git a/w.txt b/w.txt\n--- a/w.txt\n+++ b/w.txt\n@@ -9998,3 +9998,3 @@\n 9998  9998 | a\n 9999  9999 | b\n10000 10000 | c\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(Parse([]byte(c.in), testRevisions(t)).Numbered()); got != c.want {
				t.Fatalf("numbered =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

// A hunk start too wide for an int saturates, and the increment onto the next
// line saturates with it. This is the one shape where the gutter deliberately
// does not match the shell's bytes, so the numbers below are Go's and the awk's
// are named beside them.
//
// The awk counts in floating point. A start of 9223372036854775807 is held as
// the double 9223372036854775808 and `oldno++` on it adds nothing, so the shell
// prints that same value on both lines. Go stops at maxInt and repeats that
// instead: one less, the same 19 columns wide, and on the same side of zero. An
// int that wrapped would print -9223372036854775808 on the second line and hand
// a negative line number to a GitHub comment call.
func TestNumberedSaturatesRatherThanWrapping(t *testing.T) {
	const in = "diff --git a/x.txt b/x.txt\n--- a/x.txt\n+++ b/x.txt\n" +
		"@@ -9223372036854775807,2 +1,2 @@\n a\n b\n"
	got := string(Parse([]byte(in), testRevisions(t)).Numbered())
	for _, want := range []string{
		"9223372036854775807                   1 | a\n",
		"9223372036854775807                   2 | b\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("the numbered view does not contain %q\ngot:\n%s", want, got)
		}
	}
	if strings.Contains(got, "-9223372036854775808") {
		t.Fatalf("a line number wrapped past the end of the int range:\n%s", got)
	}
}

// lib/diff.sh:32 returns before awk runs when the file is empty, so the view is
// nothing rather than a blank line or an error.
func TestNumberedOnADegenerateDiff(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"an empty diff numbers to nothing", "", ""},
		{"a lone newline is one bare line", "\n", "\n"},
		{
			"a diff with no terminal newline gains one",
			"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n k",
			"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n   1    1 | k\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(Parse([]byte(c.in), testRevisions(t)).Numbered()); got != c.want {
				t.Fatalf("numbered = %q, want %q", got, c.want)
			}
		})
	}
}

// The three shapes lib/diff.sh singles out in its own comments, read off the
// frozen corpus rather than a diff written for the test.
func TestNumberedCoversTheThreeCalledOutShapes(t *testing.T) {
	view := string(parseCorpus(t).Numbered())
	cases := []struct{ name, want string }{
		// lib/diff.sh:152. `@@ -1 +1 @@` omits both counts.
		{"a hunk header with counts omitted still numbers from one",
			"   1    - |-const only = 1;"},
		{"and its added side too",
			"   -    1 |+const only = 2;"},
		// lib/diff.sh:159-160. `--- ` and `+++ ` are matched only outside a hunk.
		{"a removed line whose own text opens with two dashes is a deletion",
			"  11    - |--- a removed line whose own text starts with two dashes"},
		{"and an added line opening with two pluses is an addition",
			"   -   11 |+++ an added line whose own text starts with two pluses"},
		// lib/diff.sh:164. The no-newline annotation is not a line of the file.
		{"the no-newline annotation carries no gutter",
			"\n\\ No newline at end of file\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(view, c.want) {
				t.Fatalf("the numbered view does not contain %q", c.want)
			}
		})
	}
}

// Two malformed shapes the frozen corpus does not hold: it opens every section
// with a `diff --git` line, and its one section without side lines has no hunk
// under it either. Everything else malformed is read off the fixture by
// TestNumberedMatchesTheFrozenMalformedView.
func TestNumberedOnShapesTheFrozenCorpusDoesNotHold(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "a diff with no `diff --git` header still numbers",
			in:   "--- a/h.txt\n+++ b/h.txt\n@@ -1,1 +1,1 @@\n one\n",
			want: "--- a/h.txt\n+++ b/h.txt\n@@ -1,1 +1,1 @@\n   1    1 | one\n",
		},
		{
			name: "a hunk with no side lines before it still numbers",
			in:   "diff --git a/z.txt b/z.txt\n@@ -1,1 +1,1 @@\n one\n",
			want: "diff --git a/z.txt b/z.txt\n@@ -1,1 +1,1 @@\n   1    1 | one\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(Parse([]byte(c.in), testRevisions(t)).Numbered()); got != c.want {
				t.Fatalf("numbered =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}
