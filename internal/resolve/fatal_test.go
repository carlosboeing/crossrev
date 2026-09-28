package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// A resolve leg that dies after claiming records it on the pull request
// (_run_report_fatal at lib/run.sh:131-146). The port had reportFatal and
// called it only from the publish stage, so a harness failure — the commonest
// way a leg dies — left the claim reading `started` with a null
// blocked_reason.
func TestAFailedResolveLegRecordsTheFailureOnTheClaim(t *testing.T) {
	e := setup(t)
	e.forge.pr.Labels = []forge.Label{{Name: policy.LabelAwaitingResolution}}
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.envErr = "test tripwire"

	got := e.run(t)
	if got.Err == nil {
		t.Fatal("a harness that fails did not fail the leg")
	}

	if len(e.forge.edits) == 0 {
		t.Fatal("the claim was never edited")
	}
	body := e.forge.edits[len(e.forge.edits)-1].Body
	for _, want := range []string{`"state":"complete"`, `"blocked":true`, "test tripwire"} {
		if !strings.Contains(body, want) {
			t.Errorf("the edited claim does not carry %s:\n%s", want, body)
		}
	}
	if !slices.Contains(e.forge.addedLabels, policy.LabelHalted) {
		t.Errorf("the pull request was not labelled halted; added %v", e.forge.addedLabels)
	}
	if !slices.Contains(e.forge.removedLabels, policy.LabelAwaitingResolution) {
		t.Errorf("awaiting-resolution was not removed; removed %v", e.forge.removedLabels)
	}
}

// Not on an interrupt (lib/run.sh:141-143), and not twice.
func TestAnInterruptedResolveLegLeavesTheClaimResumable(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.envErr = "cancelled"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.runCtx = ctx
	got := e.run(t)
	if got.Err == nil {
		t.Fatal("a cancelled run did not fail the leg")
	}
	for _, ed := range e.forge.edits {
		if strings.Contains(ed.Body, `"blocked":true`) {
			t.Fatalf("an interrupt marked the claim blocked: %s", ed.Body)
		}
	}
}

// A child killed by a signal with the context still live — exit 137, no
// cancellation — is an interrupt, not a harness failure. Signal deaths map to
// 128+N (internal/exec/runner.go:29-33), and only the failure path that reads
// Result.Interrupted turns them back into the cancellation the exit mapping
// and the fatal-report skip already understand.
func TestAnUncancelledSignalDeathReadsAsAnInterrupt(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.envErr = "killed"
	e.runner.result = &exec.Result{ExitCode: 137, Stderr: []byte("killed\n")}

	got := e.run(t)
	if got.Err == nil {
		t.Fatal("a killed harness did not fail the leg")
	}
	if !errors.Is(got.Err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", got.Err)
	}
	if msg := got.Err.Error(); strings.Contains(msg, "harness failed") || strings.Contains(msg, "authentication") {
		t.Fatalf("an interrupt reads as a harness failure: %q", msg)
	}
	// The terminal must read an interrupt, not the generic refusal. A bare
	// context.Canceled reaches refusalText as a plain error and prints
	// "error  context canceled" with the doctor hint, so the error has to
	// carry the interrupt refusal the terminal prints.
	var refusal *Refusal
	if !errors.As(got.Err, &refusal) {
		t.Fatalf("err = %T (%v), want it to carry the interrupt refusal", got.Err, got.Err)
	}
	if !strings.Contains(refusal.Message, "interrupt") {
		t.Fatalf("message = %q, want it to name the interrupt", refusal.Message)
	}
	if strings.Contains(refusal.Hint, "doctor") || strings.Contains(refusal.Hint, "authentication") {
		t.Fatalf("hint = %q, want no doctor hint and no authentication hint on an interrupt", refusal.Hint)
	}
	for _, ed := range e.forge.edits {
		if strings.Contains(ed.Body, `"blocked":true`) {
			t.Fatalf("an interrupt marked the claim blocked: %s", ed.Body)
		}
	}
}

