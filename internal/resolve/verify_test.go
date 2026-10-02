package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
)

// settleGateConfig names one required check and a bounded wait.
const settleGateConfig = "version: 2\nverification:\n  required_checks: [build]\n  wait_minutes: 10\n"

// seedCompleteGeneration publishes one generation at the fixture head with
// every record covered, and names it from the posted review marker, so the
// settle gate has current complete coverage to judge beside the checks.
func seedCompleteGeneration(t *testing.T, e *testEnv) {
	t.Helper()
	unitID := string(core.FileUnitID("a.go"))
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	gen := prstate.Generation{
		Gen:      1,
		Revision: core.RevisionPair{Base: e.base, Head: e.head},
		Engine:   testEngineID(),
		Slot:     prstate.DefaultSlot,
		Producer: prstate.Producer{Harness: "codex"},
		Form:     prstate.GenerationFull,
		Paths:    []string{"a.go"},
		Records: []prstate.Record{{
			Type:       prstate.CoverageRecordUnit,
			UnitID:     unitID,
			PathIndex:  0,
			Kind:       "file",
			Change:     "modified",
			BodyDigest: digest,
			Verdict:    prstate.Some("no_issue"),
		}},
		ScopeReport: prstate.ScopeReport{ExaminedScope: "read the batch", KnownLimits: []string{}},
	}
	store := storetest.NewFakeStore()
	e.forge.ledger = store
	ref := prstate.SlotRef{Repo: e.slug, Number: 42, Slot: prstate.DefaultSlot}
	handle, err := store.PublishGeneration(context.Background(), ref, prstate.Handle{}, gen)
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	setReviewCoverage(t, e, handle)
}

func settleSkipped(t *testing.T, e *testEnv) {
	t.Helper()
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.payloads = []json.RawMessage{json.RawMessage(
		`{"blocked":false,"blocked_reason":null,"summary":"Not a bug.","commit_subject":null,` +
			`"resolutions":[{"finding_number":1,"resolution":"skipped","reply":"no",` +
			`"persist":null,"duplicate_of":null}]}`)}
	seedCompleteGeneration(t, e)
	e.git.show = map[string][]byte{
		e.base.SHA() + ":.github/crossrev.yml": []byte(settleGateConfig),
	}
}

func settleAdded(e *testEnv) []string { return e.forge.addedLabels }

func settleHasLabel(e *testEnv, label string) bool {
	for _, added := range settleAdded(e) {
		if added == label {
			return true
		}
	}
	return false
}

// A no-commit settle with passing checks reports converged, and the marker
// records the evidence it judged.
func TestSettleWithPassingChecksConverges(t *testing.T) {
	e := setup(t)
	settleSkipped(t, e)
	e.forge.checks = []forge.CheckRun{
		{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "success",
			URL: "https://github.com/acme/widget/runs/11"},
	}
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !settleHasLabel(e, policy.LabelConverged) {
		t.Fatalf("addedLabels = %v, want %q", settleAdded(e), policy.LabelConverged)
	}
	record, ok := got.Marker.Verification.Get()
	if !ok || record.State != "passed" {
		t.Fatalf("marker verification = %+v,%v, want passed", record, ok)
	}
	if e.forge.checksCalls != 1 {
		t.Errorf("check-run reads = %d, want 1", e.forge.checksCalls)
	}
}

// A failed check halts an otherwise settled no-commit pass.
func TestSettleWithFailedChecksHalts(t *testing.T) {
	e := setup(t)
	settleSkipped(t, e)
	e.forge.checks = []forge.CheckRun{
		{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "failure",
			URL: "https://github.com/acme/widget/runs/11"},
	}
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if settleHasLabel(e, policy.LabelConverged) {
		t.Fatalf("converged label applied with a failed check: %v", settleAdded(e))
	}
	if !settleHasLabel(e, policy.LabelHalted) {
		t.Fatalf("addedLabels = %v, want %q", settleAdded(e), policy.LabelHalted)
	}
	record, ok := got.Marker.Verification.Get()
	if !ok || record.State != "failed" {
		t.Fatalf("marker verification = %+v,%v, want failed", record, ok)
	}
}

// Pending checks can finish while the settle waits, without another model call.
func TestSettleWaitsForPassingChecks(t *testing.T) {
	e := setup(t)
	settleSkipped(t, e)
	e.forge.checks = []forge.CheckRun{{ID: 11, Name: "build", App: "github-actions", Status: "in_progress"}}
	sleeps := 0
	e.sleep = func(d time.Duration) {
		sleeps++
		e.now = e.now.Add(d)
		e.forge.checks[0].Status = "completed"
		e.forge.checks[0].Conclusion = "success"
	}
	got := e.run(t)
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	if !settleHasLabel(e, policy.LabelConverged) || sleeps != 1 || e.adapter.calls != 1 {
		t.Fatalf("labels=%v sleeps=%d model calls=%d", settleAdded(e), sleeps, e.adapter.calls)
	}
}

