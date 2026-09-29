package intel_test

import (
	"fmt"
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

// callMeasure measures a call as its file bodies and part diffs plus a
// fixed extra for headers and prior context. The extra is what makes
// rendered measurement differ from counting file bytes alone.
func callMeasure(extra int) intel.MeasureCall {
	return func(call intel.Call) int {
		total := extra
		for _, f := range call.Files {
			total += len(f.Body) + len(f.Diff)
		}
		if call.Part != nil {
			total += len(call.Part.Diff)
		}
		return total
	}
}

func planOpts(pack, hard, shared int) intel.PlanOptions {
	return intel.PlanOptions{
		Limits:      intel.Limits{WindowTokens: 200000, PackBytes: pack, HardBytes: hard},
		SharedBytes: shared,
	}
}

func callSizes(plan intel.BatchPlan) []int {
	var out []int
	for _, c := range plan.Calls {
		out = append(out, len(c.Files))
	}
	return out
}

// TestPlanCallsMeasureTheRenderedPrompt holds one file whose bytes fit the
// packing limit on their own, then measures it with headers and prior
// context that push it over. Only the rendered size may decide: the file
// must split into parts with no halt and no skip.
func TestPlanCallsMeasureTheRenderedPrompt(t *testing.T) {
	body := append([]byte("package f\n"), []byte(strings.Repeat("// a filler line to price the call\n", 3000))...)
	const pack, hard = 184320, 368640
	if len(body) >= pack {
		t.Fatalf("fixture body is %d bytes, want it below the %d packing limit so bytes alone would fit", len(body), pack)
	}
	scope := batchScope(t, 1, body)
	const shared = 100 * 1024
	measure := callMeasure(shared)
	plan := intel.PlanCalls(scope, nil, planOpts(pack, hard, shared), measure)

	if plan.HaltReason != "" {
		t.Fatalf("halt reason = %q, want none: an oversized file splits", plan.HaltReason)
	}
	if len(plan.Skipped) != 0 {
		t.Fatalf("skipped = %v, want none: a plain file splits", plan.Skipped)
	}
	parts := 0
	for _, call := range plan.Calls {
		if call.Part == nil {
			t.Fatalf("a call holds whole files, want only parts for a file that fits no call alone")
		}
		parts++
		if got := measure(call); got > pack {
			t.Errorf("part %d measures %d bytes with shared context, over the %d packing limit", call.Part.Index, got, pack)
		}
	}
	if parts < 2 {
		t.Errorf("split into %d parts, want at least 2", parts)
	}
}

func TestPlanCallsPackAtMostFortyFiles(t *testing.T) {
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
			plan := intel.PlanCalls(scope, nil, planOpts(1<<20, 1<<21, 128), callMeasure(0))
			got := callSizes(plan)
			if len(got) != len(tc.want) {
				t.Fatalf("calls = %v, want sizes %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("calls = %v, want sizes %v", got, tc.want)
					break
				}
			}
			if plan.HaltReason != "" {
				t.Errorf("halt reason = %q, want none", plan.HaltReason)
			}
			for _, call := range plan.Calls {
				if call.Part != nil {
					t.Errorf("a small file planned as a part")
				}
			}
		})
	}
}

