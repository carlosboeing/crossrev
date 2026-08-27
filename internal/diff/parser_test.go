package diff

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
)

// fixturePath is the single package-relative route to the frozen Bash oracle.
// Go reads it and never writes it: a test that can regenerate its own oracle
// is not an oracle.
const fixturePath = "../../tests/fixtures/parity/diff_views.json"

type anchorCase struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Side   string `json:"side"`
	Line   int    `json:"line"`
	Bound  int    `json:"bound"`
	Result string `json:"result"`
}

type excludeCase struct {
	Name       string   `json:"name"`
	Exclusions []string `json:"exclusions"`
	Output     string   `json:"output"`
}

// malformedViews is the second frozen corpus: shapes git does not produce and
// the awk still has to answer for. It is captured beside the well-formed one and
// read through the same constant, so a malformed expectation is the shell's
// answer rather than the port author's reading of the shell.
type malformedViews struct {
	Corpus     string       `json:"corpus"`
	DiffNumber string       `json:"diff_number"`
	Anchors    []anchorCase `json:"anchors"`
}

type diffViews struct {
	Corpus     string         `json:"corpus"`
	DiffNumber string         `json:"diff_number"`
	Anchors    []anchorCase   `json:"anchors"`
	Excludes   []excludeCase  `json:"excludes"`
	Malformed  malformedViews `json:"malformed"`
}

func loadViews(t *testing.T) diffViews {
	t.Helper()
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", fixturePath, err)
	}
	var v diffViews
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", fixturePath, err)
	}
	if v.Corpus == "" || v.DiffNumber == "" {
		t.Fatalf("%s carries no corpus or no numbered view", fixturePath)
	}
	if v.Malformed.Corpus == "" || v.Malformed.DiffNumber == "" {
		t.Fatalf("%s carries no malformed corpus or no numbered view for it", fixturePath)
	}
	return v
}

// parseMalformed is the malformed corpus parsed, the way parseCorpus is the
// well-formed one.
func parseMalformed(t *testing.T) *Diff {
	t.Helper()
	return Parse([]byte(loadViews(t).Malformed.Corpus), testRevisions(t))
}

// testRevisions is a plausible base/head pair. The three views do not read it;
// it travels with the diff so a later caller cannot re-derive the pair from a
// moving pull request.
func testRevisions(t *testing.T) core.RevisionPair {
	t.Helper()
	base, err := core.NewRevision("1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("base revision: %v", err)
	}
	head, err := core.NewRevision("2222222222222222222222222222222222222222")
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}
	return core.RevisionPair{Base: base, Head: head}
}

func parseCorpus(t *testing.T) *Diff {
	t.Helper()
	return Parse([]byte(loadViews(t).Corpus), testRevisions(t))
}

// Parse copies the bytes it is given. The caller owns that buffer and may reuse
// it — an io.Reader loop hands the same one back on the next read — and the
// verbatim path through Excluded returns d.raw, so a diff parsed from a shared
// buffer would answer with whatever landed in it since.
func TestParseCopiesTheRawDiff(t *testing.T) {
	const in = "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n k"
	buf := []byte(in)
	d := Parse(buf, testRevisions(t))
	for i := range buf {
		buf[i] = 'Z'
	}
	if got := string(d.Excluded(nil)); got != in {
		t.Fatalf("after overwriting the caller's buffer, excluded = %q, want %q", got, in)
	}
}

func TestParseCarriesTheRevisionPair(t *testing.T) {
	want := testRevisions(t)
	got := Parse([]byte("diff --git a/x b/x\n"), want).Revisions()
	if !got.Equal(want) {
		t.Fatalf("revisions = %v, want %v", got, want)
	}
}

// The `---` and `+++` lines carry one path each and are the only ones read.
// lib/diff.sh:87-101 decodes git's C-style quoting, drops a trailing tab, maps
// /dev/null to no path at all, and strips the a/ or b/ prefix last.
func TestParseReadsPathsOffTheSideLines(t *testing.T) {
	d := parseCorpus(t)
	want := []struct{ a, b string }{
		{"", ""}, // the implicit section before the first `diff --git`
		{"tools/crossrev/CHANGELOG.md", "tools/crossrev/CHANGELOG.md"},
		{"docs/my notes.md", "docs/my notes.md"},
		{"docs/café.md", "docs/café.md"},
		{"src/old_name.ts", "src/new_name.ts"},
		{"", "src/created.ts"},
		{"src/deleted.ts", ""},
		{"src/edges.ts", "src/edges.ts"},
		{"BACKLOG.md", "BACKLOG.md"},
		{"BACKLOG.md.old", "BACKLOG.md.old"},
		{"BACKLOGxmd", "BACKLOGxmd"},
		{"docs/backlog/item.md", "docs/backlog/item.md"},
		{"docs/backlogged.md", "docs/backlogged.md"},
		{"docs/backlog(new).md", "docs/backlog(new).md"},
	}
	if len(d.sections) != len(want) {
		t.Fatalf("sections = %d, want %d", len(d.sections), len(want))
	}
	for i, w := range want {
		if d.sections[i].pathA != w.a || d.sections[i].pathB != w.b {
			t.Errorf("section %d = (%q, %q), want (%q, %q)",
				i, d.sections[i].pathA, d.sections[i].pathB, w.a, w.b)
		}
	}
}

