package diff_test

import (
	"reflect"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
)

// Removed lines number on the base side only, added lines on the head side
// only, and context on both; headers number nothing.
func TestLineRangesSplitAddedRemovedAndContext(t *testing.T) {
	raw := "diff --git a/a.go b/a.go\nindex 111..222 100644\n--- a/a.go\n+++ b/a.go\n" +
		"@@ -1,4 +1,4 @@\n keep1\n-old\n+new\n keep2\n keep3\n"
	d := diff.Parse([]byte(raw), core.RevisionPair{})
	base, head := d.LineRanges()
	if want := []core.LineSpan{{Start: 1, End: 4}}; !reflect.DeepEqual(base, want) {
		t.Fatalf("base = %+v, want %+v", base, want)
	}
	if want := []core.LineSpan{{Start: 1, End: 4}}; !reflect.DeepEqual(head, want) {
		t.Fatalf("head = %+v, want %+v", head, want)
	}
}

// Lines either side of a pure addition still read as one base run when
// their old numbers meet: the addition hides no base line from the gutter.
func TestLineRangesMergeAcrossAnAddition(t *testing.T) {
	raw := "diff --git a/a.go b/a.go\nindex 111..222 100644\n--- a/a.go\n+++ b/a.go\n" +
		"@@ -5,3 +5,4 @@\n five\n+added\n six\n seven\n"
	d := diff.Parse([]byte(raw), core.RevisionPair{})
	base, head := d.LineRanges()
	if want := []core.LineSpan{{Start: 5, End: 7}}; !reflect.DeepEqual(base, want) {
		t.Fatalf("base = %+v, want %+v", base, want)
	}
	if want := []core.LineSpan{{Start: 5, End: 8}}; !reflect.DeepEqual(head, want) {
		t.Fatalf("head = %+v, want %+v", head, want)
	}
}

// An empty diff and a header-only diff cover nothing on either side.
func TestLineRangesEmptyForNoHunks(t *testing.T) {
	d := diff.Parse(nil, core.RevisionPair{})
	if base, head := d.LineRanges(); len(base) != 0 || len(head) != 0 {
		t.Fatalf("empty diff ranges = %+v, %+v, want none", base, head)
	}
	raw := "diff --git a/a.go b/a.go\nindex 111..222 100644\n--- a/a.go\n+++ b/a.go\n"
	d = diff.Parse([]byte(raw), core.RevisionPair{})
	if base, head := d.LineRanges(); len(base) != 0 || len(head) != 0 {
		t.Fatalf("header-only diff ranges = %+v, %+v, want none", base, head)
	}
}
