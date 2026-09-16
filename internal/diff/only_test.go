package diff

import (
	"strings"
	"testing"
)

// onlyCorpus builds a three-section diff with a rename, so the tests see
// exact matching on both sides of a section.
func onlyCorpus(t *testing.T) *Diff {
	t.Helper()
	var b strings.Builder
	for _, p := range []string{"docs/a.md", "src/x.ts"} {
		b.WriteString("diff --git a/" + p + " b/" + p + "\n")
		b.WriteString("--- a/" + p + "\n+++ b/" + p + "\n")
		b.WriteString("@@ -1,1 +1,2 @@\n kept\n+added\n")
	}
	b.WriteString("diff --git a/src/old.ts b/src/new.ts\n")
	b.WriteString("--- a/src/old.ts\n+++ b/src/new.ts\n")
	b.WriteString("@@ -1,1 +1,1 @@\n-old\n+new\n")
	return Parse([]byte(b.String()), testRevisions(t))
}

// Only is the batch prompt's slice: a batch carries its own files' hunks, so
// matching is by exact path — a directory prefix does not pull a sibling in
// the way Excluded's operator-supplied directories drop one — and either side
// of a rename keeps the section.
func TestOnlyKeepsExactlyTheNamedSections(t *testing.T) {
	d := onlyCorpus(t)
	cases := []struct {
		name  string
		paths []string
		want  []string
	}{
		{"one file keeps its own section", []string{"src/x.ts"}, []string{"src/x.ts"}},
		{"two files keep both in diff order", []string{"src/x.ts", "docs/a.md"}, []string{"docs/a.md", "src/x.ts"}},
		{"a directory prefix keeps nothing", []string{"docs"}, nil},
		{"the old side of a rename keeps it", []string{"src/old.ts"}, []string{"src/old.ts"}},
		{"the new side of a rename keeps it", []string{"src/new.ts"}, []string{"src/old.ts"}},
		{"an uninvolved path keeps nothing", []string{"README.md"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := paths(d.Only(c.paths))
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Fatalf("kept %v, want %v", got, c.want)
			}
		})
	}
}

// A batch with no paths renders no diff at all: the empty slice is not the
// verbatim short-circuit Excluded has, because keeping everything is how a
// diff past the budget prices a one-file batch out of one prompt.
func TestOnlyWithNoPathsRendersEmpty(t *testing.T) {
	d := onlyCorpus(t)
	if got := string(d.Only(nil)); got != "" {
		t.Fatalf("Only(nil) = %q, want nothing", got)
	}
	if got := string(d.Only([]string{})); got != "" {
		t.Fatalf("Only(empty) = %q, want nothing", got)
	}
	if got := string(Parse(nil, testRevisions(t)).Only([]string{"x"})); got != "" {
		t.Fatalf("Only on an empty diff = %q, want nothing", got)
	}
}

// Lines before the first `diff --git` belong to no file, so no batch keeps
// them; and like Excluded, every kept line ends with a newline whether the
// input's last line did or not.
func TestOnlyDropsThePreambleAndTerminatesLines(t *testing.T) {
	const in = "preamble line\ndiff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n k"
	d := Parse([]byte(in), testRevisions(t))
	want := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n k\n"
	if got := string(d.Only([]string{"x"})); got != want {
		t.Fatalf("Only = %q, want the section without the preamble and with a terminal newline", got)
	}
	if got := string(d.Only(nil)); got != "" {
		t.Fatalf("Only(nil) = %q, want nothing (the preamble belongs to no file)", got)
	}
}