// A resolver killed after editing files puts the pre-invoke tree back before
// answering the interrupt. The claim stays resumable, and prepareWorktree
// reuses the worktree on HEAD and ownership alone with no cleanliness check
// (internal/vcs/worktree.go), so without the restore the retry's commitAndPush
// would stage the killed attempt's edits beside the accepted answer's.
func TestAnInterruptedResolveLegRestoresTheTreeBeforeAnswering(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.envErr = "killed"
	e.adapter.payloads = []json.RawMessage{oneFindingPayload(), oneFindingPayload()}
	// The killed child edited the tree before it died.
	var ranDir string
	e.runner.onRun = func(spec exec.Spec) {
		ranDir = spec.Dir
		if e.runner.result != nil {
			_ = os.WriteFile(filepath.Join(spec.Dir, "stray.txt"), []byte("uncommitted\n"), 0o644)
		}
	}
	e.runner.result = &exec.Result{ExitCode: 137, Stderr: []byte("killed\n")}

	got := e.run(t)
	if got.Err == nil {
		t.Fatal("a killed harness did not fail the leg")
	}
	if !errors.Is(got.Err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", got.Err)
	}
	if ranDir == "" {
		t.Fatal("the harness never ran, so the kill edited nothing")
	}
	if _, err := os.Stat(filepath.Join(ranDir, "stray.txt")); err != nil {
		t.Fatalf("the killed harness left no edit behind: %v", err)
	}
	if *e.git.restoreCalls != 1 {
		t.Fatalf("restoreCalls = %d, want 1 — the interrupt answered without putting the pre-invoke tree back", *e.git.restoreCalls)
	}
	for _, ed := range e.forge.edits {
		if strings.Contains(ed.Body, `"blocked":true`) {
			t.Fatalf("an interrupt marked the claim blocked: %s", ed.Body)
		}
	}

	// The retry drives the same open claim to completion.
	e.runner.result = nil
	e.runner.onRun = nil
	e.adapter.envErr = ""
	e.git.staged = true
	retry := e.run(t)
	if retry.Err != nil {
		t.Fatalf("retry after an interrupt: %v", retry.Err)
	}
}

// A restore that will not apply turns the interrupt into a failure. Leaving
// the claim resumable on a dirty tree would let the retry commit the killed
// attempt's edits, so the leg records the failure instead.
func TestAnInterruptedResolveLegWithAFailedRestoreIsAFailure(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.envErr = "killed"
	e.runner.result = &exec.Result{ExitCode: 137, Stderr: []byte("killed\n")}
	e.git.restoreTreeErr = errors.New("the index is locked")

	got := e.run(t)
	if got.Err == nil {
		t.Fatal("a killed harness with a failed restore did not fail the leg")
	}
	if errors.Is(got.Err, context.Canceled) {
		t.Fatalf("err = %v, want no cancellation identity — the claim must not stay resumable on a dirty tree", got.Err)
	}
	msg := got.Err.Error()
	for _, want := range []string{"interrupted", "could not be put back"} {
		if !strings.Contains(msg, want) {
			t.Errorf("err = %q, want it to name %q", msg, want)
		}
	}
	for _, ed := range e.forge.edits {
		if strings.Contains(ed.Body, `"blocked":true`) {
			return
		}
	}
	t.Fatal("a failed restore left the claim without blocked:true")
}

// A settled pass is not rewritten by a later failure (lib/run.sh:127-129).
func TestASettledResolveLegIsNotRewrittenWhenALaterStepFails(t *testing.T) {
	e := setup(t)
	e.git.staged = true
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	last := e.forge.edits[len(e.forge.edits)-1].Body
	raw, ok := prstate.DecodeMarker(last)
	if !ok {
		t.Fatalf("no marker in the last body:\n%s", last)
	}
	if strings.Contains(string(raw), `"blocked":true`) {
		t.Errorf("a settled pass was recorded as blocked:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"state":"`+string(core.PassComplete)+`"`) {
		t.Errorf("the settled marker is not complete:\n%s", raw)
	}
}
