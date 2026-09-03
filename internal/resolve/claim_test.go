package resolve

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// TestClaim pins claim-before-model, create failure, stale versus fresh
// resume, measured from lib/run.sh:1922-1979 and lib/state.sh:322-339.
func TestClaim(t *testing.T) {
	t.Run("create is posted before the harness process", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if !firstCreateBefore(e.forge.order, "ReviewThreads") {
			t.Fatalf("claim was not first: %v", e.forge.order)
		}
		if len(e.forge.created) != 1 {
			t.Fatalf("created %d claims, want 1", len(e.forge.created))
		}
		body := e.forge.created[0].Body
		if !strings.Contains(body, "**crossrev — resolving pass 1**") {
			t.Errorf("claim heading missing: %s", body)
		}
		if !strings.Contains(body, "Verifying each finding against the codebase.") {
			t.Errorf("claim body missing: %s", body)
		}
		if !strings.Contains(body, "<!-- crossrev:") {
			t.Errorf("claim marker missing: %s", body)
		}
		if got.Marker.CommentID() == 0 {
			t.Fatal("marker has no comment id")
		}
	})

	t.Run("CommentCreate (0, nil) starts no harness", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.forge.zeroCreateID = true
		got := e.run(t)
		if got.Outcome != OutcomeRefused {
			t.Fatalf("Outcome = %q, want refused", got.Outcome)
		}
		if got.Err == nil || !strings.Contains(got.Err.Error(), "claim comment did not post") {
			t.Errorf("Err = %v, want the failed claim", got.Err)
		}
		if e.runner.specs != nil {
			t.Fatal("harness ran after CommentCreate (0, nil)")
		}
	})

	t.Run("create failure stops before a harness process", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.forge.createErr = errors.New("API down")
		got := e.run(t)
		if got.Outcome != OutcomeRefused {
			t.Fatalf("Outcome = %q, want refused", got.Outcome)
		}
		if e.runner.specs != nil {
			t.Fatal("harness ran after a failed claim")
		}
		var refusal *Refusal
		if !errors.As(got.Err, &refusal) {
			t.Fatalf("Err is %T %v", got.Err, got.Err)
		}
		if refusal.Message != "the claim comment did not post on acme/widget#42" {
			t.Errorf("Message = %q", refusal.Message)
		}
	})

	t.Run("fresh claim resumes without a second create", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.addResolve(t, prstate.Marker{
			State:       core.PassStarted,
			HeadSHA:     prstate.Some(e.head.SHA()),
			TS:          e.now.Unix() - 10,
			Harness:     prstate.Some("claude"),
			Resolutions: json.RawMessage("[]"),
		}, 9002)
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if len(e.forge.created) != 0 {
			t.Fatalf("resumed claim posted a second comment: %+v", e.forge.created)
		}
		if got.Marker.CommentID() != 9002 {
			t.Errorf("CommentID = %d, want 9002", got.Marker.CommentID())
		}
	})

	t.Run("a recovered started claim with mapped resolutions starts no harness", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.addResolve(t, prstate.Marker{
			State:       core.PassStarted,
			HeadSHA:     prstate.Some(e.head.SHA()),
			TS:          e.now.Unix() - 10,
			Harness:     prstate.Some("claude"),
			Resolutions: json.RawMessage(`[{"finding_id":"` + testFinding + `","resolution":"fixed","reply":"done"}]`),
		}, 9002)
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeComplete {
			t.Fatalf("Outcome = %q, want complete with recorded resolutions", got.Outcome)
		}
		if e.runner.specs != nil {
			t.Fatalf("harness ran over recorded resolutions: %d specs", len(e.runner.specs))
		}
	})

	t.Run("stale claim is abandoned and started again on the same comment", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.addResolve(t, prstate.Marker{
			State:       core.PassStarted,
			HeadSHA:     prstate.Some(e.head.SHA()),
			TS:          e.now.Add(-2 * time.Hour).Unix(),
			Harness:     prstate.Some("claude"),
			Resolutions: json.RawMessage(`[{"resolution":"fixed"}]`),
		}, 9002)
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeComplete {
			t.Fatalf("Outcome = %q, want complete", got.Outcome)
		}
		if len(e.forge.created) != 0 {
			t.Fatal("stale resume posted a new comment")
		}
		if got.Marker.CommentID() != 9002 {
			t.Errorf("CommentID = %d, want 9002", got.Marker.CommentID())
		}
		want := "abandoning the unfinished pass-1 resolve — it was made 120 minutes ago, past the 60-minute window\n   Resuming it would reconcile replies against a revision that has moved. Starting the pass again instead."
		if !strings.Contains(ui.Joined(got.Messages), want) {
			t.Errorf("messages = %q, want warning %q", got.Messages, want)
		}
		var recs []map[string]json.RawMessage
		if err := json.Unmarshal(got.Resolutions, &recs); err != nil {
			t.Fatalf("resolutions: %v", err)
		}
		if len(recs) != 1 {
			t.Fatalf("len(resolutions) = %d, want 1 from the new attempt", len(recs))
		}
		if _, ok := recs[0]["finding_id"]; !ok {
			t.Fatalf("new attempt did not map finding_id: %s", got.Resolutions)
		}
	})
}

