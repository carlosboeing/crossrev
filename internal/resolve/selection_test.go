package resolve

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// TestSelection covers the resolve-leg start-up table measured from
// lib/run.sh:1788-1836 and lib/legs.sh:157-175.
//
// Measured 2026-08-31 against this checkout:
//
//	empty: 0
//	one complete review: 1
//	two reviews newest: 2
//	declined skipped: 1
//	resolve only: 0
//	blocked redrive: yes
//	empty resolutions redrive: yes
//	fixed without commit redrive: yes
//	settled with commit: no
func TestSelection(t *testing.T) {
	t.Run("no review", func(t *testing.T) {
		e := setup(t)
		got := e.run(t)
		if got.Outcome != OutcomeRefused {
			t.Fatalf("Outcome = %q, want %q", got.Outcome, OutcomeRefused)
		}
		if got.Err == nil {
			t.Fatal("expected a refusal when there is no review")
		}
		var refusal *Refusal
		if !errors.As(got.Err, &refusal) {
			t.Fatalf("Err is %T %v, want *Refusal", got.Err, got.Err)
		}
		if refusal.Message != "acme/widget#42 has no review to resolve" {
			t.Errorf("Message = %q", refusal.Message)
		}
		if e.runner.specs != nil {
			t.Fatalf("harness ran without a review: %+v", e.runner.specs)
		}
	})

	t.Run("incomplete review", func(t *testing.T) {
		e := setup(t)
		m := prstate.Marker{
			Version:  core.MarkerVersion,
			Leg:      core.LegReview,
			Pass:     1,
			State:    core.PassStarted,
			TS:       e.now.Unix(),
			HeadSHA:  prstate.Some(e.head.SHA()),
			Verdict:  prstate.Null[string](),
			Findings: json.RawMessage("[]"),
		}
		e.postMarker(t, 9001, m)
		got := e.run(t)
		if got.Outcome != OutcomeRefused {
			t.Fatalf("Outcome = %q, want refused", got.Outcome)
		}
		var refusal *Refusal
		if !errors.As(got.Err, &refusal) {
			t.Fatalf("Err is %T %v", got.Err, got.Err)
		}
		if refusal.Message != "the pass-1 review on acme/widget#42 did not finish" {
			t.Errorf("Message = %q", refusal.Message)
		}
		if e.runner.specs != nil {
			t.Fatal("harness ran over an unfinished review")
		}
	})

	t.Run("blocked review", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, json.RawMessage("[]"), "blocked")
		got := e.run(t)
		if got.Outcome != OutcomeRefused {
			t.Fatalf("Outcome = %q, want refused", got.Outcome)
		}
		var refusal *Refusal
		if !errors.As(got.Err, &refusal) {
			t.Fatalf("Err is %T %v", got.Err, got.Err)
		}
		if refusal.Message != "the pass-1 review on acme/widget#42 was blocked" {
			t.Errorf("Message = %q", refusal.Message)
		}
		if e.runner.specs != nil {
			t.Fatal("harness ran over a blocked review")
		}
	})

	t.Run("newest of two complete reviews is the current pass", func(t *testing.T) {
		e := setup(t)
		e.addReviewPass(t, 1, defaultFindings(), "issues-remain", core.PassComplete)
		e.addReviewPass(t, 2, defaultFindings(), "issues-remain", core.PassComplete)
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Pass != 2 {
			t.Fatalf("Pass = %d, want 2 (lib/state.sh:294-296)", got.Pass)
		}
	})

	t.Run("a declined pass-2 review leaves resolve on pass 1", func(t *testing.T) {
		e := setup(t)
		e.addReviewPass(t, 1, defaultFindings(), "issues-remain", core.PassComplete)
		e.addReviewPass(t, 2, json.RawMessage("[]"), "declined", core.PassDeclined)
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Pass != 1 {
			t.Fatalf("Pass = %d, want 1; declined pass 2 is not a review that ran", got.Pass)
		}
	})

	t.Run("complete review invokes", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeComplete {
			t.Fatalf("Outcome = %q, want %q", got.Outcome, OutcomeComplete)
		}
		if got.Pass != 1 {
			t.Errorf("Pass = %d, want 1", got.Pass)
		}
		if len(e.runner.specs) == 0 {
			t.Fatal("harness was not invoked")
		}
	})

	t.Run("already resolved stays refused", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.addResolve(t, prstate.Marker{
			State:       core.PassComplete,
			Blocked:     prstate.Some(false),
			CommitSHA:   prstate.Some("d81a3f2abc000000000000000000000000000000"),
			Resolutions: json.RawMessage(`[{"resolution":"fixed"},{"resolution":"skipped"}]`),
			HeadSHA:     prstate.Some(e.head.SHA()),
			TS:          e.now.Unix() - 10,
		}, 9002)
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeAlreadyResolved {
			t.Fatalf("Outcome = %q, want %q", got.Outcome, OutcomeAlreadyResolved)
		}
		if e.runner.specs != nil {
			t.Fatal("harness ran over a settled pass")
		}
	})

	t.Run("fixed without commit redrives", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.addResolve(t, prstate.Marker{
			State:       core.PassComplete,
			Blocked:     prstate.Some(false),
			CommitSHA:   prstate.Null[string](),
			Resolutions: json.RawMessage(`[{"resolution":"fixed"},{"resolution":"disputed"}]`),
			HeadSHA:     prstate.Some(e.head.SHA()),
			TS:          e.now.Unix() - 10,
			Harness:     prstate.Some("claude"),
			Summary:     prstate.Some("tried"),
		}, 9002)
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeComplete {
			t.Fatalf("Outcome = %q, want complete on unpushed fix, got %v", got.Outcome, got.Err)
		}
		if len(e.forge.edits) == 0 {
			t.Fatal("redrive did not edit the finished claim")
		}
	})

	t.Run("empty resolution history redrives", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.addResolve(t, prstate.Marker{
			State:       core.PassComplete,
			Blocked:     prstate.Some(false),
			Resolutions: json.RawMessage(`[]`),
			HeadSHA:     prstate.Some(e.head.SHA()),
			TS:          e.now.Unix() - 10,
			Harness:     prstate.Some("claude"),
		}, 9002)
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeComplete {
			t.Fatalf("Outcome = %q, want complete", got.Outcome)
		}
	})

	t.Run("prior blocked pass redrives", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.addResolve(t, prstate.Marker{
			State:         core.PassComplete,
			Blocked:       prstate.Some(true),
			Resolutions:   json.RawMessage(`[]`),
			HeadSHA:       prstate.Some(e.head.SHA()),
			TS:            e.now.Unix() - 10,
			Harness:       prstate.Some("claude"),
			BlockedReason: prstate.Some("no write access to the working tree"),
		}, 9002)
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeComplete {
			t.Fatalf("Outcome = %q, want complete", got.Outcome)
		}
	})

	t.Run("no findings completes", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, json.RawMessage("[]"), "issues-remain")
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeNoFindings {
			t.Fatalf("Outcome = %q, want %q", got.Outcome, OutcomeNoFindings)
		}
		if e.runner.specs != nil {
			t.Fatal("harness ran over an empty finding list")
		}
	})

	t.Run("no findings with a standing escalation halts", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, json.RawMessage("[]"), "issues-remain")
		e.addResolve(t, prstate.Marker{
			Pass:        1,
			State:       core.PassComplete,
			Resolutions: json.RawMessage(`[{"resolution":"escalated"}]`),
			HeadSHA:     prstate.Some(e.head.SHA()),
			TS:          e.now.Unix() - 100,
		}, 8000)
		// Newest review is pass 2 with no findings. CurrentReviewPass is the
		// newest review, so add that review as pass 2.
		m := prstate.Marker{
			Version:  core.MarkerVersion,
			Leg:      core.LegReview,
			Pass:     2,
			State:    core.PassComplete,
			TS:       e.now.Unix() - 10,
			HeadSHA:  prstate.Some(e.head.SHA()),
			Verdict:  prstate.Some("issues-remain"),
			Findings: json.RawMessage("[]"),
		}
		e.postMarker(t, 9001, m)
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeHalted {
			t.Fatalf("Outcome = %q, want %q", got.Outcome, OutcomeHalted)
		}
		if e.runner.specs != nil {
			t.Fatal("harness ran over an empty pass with a standing escalation")
		}
	})
}