// lib/diff.sh:60-86. The whole path is wrapped in double quotes, with \\ and \"
// escaped, the usual control-character escapes, and \### octal for every byte
// outside printable ASCII. Bytes are rebuilt one at a time, so a two-byte UTF-8
// character arrives as its two octal escapes.
func TestUnquoteUndoesGitCStyleQuoting(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"unquoted passes through", `a/docs/my notes.md`, `a/docs/my notes.md`},
		{"octal rebuilds one byte at a time", `"a/docs/caf\303\251.md"`, "a/docs/café.md"},
		{"escaped quote", `"a/we\"ird"`, `a/we"ird`},
		{"escaped backslash", `"a/trail\\"`, `a/trail\`},
		{"escaped tab", `"a/we\tird"`, "a/we\tird"},
		{"escaped newline", `"a/we\nird"`, "a/we\nird"},
		{"bell backspace vtab formfeed cr", `"\a\b\v\f\r"`, "\a\b\v\f\r"},
		{"an unknown escape keeps the character", `"a/q\zx"`, `a/qzx`},
		{"a trailing backslash appends nothing", `"a/q\\`, `a/q`},
		{"an unterminated quote still loses its last byte", `"a/qx`, `a/q`},
		{"a short octal run stops at three digits", `"\1011"`, "A1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := unquote(c.in); got != c.want {
				t.Fatalf("unquote(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// lib/diff.sh:152-158. Counts are omitted when they are 1, so only the text up
// to the comma is read. A header the parser cannot read is not refused: it
// yields zero and the hunk renumbers from there. Every expectation below is the
// shipped Bash answer, not a better one.
func TestHunkHeaderCountsAreReadUpToTheComma(t *testing.T) {
	cases := []struct {
		name  string
		field string
		want  int
	}{
		{"counts present", "-6,6", 6},
		{"counts omitted", "-1", 1},
		{"new side", "+35,4", 35},
		{"zero for a created file", "+0", 0},
		{"unreadable text yields zero", "-arbage", 0},
		{"an absent field yields zero", "", 0},
		{"a second @@ read as a count yields zero", "@@", 0},
		// `@@ --5,2 +1,2 @@` numbers a hunk from -5, and diff_anchor answers -5
		// for it. The sign is read, not discarded.
		{"a negative start is carried through", "--5,2", -5},
		// `@@ - + @@` offers a one-byte field, where awk's substr answers "" and
		// a slice would run past the end.
		{"a one-byte field yields zero", "-", 0},
		// A start too wide for an int saturates rather than wrapping. The awk
		// prints 100000000000000000000; either way the number is wrong, but this
		// one stays positive.
		{"an oversized start saturates", "-99999999999999999999,1", maxInt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hunkStart(c.field); got != c.want {
				t.Fatalf("hunkStart(%q) = %d, want %d", c.field, got, c.want)
			}
		})
	}
}

// awk splits a record into fields on runs of blanks. `@@-1,2 +1,2 @@` therefore
// has no field the parser expects, which is why the malformed cases below
// renumber rather than fail.
func TestFieldsSplitOnBlankRuns(t *testing.T) {
	got := fields("@@   -6,6\t+6,8 @@ All notable")
	want := []string{"@@", "-6,6", "+6,8", "@@", "All", "notable"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("fields = %v, want %v", got, want)
	}
}

// A record is one line, and a file with no terminal newline still yields its
// last partial line. lib/diff.sh:32 refuses an empty file before awk sees it.
func TestRecordsMatchAwkLineSplitting(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty yields nothing", "", nil},
		{"a lone newline is one empty record", "\n", []string{""}},
		{"a terminal newline adds no record", "a\nb\n", []string{"a", "b"}},
		{"a missing terminal newline keeps the last line", "a\nb", []string{"a", "b"}},
		{"a blank final line is a record", "a\n\n", []string{"a", ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := records([]byte(c.in))
			if len(got) != len(c.want) {
				t.Fatalf("records(%q) = %q, want %q", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("records(%q) = %q, want %q", c.in, got, c.want)
				}
			}
		})
	}
}
