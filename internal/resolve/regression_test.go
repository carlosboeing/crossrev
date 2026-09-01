package resolve

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"os"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// TestInvokeSetsPayloadPathPerAttempt pins that the resolve invoke loop sets
// inv.PayloadPath to <transcriptBase>.payload for each attempt.
// Without it, the codex adapter refuses with ErrScratch.
func TestInvokeSetsPayloadPathPerAttempt(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter = nil // use the codex adapter from harness descriptors
	e.runner.onRun = func(spec exec.Spec) {
		for i := 0; i < len(spec.Args)-1; i++ {
			if spec.Args[i] == "-o" {
				_ = os.WriteFile(spec.Args[i+1], oneFindingPayload(), 0o644)
			}
		}
	}

	got := e.runReq(t, Request{
		PR:      42,
		Repo:    e.slug,
		Trigger: TriggerHuman,
		Harness: "codex",
	})
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.runner.specs) == 0 {
		t.Fatal("no runner spec recorded")
	}
	spec := e.runner.specs[0]
	var payloadPath string
	for i := 0; i < len(spec.Args)-1; i++ {
		if spec.Args[i] == "-o" {
			payloadPath = spec.Args[i+1]
			break
		}
	}
	if payloadPath == "" {
		t.Fatalf("spec args missing -o <payloadPath>: %v", spec.Args)
	}
	if !strings.HasSuffix(payloadPath, ".payload") {
		t.Fatalf("payload path %q does not end with .payload", payloadPath)
	}
}

// TestHarnessOverrideKeepsConfiguredEffort pins that a --harness override
// clears model and endpoint while keeping the configured .resolver.effort.
func TestHarnessOverrideKeepsConfiguredEffort(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.git.show = map[string][]byte{
		e.base.SHA() + ":.github/crossrev.yml": []byte("version: 1\nresolver:\n  harness: codex\n  model: o3-mini\n  effort: high\n  endpoint: https://example.invalid\n"),
	}
	got := e.runReq(t, Request{
		PR:      42,
		Repo:    e.slug,
		Trigger: TriggerHuman,
		Harness: "claude",
	})
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.adapter.invs) == 0 {
		t.Fatal("adapter was not invoked")
	}
	inv := e.adapter.invs[0]
	if inv.Effort != "high" {
		t.Fatalf("inv.Effort = %q, want %q", inv.Effort, "high")
	}
	if inv.Model != "" {
		t.Fatalf("inv.Model = %q, want empty", inv.Model)
	}
	if inv.Endpoint.Named() || inv.Endpoint.URL != "" {
		t.Fatalf("inv.Endpoint = %+v, want empty", inv.Endpoint)
	}
	if got.Marker.Effort.Value() != "high" {
		t.Fatalf("marker Effort = %q, want %q", got.Marker.Effort.Value(), "high")
	}
	if !got.Marker.Model.IsNull() && !got.Marker.Model.IsZero() {
		t.Fatalf("marker Model = %v, want null", got.Marker.Model)
	}
	if !got.Marker.Endpoint.IsNull() && !got.Marker.Endpoint.IsZero() {
		t.Fatalf("marker Endpoint = %v, want null", got.Marker.Endpoint)
	}
}

// TestResolveSubstituteHarnessWarningKeepsSecondSentence pins that when a missing
// configured resolver harness falls back to another harness, the warning message
// retains the second sentence naming the asked harness.
func TestResolveSubstituteHarnessWarningKeepsSecondSentence(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.git.show = map[string][]byte{
		e.base.SHA() + ":.github/crossrev.yml": []byte("version: 1\nresolver:\n  harness: codex\n"),
	}
	e.lookPath = func(name string) (string, error) {
		if name == "claude" {
			return "/usr/bin/claude", nil
		}
		return "", os.ErrNotExist
	}
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	const wantSentence = "Both legs now run on the same harness, so a bug it misses while reviewing it also misses while resolving. Install codex to get the second lineage back."
	found := false
	for _, msg := range got.Messages {
		if strings.Contains(msg, wantSentence) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("messages = %v, want warning containing sentence %q", got.Messages, wantSentence)
	}
}

