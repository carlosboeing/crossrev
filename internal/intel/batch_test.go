package intel_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
