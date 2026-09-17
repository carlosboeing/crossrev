package review_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/policy"
)

// A pull request whose head matches its base changes no files, so there is
// nothing for a reviewer to read. Two independent sources have to agree
// before the leg says so: git enumerates no required file, and GitHub
// reports changedFiles as zero. The fixtures below set both, because either
// one alone is the ordinary state of a frozen-path test.
//
// Observed on carlosboeing/crossrev-testbed#24 at v0.7.2: pass 1 fixed every
// planted defect by restoring the original code, which was the whole of that
// branch's diff, and pass 2 met a pull request that changed nothing.

// TestEmptyPullRequestNeverConverges pins the failure that matters. The leg
// used to fall through to the frozen single-prompt path, where loaded.Scope
// stays nil, both coverage gates in publish are skipped, and the model's
// answer alone decides the label. A model answering 'converged' on an empty
// prompt then put crossrev/converged on a pull request nothing had reviewed.
func TestEmptyPullRequestNeverConverges(t *testing.T) {
	e := newEnv(t)
	e.forge.pr.ChangedFiles = 0

	leg := e.leg(t)
	got := leg.Run(context.Background(), e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}

	for _, added := range e.forge.labelsAdded {
		if added == policy.LabelConverged {
			t.Fatalf("labelsAdded = %v, want no %q on a pull request that changes no files",
				e.forge.labelsAdded, policy.LabelConverged)
		}
	}

	var halted bool
	for _, added := range e.forge.labelsAdded {
		if added == policy.LabelHalted {
			halted = true
			break
		}
	}
	if !halted {
		t.Errorf("labelsAdded = %v, want it to contain %q", e.forge.labelsAdded, policy.LabelHalted)
	}
}

// TestEmptyPullRequestCallsNoReviewer pins that the leg settles the pass
// itself rather than paying a harness to read an empty prompt. The live run
// spent 20,327 tokens discovering there was nothing to review.
func TestEmptyPullRequestCallsNoReviewer(t *testing.T) {
	e := newEnv(t)
	e.forge.pr.ChangedFiles = 0

	leg := e.leg(t)
	if got := leg.Run(context.Background(), e.request(t)); got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}

	if e.runner.calls != 0 {
		t.Errorf("runner.calls = %d, want 0: a pull request that changes no files needs no reviewer", e.runner.calls)
	}
}

// TestEmptyPullRequestSummaryNamesTheState pins the words a reader meets.
// The reason is written here rather than by a model, so it says the same
// thing on every run and every harness.
func TestEmptyPullRequestSummaryNamesTheState(t *testing.T) {
	e := newEnv(t)
	e.forge.pr.ChangedFiles = 0

	leg := e.leg(t)
	if got := leg.Run(context.Background(), e.request(t)); got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}

	if len(e.forge.edits) == 0 {
		t.Fatalf("edits = 0, want the summary the leg posts")
	}
	summary := e.forge.edits[len(e.forge.edits)-1]

	for _, want := range []string{
		"Nothing to review.",
		"changes no files",
		"CrossRev stops rather than calling a reviewer with nothing to read.",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary does not contain %q\n--- summary ---\n%s", want, summary)
		}
	}

	// The frozen path's sentence claims a review happened and found nothing.
	// No review happened here, so it must not appear.
	const false_ = "No findings. Low-severity and pre-existing issues would be listed here too"
	if strings.Contains(summary, false_) {
		t.Errorf("summary contains the empty-review sentence, which states a review that never ran\n--- summary ---\n%s", summary)
	}
}
