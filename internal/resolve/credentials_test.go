package resolve

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/vcs"
)

// The leg removes the token the checkout persisted before anything else
// runs — before its own fetches and before the harness starts — and refuses
// when that removal fails.
func TestTheResolveLegRemovesPersistedCheckoutCredentials(t *testing.T) {
	t.Run("the scrub runs once per leg", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")

		if got := e.run(t); got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if e.git.removePersistedCalls != 1 {
			t.Fatalf("scrub calls = %d, want 1", e.git.removePersistedCalls)
		}
	})

	t.Run("a scrub failure refuses the leg", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.removePersistedErr = &vcs.Refusal{
			Message: "could not list persisted git credentials",
			Hint:    "Check that git runs there, then try again.",
		}

		got := e.run(t)
		if got.Outcome != OutcomeRefused {
			t.Fatalf("outcome = %q, want %q", got.Outcome, OutcomeRefused)
		}
		if got.Err == nil {
			t.Fatal("the leg swallowed the scrub failure")
		}
		if len(e.git.fetchCalls) != 0 {
			t.Fatalf("the leg fetched %d times after the scrub failed", len(e.git.fetchCalls))
		}
	})
}
