package review_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// The leg removes the token the checkout persisted before the harness
// starts, and refuses when that removal fails rather than starting a leg it
// cannot confirm is clean.
func TestTheReviewLegRemovesPersistedCheckoutCredentials(t *testing.T) {
	t.Run("the scrub runs once before the harness", func(t *testing.T) {
		e := newEnv(t)
		writeAppGo(t, e.dir)
		e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(convergedPayload())}}
		e.vcs.removed = []vcs.RemovedCredential{
			{Key: "http.https://github.com/.extraheader", File: ".git/config"},
			{Key: "http.https://ghe.example.com/.extraheader", File: ".git/config"},
		}

		leg := e.leg(t)
		if got := leg.Run(t.Context(), e.request(t)); got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if e.vcs.removePersistedCalls != 1 {
			t.Fatalf("scrub calls = %d, want 1", e.vcs.removePersistedCalls)
		}
		log := readRunLog(t, e)
		if !strings.Contains(log, "credentials") || !strings.Contains(log, "removed 2 persisted checkout credential entries from .git/config") {
			t.Errorf("the run log names no removal:\n%s", log)
		}
	})

	t.Run("a scrub failure ends the leg before the harness starts", func(t *testing.T) {
		e := newEnv(t)
		writeAppGo(t, e.dir)
		e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(convergedPayload())}}
		e.vcs.removePersistedErr = errors.New("could not list persisted git credentials")

		leg := e.leg(t)
		got := leg.Run(t.Context(), e.request(t))
		if got.Outcome != review.OutcomeError {
			t.Fatalf("outcome = %q, want %q", got.Outcome, review.OutcomeError)
		}
		if got.Err == nil {
			t.Fatal("the leg swallowed the scrub failure")
		}
		if specs := e.runner.Specs(); len(specs) != 0 {
			t.Fatalf("the harness started %d children after the scrub failed", len(specs))
		}
	})
}
