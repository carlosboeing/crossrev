package review_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/review"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// TestReviewSubstituteHarnessWarningKeepsSecondSentence pins that when a missing
// configured reviewer harness falls back to another harness, the warning message
// retains the second sentence naming the asked harness.
func TestReviewSubstituteHarnessWarningKeepsSecondSentence(t *testing.T) {
	e := newEnv(t)
	e.lookPath = func(name string) (string, error) {
		if name == "claude" {
			return "/usr/bin/claude", nil
		}
		return "", os.ErrNotExist
	}
	e.cfg = mustConfig(t, "version: 1\nreviewer:\n  harness: codex\n")
	req := e.request(t)
	req.HarnessOverride = ""
	leg := e.leg(t)
	got := leg.Run(context.Background(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	const wantSentence = "Both legs now run on the same harness, so a bug it misses while reviewing it also misses while resolving. Install codex to get the second lineage back."
	found := false
	for _, msg := range ui.Texts(got.Messages) {
		if strings.Contains(msg, wantSentence) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("messages = %v, want warning containing sentence %q", got.Messages, wantSentence)
	}
}

// TestDeclinedPassRemovesMutuallyExclusiveLabels pins that a declined pass
// calls applyPassLabels with PassHalted to add pass and halted labels while
// removing awaiting-review, awaiting-resolution, and converged.
func TestDeclinedPassRemovesMutuallyExclusiveLabels(t *testing.T) {
	e := newEnv(t)
	// Configure policy to decline on file count
	e.cfg = mustConfig(t, "version: 1\npolicy:\n  max_files_changed_per_pr: 1\n")
	e.forge.pr.ChangedFiles = 5
	req := e.request(t)
	req.Trigger = review.TriggerAutomatic

	leg := e.leg(t)
	got := leg.Run(context.Background(), req)
	if got.Outcome != review.OutcomeDeclined {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, review.OutcomeDeclined)
	}

	wantAdded := []string{
		policy.LabelPassPrefix + "1",
		policy.LabelHalted,
	}
	for _, want := range wantAdded {
		var found bool
		for _, added := range e.forge.labelsAdded {
			if added == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("labelsAdded = %v, want it to contain %q", e.forge.labelsAdded, want)
		}
	}

	wantRemoved := []string{
		policy.LabelAwaitingReview,
		policy.LabelAwaitingResolution,
		policy.LabelConverged,
	}
	for _, want := range wantRemoved {
		var found bool
		for _, removed := range e.forge.labelsRemoved {
			if removed == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("labelsRemoved = %v, want it to contain %q", e.forge.labelsRemoved, want)
		}
	}
}
