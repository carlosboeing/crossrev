package review_test

import (
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/review"
)

// A refused call degrades where the policy says degrade and halts where it
// says halt; the halted call publishes nothing.
func TestReadsDispositionDegradesAndHalts(t *testing.T) {
	stats := prstate.ReadsStats{Handshake: true, Calls: 1, Reads: 2, Bytes: 512, Refused: 1}

	reason, halt := review.AssessReads("degrade", stats, nil)
	if halt {
		t.Error("degrade halts on a refused call")
	}
	if reason == "" {
		t.Error("degrade continues without naming the reason")
	}

	reason, halt = review.AssessReads("halt", stats, nil)
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
		if reason, halt := review.AssessReads(policy, stats, nil); halt || reason != "" {
			t.Errorf("policy %s answers (%q, %t) for a healthy call", policy, reason, halt)
		}
	}
}

// A failed self-test stops where the policy says halt and degrades where it
// says degrade; a missing handshake degrades the same way.
func TestReadsDispositionSelfTestAndHandshake(t *testing.T) {
	failed := errors.New("byte mismatch on a.go")

	if _, halt := review.AssessReads("halt", prstate.ReadsStats{}, failed); !halt {
		t.Error("halt continues on a failed self-test")
	}
	if reason, halt := review.AssessReads("degrade", prstate.ReadsStats{}, failed); halt || reason == "" {
		t.Errorf("degrade answers (%q, %t) for a failed self-test", reason, halt)
	}

	missing := prstate.ReadsStats{Calls: 1}
	if _, halt := review.AssessReads("halt", missing, nil); !halt {
		t.Error("halt continues on a missing handshake")
	}
	if reason, halt := review.AssessReads("degrade", missing, nil); halt || reason == "" {
		t.Errorf("degrade answers (%q, %t) for a missing handshake", reason, halt)
	}
}

// A command event always halts: the tripwire is not a degradation.
func TestReviewTripwireAlwaysHalts(t *testing.T) {
	stdout := []byte("{\"type\":\"item.completed\",\"item\":{\"type\":\"command_execution\",\"command\":\"id\"}}\n")
	if _, tripped := harness.ReviewCommand("codex", stdout); !tripped {
		t.Fatal("the fixture command does not trip")
	}
	for _, policy := range []string{"degrade", "halt"} {
		if !review.ReviewCommandHalts(policy) {
			t.Errorf("policy %s does not halt on a review-leg command", policy)
		}
	}
}
