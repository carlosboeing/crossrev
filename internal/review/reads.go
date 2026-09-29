// reads.go — what a review leg does when the served read path is not
// serving.
//
// Health is three checks: the leg-start self-test (initialize, tools/list,
// one read byte-checked against git), the post-call handshake (initialize
// plus tools_list in the server log), and post-call refusal matching
// (refused calls in the log matched against the harness transcript). Any
// command event is not a degradation: it halts with
// review_leg_ran_command, discards the call unpublished, and redacts the
// command into the run log only.
//
// A failed self-test, a missing handshake or refused calls degrade visibly
// with the reason in the pass comment, the ledger and the run log — or stop
// the leg where `.policy.on_reads_unavailable` says halt.

package review

import (
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// Reads degradation reasons, one per failed check. They travel in the reads
// envelope, the pass comment warning and the run log together.
const (
	ReadsReasonSelfTestFailed  = "self_test_failed"
	ReadsReasonMissingHandshake = "missing_handshake"
	ReadsReasonCallsRefused    = "calls_refused"
	ReadsReasonFileToolUnwired = "file_tool_unwired"
)

// AssessReads maps one call's reads health onto the policy: the reason to
// record, and whether the leg halts. A refused call, a missing handshake
// and a failed self-test degrade where the policy says degrade and stop it
// where it says halt. A healthy call leaves no reason and never halts,
// whatever the policy.
func AssessReads(onUnavailable string, stats prstate.ReadsStats, selfTestErr error) (reason string, halt bool) {
	halt = onUnavailable == "halt"
	if selfTestErr != nil {
		return ReadsReasonSelfTestFailed, halt
	}
	if stats.Calls > 0 && !stats.Handshake {
		return ReadsReasonMissingHandshake, halt
	}
	if stats.Refused > 0 {
		return ReadsReasonCallsRefused, halt
	}
	return "", false
}

// ReviewCommandHalts reports that a command event on a review leg always
// halts, under either policy: the tripwire is not a degradation.
func ReviewCommandHalts(onUnavailable string) bool { return true }

// EffectiveReadMode resolves the declared mode to the one the leg runs:
// file_tool is accepted but unwired until slice 9, so it runs supplied and
// records why. Served and supplied run as declared; the served-to-supplied
// fallback on a failed self-test is decided by the caller, which owns the
// policy.
func EffectiveReadMode(declared harness.ReadMode) (effective harness.ReadMode, reason string) {
	if declared == harness.ReadModeFileTool {
		return harness.ReadModeSupplied, ReadsReasonFileToolUnwired
	}
	return declared, ""
}

// ReadsEnvelope builds the envelope the marker and the manifest record for
// one call: declared and effective modes, the reason they differ or the
// degradation reason, and the summed counts.
func ReadsEnvelope(declared, effective harness.ReadMode, reason string, stats prstate.ReadsStats) prstate.ReadsEnvelope {
	return prstate.NewReadsEnvelope(
		string(declared), string(effective), reason,
		stats.Calls, stats.Reads, stats.Bytes, stats.Refused, stats.BudgetExhausted,
	)
}
