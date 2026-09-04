package policy

import (
	"fmt"

	"github.com/carlosboeing/crossrev/internal/core"
)

// Action is the first word `legs_should_continue` prints, and the word every
// caller reads with `cut -d' ' -f1` (lib/legs.sh:17-48).
type Action string

// The three actions a termination decision can carry.
const (
	ActionContinue  Action = "continue"
	ActionConverged Action = "converged"
	ActionHalt      Action = "halt"
)

// Termination is the state ShouldContinue decides over, in the order
// lib/legs.sh:18-19 takes its nine positional arguments.
//
// Pass is a core.PassNumber and the three caps are plain ints, which is one
// vocabulary rather than two: a pass number counts from one and names a
// particular pass, and PassLabelName already spells it that way. A cap is a
// bound on a count, and zero is its load-bearing sentinel for "no bound applies"
// (lib/config.sh:294) — a value core.NewPassNumber refuses by construction, so
// typing the caps as pass numbers would put a live sentinel inside the band the
// type exists to exclude.
type Termination struct {
	Verdict              core.Verdict
	Pass                 core.PassNumber
	MaxPassesPerCycle    int
	Stop                 bool
	Blocked              bool
	OtherPRsToday        int
	MaxPRsPerDay         int
	FilesChanged         int
	MaxFilesChangedPerPR int
}

// Decision is one termination answer: the word the loop keys off and the reason
// a person reads.
type Decision struct {
	Action Action
	Reason string
}

// Continues reports whether the loop runs another pass. It is the Go form of
// `legs_should_continue`'s exit status, which is zero only for `continue`.
func (d Decision) Continues() bool { return d.Action == ActionContinue }

// String renders the decision as one line, the way lib/legs.sh prints it.
func (d Decision) String() string { return string(d.Action) + " " + d.Reason }

// ShouldContinue decides whether the loop runs another pass.
//
// The order of the six terminating checks is the contract, not an
// implementation detail. lib/legs.sh:23-44 asks them as stop, blocked,
// converged, the pass cap, the daily cap and the file cap, and a run that trips
// two of them reports the first. Reordering them changes the halt reason a
// person reads without changing whether the loop stopped, which is exactly the
// class of divergence nothing else would catch.
//
// A cap of zero bounds nothing. lib/config.sh:294 uses that as its sentinel for
// "no pass bound applies to this cycle", so it is load-bearing rather than a
// guard against a missing value.
func ShouldContinue(t Termination) Decision {
	// A human's request outranks everything, including a healthy verdict:
	// crossrev/stop is an instruction, not a state (lib/legs.sh:21-23).
	if t.Stop {
		return Decision{ActionHalt, "a human applied crossrev/stop"}
	}
	if t.Blocked {
		return Decision{ActionHalt, "the resolver reported blocked"}
	}
	if t.Verdict == core.VerdictConverged {
		return Decision{ActionConverged, "nothing at or above min_fix_severity remains"}
	}
	// At exactly the cap, stop. Pass 3 of max_passes_per_cycle 3 is the last
	// pass, not the one after which a fourth begins (lib/legs.sh:31-35).
	if t.MaxPassesPerCycle > 0 && t.Pass.Int() >= t.MaxPassesPerCycle {
		return Decision{ActionHalt, fmt.Sprintf("reached max_passes_per_cycle (%d)", t.MaxPassesPerCycle)}
	}
	if t.MaxPRsPerDay > 0 && t.OtherPRsToday >= t.MaxPRsPerDay {
		return Decision{ActionHalt, fmt.Sprintf(
			"reached max_prs_per_day (%d) — %d other pull requests were already reviewed in the last 24 hours",
			t.MaxPRsPerDay, t.OtherPRsToday)}
	}
	if t.MaxFilesChangedPerPR > 0 && t.FilesChanged > t.MaxFilesChangedPerPR {
		return Decision{ActionHalt, fmt.Sprintf(
			"%d files changed, above max_files_changed_per_pr (%d)",
			t.FilesChanged, t.MaxFilesChangedPerPR)}
	}
	return Decision{ActionContinue, "issues remain and no cap is reached"}
}
