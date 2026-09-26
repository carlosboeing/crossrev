package intel_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/intel"
)

// batchScope builds a scope of n required files with the given body, named
// src/f0000.go and on, so tests control counts and sizes exactly.
func batchScope(t *testing.T, n int, body []byte) intel.Scope {
	t.Helper()
	base, head := stubRevisions(t)
	scope := intel.Scope{Base: base, Head: head, Engine: core.FileEngineVersion, EngineID: core.FileEngineID()}
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("src/f%04d.go", i)
		scope.Required = append(scope.Required, intel.FileUnit{
			ID:              core.FileUnitID(path),
			Path:            path,
			Change:          core.ChangeModified,
			ContentRevision: head,
			BodyDigest:      core.BodyDigestHex(body),
			Body:            body,
			Available:       true,
		})
	}
	return scope
}

// sizeRender measures a batch as its file bytes plus a fixed extra for headers
// and prior context. The extra is what makes rendered measurement differ from
// counting file bytes alone.
func sizeRender(extra int) intel.RenderBatch {
	return func(files []intel.FileUnit) int {
		total := extra
		for _, f := range files {
			total += len(f.Body)
		}
		return total
	}
}

func batchSizes(plan intel.BatchPlan) []int {
	var out []int
	for _, b := range plan.Batches {
		out = append(out, len(b.Files))
	}
	return out
}

// TestBatchesMeasureTheRenderedPrompt holds one file whose bytes fit the 180
// KB budget on their own, then renders it with headers and prior context that
// push it over. Only the rendered size may decide: the file must halt with
// input_exceeds_budget and no batch.
func TestBatchesMeasureTheRenderedPrompt(t *testing.T) {
	body := make([]byte, 100*1024)
	for i := range body {
		body[i] = 'a'
	}
	scope := batchScope(t, 1, body)
	if len(body) >= intel.MaxPromptBytes {
		t.Fatalf("fixture body is %d bytes, want it below the %d budget so bytes alone would fit", len(body), intel.MaxPromptBytes)
	}
	plan := intel.Batches(scope, nil, sizeRender(100*1024))

	if len(plan.Batches) != 0 {
		t.Errorf("Batches scheduled %d batches, want none: the rendered prompt exceeds the budget", len(plan.Batches))
	}
	if plan.HaltReason != "input_exceeds_budget" {
		t.Errorf("halt reason = %q, want input_exceeds_budget", plan.HaltReason)
	}
	if plan.HaltPath != "src/f0000.go" {
		t.Errorf("halt path = %q, want src/f0000.go", plan.HaltPath)
	}
	if len(plan.Unbatched) != 1 {
		t.Errorf("unbatched holds %d files, want the 1 oversized file", len(plan.Unbatched))
	}
}

func TestBatchesPackAtMostFortyFiles(t *testing.T) {
	for _, tc := range []struct {
		files int
		want  []int
	}{
		{39, []int{39}},
		{40, []int{40}},
		{41, []int{40, 1}},
	} {
		t.Run(fmt.Sprintf("%d files", tc.files), func(t *testing.T) {
			scope := batchScope(t, tc.files, []byte("package f\n"))
			plan := intel.Batches(scope, nil, sizeRender(128))
			got := batchSizes(plan)
			if len(got) != len(tc.want) {
				t.Fatalf("batches = %v, want sizes %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("batches = %v, want sizes %v", got, tc.want)
					break
				}
			}
			if plan.HaltReason != "" {
				t.Errorf("halt reason = %q, want none", plan.HaltReason)
			}
		})
	}
}

func TestBatchesAdmitAtMostFourHundredPerPass(t *testing.T) {
	for _, tc := range []struct {
		files   int
		batched int
		carried int
		reason  string
	}{
		{399, 399, 0, ""},
		{400, 400, 0, ""},
		{401, 400, 1, "review_budget_reached"},
	} {
		t.Run(fmt.Sprintf("%d required", tc.files), func(t *testing.T) {
			scope := batchScope(t, tc.files, []byte("package f\n"))
			plan := intel.Batches(scope, nil, sizeRender(64))
			batched := 0
			for _, b := range plan.Batches {
				batched += len(b.Files)
			}
			if batched != tc.batched {
				t.Errorf("batched %d files, want %d", batched, tc.batched)
			}
			if len(plan.Carried) != tc.carried {
				t.Errorf("carried %d files, want %d", len(plan.Carried), tc.carried)
			}
			if plan.CarryReason != tc.reason {
				t.Errorf("carry reason = %q, want %q", plan.CarryReason, tc.reason)
			}
		})
	}
}