func TestPlanCallsAdmitAtMostFourHundredPerPass(t *testing.T) {
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
			plan := intel.PlanCalls(scope, nil, planOpts(1<<20, 1<<21, 64), callMeasure(0))
			batched := 0
			for _, c := range plan.Calls {
				batched += len(c.Files)
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

func TestPlanCallsSplitOnTheRenderedByteBoundary(t *testing.T) {
	const pack, hard = 184320, 368640
	half := pack / 2
	exact := batchScope(t, 2, make([]byte, half))
	plan := intel.PlanCalls(exact, nil, planOpts(pack, hard, 0), callMeasure(0))
	if got := callSizes(plan); len(got) != 1 || got[0] != 2 {
		t.Errorf("exact-budget calls = %v, want one call of 2", got)
	}

	over := batchScope(t, 2, make([]byte, half+1))
	plan = intel.PlanCalls(over, nil, planOpts(pack, hard, 0), callMeasure(0))
	if got := callSizes(plan); len(got) != 2 {
		t.Errorf("over-budget calls = %v, want two calls of 1", got)
	}
}

// A file that fits no call alone splits into measured parts rather than
// halting: every part's call fits the packing limit, the parts reassemble
// to the whole body, and the plan carries no halt and no skip.
func TestPlanCallsSplitAFileThatFitsNoCallAlone(t *testing.T) {
	const pack, hard = 184320, 368640
	body := []byte("package f\n" + strings.Repeat("// a filler line to force a split\n", 8000))
	scope := batchScope(t, 1, body)
	plan := intel.PlanCalls(scope, nil, planOpts(pack, hard, 0), callMeasure(0))

	if plan.HaltReason != "" {
		t.Fatalf("halt reason = %q, want none: an oversized file splits", plan.HaltReason)
	}
	if len(plan.Skipped) != 0 {
		t.Fatalf("skipped = %v, want none", plan.Skipped)
	}
	if len(plan.Calls) < 2 {
		t.Fatalf("calls = %d, want at least 2 parts", len(plan.Calls))
	}
	var reassembled strings.Builder
	for i, call := range plan.Calls {
		if call.Part == nil {
			t.Fatalf("call %d holds whole files, want a part", i)
		}
		part := call.Part
		if part.Index != i || part.Count != len(plan.Calls) {
			t.Errorf("part position = %d of %d, want %d of %d", part.Index, part.Count, i, len(plan.Calls))
		}
		if len(part.Diff) == 0 {
			t.Errorf("part %d carries no diff", i)
		}
		if got := callMeasure(0)(call); got > pack {
			t.Errorf("part %d measures %d bytes, over the %d packing limit", i, got, pack)
		}
		reassembled.Write(part.Diff)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(reassembled.String(), line) {
			t.Fatalf("reassembled parts miss body line %q", line)
		}
	}
}

func TestPlanCallsArePathOrderedAndSkipAccepted(t *testing.T) {
	scope := batchScope(t, 3, []byte("package f\n"))
	scope.Required[0], scope.Required[2] = scope.Required[2], scope.Required[0]
	accepted := map[core.UnitID]bool{scope.Required[1].ID: true}
	plan := intel.PlanCalls(scope, accepted, planOpts(1<<20, 1<<21, 64), callMeasure(0))
	if len(plan.Calls) != 1 {
		t.Fatalf("calls = %v, want one call", callSizes(plan))
	}
	files := plan.Calls[0].Files
	if len(files) != 2 || files[0].Path != "src/f0000.go" || files[1].Path != "src/f0002.go" {
		t.Errorf("call holds %v, want path-ordered src/f0000.go and src/f0002.go", files)
	}
}

func TestPlanCallsEmptyScopeSchedulesNothing(t *testing.T) {
	base, head := stubRevisions(t)
	scope := intel.Scope{Base: base, Head: head, Engine: core.FileEngineVersion, EngineID: core.FileEngineID()}
	plan := intel.PlanCalls(scope, nil, planOpts(1<<20, 1<<21, 64), callMeasure(0))
	if len(plan.Calls) != 0 || len(plan.Carried) != 0 || plan.HaltReason != "" || plan.CarryReason != "" || plan.OverBudget {
		t.Errorf("empty scope plan = %+v, want no calls and no reasons", plan)
	}
}

// hunkDiff builds a per-file diff of hunks hunkCount, each carrying
// linesPerHunk changed lines padded to lineBytes, so tests control part
// counts exactly.
func hunkDiff(path string, hunkCount, linesPerHunk, lineBytes int) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n", path, path, path, path)
	line := strings.Repeat("x", lineBytes-1) + "\n"
	for h := 0; h < hunkCount; h++ {
		start := h*linesPerHunk + 1
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@ func part%d()\n", start, linesPerHunk, start, linesPerHunk, h)
		for l := 0; l < linesPerHunk; l++ {
			b.WriteString("-" + line)
			b.WriteString("+" + line)
		}
	}
	return []byte(b.String())
}

// A file with hunk structure splits at hunk boundaries: no hunk is cut,
// every part repeats the header, and the parts' blocks reassemble to the
// original hunks in order.
func TestPlanCallsSplitAtHunkBoundaries(t *testing.T) {
	const pack, hard = 500, 1000
	scope := batchScope(t, 1, []byte("package f\n"))
	path := scope.Required[0].Path
	raw := hunkDiff(path, 3, 4, 28)
	scope.Required[0].Diff = raw
	plan := intel.PlanCalls(scope, nil, planOpts(pack, hard, 0), callMeasure(0))

	if plan.HaltReason != "" {
		t.Fatalf("halt = %q, want none", plan.HaltReason)
	}
	if len(plan.Calls) != 3 {
		t.Fatalf("calls = %d, want 3 (one ~250-byte hunk per part)", len(plan.Calls))
	}
	var blocks []string
	for i, call := range plan.Calls {
		if call.Part == nil {
			t.Fatalf("call %d holds whole files, want a part", i)
		}
		text := string(call.Part.Diff)
		if !strings.Contains(text, "diff --git") || !strings.Contains(text, "+++ b/") {
			t.Errorf("part %d carries no diff header", i)
		}
		if got := callMeasure(0)(call); got > pack {
			t.Errorf("part %d measures %d bytes, over the %d packing limit", i, got, pack)
		}
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "@@") {
				blocks = append(blocks, line)
			}
		}
	}
	if len(blocks) != 3 {
		t.Fatalf("parts carry %d hunk headers, want the original 3 uncut", len(blocks))
	}
	want := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "@@") {
			if blocks[want] != line {
				t.Fatalf("part hunk %d = %q, want original %q", want, blocks[want], line)
			}
			want++
		}
	}
}

