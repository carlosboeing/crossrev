package diff_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
)

// clipCase builds a one-file diff around one hunk for the clip tests.
func clipCase(header string, body ...string) []byte {
	var b strings.Builder
	b.WriteString("diff --git a/f b/f\n--- a/f\n+++ b/f\n")
	b.WriteString(header + "\n")
	for _, line := range body {
		b.WriteString(line + "\n")
	}
	return []byte(b.String())
}

func clipHeaders(t *testing.T, clipped []byte) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(string(clipped), "\n") {
		if strings.HasPrefix(line, "@@") {
			out = append(out, line)
		}
	}
	return out
}

// Two changes far apart clip to two hunks with corrected starts and
// counts, each carrying the original hunk's function suffix.
func TestClipHunksSplitsFarChanges(t *testing.T) {
	var body []string
	for i := 1; i <= 60; i++ {
		switch i {
		case 10:
			body = append(body, "-old ten", "+new ten")
		case 50:
			body = append(body, "-old fifty", "+new fifty")
		default:
			body = append(body, " context")
		}
	}
	raw := clipCase("@@ -1,60 +1,60 @@ func f()", body...)
	clipped := diff.Parse(raw, core.RevisionPair{}).ClipHunks(2)

	headers := clipHeaders(t, clipped)
	if len(headers) != 2 {
		t.Fatalf("clipped hunks = %d, want 2:\n%s", len(headers), clipped)
	}
	if headers[0] != "@@ -8,5 +8,5 @@ func f()" {
		t.Errorf("first hunk header = %q, want the window around line 10", headers[0])
	}
	if headers[1] != "@@ -48,5 +48,5 @@ func f()" {
		t.Errorf("second hunk header = %q, want the window around line 50", headers[1])
	}
	for _, want := range []string{"-old ten", "+new ten", "-old fifty", "+new fifty"} {
		if !strings.Contains(string(clipped), want) {
			t.Errorf("clipped hunks lost %q", want)
		}
	}
}

// A kept run opening on the added side starts its old range on the last
// consumed old line, so the re-emitted header numbers what follows
// correctly.
func TestClipHunksOpensInsertionRunsOnTheLineBefore(t *testing.T) {
	raw := clipCase("@@ -1,6 +1,7 @@",
		" one", " two", " three", " four", " five", " six", "+seven")
	clipped := diff.Parse(raw, core.RevisionPair{}).ClipHunks(1)

	headers := clipHeaders(t, clipped)
	if len(headers) != 1 {
		t.Fatalf("clipped hunks = %d, want 1:\n%s", len(headers), clipped)
	}
	if headers[0] != "@@ -6 +6,2 @@" {
		t.Errorf("insertion hunk header = %q, want @@ -6 +6,2 @@", headers[0])
	}
	// The re-emitted hunk re-parses to the same numbers: the insertion at
	// new line 7 with old line 6 before it.
	reparsed := diff.Parse(clipped, core.RevisionPair{})
	if !reparsed.ChangedLine("f", core.SideRight, 7) {
		t.Errorf("reparsed hunk lost the insertion at new line 7:\n%s", clipped)
	}
}

// A `\ No newline` annotation stands or falls with the line it
// annotates: kept beside a kept change, dropped with a dropped one.
func TestClipHunksKeepsAnnotationsWithTheirLines(t *testing.T) {
	var body []string
	for i := 1; i <= 10; i++ {
		body = append(body, " context")
	}
	body = append(body, "-last", `\ No newline at end of file`, "+last", `\ No newline at end of file`)
	raw := clipCase("@@ -1,11 +1,11 @@", body...)
	clipped := diff.Parse(raw, core.RevisionPair{}).ClipHunks(1)

	if n := strings.Count(string(clipped), `\ No newline at end of file`); n != 2 {
		t.Errorf("annotations = %d, want 2 beside the kept change:\n%s", n, clipped)
	}

	dropped := clipCase("@@ -1,12 +1,12 @@", append(body, " tail", `\ No newline at end of file`)...)
	reclipped := diff.Parse(dropped, core.RevisionPair{}).ClipHunks(0)
	if strings.Contains(string(reclipped), "tail") {
		t.Errorf("radius zero kept far context:\n%s", reclipped)
	}
}

// A hunk header the parser cannot read is kept verbatim: malformed input
// shows too much, never too little.
func TestClipHunksKeepsUnparseableHeadersVerbatim(t *testing.T) {
	raw := clipCase("@@ nonsense @@", "-old", "+new")
	clipped := diff.Parse(raw, core.RevisionPair{}).ClipHunks(100)
	for _, want := range []string{"@@ nonsense @@", "-old", "+new"} {
		if !strings.Contains(string(clipped), want) {
			t.Errorf("verbatim hunk lost %q:\n%s", want, clipped)
		}
	}
}

// HasChanges tells hunks from headers: renames, mode changes and empty
// additions carry no +/- line.
func TestHasChangesTellsHunksFromHeaders(t *testing.T) {
	withHunk := clipCase("@@ -1 +1,2 @@", " context", "+added")
	if !diff.Parse(withHunk, core.RevisionPair{}).HasChanges() {
		t.Error("hunk diff reports no changes")
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"pure rename", []byte("diff --git a/o b/n\nsimilarity index 100%\nrename from o\nrename to n\n")},
		{"mode-only", []byte("diff --git a/f b/f\nold mode 100644\nnew mode 100755\n")},
		{"context-only", clipCase("@@ -1 +1 @@", " context")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff.Parse(tc.raw, core.RevisionPair{}).HasChanges() {
				t.Errorf("%s reports changes", tc.name)
			}
		})
	}
}

// Headers answers the header lines alone, so a changeless file still
// shows what changed about it.
func TestHeadersAnswersHeadersAlone(t *testing.T) {
	raw := clipCase("@@ -1 +1,2 @@", " context", "+added")
	got := string(diff.Parse(raw, core.RevisionPair{}).Headers())
	if !strings.Contains(got, "diff --git a/f b/f") {
		t.Errorf("headers lost the file header:\n%s", got)
	}
	if strings.Contains(got, "+added") || strings.Contains(got, " context") {
		t.Errorf("headers carry body lines:\n%s", got)
	}
}

// ChangedLine answers changed lines only: additions and deletions on
// their own side, never context, never the other side.
func TestChangedLineAnswersChangedLinesOnly(t *testing.T) {
	raw := clipCase("@@ -1,2 +1,2 @@", " context", "-old", "+new")
	parsed := diff.Parse(raw, core.RevisionPair{})
	for _, tc := range []struct {
		path string
		side core.Side
		line int
		want bool
	}{
		{"f", core.SideRight, 1, false}, // context
		{"f", core.SideLeft, 1, false},  // context
		{"f", core.SideRight, 2, true},  // addition
		{"f", core.SideLeft, 2, true},   // deletion
		{"f", core.SideRight, 3, false}, // off-hunk
		{"f", core.SideLeft, 2 - 1, false},
		{"other", core.SideRight, 2, false},
	} {
		if got := parsed.ChangedLine(tc.path, tc.side, tc.line); got != tc.want {
			t.Errorf("ChangedLine(%q, %s, %d) = %v, want %v", tc.path, tc.side, tc.line, got, tc.want)
		}
	}
}
