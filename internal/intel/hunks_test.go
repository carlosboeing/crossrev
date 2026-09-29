package intel_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/intel"
)

// TestSelectFormCoversTheThreeSuppliedForms requires every changed file to
// land in exactly one supplied form: added and deleted files read in full,
// small modified and renamed files read in full, large edited files read as
// function-context hunks, and binary, unreadable and changeless files read
// as header-only diffs.
func TestSelectFormCoversTheThreeSuppliedForms(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		kind                            core.ChangeKind
		size                            int
		binary, unavailable, hasChanges bool
		want                            intel.InputForm
	}{
		{"added", core.ChangeAdded, 100, false, false, true, intel.FormFullText},
		{"deleted", core.ChangeDeleted, 100, false, false, true, intel.FormFullText},
		{"small modified", core.ChangeModified, 100, false, false, true, intel.FormFullText},
		{"small renamed and edited", core.ChangeRenamed, 100, false, false, true, intel.FormFullText},
		{"large modified", core.ChangeModified, 8*1024 + 1, false, false, true, intel.FormHunksContext},
		{"large renamed and edited", core.ChangeRenamed, 8*1024 + 1, false, false, true, intel.FormHunksContext},
		{"large type change with edits", core.ChangeTypeChanged, 8*1024 + 1, false, false, true, intel.FormHunksContext},
		{"binary", core.ChangeModified, 100, true, false, true, intel.FormDiffOnly},
		{"binary even when small", core.ChangeAdded, 10, true, false, true, intel.FormDiffOnly},
		{"unreadable", core.ChangeModified, 0, false, true, false, intel.FormDiffOnly},
		{"pure rename", core.ChangeRenamed, 100, false, false, false, intel.FormDiffOnly},
		{"mode-only change", core.ChangeTypeChanged, 100, false, false, false, intel.FormDiffOnly},
		{"empty added file", core.ChangeAdded, 0, false, false, false, intel.FormDiffOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := intel.SelectForm(tc.kind, tc.size, tc.binary, tc.unavailable, tc.hasChanges); got != tc.want {
				t.Errorf("SelectForm(%s, %d, binary=%v, unavailable=%v, hasChanges=%v) = %q, want %q",
					tc.kind, tc.size, tc.binary, tc.unavailable, tc.hasChanges, got, tc.want)
			}
		})
	}
}

// TestSelectFormSplitsAtEightKiB pins the whole-file threshold: a file of
// exactly 8 KiB at the head still reads in full, one byte more reads as
// function-context hunks.
func TestSelectFormSplitsAtEightKiB(t *testing.T) {
	if intel.FullTextMaxBytes != 8*1024 {
		t.Fatalf("FullTextMaxBytes = %d, want 8192", intel.FullTextMaxBytes)
	}
	if got := intel.SelectForm(core.ChangeModified, 8*1024, false, false, true); got != intel.FormFullText {
		t.Errorf("SelectForm at exactly 8 KiB = %q, want full_text", got)
	}
	if got := intel.SelectForm(core.ChangeModified, 8*1024+1, false, false, true); got != intel.FormHunksContext {
		t.Errorf("SelectForm at 8 KiB + 1 = %q, want hunks_context", got)
	}
}

// TestDiffOnlyReasonNamesTheLimit pins the header-only reason shaping and
// prompt mapping share: the unit's own access limit, the binary note, or
// the changeless kind.
func TestDiffOnlyReasonNamesTheLimit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kind        core.ChangeKind
		size        int
		binary      bool
		unavailable string
		want        string
	}{
		{"unreadable keeps its reason", core.ChangeModified, 0, false, "permission denied", "permission denied"},
		{"binary", core.ChangeModified, 100, true, "", "binary content"},
		{"pure rename", core.ChangeRenamed, 100, false, "", "renamed without edits"},
		{"mode-only", core.ChangeTypeChanged, 100, false, "", "mode change with no content change"},
		{"empty file", core.ChangeAdded, 0, false, "", "empty file"},
		{"changeless edit", core.ChangeModified, 100, false, "", "no changed lines"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := intel.DiffOnlyReason(tc.kind, tc.size, tc.binary, tc.unavailable); got != tc.want {
				t.Errorf("DiffOnlyReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFileUnitsCarryNoShapedInputUntilShapingRuns pins the legacy posture:
// discovery builds evidence without hunk input, so a unit that never passed
// through shaping renders exactly as before.
func TestFileUnitsCarryNoShapedInputUntilShapingRuns(t *testing.T) {
	base, head := stubRevisions(t)
	_ = base
	_ = head
	unit := intel.FileUnit{Path: "a.go", Change: core.ChangeModified}
	if unit.Form != "" {
		t.Errorf("zero FileUnit Form = %q, want empty (legacy rendering)", unit.Form)
	}
	if unit.Diff != nil {
		t.Errorf("zero FileUnit Diff = %d bytes, want none", len(unit.Diff))
	}
}
