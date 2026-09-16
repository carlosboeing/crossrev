package review_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// findingAnswer answers n numbered units with one finding on the first unit:
// finding number 1 names the returned finding, every other unit no_issue.
func findingAnswer(t *testing.T, firstPath string, rest []string, title string) string {
	t.Helper()
	paths := append([]string{firstPath}, rest...)
	var b strings.Builder
	b.WriteString(`{"verdict":"issues-remain","blocked_reason":null,"findings":[{"number":1,"path":"` + firstPath + `","line":1,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"` + title + `","why":"A failed request reads as success","fix":"Check it"}],"coverage":[`)
	for i, path := range paths {
		if i > 0 {
			b.WriteByte(',')
		}
		if i == 0 {
			b.WriteString(`{"unit_number":` + itoa2(i+1) + `,"verdict":"finding","finding_numbers":[1],"evidence":[{"path":"` + path + `","revision":"` + headSHA + `","start_line":1,"end_line":1,"source":"git","note":null}],"reason":null}`)
		} else {
			b.WriteString(`{"unit_number":` + itoa2(i+1) + `,"verdict":"no_issue","finding_numbers":[],"evidence":[{"path":"` + path + `","revision":"` + headSHA + `","start_line":null,"end_line":null,"source":"git","note":null}],"reason":null}`)
		}
	}
	b.WriteString(`],"examined_scope":"read the batch","known_limits":[]}`)
	return b.String()
}

func parseTestFindings(t *testing.T, raw json.RawMessage) []map[string]json.RawMessage {
	t.Helper()
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("findings decode: %v", err)
	}
	return out
}

// TestReviewHaltListsOutstandingPaths pins the bounded halt record: a file
// that cannot fit alone in one rendered prompt halts the pass with the halt
// word, the stop counts and the outstanding path, and no convergence.
func TestReviewHaltListsOutstandingPaths(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "huge.go", "package huge\n"+strings.Repeat("// filler line to exceed the prompt budget\n", 8000))
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeHalted {
		t.Fatalf("Outcome = %q, want halted", got.Outcome)
	}
	if got.Reason != "input_exceeds_budget" {
		t.Errorf("Reason = %q, want input_exceeds_budget", got.Reason)
	}
	stop, ok := got.Marker.CoverageStop.Get()
	if !ok {
		t.Fatal("halted marker carries no coverage_stop")
	}
	if stop.OutstandingCount != 1 || stop.RequiredCount != 1 {
		t.Errorf("stop = %+v, want required 1 outstanding 1", stop)
	}
	if len(e.runner.Specs()) != 0 {
		t.Errorf("harness calls = %d, want 0 (halt before the first batch)", len(e.runner.Specs()))
	}
	var halted bool
	for _, label := range e.forge.labelsAdded {
		if label == "crossrev/halted" {
			halted = true
		}
	}
	if !halted {
		t.Errorf("labels added = %v, want crossrev/halted", e.forge.labelsAdded)
	}
	if got.Marker.State != core.PassIncomplete {
		t.Errorf("marker state = %q, want incomplete", got.Marker.State)
	}
}

// TestReviewBoundedHaltKeepsTheLastCompleteGeneration pins that a halt never
// deletes the last complete generation: the ledger's current generation
// stands even when the pass stops early.
func TestReviewBoundedHaltKeepsTheLastCompleteGeneration(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	first := runLeg(t, e, e.request(t))
	if first.Err != nil {
		t.Fatalf("first Run: %v", first.Err)
	}
	if first.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked", first.Outcome)
	}
	gens := ledgerGenerations(t, e)
	if len(gens) == 0 {
		t.Fatal("no complete generation after the accepted batch")
	}
}

// TestReviewCoveredPassAppliesNoHaltedLabel pins that a covered pass applies
// no halted label: the halted label belongs to bounded halts alone.
func TestReviewCoveredPassAppliesNoHaltedLabel(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	for _, label := range e.forge.labelsAdded {
		if strings.Contains(label, "halted") {
			t.Fatalf("covered pass applied a halted label: %q", label)
		}
	}
}

