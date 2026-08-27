package policy_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
)

// TestParityShouldContinue runs the table generated from
// tests/test-legs.sh's [policy-table: legs_should_continue] block. The
// generated file is the oracle: nothing here adjusts an expected value.
func TestParityShouldContinue(t *testing.T) {
	for _, tc := range parityShouldContinueCases {
		t.Run(tc.desc, func(t *testing.T) {
			got := policy.ShouldContinue(policy.Termination{
				Verdict:              core.Verdict(tc.verdict),
				Pass:                 core.PassNumber(tc.pass),
				MaxPassesPerCycle:    tc.maxPasses,
				Stop:                 tc.stop,
				Blocked:              tc.blocked,
				OtherPRsToday:        tc.otherPRsToday,
				MaxPRsPerDay:         tc.maxPRsPerDay,
				FilesChanged:         tc.files,
				MaxFilesChangedPerPR: tc.maxFilesChangedPerPR,
			})
			if string(got.Action) != tc.wantAction {
				t.Errorf("action = %q, want %q", got.Action, tc.wantAction)
			}
			if tc.wantFull != "" && got.String() != tc.wantFull {
				t.Errorf("full = %q, want %q", got.String(), tc.wantFull)
			}
			if got.Continues() != (tc.wantAction == "continue") {
				t.Errorf("Continues() = %t for action %q", got.Continues(), got.Action)
			}
		})
	}
}

// TestHaltOrder pins the order of the six terminating checks, which the
// generated table cannot: it never sets two of them at once. lib/legs.sh:23-44
// checks stop, blocked, converged, the pass cap, the daily cap and the file cap
// in that order, and a run that trips two reports the first.
func TestHaltOrder(t *testing.T) {
	// Every check is tripped at once. Peeling one input at a time walks down
	// the order and names the reason each level is expected to report.
	all := policy.Termination{
		Verdict:              core.VerdictConverged,
		Pass:                 core.PassNumber(9),
		MaxPassesPerCycle:    3,
		Stop:                 true,
		Blocked:              true,
		OtherPRsToday:        12,
		MaxPRsPerDay:         12,
		FilesChanged:         201,
		MaxFilesChangedPerPR: 200,
	}

	steps := []struct {
		peel   func(t policy.Termination) policy.Termination
		action policy.Action
		reason string
	}{
		{func(x policy.Termination) policy.Termination { return x },
			policy.ActionHalt, "a human applied crossrev/stop"},
		{func(x policy.Termination) policy.Termination { x.Stop = false; return x },
			policy.ActionHalt, "the resolver reported blocked"},
		{func(x policy.Termination) policy.Termination { x.Blocked = false; return x },
			policy.ActionConverged, "nothing at or above min_fix_severity remains"},
		{func(x policy.Termination) policy.Termination { x.Verdict = core.VerdictIssuesRemain; return x },
			policy.ActionHalt, "reached max_passes_per_cycle (3)"},
		{func(x policy.Termination) policy.Termination { x.MaxPassesPerCycle = 0; return x },
			policy.ActionHalt, "reached max_prs_per_day (12) — 12 other pull requests were already reviewed in the last 24 hours"},
		{func(x policy.Termination) policy.Termination { x.MaxPRsPerDay = 0; return x },
			policy.ActionHalt, "201 files changed, above max_files_changed_per_pr (200)"},
		{func(x policy.Termination) policy.Termination { x.MaxFilesChangedPerPR = 0; return x },
			policy.ActionContinue, "issues remain and no cap is reached"},
	}

	cur := all
	for i, step := range steps {
		cur = step.peel(cur)
		got := policy.ShouldContinue(cur)
		if got.Action != step.action || got.Reason != step.reason {
			t.Errorf("level %d: got %q / %q, want %q / %q",
				i, got.Action, got.Reason, step.action, step.reason)
		}
	}
}

// TestZeroCapsAreNoCap pins the sentinel lib/config.sh:258 relies on: a cap of
// zero bounds nothing. The generated table covers only the file cap.
func TestZeroCapsAreNoCap(t *testing.T) {
	base := policy.Termination{Verdict: core.VerdictIssuesRemain, Pass: core.PassNumber(9999), MaxPassesPerCycle: 0,
		OtherPRsToday: 9999, MaxPRsPerDay: 0, FilesChanged: 9999, MaxFilesChangedPerPR: 0}
	if got := policy.ShouldContinue(base); got.Action != policy.ActionContinue {
		t.Errorf("all caps zero: got %q, want continue", got.String())
	}
}
