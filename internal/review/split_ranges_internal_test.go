package review

import (
	"encoding/json"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// The merged record of a split file carries the union of its parts'
// ranges and the part count: coverage counts the slices the reviewer was
// actually shown, and the count says how many slices that was.
func TestMergePendingSplitCarriesRangesAndParts(t *testing.T) {
	part1 := "diff --git a/huge.go b/huge.go\nindex 111..222 100644\n--- a/huge.go\n+++ b/huge.go\n" +
		"@@ -1,3 +1,4 @@\n line1\n+added1\n line2\n line3\n"
	part2 := "diff --git a/huge.go b/huge.go\nindex 111..222 100644\n--- a/huge.go\n+++ b/huge.go\n" +
		"@@ -50,3 +51,4 @@\n line50\n+added51\n line51\n line52\n"
	unit := intel.FileUnit{
		ID:     core.FileUnitID("huge.go"),
		Path:   "huge.go",
		Change: core.ChangeModified,
		Form:   intel.FormHunksContext,
	}
	ps := &pendingSplit{
		unit:     unit,
		count:    2,
		verdicts: []string{"no_issue", "no_issue"},
		numbers:  [][]int{nil, nil},
		reasons:  []string{"", ""},
		evidence: [][]prstate.Evidence{nil, nil},
		payloads: []json.RawMessage{[]byte(`{"findings":[]}`), []byte(`{"findings":[]}`)},
		diffs:    [][]byte{[]byte(part1), []byte(part2)},
	}
	verdicts, supplied, _, err := mergePendingSplit(ps)
	if err != nil {
		t.Fatal(err)
	}
	disp, ok := verdicts[unit.ID]
	if !ok || disp.Verdict != "no_issue" {
		t.Fatalf("merged verdict = %+v, want no_issue", disp)
	}
	s, ok := supplied[unit.ID]
	if !ok {
		t.Fatal("merged split carries no supplied input")
	}
	if s.Form != prstate.SuppliedFormHunksContext {
		t.Errorf("merged form = %q, want hunks_context", s.Form)
	}
	if s.Parts != 2 {
		t.Errorf("merged parts = %d, want 2", s.Parts)
	}
	want := core.SuppliedRanges{
		Base: []core.LineSpan{{Start: 1, End: 3}, {Start: 50, End: 52}},
		Head: []core.LineSpan{{Start: 1, End: 4}, {Start: 51, End: 54}},
	}
	if !equalRanges(s.Ranges, want) {
		t.Errorf("merged ranges = %+v, want %+v", s.Ranges, want)
	}
	numbered := append(
		diff.Parse([]byte(part1), core.RevisionPair{}).Numbered(),
		diff.Parse([]byte(part2), core.RevisionPair{}).Numbered()...,
	)
	if want := core.BodyDigestHex(numbered); s.Digest != want {
		t.Errorf("merged digest %s, want %s (the rendered part bytes)", s.Digest, want)
	}
}

func equalRanges(got, want core.SuppliedRanges) bool {
	if len(got.Base) != len(want.Base) || len(got.Head) != len(want.Head) {
		return false
	}
	for i := range got.Base {
		if got.Base[i] != want.Base[i] {
			return false
		}
	}
	for i := range got.Head {
		if got.Head[i] != want.Head[i] {
			return false
		}
	}
	return true
}