// One oversized hunk splits into line chunks with continuous gutters:
// each chunk's @@ header picks up where the previous chunk's lines end.
func TestAnOversizedHunkSplitsIntoLineChunks(t *testing.T) {
	const pack, hard = 700, 1400
	scope := batchScope(t, 1, []byte("package f\n"))
	path := scope.Required[0].Path
	scope.Required[0].Diff = hunkDiff(path, 1, 40, 28)
	plan := intel.PlanCalls(scope, nil, planOpts(pack, hard, 0), callMeasure(0))

	if len(plan.Calls) < 2 {
		t.Fatalf("calls = %d, want line chunks of the one hunk", len(plan.Calls))
	}
	type span struct{ oldStart, oldLen, newStart, newLen int }
	var spans []span
	var lines []string
	for i, call := range plan.Calls {
		if call.Part == nil {
			t.Fatalf("call %d holds whole files, want a part", i)
		}
		if got := callMeasure(0)(call); got > pack {
			t.Errorf("part %d measures %d bytes, over the %d packing limit", i, got, pack)
		}
		var head string
		for _, line := range strings.Split(string(call.Part.Diff), "\n") {
			if strings.HasPrefix(line, "@@") {
				if head != "" {
					t.Fatalf("part %d carries two hunk headers: one hunk must chunk, not fork", i)
				}
				head = line
				continue
			}
			if head == "" || line == "" {
				continue
			}
			lines = append(lines, line)
		}
		var s span
		if _, err := fmt.Sscanf(head, "@@ -%d,%d +%d,%d @@", &s.oldStart, &s.oldLen, &s.newStart, &s.newLen); err != nil {
			t.Fatalf("part %d header does not parse: %q", i, head)
		}
		spans = append(spans, s)
	}
	for i := 1; i < len(spans); i++ {
		if spans[i].newStart != spans[i-1].newStart+spans[i-1].newLen {
			t.Errorf("chunk %d starts at new line %d, want %d: gutters must continue",
				i, spans[i].newStart, spans[i-1].newStart+spans[i-1].newLen)
		}
		if spans[i].oldStart != spans[i-1].oldStart+spans[i-1].oldLen {
			t.Errorf("chunk %d starts at old line %d, want %d: gutters must continue",
				i, spans[i].oldStart, spans[i-1].oldStart+spans[i-1].oldLen)
		}
	}
	if len(lines) != 80 {
		t.Errorf("chunks carry %d content lines, want the hunk's 80", len(lines))
	}
}

// Shared context alone past the hard limit halts the pass before any
// file is scheduled: no file fits beside it.
func TestSharedContextOverHardHalts(t *testing.T) {
	const pack, hard = 184320, 368640
	scope := batchScope(t, 2, []byte("package f\n"))
	plan := intel.PlanCalls(scope, nil, planOpts(pack, hard, hard+1), callMeasure(0))
	if plan.HaltReason != intel.HaltSharedContextExceedsWindow {
		t.Fatalf("halt reason = %q, want shared_context_exceeds_window", plan.HaltReason)
	}
	if len(plan.Calls) != 0 {
		t.Errorf("calls = %v, want none under a shared-context halt", callSizes(plan))
	}
}