func TestGateHeldSettleRedrivesWithoutModel(t *testing.T) {
	e := setup(t)
	settleSkipped(t, e)
	e.forge.checks = []forge.CheckRun{{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "failure"}}
	got := e.run(t)
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	if !settleHasLabel(e, policy.LabelHalted) {
		t.Fatalf("labels=%v", settleAdded(e))
	}
	var output strings.Builder
	for _, line := range got.Messages {
		output.WriteString(line.Text)
	}
	if !strings.Contains(output.String(), "required_check_failed: build") || !strings.Contains(output.String(), "crossrev restart --pr 42") {
		t.Fatalf("output=%s", output.String())
	}
	if !strings.Contains(e.forge.edits[len(e.forge.edits)-1].Body, "required_check_failed: build") {
		t.Fatal("summary omits gate debt")
	}
	// Persist edits as GitHub does, including the complete resolve marker.
	for _, edit := range e.forge.edits {
		for i := range e.forge.comments {
			if e.forge.comments[i].ID == edit.CommentID {
				e.forge.comments[i].Body = edit.Body
			}
		}
	}
	e.forge.addedLabels = nil
	e.forge.checks[0].Conclusion = "success"
	got = e.run(t)
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	if !settleHasLabel(e, policy.LabelConverged) || e.adapter.calls != 1 {
		t.Fatalf("labels=%v model calls=%d outcome=%s", settleAdded(e), e.adapter.calls, got.Outcome)
	}
	if ev, ok := got.Marker.Verification.Get(); !ok || ev.State != "passed" {
		t.Fatalf("evidence=%+v", ev)
	}
}

func TestEmptyFindingsRunLoadsGate(t *testing.T) {
	for _, covered := range []bool{false, true} {
		t.Run(fmt.Sprint(covered), func(t *testing.T) {
			e := setup(t)
			e.addReview(t, nil, "converged")
			if covered {
				seedCompleteGeneration(t, e)
			}
			e.git.show = map[string][]byte{e.base.SHA() + ":.github/crossrev.yml": []byte(settleGateConfig)}
			e.forge.checks = []forge.CheckRun{{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "failure"}}
			got := e.run(t)
			if got.Err != nil {
				t.Fatal(got.Err)
			}
			if settleHasLabel(e, policy.LabelConverged) || !settleHasLabel(e, policy.LabelHalted) {
				t.Fatalf("labels=%v", settleAdded(e))
			}
			if ev, ok := got.Marker.Verification.Get(); !ok || ev.State != "failed" {
				t.Fatalf("evidence=%+v", ev)
			}
			if e.adapter.calls != 0 {
				t.Fatal("empty settle invoked model")
			}
		})
	}
}

// An unreadable gate refuses the settle the way a failed one does.
func TestSettleWithUnreadableGateHalts(t *testing.T) {
	e := setup(t)
	settleSkipped(t, e)
	e.forge.checksErr = &forge.CheckRunsDenied{Status: 403, Err: errSettleDenied}
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if settleHasLabel(e, policy.LabelConverged) {
		t.Fatalf("converged label applied with an unreadable gate: %v", settleAdded(e))
	}
	if !settleHasLabel(e, policy.LabelHalted) {
		t.Fatalf("addedLabels=%v, want halted", settleAdded(e))
	}
	record, ok := got.Marker.Verification.Get()
	if !ok || record.State != "unreadable" {
		t.Fatalf("marker verification = %+v,%v, want unreadable", record, ok)
	}
}

// An unconfigured settle reads no check runs and keeps its legacy label.
func TestUnconfiguredSettleReadsNoCheckRuns(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.payloads = []json.RawMessage{json.RawMessage(
		`{"blocked":false,"blocked_reason":null,"summary":"Not a bug.","commit_subject":null,` +
			`"resolutions":[{"finding_number":1,"resolution":"skipped","reply":"no",` +
			`"persist":null,"duplicate_of":null}]}`)}
	seedCompleteGeneration(t, e)
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !settleHasLabel(e, policy.LabelConverged) {
		t.Fatalf("addedLabels = %v, want %q", settleAdded(e), policy.LabelConverged)
	}
	if e.forge.checksCalls != 0 {
		t.Errorf("check-run reads = %d, want none", e.forge.checksCalls)
	}
	if _, ok := got.Marker.Verification.Get(); ok {
		t.Error("unconfigured marker carries a verification record")
	}
}