func TestBatchesSplitOnTheRenderedByteBoundary(t *testing.T) {
	half := intel.MaxPromptBytes / 2
	exact := batchScope(t, 2, make([]byte, half))
	plan := intel.Batches(exact, nil, sizeRender(0))
	if got := batchSizes(plan); len(got) != 1 || got[0] != 2 {
		t.Errorf("exact-budget batches = %v, want one batch of 2", got)
	}

	over := batchScope(t, 2, make([]byte, half+1))
	plan = intel.Batches(over, nil, sizeRender(0))
	if got := batchSizes(plan); len(got) != 2 {
		t.Errorf("over-budget batches = %v, want two batches of 1", got)
	}
}

func TestBatchesHaltWhenOneFileCannotFitAlone(t *testing.T) {
	big := make([]byte, intel.MaxPromptBytes+1)
	scope := batchScope(t, 1, big)
	plan := intel.Batches(scope, nil, sizeRender(0))
	if plan.HaltReason != "input_exceeds_budget" {
		t.Errorf("halt reason = %q, want input_exceeds_budget", plan.HaltReason)
	}
	if len(plan.Batches) != 0 {
		t.Errorf("Batches scheduled %v, want none", batchSizes(plan))
	}

	mixed := batchScope(t, 3, []byte("package f\n"))
	mixed.Required[1].Body = big
	mixed.Required[1].BodyDigest = core.BodyDigestHex(big)
	plan = intel.Batches(mixed, nil, sizeRender(0))
	if plan.HaltReason != "input_exceeds_budget" {
		t.Fatalf("halt reason = %q, want input_exceeds_budget", plan.HaltReason)
	}
	if plan.HaltPath != "src/f0001.go" {
		t.Errorf("halt path = %q, want src/f0001.go", plan.HaltPath)
	}
	if got := batchSizes(plan); len(got) != 1 || got[0] != 1 {
		t.Errorf("batches before the halt = %v, want one batch of 1", got)
	}
	if len(plan.Unbatched) != 2 {
		t.Errorf("unbatched holds %d files, want the halting file and the one after it", len(plan.Unbatched))
	}
}

func TestBatchesArePathOrderedAndSkipAccepted(t *testing.T) {
	scope := batchScope(t, 3, []byte("package f\n"))
	scope.Required[0], scope.Required[2] = scope.Required[2], scope.Required[0]
	accepted := map[core.UnitID]bool{scope.Required[1].ID: true}
	plan := intel.Batches(scope, accepted, sizeRender(64))
	if len(plan.Batches) != 1 {
		t.Fatalf("batches = %v, want one batch", batchSizes(plan))
	}
	files := plan.Batches[0].Files
	if len(files) != 2 || files[0].Path != "src/f0000.go" || files[1].Path != "src/f0002.go" {
		t.Errorf("batch holds %v, want path-ordered src/f0000.go and src/f0002.go", files)
	}
}

func TestBatchesEmptyScopeSchedulesNothing(t *testing.T) {
	base, head := stubRevisions(t)
	scope := intel.Scope{Base: base, Head: head, Engine: core.FileEngineVersion, EngineID: core.FileEngineID()}
	plan := intel.Batches(scope, nil, sizeRender(64))
	if len(plan.Batches) != 0 || len(plan.Carried) != 0 || plan.HaltReason != "" || plan.CarryReason != "" {
		t.Errorf("empty scope plan = %+v, want no batches and no reasons", plan)
	}
}

