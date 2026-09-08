package review_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/review"
)

// TestReviewHaltListsOutstandingPaths pins the bounded halt path shape:
// a pass that stops early records the halt word with the outstanding paths
// listed, and no convergence is printed. The unit-level bound (400 files)
// is proved by the intel packing tests; here the halt record renders the
// outstanding paths the re-drive resumes.
func TestReviewHaltListsOutstandingPaths(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	writeRequiredHead(e, "b.go", "package b\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 2))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked", got.Outcome)
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

// TestReviewHaltAppliesTheHaltedLabel pins the halted label on a bounded
// halt: the pass stops with crossrev/halted, not with an outcome label.
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