// A no-commit settle over a review marker with no coverage claim still
// judges the required-check gate: with checks failing, the frozen path
// holds off converged rather than keeping its legacy label.
func TestSettleWithoutCoverageClaimRefusesConvergedOnAFailedGate(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.payloads = []json.RawMessage{json.RawMessage(
		`{"blocked":false,"blocked_reason":null,"summary":"Not a bug.","commit_subject":null,` +
			`"resolutions":[{"finding_number":1,"resolution":"skipped","reply":"no",` +
			`"persist":null,"duplicate_of":null}]}`)}
	e.git.show = map[string][]byte{
		e.base.SHA() + ":.github/crossrev.yml": []byte(settleGateConfig),
	}
	e.forge.checks = []forge.CheckRun{
		{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "failure",
			URL: "https://github.com/acme/widget/runs/11"},
	}
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if settleHasLabel(e, policy.LabelConverged) {
		t.Fatalf("converged label applied with a failed check: %v", settleAdded(e))
	}
	if !settleHasLabel(e, policy.LabelHalted) {
		t.Fatalf("addedLabels = %v, want %q", settleAdded(e), policy.LabelHalted)
	}
	if e.forge.checksCalls != 1 {
		t.Errorf("check-run reads = %d, want 1", e.forge.checksCalls)
	}
}

// A no-commit settle over a review marker with no coverage claim keeps its
// legacy label when the gate passes: the frozen path holds converged to
// the checks, it does not refuse it outright.
func TestSettleWithoutCoverageClaimKeepsLegacyLabelOnAPassingGate(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.payloads = []json.RawMessage{json.RawMessage(
		`{"blocked":false,"blocked_reason":null,"summary":"Not a bug.","commit_subject":null,` +
			`"resolutions":[{"finding_number":1,"resolution":"skipped","reply":"no",` +
			`"persist":null,"duplicate_of":null}]}`)}
	e.git.show = map[string][]byte{
		e.base.SHA() + ":.github/crossrev.yml": []byte(settleGateConfig),
	}
	e.forge.checks = []forge.CheckRun{
		{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "success",
			URL: "https://github.com/acme/widget/runs/11"},
	}
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !settleHasLabel(e, policy.LabelConverged) {
		t.Fatalf("addedLabels = %v, want %q", settleAdded(e), policy.LabelConverged)
	}
	if e.forge.checksCalls != 1 {
		t.Errorf("check-run reads = %d, want 1", e.forge.checksCalls)
	}
}

var errSettleDenied = errors.New("gh exited 1")

// The empty-findings route refuses converged on a failed gate: with no
// findings to settle and checks failing, a human must re-drive the review.
func TestEmptyFindingsRefusesConvergedOnAFailedGate(t *testing.T) {
	e := setup(t)
	store := storetest.NewFakeStore()
	e.forge.ledger = store
	gen := prstate.Generation{
		Gen:      1,
		Revision: core.RevisionPair{Base: e.forge.pr.BaseRefOid, Head: e.forge.pr.HeadRefOid},
		Engine:   testEngineID(),
		Slot:     prstate.DefaultSlot,
		Producer: prstate.Producer{Harness: "codex"},
		Form:     prstate.GenerationFull,
		Paths:    []string{"a.go"},
		Records: []prstate.Record{{
			Type:       prstate.CoverageRecordUnit,
			UnitID:     string(core.FileUnitID("a.go")),
			PathIndex:  0,
			Kind:       prstate.CoverageGranularityFile,
			Change:     string(core.ChangeModified),
			BodyDigest: core.BodyDigestHex([]byte("body of a.go")),
			Verdict:    prstate.Some("no_issue"),
		}},
		ScopeReport: prstate.ScopeReport{ExaminedScope: "read the batch"},
	}
	handle, err := store.PublishGeneration(context.Background(),
		prstate.SlotRef{Repo: e.slug, Number: 42, Slot: prstate.DefaultSlot}, prstate.Handle{}, gen)
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	review := cutoverReviewMarker(t, e, func(m *prstate.Marker) {
		m.RecordCoverage(handle)
	})
	s := cutoverSession(t, e, review)
	s.settings.RequiredChecks = []config.RequiredCheck{{Name: "build", App: "github-actions"}}
	e.forge.checks = []forge.CheckRun{
		{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "failure",
			URL: "https://github.com/acme/widget/runs/11"},
	}
	got := cutoverLeg(e).finishEmpty(context.Background(), s, Result{Outcome: OutcomeComplete})
	if got.Outcome != OutcomeComplete {
		t.Fatalf("Outcome = %q, want the pass outcome carried through", got.Outcome)
	}
	if settleHasLabel(e, policy.LabelConverged) {
		t.Fatalf("converged label applied with a failed check: %v", settleAdded(e))
	}
	if !settleHasLabel(e, policy.LabelHalted) {
		t.Fatalf("addedLabels = %v, want %q", settleAdded(e), policy.LabelHalted)
	}
}