// Shared context between 0.75 x P and H still runs and records
// over_budget, packing against P until shared context passes it.
func TestSharedContextInBandRunsOverBudget(t *testing.T) {
	const pack, hard = 184320, 368640
	shared := int(0.75*float64(pack)) + 1000
	scope := batchScope(t, 2, []byte("package f\n"))
	plan := intel.PlanCalls(scope, nil, planOpts(pack, hard, shared), callMeasure(0))
	if plan.HaltReason != "" {
		t.Fatalf("halt = %q, want none: the band runs", plan.HaltReason)
	}
	if !plan.OverBudget {
		t.Error("OverBudget = false, want true between 0.75P and H")
	}
	measure := callMeasure(shared)
	for i, call := range plan.Calls {
		if got := measure(call); got > hard {
			t.Errorf("call %d measures %d bytes, over the %d hard limit", i, got, hard)
		}
	}
}

// No budget packs nothing: a zero packing limit halts rather than
// scheduling calls no prompt could hold.
func TestPlanCallsHaltWithNoBudget(t *testing.T) {
	scope := batchScope(t, 1, []byte("package f\n"))
	plan := intel.PlanCalls(scope, nil, intel.PlanOptions{}, callMeasure(0))
	if plan.HaltReason != intel.HaltSharedContextExceedsWindow {
		t.Fatalf("halt = %q, want shared_context_exceeds_window", plan.HaltReason)
	}
}

// TestBatchingMatchesTheFrozenFixture requires the packing constants to
// equal the frozen bounds. The rendered-byte bound is per harness — the
// fixture pins the file and pass counts, and budget_test.go pins each
// window's bytes.
func TestBatchingMatchesTheFrozenFixture(t *testing.T) {
	oracle := loadAcceptanceOracle(t)
	if intel.MaxFilesPerBatch != oracle.Batching.MaxFilesPerBatch {
		t.Errorf("MaxFilesPerBatch = %d, want frozen %d", intel.MaxFilesPerBatch, oracle.Batching.MaxFilesPerBatch)
	}
	if intel.MaxUnitsPerPass != oracle.Batching.MaxUnitsPerPass {
		t.Errorf("MaxUnitsPerPass = %d, want frozen %d", intel.MaxUnitsPerPass, oracle.Batching.MaxUnitsPerPass)
	}
}

// generatedUnit marks the i-th required file with a built-in signal and,
// when big is set, a body no rendered prompt can hold alone.
func generatedUnit(scope intel.Scope, i int, signal string, big bool) intel.Scope {
	scope.Required[i].Generated = signal
	if big {
		body := append([]byte("package f\n"), []byte(strings.Repeat("// a filler line to force a split\n", 8000))...)
		scope.Required[i].Body = body
		scope.Required[i].BodyDigest = core.BodyDigestHex(body)
	}
	return scope
}

// A generated file that would need splitting is skipped and packing
// continues: the files after it are still reviewed.
func TestPlanCallsSkipGeneratedFilesNeedingASplit(t *testing.T) {
	const pack, hard = 184320, 368640
	scope := batchScope(t, 3, []byte("package f\n"))
	scope = generatedUnit(scope, 0, intel.SignalHeader, true)
	plan := intel.PlanCalls(scope, nil, planOpts(pack, hard, 0), callMeasure(0))

	if plan.HaltReason != "" {
		t.Errorf("halt = %q, want none: a generated file skips", plan.HaltReason)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].Path != "src/f0000.go" {
		t.Fatalf("skipped = %v, want src/f0000.go", plan.Skipped)
	}
	if got := callSizes(plan); len(got) != 1 || got[0] != 2 {
		t.Errorf("calls = %v, want one call holding the two small files", got)
	}
}

