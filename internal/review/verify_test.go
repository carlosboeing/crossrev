package review_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// gateConfig names one required check and no wait, so terminal evidence is
// judged at once and pending evidence halts rather than sleeping.
const gateConfig = "version: 2\nverification:\n  required_checks: [build]\n  wait_minutes: 0\n"

// convergedBatchPayload is the accepted converged answer over one required
// file: no findings, the unit covered, the scope reported. Coverage is
// clear, so only the gate decides.
func convergedBatchPayload() string {
	return `{"verdict":"converged","blocked_reason":null,"findings":[],"coverage":[` +
		`{"unit_number":1,"verdict":"no_issue","finding_numbers":[],` +
		`"evidence":[{"path":"a.go","revision":"` + headSHA + `","start_line":null,"end_line":null,"source":"git","note":null}],"reason":null}],` +
		`"examined_scope":"read the batch","known_limits":[]}`
}

func gateRun(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.cfg = mustConfig(t, gateConfig)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(convergedBatchPayload())},
	}
	return e
}

func labelsAdded(e *env) []string { return e.forge.labelsAdded }

func hasLabel(e *env, label string) bool {
	for _, added := range labelsAdded(e) {
		if added == label {
			return true
		}
	}
	return false
}

// A passing required check lets a clear pass converge, and the summary
// names the check with its run URL.
func TestRequiredChecksPassedConverges(t *testing.T) {
	e := gateRun(t)
	e.forge.checks = []forge.CheckRun{
		{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "success",
			URL: "https://github.com/acme/widget/runs/11"},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !hasLabel(e, policy.LabelConverged) {
		t.Fatalf("labelsAdded = %v, want %q", labelsAdded(e), policy.LabelConverged)
	}
	record, ok := got.Marker.Verification.Get()
	if !ok || record.State != "passed" {
		t.Fatalf("marker verification = %+v,%v, want passed", record, ok)
	}
	stored := e.forge.edits[len(e.forge.edits)-1]
	want := "Required checks: `build` passed ([run](https://github.com/acme/widget/runs/11))."
	if !strings.Contains(stored, want) {
		t.Errorf("stored summary lacks %q", want)
	}
	if !strings.Contains(stored, `"verification":{"state":"passed"`) {
		t.Errorf("stored marker lacks the verification record")
	}
}

// A failed check halts the pass with required_check_failed, naming the
// check, its conclusion and its run URL, with restart as the next step.
func TestRequiredCheckFailedHalts(t *testing.T) {
	e := gateRun(t)
	e.forge.checks = []forge.CheckRun{
		{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "failure",
			URL: "https://github.com/acme/widget/runs/11"},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if hasLabel(e, policy.LabelConverged) {
		t.Fatalf("converged label applied with a failed check: %v", labelsAdded(e))
	}
	if !hasLabel(e, policy.LabelHalted) {
		t.Fatalf("labelsAdded = %v, want %q", labelsAdded(e), policy.LabelHalted)
	}
	if verdict := got.Marker.Verdict.Value(); verdict != "blocked" {
		t.Errorf("marker verdict = %q, want blocked", verdict)
	}
	reason, _ := got.Marker.BlockedReason.Get()
	for _, want := range []string{"required_check_failed", "build", "failure", "https://github.com/acme/widget/runs/11"} {
		if !strings.Contains(reason, want) {
			t.Errorf("blocked reason = %q, want it to name %q", reason, want)
		}
	}
	joined := ui.Joined(got.Messages)
	if !strings.Contains(joined, "crossrev restart --pr 42") {
		t.Errorf("messages lack the restart next step: %q", joined)
	}
	stored := e.forge.edits[len(e.forge.edits)-1]
	if !strings.Contains(stored, "`build` failed (failure) ([run](https://github.com/acme/widget/runs/11))") {
		t.Errorf("stored summary lacks the failed check with its run URL")
	}
}

// A check with no reported run halts with required_check_missing, naming it.
func TestRequiredCheckMissingHalts(t *testing.T) {
	e := gateRun(t)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if hasLabel(e, policy.LabelConverged) {
		t.Fatalf("converged label applied with a missing check: %v", labelsAdded(e))
	}
	reason, _ := got.Marker.BlockedReason.Get()
	for _, want := range []string{"required_check_missing", "build"} {
		if !strings.Contains(reason, want) {
			t.Errorf("blocked reason = %q, want it to name %q", reason, want)
		}
	}
	stored := e.forge.edits[len(e.forge.edits)-1]
	if !strings.Contains(stored, "`build` has no reported run") {
		t.Errorf("stored summary lacks the missing check")
	}
}

// A check still running with no wait left halts with required_check_pending.
func TestRequiredCheckPendingHalts(t *testing.T) {
	e := gateRun(t)
	e.forge.checks = []forge.CheckRun{
		{ID: 11, Name: "build", App: "github-actions", Status: "in_progress",
			URL: "https://github.com/acme/widget/runs/11"},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if hasLabel(e, policy.LabelConverged) {
		t.Fatalf("converged label applied with a pending check: %v", labelsAdded(e))
	}
	reason, _ := got.Marker.BlockedReason.Get()
	for _, want := range []string{"required_check_pending", "build"} {
		if !strings.Contains(reason, want) {
			t.Errorf("blocked reason = %q, want it to name %q", reason, want)
		}
	}
}

// A gate the token may not read halts with required_checks_unreadable,
// naming the permission.
func TestRequiredChecksUnreadableHalts(t *testing.T) {
	e := gateRun(t)
	e.forge.checksErr = &forge.CheckRunsDenied{Status: 403, Err: errors.New("gh exited 1")}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if hasLabel(e, policy.LabelConverged) {
		t.Fatalf("converged label applied with an unreadable gate: %v", labelsAdded(e))
	}
	reason, _ := got.Marker.BlockedReason.Get()
	for _, want := range []string{"required_checks_unreadable", "checks: read"} {
		if !strings.Contains(reason, want) {
			t.Errorf("blocked reason = %q, want it to name %q", reason, want)
		}
	}
}

// A truncated enumeration is unreadable even when the runs it did list
// pass: the gate never judges a partial list.
func TestTruncatedEnumerationHaltsUnreadable(t *testing.T) {
	e := gateRun(t)
	e.forge.checks = []forge.CheckRun{
		{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "success",
			URL: "https://github.com/acme/widget/runs/11"},
	}
	e.forge.checksTruncated = true
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if hasLabel(e, policy.LabelConverged) {
		t.Fatalf("converged label applied on a truncated enumeration: %v", labelsAdded(e))
	}
	reason, _ := got.Marker.BlockedReason.Get()
	if !strings.Contains(reason, "required_checks_unreadable") {
		t.Errorf("blocked reason = %q, want required_checks_unreadable", reason)
	}
}

// An unconfigured gate reads no check runs at all: no forge call, no
// marker record, and the pass converges exactly as it always has.
func TestUnconfiguredGateReadsNoCheckRuns(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(convergedBatchPayload())},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !hasLabel(e, policy.LabelConverged) {
		t.Fatalf("labelsAdded = %v, want %q", labelsAdded(e), policy.LabelConverged)
	}
	if e.forge.checksCalls != 0 {
		t.Errorf("check-run reads = %d, want none", e.forge.checksCalls)
	}
	if _, ok := got.Marker.Verification.Get(); ok {
		t.Error("unconfigured marker carries a verification record")
	}
	stored := e.forge.edits[len(e.forge.edits)-1]
	if strings.Contains(stored, "Required checks") {
		t.Error("unconfigured summary names required checks")
	}
}
