package resolve

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
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