// TestReviewPublishesFindingsFromEveryBatch pins the multi-batch union: 41
// required files pack into two batches (40 + 1), each batch's finding
// reaches the marker, and the current generation covers all 41 units.
func TestReviewPublishesFindingsFromEveryBatch(t *testing.T) {
	e := newEnv(t)
	var first, rest []string
	for i := 1; i <= 41; i++ {
		path := fmt.Sprintf("file%02d.go", i)
		writeRequiredHead(e, path, "package x\n")
		if i <= 40 {
			first = append(first, path)
		} else {
			rest = append(rest, path)
		}
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(findingAnswer(t, first[0], first[1:], "First batch finding"))},
		{ExitCode: 0, Stdout: claudeStdout(findingAnswer(t, rest[0], nil, "Second batch finding"))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 2 {
		t.Fatalf("harness calls = %d, want 2 (two batches)", e.runner.calls)
	}
	findings := parseTestFindings(t, got.Marker.Findings)
	if len(findings) != 2 {
		t.Fatalf("marker findings = %d, want 2 (one per batch)", len(findings))
	}
	gens := ledgerGenerations(t, e)
	last := gens[len(gens)-1]
	covered := 0
	for _, record := range last.Records {
		if record.Type == prstate.CoverageRecordUnit && record.Verdict.Present() {
			covered++
		}
	}
	if covered != 41 {
		t.Fatalf("covered units = %d, want 41 (every required file with a verdict)", covered)
	}
}

// TestReviewResumeSkipsCoveredBatches pins resumption across runs: after a
// first run covers the one required file, a second run at the same revision
// reuses the accepted verdict, invokes no batch, and still reports
// invoked.
func TestReviewResumeSkipsCoveredBatches(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	first := runLeg(t, e, e.request(t))
	if first.Err != nil {
		t.Fatalf("first Run: %v", first.Err)
	}
	calls := e.runner.calls
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	second := runLeg(t, e, e.request(t))
	if second.Err != nil {
		t.Fatalf("second Run: %v", second.Err)
	}
	if second.Outcome != review.OutcomeInvoked {
		t.Fatalf("second Outcome = %q, want invoked", second.Outcome)
	}
	if e.runner.calls != calls {
		t.Fatalf("second run invoked the harness %d more time(s), want 0 (resumed)", e.runner.calls-calls)
	}
}

// acceptAll batches the validation seam for cases that measure packing and
// prompt shape rather than the answer check: any payload passes.
func acceptAll(e *env) {
	e.validate = func([]byte, validate.ReviewExpectations) error { return nil }
}

// TestReviewSplitsAnOversizedDiffAcrossBatches reproduces the measurement
// behind per-batch diff slicing: a rendered diff past the 180 KiB prompt
// budget, spread across 41 individually small files, used to halt every
// candidate — even a one-file batch carried the whole diff — so the pass made
// zero model calls and stopped on input_exceeds_budget. With each batch
// carrying only its own files' hunks, splitting shrinks the input and the
// pass covers the scope.
func TestReviewSplitsAnOversizedDiffAcrossBatches(t *testing.T) {
	e := newEnv(t)
	var raw strings.Builder
	filler := strings.Repeat("+// a rendered line of change to price the batch\n", 100)
	for i := 1; i <= 41; i++ {
		path := fmt.Sprintf("file%02d.go", i)
		writeRequiredHead(e, path, "package x\n")
		fmt.Fprintf(&raw, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1,1 +1,101 @@\n context\n%s", path, path, path, path, filler)
	}
	e.forge.diff = []byte(raw.String())
	if len(e.forge.diff) <= intel.MaxPromptBytes {
		t.Fatalf("fixture diff is %d bytes, want it past the %d budget so the whole diff cannot fit one prompt", len(e.forge.diff), intel.MaxPromptBytes)
	}
	acceptAll(e)
	prompts := capturePrompt(e)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked (splitting must shrink the input, not halt)", got.Outcome)
	}
	if e.runner.calls < 2 {
		t.Fatalf("harness calls = %d, want at least 2 (the budget splits the 41 files into batches)", e.runner.calls)
	}
	for i, prompt := range *prompts {
		if len(prompt) > intel.MaxPromptBytes {
			t.Errorf("prompt %d is %d bytes, over the %d budget the packer measured against", i+1, len(prompt), intel.MaxPromptBytes)
		}
	}
}

// TestReviewBatchDiffDropsFilesOutsideTheBatch pins the positive selection:
// one small required file beside a generated file the scope never required.
// The batch prompt slices the diff to its own files, so a section the batch
// does not hold cannot price the pass out, no matter its size.
func TestReviewBatchDiffDropsFilesOutsideTheBatch(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "small.go", "package small\n")
	e.forge.diff = []byte("diff --git a/small.go b/small.go\n--- a/small.go\n+++ b/small.go\n@@ -1,1 +1,2 @@\n context\n+added\n" +
		"diff --git a/gen/big.go b/gen/big.go\n--- a/gen/big.go\n+++ b/gen/big.go\n@@ -1,1 +1,8001 @@\n context\n" +
		strings.Repeat("+generated line priced out of every batch\n", 8000))
	acceptAll(e)
	prompts := capturePrompt(e)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked (the batch's own diff fits)", got.Outcome)
	}
	if len(*prompts) != 1 {
		t.Fatalf("prompts = %d, want 1 (one file, one batch)", len(*prompts))
	}
	prompt := (*prompts)[0]
	if !strings.Contains(prompt, "b/small.go") {
		t.Error("prompt dropped the batch's own diff section")
	}
	if strings.Contains(prompt, "gen/big.go") {
		t.Error("prompt carried a diff section the batch does not hold")
	}
	if len(prompt) > intel.MaxPromptBytes {
		t.Errorf("prompt is %d bytes, over the %d budget", len(prompt), intel.MaxPromptBytes)
	}
}

// TestReviewDiscoversSharedContextOncePerPass pins the cached snapshot: the
// packer measures one candidate per admitted file, and each measurement used
// to repeat advisory discovery, the diff read and the thread fetch — four
// files carrying two distinct search terms meant ten ExactSearch calls before
// the first model invocation. The shared context is discovered once per pass
// and candidates render from it.
func TestReviewDiscoversSharedContextOncePerPass(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 4; i++ {
		writeRequiredHead(e, fmt.Sprintf("f%d.go", i), "alphaBeta gammaDelta\n")
	}
	acceptAll(e)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked", got.Outcome)
	}
	if e.vcs.searchCalls != 2 {
		t.Errorf("ExactSearch calls = %d, want 2 (one per distinct term, once for the pass)", e.vcs.searchCalls)
	}
	// Two thread fetches stand: the snapshot every candidate and batch
	// renders from, and the attach the publish path records findings with.
	if e.forge.threadCalls != 2 {
		t.Errorf("ReviewThreads calls = %d, want 2 (the pass snapshot and the publish-path attach)", e.forge.threadCalls)
	}
	// Two diff reads stand: the snapshot every candidate and batch renders
	// from, and the enrich read the publish path anchors findings against.
	if e.forge.diffCalls != 2 {
		t.Errorf("PullRequestDiff calls = %d, want 2 (the pass snapshot and the publish-path enrich)", e.forge.diffCalls)
	}
}