func TestResolveRecordsBillingWithoutUsage(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Envelope.Usage != nil {
		t.Fatal("test harness unexpectedly reported usage")
	}
	if billing := got.Marker.Billing.Value(); billing == "" {
		t.Errorf("billing = %q, want the configured billing mode even without usage", billing)
	}
}

func TestNewClaimParityKeys(t *testing.T) {
	s := &session{
		pass:     1,
		settings: legSettings{Harness: "claude"},
	}
	m := newClaim(s, 1700000000, "abcdef0", "run-1")
	raw, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	const want = `{"v":1,"leg":"resolve","pass":1,"state":"started","ts":1700000000,"done_ts":null,"run_id":"run-1","head_sha":"abcdef0","harness":"claude","model":null,"effort":null,"endpoint":null,"model_reported":null,"tokens":null,"usage":null,"billing":null,"blocked":false,"blocked_reason":null,"commit_sha":null,"commit_subject":null,"summary":"","resolutions":[]}`
	if string(raw) != want {
		t.Errorf("marshalled\n got %s\nwant %s", string(raw), want)
	}
}

func TestResetRedriveParityKeys(t *testing.T) {
	done := prstate.Marker{
		Version:       core.MarkerVersion,
		Leg:           core.LegResolve,
		Pass:          1,
		State:         core.PassComplete,
		TS:            1700000000,
		DoneTS:        prstate.Some(int64(1700000010)),
		RunID:         prstate.Some("run-1"),
		HeadSHA:       prstate.Some("abcdef0"),
		Harness:       prstate.Some("claude"),
		Model:         prstate.Some("claude-3-5"),
		Effort:        prstate.Some("medium"),
		Endpoint:      prstate.Null[string](),
		ModelReported: prstate.Some("claude-3-5-sonnet"),
		Tokens:        json.RawMessage(`{"input":100}`),
		Usage:         json.RawMessage(`{"input":100}`),
		Billing:       prstate.Some("paid"),
		Blocked:       prstate.Some(true),
		BlockedReason: prstate.Some("some reason"),
		CommitSHA:     prstate.Some("c123456"),
		CommitSubject: prstate.Some("fix: something"),
		Summary:       prstate.Some("did some work"),
		Resolutions:   json.RawMessage(`[{"finding_id":"f1","resolution":"fixed"}]`),
		Unthreaded:    prstate.Some(1),
	}
	m := resetRedrive(done, 1700000100, "abcdef0", "run-2", legSettings{Harness: "claude"})
	raw, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	const want = `{"v":1,"leg":"resolve","pass":1,"state":"started","ts":1700000100,"done_ts":null,"run_id":"run-2","head_sha":"abcdef0","harness":"claude","model":null,"effort":null,"endpoint":null,"model_reported":null,"tokens":null,"usage":null,"billing":null,"blocked":false,"blocked_reason":null,"commit_sha":null,"commit_subject":null,"summary":"","resolutions":[]}`
	if string(raw) != want {
		t.Errorf("marshalled\n got %s\nwant %s", string(raw), want)
	}
}
