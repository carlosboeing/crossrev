package review_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/review"
)

// A refused call degrades where the policy says degrade and halts where it
// says halt; the halted call publishes nothing.
func TestReadsDispositionDegradesAndHalts(t *testing.T) {
	stats := prstate.ReadsStats{Handshake: true, Calls: 1, Reads: 2, Bytes: 512, Refused: 1}

	reason, halt := review.AssessReads("degrade", harness.ReadModeServed, stats)
	if halt {
		t.Error("degrade halts on a refused call")
	}
	if reason == "" {
		t.Error("degrade continues without naming the reason")
	}

	reason, halt = review.AssessReads("halt", harness.ReadModeServed, stats)
	if !halt {
		t.Error("halt continues on a refused call")
	}
	if reason == "" {
		t.Error("halt stops without naming the reason")
	}
}

// A healthy call leaves no reason and never halts, whatever the policy.
func TestReadsDispositionHealthyPassesThrough(t *testing.T) {
	stats := prstate.ReadsStats{Handshake: true, Calls: 1, Reads: 2, Bytes: 512}
	for _, policy := range []string{"degrade", "halt"} {
		if reason, halt := review.AssessReads(policy, harness.ReadModeServed, stats); halt || reason != "" {
			t.Errorf("policy %s answers (%q, %t) for a healthy call", policy, reason, halt)
		}
	}
}

// A missing handshake degrades where the policy says degrade and stops the
// leg where it says halt. A failed self-test never reaches this check:
// runPrompt decides its disposition inline, and the leg tests pin it there.
func TestReadsDispositionHandshake(t *testing.T) {
	// A served child that never reached the server leaves an empty log:
	// reads and refusals only arrive after initialize and tools/list, so
	// the handshake is required whatever the call count is.
	empty := prstate.ReadsStats{}
	if reason, halt := review.AssessReads("halt", harness.ReadModeServed, empty); !halt || reason != "missing_handshake" {
		t.Errorf("halt answers (%q, %t) for a served call with an empty log, want (missing_handshake, true)", reason, halt)
	}
	if reason, halt := review.AssessReads("degrade", harness.ReadModeServed, empty); halt || reason != "missing_handshake" {
		t.Errorf("degrade answers (%q, %t) for a served call with an empty log, want (missing_handshake, false)", reason, halt)
	}

	// A supplied call has no server to shake hands with, so an empty log
	// is healthy there.
	if reason, halt := review.AssessReads("halt", harness.ReadModeSupplied, empty); halt || reason != "" {
		t.Errorf("halt answers (%q, %t) for a supplied call with an empty log, want no reason", reason, halt)
	}
}
