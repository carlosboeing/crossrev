package diff

import (
	"strings"
	"testing"
)

// The frozen view. tests/capture-parity.sh:372 captured `diff_number` through a
// command substitution, which strips every terminal newline, so the fixture
// carries the view without its final one. The Go view keeps it, because awk's
// `print` ends every line (lib/diff.sh:181-183) and the prompt is assembled from
// those bytes.
func TestNumberedMatchesTheFrozenView(t *testing.T) {
	v := loadViews(t)
	got := string(Parse([]byte(v.Corpus), testRevisions(t)).Numbered())

	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("the numbered view does not end with a newline")
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Fatalf("the numbered view ends with more than one newline")
	}
	if trimmed := strings.TrimSuffix(got, "\n"); trimmed != v.DiffNumber {
		gl := strings.Split(trimmed, "\n")
		wl := strings.Split(v.DiffNumber, "\n")
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
}

// The gutter is what the review leg reads instead of counting under a `@@`
// header. lib/diff.sh:179-180 pads both columns to the widest line number, and
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
			name: "and it widens to the widest number",
			in:   "diff --git a/w.txt b/w.txt\n--- a/w.txt\n+++ b/w.txt\n@@ -99998,2 +99998,2 @@\n a\n b\n",
			want: "diff --git a/w.txt b/w.txt\n--- a/w.txt\n+++ b/w.txt\n@@ -99998,2 +99998,2 @@\n99998 99998 | a\n99999 99999 | b\n",
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
		// lib/diff.sh:151-152. `@@ -1 +1 @@` omits both counts.
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

// A header the parser cannot read is not refused. Every expectation here is the
// shipped Bash answer, captured from lib/diff.sh's own functions.
func TestNumberedOnMalformedAndTruncatedHunks(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "a truncated hunk numbers the lines it has",
			in:   "diff --git a/t.txt b/t.txt\n--- a/t.txt\n+++ b/t.txt\n@@ -1,3 +1,3 @@\n one\n",
			want: "diff --git a/t.txt b/t.txt\n--- a/t.txt\n+++ b/t.txt\n@@ -1,3 +1,3 @@\n   1    1 | one\n",
		},
		{
			name: "an unreadable hunk header renumbers from zero",
			in:   "diff --git a/m.txt b/m.txt\n--- a/m.txt\n+++ b/m.txt\n@@ garbage @@\n one\n+two\n-three\n",
			want: "diff --git a/m.txt b/m.txt\n--- a/m.txt\n+++ b/m.txt\n@@ garbage @@\n   0    0 | one\n   -    1 |+two\n   1    - |-three\n",
		},
		{
			name: "a bare @@ is still a hunk header",
			in:   "diff --git a/b.txt b/b.txt\n--- a/b.txt\n+++ b/b.txt\n@@\n one\n",
			want: "diff --git a/b.txt b/b.txt\n--- a/b.txt\n+++ b/b.txt\n@@\n   0    0 | one\n",
		},
		{
			name: "a header with no blank after @@ shifts the fields it reads",
			in:   "diff --git a/n.txt b/n.txt\n--- a/n.txt\n+++ b/n.txt\n@@-1,2 +1,2 @@\n one\n+two\n",
			want: "diff --git a/n.txt b/n.txt\n--- a/n.txt\n+++ b/n.txt\n@@-1,2 +1,2 @@\n   1    0 | one\n   -    1 |+two\n",
		},
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