// The same size with no signal splits exactly as before: only a
// recognised generated file turns the split into a skip.
func TestPlanCallsSplitAPlainFileOfTheSameSize(t *testing.T) {
	const pack, hard = 184320, 368640
	scope := batchScope(t, 3, []byte("package f\n"))
	scope = generatedUnit(scope, 0, "", true)
	plan := intel.PlanCalls(scope, nil, planOpts(pack, hard, 0), callMeasure(0))
	if plan.HaltReason != "" {
		t.Fatalf("halt reason = %q, want none: a plain file splits", plan.HaltReason)
	}
	if len(plan.Skipped) != 0 {
		t.Errorf("skipped = %v, want none", plan.Skipped)
	}
	parts := 0
	for _, call := range plan.Calls {
		if call.Part != nil {
			parts++
		}
	}
	if parts == 0 {
		t.Error("no part call scheduled for the oversized plain file")
	}
}

// A handwritten Markdown file too large for one prompt splits rather
// than skipping: its long unwrapped lines are not a generated signal, so
// nothing the author wrote is dropped from review behind a "generated"
// warning.
func TestPlanCallsSplitUnwrappedMarkdown(t *testing.T) {
	const pack, hard = 184320, 368640
	scope := batchScope(t, 3, []byte("package f\n"))
	paragraph := strings.Repeat("An unwrapped paragraph written by a person. ", 10) + "\n"
	body := []byte(strings.Repeat(paragraph, 400))
	scope.Required[0].Path = "CHANGELOG.md"
	scope.Required[0].Body = body
	scope.Required[0].BodyDigest = core.BodyDigestHex(body)
	scope.Required[0].Generated = intel.GeneratedSignal("CHANGELOG.md", body)

	plan := intel.PlanCalls(scope, nil, planOpts(pack, hard, 0), callMeasure(0))
	if plan.HaltReason != "" {
		t.Fatalf("halt = %q, want none: a handwritten file splits", plan.HaltReason)
	}
	if len(plan.Skipped) != 0 {
		t.Errorf("skipped = %v, want none", plan.Skipped)
	}
}

// A generated file that fits is packed like any other: the signal only
// matters where a split would start.
func TestPlanCallsPackGeneratedFilesThatFit(t *testing.T) {
	scope := batchScope(t, 2, []byte("package f\n"))
	scope = generatedUnit(scope, 0, intel.SignalHeader, false)
	scope = generatedUnit(scope, 1, intel.SignalLockfile, false)
	plan := intel.PlanCalls(scope, nil, planOpts(1<<20, 1<<21, 64), callMeasure(0))
	if len(plan.Skipped) != 0 {
		t.Errorf("skipped = %v, want none", plan.Skipped)
	}
	if got := callSizes(plan); len(got) != 1 || got[0] != 2 {
		t.Errorf("calls = %v, want one call of 2", got)
	}
}

// An oversized lockfile skips like any other oversized generated file.
func TestPlanCallsSkipOversizedLockfiles(t *testing.T) {
	const pack, hard = 184320, 368640
	scope := batchScope(t, 2, []byte("package f\n"))
	scope.Required[1].Path = "web/package-lock.json"
	scope.Required[1].ID = core.FileUnitID("web/package-lock.json")
	scope = generatedUnit(scope, 1, intel.SignalLockfile, true)
	plan := intel.PlanCalls(scope, nil, planOpts(pack, hard, 0), callMeasure(0))
	if len(plan.Skipped) != 1 || plan.Skipped[0].Path != "web/package-lock.json" {
		t.Fatalf("skipped = %v, want the lockfile", plan.Skipped)
	}
	if got := callSizes(plan); len(got) != 1 || got[0] != 1 {
		t.Errorf("calls = %v, want one call of the small file", got)
	}
}

// The skip reason names the signal, the byte size and the packing limit
// in force.
func TestSkipReason(t *testing.T) {
	unit := intel.FileUnit{Path: "src/webAssets.ts", Generated: intel.SignalHeader, Body: make([]byte, 350797)}
	want := "generated (header), 350797 bytes, over the 390000-byte prompt budget"
	if got := intel.SkipReason(unit, 390000); got != want {
		t.Errorf("SkipReason = %q, want %q", got, want)
	}
}

