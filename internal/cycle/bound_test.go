package cycle

import (
	"context"
	"testing"
)

// sayLine is ui_say's shape (lib/ui.sh:96), the same one sayCycling in
// cycle_test.go is built from.
func sayLine(text string) string { return "  " + text + "\n" }

// --- the two the bound exists to get right ----------------------------------

// TestBoundStartsNoFurtherPassAtTheCap pins that a cap of three runs no fourth
// pass. The pull request sits on a complete review pass 3 with an actionable
// finding and no resolve marker, so the pass is owed its resolve leg and
// nothing else (lib/run.sh:2853-2854, :2887). Measured:
//
//	Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Its resolve leg is still owed.
//	LEG resolve --pr 42 --no-tips
//	LOAD 42 acme/widget
//	└  Reached max_passes_per_cycle (3) on acme/widget#42 — pass 3 is resolved, and no further pass starts.
func TestBoundStartsNoFurtherPassAtTheCap(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewIssues, 3))},
		{state: loaded(t, marker(reviewIssues, 3), marker(resolvePushed, 3))},
	})

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "resolve", "load")
	r.wantOut(t, sayCycling+
		sayLine("Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Its resolve leg is still owed.")+
		endLine("Reached max_passes_per_cycle (3) on acme/widget#42 — pass 3 is resolved, and no further pass starts."))
}

// TestBoundRunsNoResolveWhenTheReviewRaisedNothingActionable pins the arm at
// lib/run.sh:2837-2846: a complete review pass at the bound that raised nothing
// at or above min_fix_severity is a convergence, and the resolve leg is never
// billed for it.
func TestBoundRunsNoResolveWhenTheReviewRaisedNothingActionable(t *testing.T) {
	r := newRig(t, []loadStep{{state: loaded(t, marker(reviewEmpty, 3))}})

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load")
	r.wantOut(t, sayCycling+
		endLine("Converged after pass 3 — nothing at or above min_fix_severity (medium) remains."))
}
