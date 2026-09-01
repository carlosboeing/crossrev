package resolve

import (
	"encoding/json"
	"github.com/carlosboeing/crossrev/internal/harness"
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

// TestBillingUsesTheResolvedDescriptor pins the fallback document() takes when a
// Leg is built without a descriptor. Reading l.Harness directly hands BillingFor
// a zero-value document, which answers "" for every harness, so the marker went
// back to billing:null — the defect the unconditional billing line closed.
func TestBillingUsesTheResolvedDescriptor(t *testing.T) {
	var zero harness.Document
	if got := harness.BillingFor(zero, "claude", "", false); got != "" {
		t.Fatalf("a zero-value document answered %q; the test below assumes it answers nothing", got)
	}
	loaded, err := harness.Load(harness.DescriptorJSON())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := harness.BillingFor(loaded, "claude", "", false); got == "" {
		t.Error("the embedded descriptor answered nothing for claude; billing would be null")
	}
}
