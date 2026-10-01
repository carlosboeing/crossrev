package review

import (
	"context"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/verify"
)

// waitForge is the forge double the wait tests drive the leg with: scripted
// check runs, labels and head, with a hook that runs after each evidence
// read so a case can finish a check, apply the stop label, move the head or
// cancel mid-wait.
type waitForge struct {
	forge.Forge
	labels      []string
	pr          forge.PullRequest
	checks      []forge.CheckRun
	checksCalls int
	onCheckRuns func(calls int)
}

func (f *waitForge) PullRequestLabels(context.Context, core.Slug, int) []string {
	return f.labels
}

func (f *waitForge) PullRequest(context.Context, core.Slug, int) (forge.PullRequest, error) {
	return f.pr, nil
}

func (f *waitForge) CheckRuns(context.Context, core.Slug, core.Revision) (forge.CheckRuns, error) {
	f.checksCalls++
	if f.onCheckRuns != nil {
		f.onCheckRuns(f.checksCalls)
	}
	return forge.CheckRuns{Runs: f.checks}, nil
}

// waitClock is the controllable clock the wait tests run under: Now reads
// the current time, Sleep advances it.
type waitClock struct {
	now    time.Time
	sleeps int
}

func (c *waitClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
	c.sleeps++
}

func waitHead(t *testing.T) core.Revision {
	t.Helper()
	head, err := core.NewRevision(cutoverHead)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func waitSettings(wait int) legSettings {
	return legSettings{
		requiredChecks: []config.RequiredCheck{{Name: "build", App: "github-actions"}},
		checkWait:      wait,
	}
}

func waitLeg(clock *waitClock, f *waitForge) *Leg {
	return &Leg{
		Forge: f,
		Now:   func() time.Time { return clock.now },
		Sleep: clock.advance,
	}
}

func pendingRun() forge.CheckRun {
	return forge.CheckRun{ID: 11, Name: "build", App: "github-actions", Status: "in_progress"}
}

func passedRun() forge.CheckRun {
	return forge.CheckRun{ID: 11, Name: "build", App: "github-actions", Status: "completed", Conclusion: "success"}
}

// A check that finishes mid-wait ends the wait passed: the first re-read
// sees the terminal state and stops.
func TestWaitReturnsWhenChecksFinish(t *testing.T) {
	clock := &waitClock{now: time.Unix(1_700_000_000, 0)}
	f := &waitForge{checks: []forge.CheckRun{pendingRun()}, pr: forge.PullRequest{HeadRefOid: waitHead(t)}}
	f.onCheckRuns = func(calls int) {
		if calls == 2 {
			f.checks = []forge.CheckRun{passedRun()}
		}
	}
	leg := waitLeg(clock, f)
	settings := waitSettings(10)
	current := leg.verificationEvidence(context.Background(), Context{}, settings, waitHead(t))
	if current.State != verify.Pending {
		t.Fatalf("initial evidence = %q, want pending", current.State)
	}
	// The hook answers the wait's first re-read with the finished check.
	got := leg.waitForVerification(context.Background(), Request{PR: 42}, Context{}, settings, waitHead(t), current)
	if got.State != verify.Passed {
		t.Errorf("waited evidence = %q, want passed", got.State)
	}
	if clock.sleeps != 1 || f.checksCalls != 2 {
		t.Errorf("sleeps = %d reads = %d, want 1 and 2", clock.sleeps, f.checksCalls)
	}
}

// The stop label ends the wait at once: the next tick sees it and returns
// the last evidence unread.
func TestWaitStopsOnStopLabel(t *testing.T) {
	clock := &waitClock{now: time.Unix(1_700_000_000, 0)}
	f := &waitForge{checks: []forge.CheckRun{pendingRun()}, pr: forge.PullRequest{HeadRefOid: waitHead(t)}}
	f.onCheckRuns = func(calls int) {
		if calls == 2 {
			f.labels = []string{policy.LabelStop}
		}
	}
	leg := waitLeg(clock, f)
	settings := waitSettings(10)
	current := leg.verificationEvidence(context.Background(), Context{}, settings, waitHead(t))
	got := leg.waitForVerification(context.Background(), Request{PR: 42}, Context{}, settings, waitHead(t), current)
	if got.State != verify.Pending {
		t.Errorf("waited evidence = %q, want the pending read back", got.State)
	}
	if clock.sleeps != 2 || f.checksCalls != 2 {
		t.Errorf("sleeps = %d reads = %d, want 2 and 2 (one tick read, one tick stopped)", clock.sleeps, f.checksCalls)
	}
}

// A moved head ends the wait: evidence for the old head is not worth
// waiting out.
func TestWaitStopsOnHeadChange(t *testing.T) {
	clock := &waitClock{now: time.Unix(1_700_000_000, 0)}
	f := &waitForge{checks: []forge.CheckRun{pendingRun()}, pr: forge.PullRequest{HeadRefOid: waitHead(t)}}
	moved, err := core.NewRevision("5555555555555555555555555555555555555555")
	if err != nil {
		t.Fatal(err)
	}
	f.onCheckRuns = func(calls int) {
		if calls == 2 {
			f.pr.HeadRefOid = moved
		}
	}
	leg := waitLeg(clock, f)
	settings := waitSettings(10)
	current := leg.verificationEvidence(context.Background(), Context{}, settings, waitHead(t))
	got := leg.waitForVerification(context.Background(), Request{PR: 42}, Context{}, settings, waitHead(t), current)
	if got.State != verify.Pending {
		t.Errorf("waited evidence = %q, want the pending read back", got.State)
	}
	if clock.sleeps != 2 || f.checksCalls != 2 {
		t.Errorf("sleeps = %d reads = %d, want 2 and 2", clock.sleeps, f.checksCalls)
	}
}

// Cancellation ends the wait without another read.
func TestWaitStopsOnCancellation(t *testing.T) {
	clock := &waitClock{now: time.Unix(1_700_000_000, 0)}
	f := &waitForge{checks: []forge.CheckRun{pendingRun()}, pr: forge.PullRequest{HeadRefOid: waitHead(t)}}
	ctx, cancel := context.WithCancel(context.Background())
	f.onCheckRuns = func(calls int) {
		if calls == 2 {
			cancel()
		}
	}
	leg := waitLeg(clock, f)
	settings := waitSettings(10)
	current := leg.verificationEvidence(ctx, Context{}, settings, waitHead(t))
	got := leg.waitForVerification(ctx, Request{PR: 42}, Context{}, settings, waitHead(t), current)
	if got.State != verify.Pending {
		t.Errorf("waited evidence = %q, want the pending read back", got.State)
	}
	if clock.sleeps != 1 || f.checksCalls != 2 {
		t.Errorf("sleeps = %d reads = %d, want 1 and 2", clock.sleeps, f.checksCalls)
	}
}

// A wait that never turns spends its whole budget re-reading every thirty
// seconds, then answers the last read: one minute buys two re-reads.
func TestWaitGivesUpAtDeadline(t *testing.T) {
	clock := &waitClock{now: time.Unix(1_700_000_000, 0)}
	f := &waitForge{checks: []forge.CheckRun{pendingRun()}, pr: forge.PullRequest{HeadRefOid: waitHead(t)}}
	leg := waitLeg(clock, f)
	settings := waitSettings(1)
	current := leg.verificationEvidence(context.Background(), Context{}, settings, waitHead(t))
	got := leg.waitForVerification(context.Background(), Request{PR: 42}, Context{}, settings, waitHead(t), current)
	if got.State != verify.Pending {
		t.Errorf("waited evidence = %q, want pending", got.State)
	}
	if clock.sleeps != 2 || f.checksCalls != 3 {
		t.Errorf("sleeps = %d reads = %d, want 2 and 3", clock.sleeps, f.checksCalls)
	}
}

// No wait configured means no waiting: the first read stands however long
// the checks still need.
func TestWaitSkippedAtZeroMinutes(t *testing.T) {
	clock := &waitClock{now: time.Unix(1_700_000_000, 0)}
	f := &waitForge{checks: []forge.CheckRun{pendingRun()}, pr: forge.PullRequest{HeadRefOid: waitHead(t)}}
	leg := waitLeg(clock, f)
	settings := waitSettings(0)
	current := leg.verificationEvidence(context.Background(), Context{}, settings, waitHead(t))
	got := leg.waitForVerification(context.Background(), Request{PR: 42}, Context{}, settings, waitHead(t), current)
	if got.State != verify.Pending {
		t.Errorf("evidence = %q, want the pending read back unwaited", got.State)
	}
	if clock.sleeps != 0 || f.checksCalls != 1 {
		t.Errorf("sleeps = %d reads = %d, want 0 and 1", clock.sleeps, f.checksCalls)
	}
}

// Terminal evidence is never waited on, however long the wait is.
func TestWaitSkippedOnTerminalEvidence(t *testing.T) {
	clock := &waitClock{now: time.Unix(1_700_000_000, 0)}
	f := &waitForge{pr: forge.PullRequest{HeadRefOid: waitHead(t)}}
	leg := waitLeg(clock, f)
	settings := waitSettings(10)
	for _, state := range []verify.State{verify.Passed, verify.Failed, verify.Unreadable, verify.None} {
		got := leg.waitForVerification(context.Background(), Request{PR: 42}, Context{}, settings, waitHead(t),
			verify.Evidence{State: state})
		if got.State != state {
			t.Errorf("state %q waited into %q", state, got.State)
		}
	}
	if clock.sleeps != 0 || f.checksCalls != 0 {
		t.Errorf("sleeps = %d reads = %d, want none", clock.sleeps, f.checksCalls)
	}
}