// ParseSkipReason reads back every signal SkipReason writes, and refuses a
// policy exclusion, which shares the generation's exclusion list.
func TestSkipReasonRoundTrip(t *testing.T) {
	for _, signal := range []string{intel.SignalLockfile, intel.SignalBundleName, intel.SignalHeader, intel.SignalMinified} {
		unit := intel.FileUnit{Generated: signal, Body: make([]byte, 350797)}
		reason := intel.SkipReason(unit, 390000)
		gotSignal, gotSize, gotBudget, ok := intel.ParseSkipReason(reason)
		if !ok || gotSignal != signal || gotSize != 350797 || gotBudget != 390000 {
			t.Errorf("ParseSkipReason(%q) = %q, %d, %d, %v", reason, gotSignal, gotSize, gotBudget, ok)
		}
		if again := intel.SkipReasonText(gotSignal, gotSize, gotBudget); again != reason {
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
func TestPlanCallsCarryRatherThanSkipPastThePassBudget(t *testing.T) {
	const pack, hard = 184320, 368640
	scope := batchScope(t, 401, []byte("package f\n"))
	scope = generatedUnit(scope, 400, intel.SignalHeader, true)
	plan := intel.PlanCalls(scope, nil, planOpts(pack, hard, 64), callMeasure(0))
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
	plan = intel.PlanCalls(scope, nil, planOpts(pack, hard, 64), callMeasure(0))
	if len(plan.Skipped) != 1 || plan.Skipped[0].Path != "src/f0005.go" {
		t.Errorf("skipped = %v, want src/f0005.go", plan.Skipped)
	}
	if len(plan.Carried) != 0 {
		t.Errorf("carried = %v, want none", plan.Carried)
	}
	packed := 0
	for _, call := range plan.Calls {
		packed += len(call.Files)
	}
	if packed != 400 {
		t.Errorf("packed = %d, want all 400 reviewable files", packed)
	}
}

// Skipped files cannot consume the 400 review slots. Otherwise a re-drive
// skips the same 400 files and carries the first reviewable file forever.
func TestPlanCallsReachReviewableFileAfterFourHundredSkips(t *testing.T) {
	const pack, hard = 184320, 368640
	scope := batchScope(t, 401, []byte("package f\n"))
	for i := 0; i < 400; i++ {
		scope = generatedUnit(scope, i, intel.SignalHeader, true)
	}
	plan := intel.PlanCalls(scope, nil, planOpts(pack, hard, 64), callMeasure(0))
	if len(plan.Skipped) != 400 {
		t.Errorf("skipped = %d, want 400", len(plan.Skipped))
	}
	if len(plan.Carried) != 0 || plan.HaltReason != "" {
		t.Errorf("carry = %d, halt = %q, want neither", len(plan.Carried), plan.HaltReason)
	}
	if len(plan.Calls) != 1 || len(plan.Calls[0].Files) != 1 || plan.Calls[0].Files[0].Path != "src/f0400.go" {
		t.Errorf("calls = %d, want one call holding only src/f0400.go", len(plan.Calls))
	}
}

// The merge precedence: could_not_review wins, then finding with joined
// numbers, then not_affected only when unanimous, else no_issue.
func TestMergeSplitVerdictsPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		parts   []intel.SplitVerdict
		verdict string
		numbers []int
	}{
		{name: "empty", parts: nil, verdict: "no_issue"},
		{name: "unanimous no_issue", parts: []intel.SplitVerdict{{Verdict: "no_issue"}, {Verdict: "no_issue"}}, verdict: "no_issue"},
		{name: "unanimous not_affected", parts: []intel.SplitVerdict{{Verdict: "not_affected"}, {Verdict: "not_affected"}}, verdict: "not_affected"},
		{name: "mixed not_affected and no_issue", parts: []intel.SplitVerdict{{Verdict: "not_affected"}, {Verdict: "no_issue"}}, verdict: "no_issue"},
		{
			name:    "finding joins numbers",
			parts:   []intel.SplitVerdict{{Verdict: "no_issue"}, {Verdict: "finding", FindingNumbers: []int{2}}, {Verdict: "finding", FindingNumbers: []int{1, 2}}},
			verdict: "finding", numbers: []int{1, 2},
		},
		{
			name:    "could_not_review wins over finding",
			parts:   []intel.SplitVerdict{{Verdict: "finding", FindingNumbers: []int{1}}, {Verdict: "could_not_review"}},
			verdict: "could_not_review",
		},
		{
			name:    "could_not_review wins over not_affected",
			parts:   []intel.SplitVerdict{{Verdict: "not_affected"}, {Verdict: "could_not_review"}},
			verdict: "could_not_review",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verdict, numbers := intel.MergeSplitVerdicts(tc.parts)
			if verdict != tc.verdict {
				t.Errorf("verdict = %q, want %q", verdict, tc.verdict)
			}
			if len(numbers) != len(tc.numbers) {
				t.Fatalf("numbers = %v, want %v", numbers, tc.numbers)
			}
			for i := range numbers {
				if numbers[i] != tc.numbers[i] {
					t.Fatalf("numbers = %v, want %v", numbers, tc.numbers)
				}
			}
		})
	}
}

