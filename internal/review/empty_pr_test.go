package review_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/vcs"
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

// TestEmptyGitScopeNeverConvergesWhenTheAPIDisagrees pins the half of the
// defect that the first cut of this fix left open. Git enumerates no required
// file while GitHub still reports a changed count — a lagging read after a
// force-push or a revert produces exactly that — so the pass is not settled as
// "nothing to review". It still runs, because the diff comes from the forge
// and may hold real content. What it may never do is report green: Run binds
// the coverage obligation on every successful enumeration, and
// policy.Converged refuses a required count of zero.
//
// Without that binding the pass reached the frozen single-prompt path with
// loaded.Scope nil, both publication gates were skipped, and the stock
// converged harness reply produced `[crossrev/pass-1 crossrev/converged]` on a
// pull request nothing had read.
func TestEmptyGitScopeNeverConvergesWhenTheAPIDisagrees(t *testing.T) {
	e := newEnv(t)
	e.forge.pr.ChangedFiles = 1 // GitHub disagrees with git's empty enumeration

	leg := e.leg(t)
	got := leg.Run(context.Background(), e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}

	for _, added := range e.forge.labelsAdded {
		if added == policy.LabelConverged {
			t.Fatalf("labelsAdded = %v, want no %q: the required set was empty, so no file was covered",
				e.forge.labelsAdded, policy.LabelConverged)
		}
	}
}

// A pull request whose every changed path is marked linguist-generated at
// the base leaves nothing to review. The pass settles without a model:
// verdict blocked, the halted label, and a reason CrossRev writes — never
// converged on a pull request nothing read.
func TestNothingToReviewWhenEveryFileIsExcluded(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "dist/bundle.js", "var bundle = 1\n")
	writeRequiredHead(e, "gen/output.go", "package gen\n")
	e.vcs.attrs = map[string]vcs.AttributeDecision{
		"dist/bundle.js": vcs.AttributeSet,
		"gen/output.go":  vcs.AttributeSet,
	}
	e.runner.onSpec = func(exec.Spec) { t.Error("the harness ran for a pass with nothing to review") }

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeHalted {
		t.Errorf("Outcome = %q, want halted", got.Outcome)
	}
	if e.runner.calls != 0 {
		t.Errorf("runner.calls = %d, want 0", e.runner.calls)
	}
	for _, added := range e.forge.labelsAdded {
		if added == policy.LabelConverged {
			t.Fatalf("labelsAdded = %v, want no %q on a pass that read nothing", e.forge.labelsAdded, policy.LabelConverged)
		}
	}
	if !containsString(e.forge.labelsAdded, policy.LabelHalted) {
		t.Errorf("labelsAdded = %v, want %q", e.forge.labelsAdded, policy.LabelHalted)
	}
	const reason = "Every changed file is marked `linguist-generated` in `.gitattributes` at the base, so there was nothing to review. A PR that changes generated output without its source is worth a look."
	final := decodeEditMarker(t, e.forge.edits[len(e.forge.edits)-1])
	if v := final.Verdict.Value(); v != string(core.VerdictBlocked) {
		t.Errorf("verdict = %q, want blocked", v)
	}
	if r, _ := final.BlockedReason.Get(); r != reason {
		t.Errorf("blocked reason = %q, want the repository-exclusion reason", r)
	}
	summary := e.forge.edits[len(e.forge.edits)-1]
	if !strings.Contains(summary, reason) {
		t.Errorf("summary does not carry the reason\n--- summary ---\n%s", summary)
	}
}

// A pull request whose every changed file is recognised as generated and is
// too large for one prompt settles the same way, with the skip reason.
func TestNothingToReviewWhenEveryFileIsSkipped(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "gen/big.ts", oversizedHeaderBody())
	writeRequiredHead(e, "src/webAssets.ts", oversizedHeaderBody())
	e.runner.onSpec = func(exec.Spec) { t.Error("the harness ran for a pass with nothing to review") }

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeHalted {
		t.Errorf("Outcome = %q, want halted", got.Outcome)
	}
	if e.runner.calls != 0 {
		t.Errorf("runner.calls = %d, want 0", e.runner.calls)
	}
	if !containsString(e.forge.labelsAdded, policy.LabelHalted) {
		t.Errorf("labelsAdded = %v, want %q", e.forge.labelsAdded, policy.LabelHalted)
	}
	const reason = "Every changed file was recognised as generated and is too large for one review prompt, so nothing was reviewed."
	final := decodeEditMarker(t, e.forge.edits[len(e.forge.edits)-1])
	if r, _ := final.BlockedReason.Get(); r != reason {
		t.Errorf("blocked reason = %q, want the all-skipped reason", r)
	}
}

// When repository policy excludes some files and packing skips the rest, the
// reason names both counts.
func TestNothingToReviewNamesBothCounts(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "dist/bundle.js", "var bundle = 1\n")
	writeRequiredHead(e, "src/webAssets.ts", oversizedHeaderBody())
	e.vcs.attrs = map[string]vcs.AttributeDecision{"dist/bundle.js": vcs.AttributeSet}
	e.runner.onSpec = func(exec.Spec) { t.Error("the harness ran for a pass with nothing to review") }

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeHalted {
		t.Errorf("Outcome = %q, want halted", got.Outcome)
	}
	const reason = "Nothing was reviewed: 1 changed file excluded by repository policy, 1 changed file recognised as generated and too large for one review prompt."
	final := decodeEditMarker(t, e.forge.edits[len(e.forge.edits)-1])
	if r, _ := final.BlockedReason.Get(); r != reason {
		t.Errorf("blocked reason = %q, want the combined counts", r)
	}
}
