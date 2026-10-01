package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
)

// settleGateConfig names one required check. The wait is irrelevant to the
// settle — the resolve leg reads once — but the config carries it the way
// a real one would.
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

// A no-commit settle with a failed check stays awaiting-review: the loop
// owes the reviewer another look, which waits on and then halts over the
// same evidence.
func TestSettleWithFailedChecksStaysAwaitingReview(t *testing.T) {
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
	if !settleHasLabel(e, policy.LabelAwaitingReview) {
		t.Fatalf("addedLabels = %v, want %q", settleAdded(e), policy.LabelAwaitingReview)
	}
	record, ok := got.Marker.Verification.Get()
	if !ok || record.State != "failed" {
		t.Fatalf("marker verification = %+v,%v, want failed", record, ok)
	}
}

// The settle reads once and never waits: a pending gate refuses converged
// on one read.
func TestSettleReadsTheGateOnce(t *testing.T) {
	e := setup(t)
	settleSkipped(t, e)
	e.forge.checks = []forge.CheckRun{
		{ID: 11, Name: "build", App: "github-actions", Status: "in_progress",
			URL: "https://github.com/acme/widget/runs/11"},
	}
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if settleHasLabel(e, policy.LabelConverged) {
		t.Fatalf("converged label applied with a pending check: %v", settleAdded(e))
	}
	if e.forge.checksCalls != 1 {
		t.Errorf("check-run reads = %d, want 1 (the settle does not wait)", e.forge.checksCalls)
	}
}

// An unreadable gate refuses the settle the way a failed one does.
func TestSettleWithUnreadableGateStaysAwaitingReview(t *testing.T) {
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
	if !settleHasLabel(e, policy.LabelAwaitingReview) {
		t.Fatalf("addedLabels = %v, want %q", settleAdded(e), policy.LabelAwaitingReview)
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
