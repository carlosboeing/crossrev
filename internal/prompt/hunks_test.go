package prompt_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/prompt"
)

// shapedUnit is one batch unit carrying shaped hunk input: the form the
// shaping step decided and the gutter-numbered diff the prompt shows.
func shapedUnit(t *testing.T, path string, form intel.InputForm, numbered string) prompt.BatchUnit {
	t.Helper()
	head := revisionOf(t, headSHA)
	return prompt.BatchUnit{
		Path:            path,
		Change:          core.ChangeModified,
		ContentRevision: head,
		Body:            []byte("package x\n"),
		Available:       true,
		Form:            form,
		NumberedDiff:    []byte(numbered),
	}
}

const headSHA = "2222222222222222222222222222222222222222"

// unitSection cuts the rendered prompt down to one numbered unit's block,
// so a fence count names the unit's own rendering rather than the whole
// prompt's.
func unitSection(t *testing.T, rendered, heading string) string {
	t.Helper()
	at := strings.Index(rendered, heading)
	if at < 0 {
		t.Fatalf("prompt carries no %q block", heading)
	}
	rest := rendered[at:]
	if next := strings.Index(rest[len(heading):], "### "); next >= 0 {
		return rest[:len(heading)+next]
	}
	if end := strings.Index(rest, "### Advisory context"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// A small file's whole-file read renders once as one numbered hunk: the
// form note, a single diff fence, and no second body fence beside it.
func TestShapedFullTextRendersOnceAsOneNumberedHunk(t *testing.T) {
	o := loadReviewOracle(t)
	numbered := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n" +
		"@@ -1,2 +1,2 @@\n   1    1 |package a\n   2    2 |func A() {}\n"
	units := []prompt.BatchUnit{shapedUnit(t, "a.go", intel.FormFullText, numbered)}
	got := string(batchReview(o, units, nil, nil).Render())
	section := unitSection(t, got, "### 1. `a.go`")

	if !strings.Contains(section, "Shown in full as one numbered hunk.") {
		t.Errorf("full_text unit names no whole-file note:\n%s", section)
	}
	if n := strings.Count(section, "````diff"); n != 1 {
		t.Errorf("full_text unit renders %d diff fences, want 1:\n%s", n, section)
	}
	headers := 0
	for _, line := range strings.Split(numbered, "\n") {
		if strings.HasPrefix(line, "@@") {
			headers++
		}
	}
	if headers != 1 {
		t.Fatalf("fixture is not one hunk")
	}
	if !strings.Contains(section, "   1    1 |package a") {
		t.Errorf("full_text unit lost the gutter numbers:\n%s", section)
	}
}

// A large file's function-context hunks render with the clip they carry:
// the enclosing function, the window note, and the changed lines.
func TestShapedHunksContextRendersTheClippedWindow(t *testing.T) {
	o := loadReviewOracle(t)
	numbered := "diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n" +
		"@@ -50,3 +50,3 @@ func F150() int {\n  50   50 | \n  51   51 |func F150() int {\n  52    - |-\treturn 150\n   -   52 |+\treturn 9999\n"
	units := []prompt.BatchUnit{shapedUnit(t, "big.go", intel.FormHunksContext, numbered)}
	got := string(batchReview(o, units, nil, nil).Render())
	section := unitSection(t, got, "### 1. `big.go`")

	if !strings.Contains(section, "enclosing function") || !strings.Contains(section, "100 lines") {
		t.Errorf("hunks_context unit names no function window:\n%s", section)
	}
	if !strings.Contains(section, "return 9999") {
		t.Errorf("hunks_context unit lost the changed line:\n%s", section)
	}
}

// A binary unit renders header-only with its access limit: the binary
// header, the limit sentence, and none of the body bytes.
func TestShapedDiffOnlyBinaryRendersHeaderOnly(t *testing.T) {
	o := loadReviewOracle(t)
	unit := shapedUnit(t, "blob.bin", intel.FormDiffOnly,
		"diff --git a/blob.bin b/blob.bin\nindex 1111111..2222222 100644\nBinary files a/blob.bin and b/blob.bin differ\n")
	unit.Available = true
	unit.Binary = true
	got := string(batchReview(o, []prompt.BatchUnit{unit}, nil, nil).Render())
	section := unitSection(t, got, "### 1. `blob.bin`")

	if !strings.Contains(section, "Binary files a/blob.bin and b/blob.bin differ") {
		t.Errorf("diff_only unit lost the binary header:\n%s", section)
	}
	if !strings.Contains(section, "Binary content is not shown") {
		t.Errorf("diff_only unit names no access limit:\n%s", section)
	}
	if strings.Contains(section, "@@") {
		t.Errorf("diff_only unit carries a hunk:\n%s", section)
	}
}

// A pure rename renders header-only with the reason the shaping step
// named: the rename headers and the changeless sentence, no hunks.
func TestShapedDiffOnlyPureRenameRendersHeaderOnly(t *testing.T) {
	o := loadReviewOracle(t)
	unit := shapedUnit(t, "new.go", intel.FormDiffOnly,
		"diff --git a/old.go b/new.go\nsimilarity index 100%\nrename from old.go\nrename to new.go\n")
	unit.OldPath = "old.go"
	unit.Change = core.ChangeRenamed
	unit.Reason = "renamed without edits"
	got := string(batchReview(o, []prompt.BatchUnit{unit}, nil, nil).Render())
	section := unitSection(t, got, "### 1. `new.go`")

	for _, want := range []string{"rename from old.go", "renamed without edits", "Previously `old.go`."} {
		if !strings.Contains(section, want) {
			t.Errorf("diff_only rename lacks %q:\n%s", want, section)
		}
	}
	if strings.Contains(section, "@@") {
		t.Errorf("diff_only rename carries a hunk:\n%s", section)
	}
}

// The separate diff section goes once units carry their own hunks: a
// shaped batch renders no "The diff under review", while an unshaped
// batch and the frozen prompt keep it byte for byte.
func TestShapedBatchesOmitTheSeparateDiffSection(t *testing.T) {
	o := loadReviewOracle(t)

	shaped := string(batchReview(o,
		[]prompt.BatchUnit{shapedUnit(t, "a.go", intel.FormFullText, "diff --git a/a.go b/a.go\n")},
		nil, nil).Render())
	if strings.Contains(shaped, "## The diff under review") {
		t.Error("a shaped batch still renders the separate diff section")
	}

	legacy := string(batchReview(o, []prompt.BatchUnit{{
		Path: "a.go", Change: core.ChangeModified, ContentRevision: revisionOf(t, headSHA),
		Body: []byte("package a\n"), Available: true,
	}}, nil, nil).Render())
	if !strings.Contains(legacy, "## The diff under review") {
		t.Error("an unshaped batch lost the sliced diff section it still renders")
	}
}