// blocklessScope plans one unit alone: the no-block cases below replace
// the scope's single file with a shaped unit carrying no splittable
// content.
func blocklessScope(t *testing.T, unit intel.FileUnit) intel.Scope {
	t.Helper()
	scope := batchScope(t, 1, nil)
	unit.ID = scope.Required[0].ID
	unit.Path = scope.Required[0].Path
	unit.Change = core.ChangeModified
	unit.ContentRevision = scope.Required[0].ContentRevision
	unit.BodyDigest = core.BodyDigestHex(unit.Body)
	scope.Required[0] = unit
	return scope
}

// blocklessMeasure counts what the rendered prompt charges a unit with
// no readable bytes: its shaped diff and its access reason beside the
// shared context every call carries.
func blocklessMeasure(shared int) intel.MeasureCall {
	return func(call intel.Call) int {
		total := shared
		for _, f := range call.Files {
			total += len(f.Body) + len(f.Diff) + len(f.Reason)
		}
		if call.Part != nil {
			total += len(call.Part.Diff)
		}
		return total
	}
}

// planBlocklessUnit runs the split path for one unit with no hunk
// blocks: shared context sits ten bytes under the packing limit, so the
// unit fits no call alone however small its rendering.
func planBlocklessUnit(t *testing.T, unit intel.FileUnit) intel.BatchPlan {
	t.Helper()
	const pack, hard = 184320, 368640
	shared := pack - 10
	measure := blocklessMeasure(shared)
	if got := measure(intel.Call{Files: []intel.FileUnit{unit}}); got <= pack {
		t.Fatalf("fixture measures %d bytes, want it past the %d packing limit so it reaches the split path", got, pack)
	}
	return intel.PlanCalls(blocklessScope(t, unit), nil, planOpts(pack, hard, shared), measure)
}

// assertCarriedWhole requires the plan to hold the unit as one
// whole-file call: no part, no skip, no halt — the whole-file render
// shows the shaped header and reason a part would drop.
func assertCarriedWhole(t *testing.T, plan intel.BatchPlan, path string) {
	t.Helper()
	if plan.HaltReason != "" {
		t.Fatalf("halt = %q, want none: an unsplittable unit rides whole", plan.HaltReason)
	}
	if len(plan.Skipped) != 0 {
		t.Fatalf("skipped = %v, want none: a plain unit is carried, not skipped", plan.Skipped)
	}
	if len(plan.Calls) != 1 {
		t.Fatalf("calls = %d, want 1 whole-file call", len(plan.Calls))
	}
	call := plan.Calls[0]
	if call.Part != nil {
		t.Fatalf("scheduled as part %d of %d with a %d-byte diff, want the unit carried whole", call.Part.Index+1, call.Part.Count, len(call.Part.Diff))
	}
	if len(call.Files) != 1 || call.Files[0].Path != path {
		t.Fatalf("call holds %v, want the whole unit %q", call.Files, path)
	}
}

// A binary unit with no body to split on is carried whole: the
// whole-file render shows its header lines and access reason, where a
// part would show neither.
func TestPlanCallsCarriesABinaryUnitWhole(t *testing.T) {
	diff := "diff --git a/src/f0000.go b/src/f0000.go\nBinary files a/src/f0000.go and b/src/f0000.go differ\n"
	unit := intel.FileUnit{Binary: true, Reason: "binary content is not shown", Form: intel.FormDiffOnly, Diff: []byte(diff)}
	plan := planBlocklessUnit(t, unit)
	assertCarriedWhole(t, plan, "src/f0000.go")
	if got := plan.Calls[0].Files[0]; len(got.Diff) == 0 || got.Reason == "" {
		t.Errorf("carried unit keeps diff=%d bytes reason=%q, want the header and reason intact", len(got.Diff), got.Reason)
	}
}