// TestBatchingMatchesTheFrozenFixture requires the packing constants to equal
// the frozen bounds.
func TestBatchingMatchesTheFrozenFixture(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above the test directory")
		}
		dir = parent
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tests", "fixtures", "intelligence", "file-units.json"))
	if err != nil {
		t.Fatalf("read the frozen oracle: %v", err)
	}
	var fixture struct {
		Batching struct {
			MaxFilesPerBatch int `json:"max_files_per_batch"`
			MaxUnitsPerPass  int `json:"max_units_per_pass"`
			MaxPromptBytes   int `json:"max_prompt_bytes"`
		} `json:"batching"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode the frozen oracle: %v", err)
	}
	if intel.MaxFilesPerBatch != fixture.Batching.MaxFilesPerBatch {
		t.Errorf("MaxFilesPerBatch = %d, want frozen %d", intel.MaxFilesPerBatch, fixture.Batching.MaxFilesPerBatch)
	}
	if intel.MaxUnitsPerPass != fixture.Batching.MaxUnitsPerPass {
		t.Errorf("MaxUnitsPerPass = %d, want frozen %d", intel.MaxUnitsPerPass, fixture.Batching.MaxUnitsPerPass)
	}
	if intel.MaxPromptBytes != fixture.Batching.MaxPromptBytes {
		t.Errorf("MaxPromptBytes = %d, want frozen %d", intel.MaxPromptBytes, fixture.Batching.MaxPromptBytes)
	}
}

// generatedUnit marks the i-th required file with a built-in signal and,
// when big is set, a body no rendered prompt can hold alone.
func generatedUnit(scope intel.Scope, i int, signal string, big bool) intel.Scope {
	scope.Required[i].Generated = signal
	if big {
		body := make([]byte, intel.MaxPromptBytes+1)
		for j := range body {
			body[j] = 'a'
		}
		scope.Required[i].Body = body
		scope.Required[i].BodyDigest = core.BodyDigestHex(body)
	}
	return scope
}

// A generated file that cannot fit alone is skipped and packing continues:
// the files after it are still reviewed.
func TestBatchesSkipOversizedGeneratedFiles(t *testing.T) {
	scope := batchScope(t, 3, []byte("package f\n"))
	scope = generatedUnit(scope, 0, intel.SignalHeader, true)
	plan := intel.Batches(scope, nil, sizeRender(0))

	if plan.HaltReason != "" {
		t.Errorf("halt = %q at %q, want none: a generated file skips", plan.HaltReason, plan.HaltPath)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].Path != "src/f0000.go" {
		t.Fatalf("skipped = %v, want src/f0000.go", plan.Skipped)
	}
	if got := batchSizes(plan); len(got) != 1 || got[0] != 2 {
		t.Errorf("batches = %v, want one batch holding the two small files", got)
	}
	if len(plan.Unbatched) != 0 {
		t.Errorf("unbatched = %d files, want none", len(plan.Unbatched))
	}
}

// The same size with no signal halts exactly as before: only a recognised
// generated file turns the halt into a skip.
func TestBatchesHaltAtAnOversizedPlainFile(t *testing.T) {
	scope := batchScope(t, 3, []byte("package f\n"))
	scope = generatedUnit(scope, 0, "", true)
	plan := intel.Batches(scope, nil, sizeRender(0))
	if plan.HaltReason != "input_exceeds_budget" {
		t.Fatalf("halt reason = %q, want input_exceeds_budget", plan.HaltReason)
	}
	if plan.HaltPath != "src/f0000.go" {
		t.Errorf("halt path = %q, want src/f0000.go", plan.HaltPath)
	}
	if len(plan.Skipped) != 0 {
		t.Errorf("skipped = %v, want none", plan.Skipped)
	}
	if len(plan.Unbatched) != 3 {
		t.Errorf("unbatched = %d files, want all three", len(plan.Unbatched))
	}
}

// A handwritten Markdown file too large for one prompt halts rather than
// skipping: its long unwrapped lines are not a generated signal, so nothing
// the author wrote is dropped from review behind a "generated" warning.
func TestBatchesHaltAtOversizedUnwrappedMarkdown(t *testing.T) {
	scope := batchScope(t, 3, []byte("package f\n"))
	paragraph := strings.Repeat("An unwrapped paragraph written by a person. ", 10) + "\n"
	body := []byte(strings.Repeat(paragraph, intel.MaxPromptBytes/len(paragraph)+1))
	scope.Required[0].Path = "CHANGELOG.md"
	scope.Required[0].Body = body
	scope.Required[0].BodyDigest = core.BodyDigestHex(body)
	scope.Required[0].Generated = intel.GeneratedSignal("CHANGELOG.md", body)

	plan := intel.Batches(scope, nil, sizeRender(0))
	if plan.HaltReason != "input_exceeds_budget" || plan.HaltPath != "CHANGELOG.md" {
		t.Fatalf("halt = %q at %q, want input_exceeds_budget at CHANGELOG.md", plan.HaltReason, plan.HaltPath)
	}
	if len(plan.Skipped) != 0 {
		t.Errorf("skipped = %v, want none", plan.Skipped)
	}
}

// A generated file that fits is packed like any other: the signal only
// matters at the budget.
func TestBatchesPackGeneratedFilesThatFit(t *testing.T) {
	scope := batchScope(t, 2, []byte("package f\n"))
	scope = generatedUnit(scope, 0, intel.SignalHeader, false)
	scope = generatedUnit(scope, 1, intel.SignalLockfile, false)
	plan := intel.Batches(scope, nil, sizeRender(64))
	if len(plan.Skipped) != 0 {
		t.Errorf("skipped = %v, want none", plan.Skipped)
	}
	if got := batchSizes(plan); len(got) != 1 || got[0] != 2 {
		t.Errorf("batches = %v, want one batch of 2", got)
	}
}

// An oversized lockfile skips like any other oversized generated file.
func TestBatchesSkipOversizedLockfiles(t *testing.T) {
	scope := batchScope(t, 2, []byte("package f\n"))
	scope.Required[1].Path = "web/package-lock.json"
	scope.Required[1].ID = core.FileUnitID("web/package-lock.json")
	scope = generatedUnit(scope, 1, intel.SignalLockfile, true)
	plan := intel.Batches(scope, nil, sizeRender(0))
	if len(plan.Skipped) != 1 || plan.Skipped[0].Path != "web/package-lock.json" {
		t.Fatalf("skipped = %v, want the lockfile", plan.Skipped)
	}
	if got := batchSizes(plan); len(got) != 1 || got[0] != 1 {
		t.Errorf("batches = %v, want one batch of the small file", got)
	}
}

// A plain halt later in path order keeps the earlier skip and the batches
// already packed.
func TestBatchesPlainHaltRetainsEarlierSkips(t *testing.T) {
	scope := batchScope(t, 5, []byte("package f\n"))
	scope = generatedUnit(scope, 1, intel.SignalMinified, true) // skipped
	scope = generatedUnit(scope, 3, "", true)                   // plain: halts
	plan := intel.Batches(scope, nil, sizeRender(0))
	if len(plan.Skipped) != 1 || plan.Skipped[0].Path != "src/f0001.go" {
		t.Errorf("skipped = %v, want src/f0001.go", plan.Skipped)
	}
	if plan.HaltReason != "input_exceeds_budget" || plan.HaltPath != "src/f0003.go" {
		t.Errorf("halt = %q at %q, want input_exceeds_budget at src/f0003.go", plan.HaltReason, plan.HaltPath)
	}
	// Trying f0001 against the open batch flushed f0000 first, so the two
	// small files land in one batch each.
	if got := batchSizes(plan); len(got) != 2 || got[0] != 1 || got[1] != 1 {
		t.Errorf("batches = %v, want [f0000] then [f0002]", got)
	}
	if len(plan.Unbatched) != 2 || plan.Unbatched[0].Path != "src/f0003.go" {
		t.Errorf("unbatched = %v, want f0003 and f0004", plan.Unbatched)
	}
}

// The skip reason names the signal, the byte size and the budget.
func TestSkipReason(t *testing.T) {
	unit := intel.FileUnit{Path: "src/webAssets.ts", Generated: intel.SignalHeader, Body: make([]byte, 350797)}
	want := "generated (header), 350797 bytes, over the 184320-byte prompt budget"
	if got := intel.SkipReason(unit); got != want {
		t.Errorf("SkipReason = %q, want %q", got, want)
	}
}

// ParseSkipReason reads back every signal SkipReason writes, and refuses a
// policy exclusion, which shares the generation's exclusion list.
func TestSkipReasonRoundTrip(t *testing.T) {
	for _, signal := range []string{intel.SignalLockfile, intel.SignalBundleName, intel.SignalHeader, intel.SignalMinified} {
		unit := intel.FileUnit{Generated: signal, Body: make([]byte, 350797)}
		reason := intel.SkipReason(unit)
		gotSignal, gotSize, gotBudget, ok := intel.ParseSkipReason(reason)
		if !ok || gotSignal != signal || gotSize != 350797 || gotBudget != intel.MaxPromptBytes {
			t.Errorf("ParseSkipReason(%q) = %q, %d, %d, %v", reason, gotSignal, gotSize, gotBudget, ok)
		}
		if again := intel.SkipReasonText(gotSignal, gotSize); again != reason {
			t.Errorf("SkipReasonText = %q, want %q", again, reason)
		}
	}
	for _, reason := range []string{intel.GeneratedAttributeReason, "backlog destination", ""} {
		if _, _, _, ok := intel.ParseSkipReason(reason); ok {
			t.Errorf("ParseSkipReason(%q) parsed a non-skip", reason)
		}
	}
}

// A generated file past 400 reviewable files is carried. A skip inside the
// bound frees its slot for the next file.
func TestBatchesCarryRatherThanSkipPastThePassBudget(t *testing.T) {
	scope := batchScope(t, 401, []byte("package f\n"))
	scope = generatedUnit(scope, 400, intel.SignalHeader, true)
	plan := intel.Batches(scope, nil, sizeRender(64))
	if len(plan.Skipped) != 0 {
		t.Errorf("skipped = %v, want none: the 401st file was never admitted", plan.Skipped)
	}
	if len(plan.Carried) != 1 || plan.Carried[0].Path != "src/f0400.go" {
		t.Errorf("carried = %v, want src/f0400.go", plan.Carried)
	}
	if plan.CarryReason != intel.CarryReviewBudgetReached {
		t.Errorf("carry reason = %q", plan.CarryReason)
	}

	// Inside the admission bound the skip frees a slot for the last file.
	scope = batchScope(t, 401, []byte("package f\n"))
	scope = generatedUnit(scope, 5, intel.SignalHeader, true)
	plan = intel.Batches(scope, nil, sizeRender(64))
	if len(plan.Skipped) != 1 || plan.Skipped[0].Path != "src/f0005.go" {
		t.Errorf("skipped = %v, want src/f0005.go", plan.Skipped)
	}
	if len(plan.Carried) != 0 {
		t.Errorf("carried = %v, want none", plan.Carried)
	}
	packed := 0
	for _, batch := range plan.Batches {
		packed += len(batch.Files)
	}
	if packed != 400 {
		t.Errorf("packed = %d, want all 400 reviewable files", packed)
	}
}

// Skipped files cannot consume the 400 review slots. Otherwise a re-drive
// skips the same 400 files and carries the first reviewable file forever.
func TestBatchesReachReviewableFileAfterFourHundredSkips(t *testing.T) {
	scope := batchScope(t, 401, []byte("package f\n"))
	for i := 0; i < 400; i++ {
		scope = generatedUnit(scope, i, intel.SignalHeader, true)
	}
	plan := intel.Batches(scope, nil, sizeRender(64))
	if len(plan.Skipped) != 400 {
		t.Errorf("skipped = %d, want 400", len(plan.Skipped))
	}
	if len(plan.Carried) != 0 || plan.HaltReason != "" {
		t.Errorf("carry = %d, halt = %q, want neither", len(plan.Carried), plan.HaltReason)
	}
	if len(plan.Batches) != 1 || len(plan.Batches[0].Files) != 1 || plan.Batches[0].Files[0].Path != "src/f0400.go" {
		t.Errorf("batches = %v, want only src/f0400.go", plan.Batches)
	}
}
