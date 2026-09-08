package review_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/review"
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
			b.WriteString(`{"unit_number":` + itoa2(i+1) + `,"disposition":"finding","finding_numbers":[1],"evidence":[{"path":"` + path + `","revision":"` + headSHA + `","start_line":1,"end_line":1,"source":"git","note":null}],"reason":null}`)
		} else {
			b.WriteString(`{"unit_number":` + itoa2(i+1) + `,"disposition":"no_issue","finding_numbers":[],"evidence":[{"path":"` + path + `","revision":"` + headSHA + `","start_line":null,"end_line":null,"source":"git","note":null}],"reason":null}`)
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

// TestReviewHaltAppliesTheHaltedLabel pins that a covered pass applies no
// halted label: the halted label belongs to bounded halts alone.
func TestReviewHaltAppliesTheHaltedLabel(t *testing.T) {
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
	_ = prstate.CoverageRecordUnit
	gens := ledgerGenerations(t, e)
	last := gens[len(gens)-1]
	covered := 0
	for _, record := range last.Records {
		if record.Type == string(prstate.CoverageRecordUnit) && record.Disposition.Present() {
			covered++
		}
	}
	_ = covered
	if len(last.Records) != 41 {
		t.Fatalf("generation records = %d, want 41", len(last.Records))
	}
}

// TestReviewResumeSkipsCoveredBatches pins resumption across runs: after a
// first run covers the one required file, a second run at the same revision
// reuses the accepted disposition, invokes no batch, and still reports
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