// An unreadable unit is carried whole: its header lines and access
// reason stay on the scheduled call rather than scheduling an empty
// part.
func TestPlanCallsCarriesAnUnreadableUnitWhole(t *testing.T) {
	diff := "diff --git a/src/f0000.go b/src/f0000.go\n"
	unit := intel.FileUnit{Reason: "evidence unreadable: permission denied", Form: intel.FormDiffOnly, Diff: []byte(diff)}
	plan := planBlocklessUnit(t, unit)
	assertCarriedWhole(t, plan, "src/f0000.go")
	if got := plan.Calls[0].Files[0]; len(got.Diff) == 0 || got.Reason == "" {
		t.Errorf("carried unit keeps diff=%d bytes reason=%q, want the header and reason intact", len(got.Diff), got.Reason)
	}
}

// A shaping that found no header to show — Diff nil, the reason alone
// — is carried whole rather than scheduled as an empty part.
func TestPlanCallsCarriesAHeaderlessShapedUnitWhole(t *testing.T) {
	unit := intel.FileUnit{Reason: "no changed lines: pure rename", Form: intel.FormDiffOnly}
	plan := planBlocklessUnit(t, unit)
	assertCarriedWhole(t, plan, "src/f0000.go")
	if got := plan.Calls[0].Files[0].Reason; got == "" {
		t.Error("carried unit lost its access reason, want it intact")
	}
}

// Shared context in the band between 0.75 x P and P still packs every
// call against the packing limit: an oversized file splits into parts
// that each fit P.
func TestPlanCallsPackAgainstPackInBand(t *testing.T) {
	const pack, hard = 184320, 368640
	body := append([]byte("package f\n"), []byte(strings.Repeat("// a filler line to force a split\n", 3000))...)
	shared := int(0.75*float64(pack)) + 1000
	measure := callMeasure(shared)
	plan := intel.PlanCalls(batchScope(t, 1, body), nil, planOpts(pack, hard, shared), measure)
	if plan.HaltReason != "" {
		t.Fatalf("halt = %q, want none: the band runs", plan.HaltReason)
	}
	if !plan.OverBudget {
		t.Error("OverBudget = false, want true between 0.75P and P")
	}
	var parts int
	for i, call := range plan.Calls {
		if call.Part == nil {
			t.Fatalf("call %d holds whole files, want only parts in the band", i)
		}
		parts++
		if got := measure(call); got > pack {
			t.Errorf("part %d measures %d bytes, over the %d packing limit it packs against", call.Part.Index, got, pack)
		}
	}
	if parts < 2 {
		t.Errorf("split into %d parts, want at least 2", parts)
	}
}

// Shared context past the packing limit but inside the hard limit packs
// every call against the hard limit instead: parts may exceed P and
// must fit H.
func TestPlanCallsPackAgainstHardPastPack(t *testing.T) {
	const pack, hard = 184320, 368640
	body := append([]byte("package f\n"), []byte(strings.Repeat("// a filler line to force a split\n", 6000))...)
	shared := pack + 1000
	measure := callMeasure(shared)
	plan := intel.PlanCalls(batchScope(t, 1, body), nil, planOpts(pack, hard, shared), measure)
	if plan.HaltReason != "" {
		t.Fatalf("halt = %q, want none: shared context inside H runs", plan.HaltReason)
	}
	if !plan.OverBudget {
		t.Error("OverBudget = false, want true past P inside H")
	}
	var parts, pastPack int
	for i, call := range plan.Calls {
		if call.Part == nil {
			t.Fatalf("call %d holds whole files, want only parts past the packing limit", i)
		}
		parts++
		got := measure(call)
		if got > hard {
			t.Errorf("part %d measures %d bytes, over the %d hard limit", call.Part.Index, got, hard)
		}
		if got > pack {
			pastPack++
		}
	}
	if parts < 2 {
		t.Errorf("split into %d parts, want at least 2", parts)
	}
	if pastPack == 0 {
		t.Errorf("no part measures past the %d packing limit, want hard-limit packing past P", pack)
	}
}