// TestResolveCompletionMarkerHoldsEffortReportedNull pins that a completed resolve
// pass with an envelope reporting no effort still serializes \"effort_reported\":null.
func TestResolveCompletionMarkerHoldsEffortReportedNull(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	raw, err := json.Marshal(got.Marker)
	if err != nil {
		t.Fatalf("json.Marshal(Marker): %v", err)
	}
	if !strings.Contains(string(raw), `"effort_reported":null`) {
		t.Fatalf("marker JSON = %s, want it to contain \"effort_reported\":null", string(raw))
	}
	found := false
	for _, edit := range e.forge.edits {
		if strings.Contains(edit.Body, `"effort_reported":null`) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("forge comment edits did not contain \"effort_reported\":null: %v", e.forge.edits)
	}
}

// TestBillingUsesTheResolvedDescriptor drives the marker path on a Leg built
// with no descriptor, which is the case document() exists for: it falls back to
// the embedded one. Reading l.Harness directly hands BillingFor a zero-value
// document, which answers "" for every harness, so the marker reads
// billing:null — the defect the unconditional billing line closed.
//
// Asserting on BillingFor alone would not catch that, because the substitution
// happens in attachPayload rather than in BillingFor.
func TestBillingUsesTheResolvedDescriptor(t *testing.T) {
	leg := &Leg{} // no Harness: document() must fall back to the embedded one
	marker := prstate.Marker{
		Harness:  prstate.Some("claude"),
		Endpoint: prstate.Null[string](),
	}
	got := leg.attachPayload(marker, Result{Envelope: harness.Envelope{}})
	billing, ok := got.Billing.Get()
	if !ok || billing == "" {
		t.Fatalf("billing = %v (present=%v), want a mode from the embedded descriptor", billing, ok)
	}
	if got.Billing.IsNull() {
		t.Error("billing is null; the zero-value document was used instead of the fallback")
	}
}

// TestRestoreFailureNamesTheRunsOwnFailure pins that a runner failure survives a
// failed restore. Bash puts the reason the attempt is abandoned in that message
// (lib/run.sh:684), so replacing it with a placeholder loses the half a reader
// acts on.
func TestRestoreFailureNamesTheRunsOwnFailure(t *testing.T) {
	cases := []struct {
		name string
		res  exec.Result
		want string
	}{
		{"runner error", exec.Result{Err: errors.New("exec: signal killed")}, "exec: signal killed"},
		{"non-zero exit", exec.Result{ExitCode: 3}, "the harness exited 3"},
		{"clean run", exec.Result{}, "the attempt finished and its answer was not read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := runFailureCause(c.res); got != c.want {
				t.Errorf("runFailureCause = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRestoreFailureCallSiteDerivesTheCause is a source assertion, because the
// unit test above pins runFailureCause and not the call that uses it: reverting
// the call site to a literal leaves that test green. The neutral phrase belongs
// inside runFailureCause and nowhere else, so finding it in invoke.go means the
// call site stopped reading the run's own failure.
func TestRestoreFailureCallSiteDerivesTheCause(t *testing.T) {
	src, err := os.ReadFile("invoke.go")
	if err != nil {
		t.Fatalf("read invoke.go: %v", err)
	}
	const placeholder = `"the attempt finished and its answer was not read"`
	if bytes.Contains(src, []byte(placeholder)) {
		t.Errorf("invoke.go names the neutral cause directly; it must call runFailureCause(res) so a runner error or a non-zero exit reaches the message")
	}
	if !bytes.Contains(src, []byte("runFailureCause(res)")) {
		t.Error("invoke.go no longer derives the cause from the run")
	}
}
