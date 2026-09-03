package review_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// A leg that dies after posting its claim records the failure on the pull
// request: _run_report_fatal (lib/run.sh:131-146) through
// _run_report_invoke_failure (lib/run.sh:725-769).
//
// Without it the claim stays `started` with a null blocked_reason, so
// `crossrev status` reports the leg as abandoned — it probes whether the run
// id's process is still alive — and cannot say why. The cause survived only in
// the terminal that saw it.
func TestAFailedReviewLegRecordsTheFailureOnTheClaim(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.forge.pr.Labels = []forge.Label{
		{Name: policy.LabelPassPrefix + "1"},
		{Name: policy.LabelAwaitingReview},
		{Name: policy.LabelWatchdogRetried},
	}
	e.runner.script = []exec.Result{{ExitCode: 1, Stderr: []byte("test tripwire\n")}}

	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("a harness that exits 1 did not fail the leg")
	}

	if len(e.forge.edits) != 1 {
		t.Fatalf("edits to the claim = %d, want 1", len(e.forge.edits))
	}
	body := e.forge.edits[0]
	for _, want := range []string{`"state":"complete"`, `"verdict":"blocked"`, "test tripwire"} {
		if !strings.Contains(body, want) {
			t.Errorf("the edited claim does not carry %s:\n%s", want, body)
		}
	}
	if !slices.Contains(e.forge.labelsAdded, policy.LabelHalted) {
		t.Errorf("the pull request was not labelled halted; added %v", e.forge.labelsAdded)
	}
	if !slices.Contains(e.forge.labelsRemoved, policy.LabelWatchdogRetried) {
		t.Errorf("the watchdog retry marker was not dropped; removed %v", e.forge.labelsRemoved)
	}
}

// A later pass moves the grey pill rather than stacking it: the failing pass's
// own label goes on and the earlier one comes off (lib/run.sh:752-767).
func TestAFailedLaterReviewPassMovesThePassLabel(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	raw := `{"v":1,"leg":"review","pass":1,"state":"complete","ts":1699950000,"run_id":"x","head_sha":"` + oldSHA + `","verdict":"issues-remain","findings":[]}`
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 8001, parseMarker(t, raw))}
	e.forge.pr.Labels = []forge.Label{
		{Name: policy.LabelPassPrefix + "1"},
		{Name: policy.LabelAwaitingReview},
	}
	e.runner.script = []exec.Result{{ExitCode: 1, Stderr: []byte("test tripwire\n")}}

	if got := runLeg(t, e, e.request(t)); got.Err == nil {
		t.Fatal("a harness that exits 1 did not fail the leg")
	}
	if !slices.Contains(e.forge.labelsAdded, policy.LabelPassPrefix+"2") {
		t.Errorf("pass-2 was not applied; added %v", e.forge.labelsAdded)
	}
	if !slices.Contains(e.forge.labelsRemoved, policy.LabelPassPrefix+"1") {
		t.Errorf("pass-1 was not removed; removed %v", e.forge.labelsRemoved)
	}
}

// The interrupt path is not a failure. Naming it here would turn every Ctrl-C
// into a halted pull request, and the claim it leaves is deliberately
// resumable (lib/run.sh:141-143).
func TestAnInterruptedReviewLegLeavesTheClaimResumable(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 130, Stderr: []byte("interrupted\n")}}

	// The real shape: signal.NotifyContext cancels the context every child was
	// started with, and the harness dies on it.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := e.request(t)
	leg := e.leg(t)
	got := leg.Run(ctx, req)
	if got.Err == nil {
		t.Fatal("a cancelled run did not fail the leg")
	}
	if len(e.forge.edits) != 0 {
		t.Fatalf("an interrupt rewrote the claim: %v", e.forge.edits)
	}
}

// A leg that finished writes `complete`, and something failing afterwards must
// not replace an accurate record with a wrong one. That is what
// run_leg_settled clears the snapshot for (lib/run.sh:127-129, :160-163).
func TestASettledReviewLegIsNotRewrittenWhenALaterStepFails(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.cfg = mustConfig(t, "version: 1\nmode: automated\n")
	t.Setenv("CROSSREV_APP_SLUG", "crossrev")
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}
	e.forge.labelAddErr = errors.New("no")

	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("a label that could not be applied did not fail an automated leg")
	}
	if len(e.forge.edits) == 0 {
		t.Fatal("the claim was never edited")
	}
	last := e.forge.edits[len(e.forge.edits)-1]
	raw, ok := prstate.DecodeMarker(last)
	if !ok {
		t.Fatalf("no marker in the last body:\n%s", last)
	}
	if strings.Contains(string(raw), `"verdict":"blocked"`) {
		t.Errorf("a settled leg was rewritten as blocked by a later failure:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"state":"complete"`) {
		t.Errorf("the settled marker is not complete:\n%s", raw)
	}
}
