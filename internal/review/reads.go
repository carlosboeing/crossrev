// reads.go — what a review leg does when the served read path is not
// serving.
//
// Health is three checks: the leg-start self-test (initialize, tools/list,
// one read byte-checked against git), the post-call handshake (initialize
// plus tools_list in the server log), and the post-call refusal tally
// (refused calls counted from the server log). Any
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

// Reads degradation reasons, one per failed check. The values live beside
// the envelope that carries them (prstate); these aliases keep the leg's
// own code naming them without the package qualifier.
const (
	ReadsReasonSelfTestFailed   = prstate.ReadsReasonSelfTestFailed
	ReadsReasonMissingHandshake = prstate.ReadsReasonMissingHandshake
	ReadsReasonCallsRefused     = prstate.ReadsReasonCallsRefused
	ReadsReasonFileToolUnwired  = prstate.ReadsReasonFileToolUnwired
)

// ReadsReasonReviewCommand is the envelope reason for a call the tripwire
// discarded. A command event halts rather than degrades, and the reason
// names the failure every surface reports it under.
const ReadsReasonReviewCommand = prstate.ReadsReasonReviewCommand

// AssessReads maps one call's reads health onto the policy: the reason to
// record, and whether the leg halts. A refused call and a missing
// handshake degrade where the policy says degrade and stop the leg where
// it says halt. A healthy call leaves no reason and never halts, whatever
// the policy. A failed self-test never reaches this check: runPrompt
// decides its disposition inline, before the call starts.
//
// The handshake is required on every served call, whatever the call count:
// reads and refusals only arrive after initialize and tools/list, so a
// served child that never reached the server leaves an empty log with zero
// calls — exactly the failure the post-call handshake exists to catch. A
// supplied call has no server, so its empty log is healthy.
func AssessReads(onUnavailable string, effective harness.ReadMode, stats prstate.ReadsStats) (reason string, halt bool) {
	halt = onUnavailable == "halt"
	if effective == harness.ReadModeServed && !stats.Handshake {
		return ReadsReasonMissingHandshake, halt
	}
	if stats.Refused > 0 {
		return ReadsReasonCallsRefused, halt
	}
	return "", false
}

// EffectiveReadMode resolves the declared mode to the one the leg runs:
// file_tool is accepted but unwired until slice 9, so it runs supplied and
// records why. Served and supplied run as declared; the served-to-supplied
// fallback on a failed self-test is decided by the caller, which owns the
// policy.
func EffectiveReadMode(declared harness.ReadMode) (effective harness.ReadMode, reason string) {
	if effective := harness.EffectiveReadMode(declared); effective != declared {
		return effective, ReadsReasonFileToolUnwired
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
