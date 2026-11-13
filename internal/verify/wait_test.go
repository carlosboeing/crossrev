package verify_test

import (
	"context"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/verify"
)

// waitRig drives Wait on a fake clock: each sleep advances it, and the
// hooks report what the test sets.
type waitRig struct {
	clock   time.Time
	sleeps  int
	reads   int
	stop    bool
	moved   bool
	answers []verify.State
}

func (r *waitRig) now() time.Time { return r.clock }
func (r *waitRig) sleep(d time.Duration) {
	r.sleeps++
	r.clock = r.clock.Add(d)
}
func (r *waitRig) read() verify.Evidence {
	r.reads++
	state := verify.Pending
	if len(r.answers) > 0 {
		state, r.answers = r.answers[0], r.answers[1:]
	}
	return verify.Evidence{State: state}
}
func (r *waitRig) wait(ctx context.Context, minutes int) verify.Evidence {
	return verify.Wait(ctx, minutes, verify.Evidence{State: verify.Pending}, r.now, r.sleep,
		func() bool { return r.stop }, func() bool { return r.moved }, r.read)
}

func TestWaitReturnsTheFirstTerminalRead(t *testing.T) {
	r := &waitRig{answers: []verify.State{verify.Pending, verify.Passed}}
	if got := r.wait(context.Background(), 10); got.State != verify.Passed || r.reads != 2 {
		t.Fatalf("state=%s reads=%d, want passed after two reads", got.State, r.reads)
	}
}

func TestWaitStopsAtTheDeadline(t *testing.T) {
	r := &waitRig{}
	got := r.wait(context.Background(), 1)
	if got.State != verify.Pending || r.sleeps != int(time.Minute/verify.WaitInterval) {
		t.Fatalf("state=%s sleeps=%d, want pending after %d sleeps", got.State, r.sleeps, time.Minute/verify.WaitInterval)
	}
}

func TestWaitStopsOnTheStopLabel(t *testing.T) {
	r := &waitRig{stop: true}
	if got := r.wait(context.Background(), 10); got.State != verify.Pending || r.reads != 0 || r.sleeps != 1 {
		t.Fatalf("state=%s reads=%d sleeps=%d, want one sleep and no read", got.State, r.reads, r.sleeps)
	}
}

func TestWaitStopsOnAHeadChange(t *testing.T) {
	r := &waitRig{moved: true}
	if got := r.wait(context.Background(), 10); got.State != verify.Pending || r.reads != 0 || r.sleeps != 1 {
		t.Fatalf("state=%s reads=%d sleeps=%d, want one sleep and no read", got.State, r.reads, r.sleeps)
	}
}

func TestWaitStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &waitRig{}
	if got := r.wait(ctx, 10); got.State != verify.Pending || r.sleeps != 0 || r.reads != 0 {
		t.Fatalf("state=%s sleeps=%d reads=%d, want no sleep and no read", got.State, r.sleeps, r.reads)
	}
}

func TestWaitDoesNotWaitOnTerminalEvidenceOrZeroMinutes(t *testing.T) {
	r := &waitRig{}
	if got := verify.Wait(context.Background(), 10, verify.Evidence{State: verify.Failed}, r.now, r.sleep,
		func() bool { return false }, func() bool { return false }, r.read); got.State != verify.Failed || r.sleeps != 0 {
		t.Fatalf("state=%s sleeps=%d, want failed with no sleep", got.State, r.sleeps)
	}
	if got := r.wait(context.Background(), 0); got.State != verify.Pending || r.sleeps != 0 {
		t.Fatalf("state=%s sleeps=%d, want pending with no sleep", got.State, r.sleeps)
	}
}
