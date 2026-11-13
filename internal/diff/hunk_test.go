package diff_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
)

const twoHunkDiff = `diff --git a/app.go b/app.go
--- a/app.go
+++ b/app.go
@@ -1,3 +1,4 @@
 context
+added
 context
 context
@@ -20,3 +21,3 @@
 before
-old
+new
 after
diff --git a/other.go b/other.go
--- a/other.go
+++ b/other.go
@@ -1,1 +1,1 @@
-x
+y
`

// Hunk answers the hunk holding the line on its side, header and body.
func TestHunkAnswersTheHoldingHunk(t *testing.T) {
	parsed := diff.Parse([]byte(twoHunkDiff), core.RevisionPair{})
	got, ok := parsed.Hunk("app.go", core.SideRight, 2)
	if !ok {
		t.Fatal("no hunk holds app.go:2 on the right")
	}
	want := "@@ -1,3 +1,4 @@\n context\n+added\n context\n context\n"
	if string(got) != want {
		t.Errorf("hunk = %q, want %q", got, want)
	}
	got, ok = parsed.Hunk("app.go", core.SideRight, 22)
	if !ok {
		t.Fatal("no hunk holds app.go:22 on the right")
	}
	want = "@@ -20,3 +21,3 @@\n before\n-old\n+new\n after\n"
	if string(got) != want {
		t.Errorf("hunk = %q, want %q", got, want)
	}
	// The left side reads the old numbers: line 21 on the left is the
	// removed line's hunk.
	got, ok = parsed.Hunk("app.go", core.SideLeft, 21)
	if !ok {
		t.Fatal("no hunk holds app.go:21 on the left")
	}
	if string(got) != want {
		t.Errorf("hunk = %q, want %q", got, want)
	}
}

// A line no hunk holds reads its nearest hunk in the same file; a file
// with no hunk on that side reads nothing.
func TestHunkFallsBackToTheNearestHunk(t *testing.T) {
	parsed := diff.Parse([]byte(twoHunkDiff), core.RevisionPair{})
	got, ok := parsed.Hunk("app.go", core.SideRight, 10)
	if !ok {
		t.Fatal("no hunk answers app.go:10 on the right")
	}
	// Line 10 sits between the hunks, closer to the first one's span.
	if want := "@@ -1,3 +1,4 @@\n context\n+added\n context\n context\n"; string(got) != want {
		t.Errorf("hunk = %q, want %q", got, want)
	}
	if _, ok := parsed.Hunk("missing.go", core.SideRight, 1); ok {
		t.Error("a file outside the diff answered a hunk")
	}
	// A pure addition carries no old number: the left side has no hunk
	// to read.
	added := "diff --git a/new.go b/new.go\n--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,1 @@\n+hello\n"
	if _, ok := diff.Parse([]byte(added), core.RevisionPair{}).Hunk("new.go", core.SideLeft, 1); ok {
		t.Error("the left side of a pure addition answered a hunk")
	}
	if _, ok := diff.Parse([]byte(added), core.RevisionPair{}).Hunk("new.go", core.SideRight, 1); !ok {
		t.Error("the right side of a pure addition answered nothing")
	}
}
