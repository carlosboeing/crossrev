package diff

import (
	"strings"
	"testing"
)

// Every exclusion the Bash oracle recorded. tests/capture-parity.sh:349 captured
// `diff_exclude` through a command substitution, so the frozen output has lost
// its terminal newline; the Go view keeps whatever awk's `print` produced.
func TestExcludedMatchesTheFrozenOutputs(t *testing.T) {
	v := loadViews(t)
	d := Parse([]byte(v.Corpus), testRevisions(t))
	if len(v.Excludes) == 0 {
		t.Fatalf("the frozen oracle carries no exclusion cases")
	}
	for _, c := range v.Excludes {
		t.Run(c.Name, func(t *testing.T) {
			got := string(d.Excluded(c.Exclusions))
			if trimmed := strings.TrimSuffix(got, "\n"); trimmed != c.Output {
				gl := strings.Split(trimmed, "\n")
				wl := strings.Split(c.Output, "\n")
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
				t.Fatalf("excluded output differs from the frozen one")
			}
		})
	}
}

// paths reads the `diff --git` headers back out, the way tests/test-diff.sh does.
func paths(out []byte) []string {
	var got []string
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "diff --git a/") {
			continue
		}
		rest := strings.TrimPrefix(line, "diff --git a/")
		if i := strings.Index(rest, " b/"); i >= 0 {
			rest = rest[:i]
		}
		got = append(got, rest)
	}
	return got
}

func excludeCorpus(t *testing.T) *Diff {
	t.Helper()
	var b strings.Builder
	for _, p := range []string{"docs/a.md", "docs/b.md", "docs/*.md", "src/x.ts"} {
		b.WriteString("diff --git a/" + p + " b/" + p + "\n")
		b.WriteString("--- a/" + p + "\n+++ b/" + p + "\n")
		b.WriteString("@@ -1,1 +1,2 @@\n kept\n+added\n")
	}
	return Parse([]byte(b.String()), testRevisions(t))
}

// lib/diff.sh:98-104 and lib/diff.sh:221-224. The paths are operator-supplied
// configuration and are compared literally: a pattern that looks like a glob
// names the one file actually called that, and matches nothing else.
func TestExcludedComparesPatternsLiterally(t *testing.T) {
	d := excludeCorpus(t)
	cases := []struct {
		name     string
		patterns []string
		want     []string
	}{
		{"a star matches only the file named with one",
			[]string{"docs/*.md"}, []string{"docs/a.md", "docs/b.md", "src/x.ts"}},
		{"a question mark matches nothing at all",
			[]string{"docs/?.md"}, []string{"docs/a.md", "docs/b.md", "docs/*.md", "src/x.ts"}},
		{"a directory drops everything inside it",
			[]string{"docs"}, []string{"src/x.ts"}},
		{"a trailing slash means the same directory",
			[]string{"docs/"}, []string{"src/x.ts"}},
		{"a slash on its own excludes nothing",
			[]string{"/"}, []string{"docs/a.md", "docs/b.md", "docs/*.md", "src/x.ts"}},
		{"an empty pattern beside a real one is dropped",
			[]string{"", "docs/a.md"}, []string{"docs/b.md", "docs/*.md", "src/x.ts"}},
		{"a pattern carrying a newline is two patterns",
			[]string{"docs/a.md\ndocs/b.md"}, []string{"docs/*.md", "src/x.ts"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := paths(d.Excluded(c.patterns))
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Fatalf("kept %v, want %v", got, c.want)
			}
		})
	}
}

// lib/diff.sh:229 short-circuits to `cat` when no path survives the empty-string
// filter, so the diff passes through byte for byte. Once awk runs, every line it
// keeps ends with a newline, whether the input's last line did or not.
func TestExcludedTerminalNewline(t *testing.T) {
	const in = "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n k"
	d := Parse([]byte(in), testRevisions(t))

	if got := string(d.Excluded(nil)); got != in {
		t.Fatalf("excluding nothing = %q, want the diff verbatim", got)
	}
	if got := string(d.Excluded([]string{""})); got != in {
		t.Fatalf("excluding only an empty path = %q, want the diff verbatim", got)
	}
	if got := string(d.Excluded([]string{"other"})); got != in+"\n" {
		t.Fatalf("excluding a path = %q, want the diff with a terminal newline", got)
	}
	if got := string(d.Excluded([]string{"x"})); got != "" {
		t.Fatalf("excluding the only file = %q, want nothing", got)
	}
}

// Either side of a section is checked, not the new one alone, so a rename out of
// an excluded directory is still dropped (lib/diff.sh:106-108).
func TestExcludedDropsEitherSideOfARename(t *testing.T) {
	d := parseCorpus(t)
	for _, p := range []string{"src/old_name.ts", "src/new_name.ts"} {
		t.Run(p, func(t *testing.T) {
			for _, kept := range paths(d.Excluded([]string{p})) {
				if kept == "src/old_name.ts" {
					t.Fatalf("excluding %q left the renamed section in place", p)
				}
			}
		})
	}
}

func TestExcludedOnAnEmptyDiff(t *testing.T) {
	d := Parse(nil, testRevisions(t))
	if got := string(d.Excluded([]string{"x"})); got != "" {
		t.Fatalf("excluded = %q, want nothing", got)
	}
	if got := string(d.Excluded(nil)); got != "" {
		t.Fatalf("excluded = %q, want nothing", got)
	}
}