// The empty-findings route holds off converged on a failed gate even with
// no coverage claim on the marker: the legacy label stands only when the
// checks passed or none were required.
func TestEmptyFindingsWithoutCoverageClaimRefusesConvergedOnAFailedGate(t *testing.T) {
	e := setup(t)
	e.forge.ledger = storetest.NewFakeStore()
	review := cutoverReviewMarker(t, e, nil)
	s := cutoverSession(t, e, review)
	s.settings.RequiredChecks = []config.RequiredCheck{{Name: "build", App: "github-actions"}}
	e.forge.checks = []forge.CheckRun{
		{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "failure",
			URL: "https://github.com/acme/widget/runs/11"},
	}
	got := cutoverLeg(e).finishEmpty(context.Background(), s, Result{Outcome: OutcomeComplete})
	if got.Outcome != OutcomeComplete {
		t.Fatalf("Outcome = %q, want the pass outcome carried through", got.Outcome)
	}
	if settleHasLabel(e, policy.LabelConverged) {
		t.Fatalf("converged label applied with a failed check: %v", settleAdded(e))
	}
	if !settleHasLabel(e, policy.LabelHalted) {
		t.Fatalf("addedLabels = %v, want %q", settleAdded(e), policy.LabelHalted)
	}
	if e.forge.checksCalls != 1 {
		t.Errorf("check-run reads = %d, want 1", e.forge.checksCalls)
	}
}

func TestEmptyGateHaltCanClearChecksOnRedrive(t *testing.T) {
	e := setup(t)
	e.addReview(t, nil, "converged")
	e.git.show = map[string][]byte{e.base.SHA() + ":.github/crossrev.yml": []byte(settleGateConfig)}
	e.forge.checks = []forge.CheckRun{{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "failure"}}
	got := e.run(t)
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	for _, edit := range e.forge.edits {
		for i := range e.forge.comments {
			if e.forge.comments[i].ID == edit.CommentID {
				e.forge.comments[i].Body = edit.Body
			}
		}
	}
	e.forge.addedLabels = nil
	got = e.runReq(t, Request{PR: 42, Repo: e.slug, NoRequiredChecks: true})
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	if ev, ok := got.Marker.Verification.Get(); !ok || ev.State != "none_required" {
		t.Fatalf("verification=%+v", ev)
	}
	if !settleHasLabel(e, policy.LabelConverged) || e.adapter.calls != 0 {
		t.Fatalf("labels=%v model calls=%d", settleAdded(e), e.adapter.calls)
	}
}

// A gate-held settle whose head moved owes the reviewer the new revision:
// the restart hands back to review instead of re-running the resolver.
func TestGateHeldSettleWithMovedHeadHandsBackToReview(t *testing.T) {
	e := setup(t)
	settleSkipped(t, e)
	e.forge.checks = []forge.CheckRun{{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "failure"}}
	if got := e.run(t); got.Err != nil {
		t.Fatal(got.Err)
	}
	for _, edit := range e.forge.edits {
		for i := range e.forge.comments {
			if e.forge.comments[i].ID == edit.CommentID {
				e.forge.comments[i].Body = edit.Body
			}
		}
	}
	e.forge.addedLabels = nil
	moved := mustRev(t, "9999999999999999999999999999999999999999")
	e.forge.pr.HeadRefOid = moved
	e.git.head = moved
	got := e.run(t)
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	if !settleHasLabel(e, policy.LabelAwaitingReview) || settleHasLabel(e, policy.LabelConverged) || e.adapter.calls != 1 {
		t.Fatalf("labels=%v model calls=%d outcome=%s", settleAdded(e), e.adapter.calls, got.Outcome)
	}
}

// A full re-drive starts its claim without the previous pass's gate
// record, so the old evidence neither skips the wait nor outlives the
// pass that judged it.
func TestResetRedriveClearsTheGateRecord(t *testing.T) {
	done := prstate.Marker{Verification: prstate.Some(prstate.MarkerVerification{State: "failed"})}
	if got := resetRedrive(done, 1, testHeadSHA, "run", legSettings{Harness: "claude"}); got.Verification.Present() {
		t.Fatalf("verification=%+v, want cleared", got.Verification)
	}
}
