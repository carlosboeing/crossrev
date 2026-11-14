package vcs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// StopState is what StopRunLock found a lock to be.
type StopState string

const (
	// StopNone means no lock named a local run for the pull request.
	StopNone StopState = "none"
	// StopCleared means the holder was already dead — a zombie included —
	// and its stale lock was removed without signalling anything.
	StopCleared StopState = "cleared"
	// StopStopped means the holder was live, was signalled, and is now
	// dead; its lock was removed, by the run itself as it unwound or here.
	StopStopped StopState = "stopped"
)

// StopResult is what a stop did, with the holder line it acted on.
type StopResult struct {
	// Holder is the lock line as it was read, empty when there was none.
	Holder Holder
	// State is which of the three outcomes above this was.
	State StopState
	// Path is the lock file, empty when there was none to look for.
	Path string
}

// The wait a stop gives a live holder. SIGTERM is the signal CrossRev's own
// entry point traps into a context cancellation, so a holder that is really
// a CrossRev run unwinds, releases its lock and writes its exit line well
// inside the term grace. The kill grace only covers a holder that heard the
// term and kept running anyway.
//
// Variables so the shape stays readable in one place; the values are the
// contract a test with a real sleeper exercises, so they are not shortened
// per test.
var (
	stopTermGrace = 5 * time.Second
	stopKillGrace = 2 * time.Second
	stopPoll      = 25 * time.Millisecond
)

// signalProcess delivers one signal to pid. A variable because StopRunLock's
// tests drive real sleepers for the answers that matter, and this seam is
// what would let one answer for a pid no test process may signal.
var signalProcess = func(pid int, sig syscall.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(sig)
}

// StopRunLock stops the local run holding one pull request, or clears the
// lock it left when it is already dead.
//
// A lock on another host is refused with the lock left in place: the pid in
// it names a process on that machine, and signalling a local process that
// happens to carry the same number would stop a stranger. A holder that is
// already dead — a zombie counts as dead, which is the whole reason
// PIDAlive exists — is cleared without a signal. A live holder gets
// SIGTERM first, because that is the signal a CrossRev run turns into an
// orderly unwind, and SIGKILL only after the term grace expires. The lock
// is removed only once the holder is dead, and only if the file still
// carries the line this call read, so a lock a new run took in the meantime
// is never the file this stop deletes.
func (r *Repository) StopRunLock(ctx context.Context, pr int) (StopResult, error) {
	common, err := r.CommonDir(ctx)
	if err != nil || common == "" {
		return StopResult{}, &Refusal{
			Message: "could not find this checkout's shared git directory",
			Hint:    "Run crossrev stop from inside a checkout of the repository.",
		}
	}
	path := filepath.Join(common, "crossrev", "pr-"+strconv.Itoa(pr)+".lock")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return StopResult{State: StopNone, Path: path}, nil
		}
		return StopResult{}, err
	}
	line := strings.TrimRight(string(raw), "\n")
	holder := ParseHolder(line)
	result := StopResult{Holder: holder, Path: path}

	if holder.Host != "" && holder.Host != hostname() {
		return result, &Refusal{
			Message: fmt.Sprintf("the run holding pull request %d is on %s, not this machine — %s", pr, holder.Host, holder.Raw),
			Hint:    "Stop it on that machine. The lock was left in place.",
		}
	}
	if holder.PID == 0 || !pidAlive(holder.PID) {
		if err := removeLockIfUnchanged(path, line); err != nil {
			return result, err
		}
		result.State = StopCleared
		return result, nil
	}
	if holder.PID == os.Getpid() {
		return result, &Refusal{
			Message: fmt.Sprintf("pull request %d is held by this process — %s", pr, holder.Raw),
			Hint:    "A stop cannot stop itself. The lock was left in place.",
		}
	}

	// Re-read before signalling: the holder this stop names must be the
	// holder still on disk, or the signal could land on a recycled pid
	// while the lock it meant has already changed hands.
	current, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			result.State = StopCleared
			return result, nil
		}
		return result, err
	}
	if strings.TrimRight(string(current), "\n") != line {
		return result, &Refusal{
			Message: fmt.Sprintf("the lock on pull request %d changed while it was being stopped — it now reads: %s", pr, strings.TrimRight(string(current), "\n")),
			Hint:    "Run crossrev stop again to act on the current holder. Nothing was signalled.",
		}
	}

	if err := signalProcess(holder.PID, syscall.SIGTERM); err != nil {
		if !pidAlive(holder.PID) {
			if err := removeLockIfUnchanged(path, line); err != nil {
				return result, err
			}
			result.State = StopCleared
			return result, nil
		}
		return result, &Refusal{
			Message: fmt.Sprintf("could not signal the run holding pull request %d — %s: %v", pr, holder.Raw, err),
			Hint:    "Check that the process is yours. The lock was left in place.",
		}
	}
	if waitForHolder(ctx, holder.PID, path, stopTermGrace) {
		if err := removeLockIfUnchanged(path, line); err != nil {
			return result, err
		}
		result.State = StopStopped
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}

	_ = signalProcess(holder.PID, syscall.SIGKILL)
	if waitForHolder(ctx, holder.PID, path, stopKillGrace) {
		if err := removeLockIfUnchanged(path, line); err != nil {
			return result, err
		}
		result.State = StopStopped
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, &Refusal{
		Message: fmt.Sprintf("the run holding pull request %d is still running after SIGTERM and SIGKILL — %s", pr, holder.Raw),
		Hint:    "The lock was left in place. Check the process, then run crossrev stop again.",
	}
}

// waitForHolder polls until the holder is dead or its lock has gone, which
// for a CrossRev holder are the same moment seen from two sides: the run
// releases the lock as it unwinds. It answers false only when the grace
// expires with both still standing, or when the context ends — the caller
// reads ctx to tell those two apart.
func waitForHolder(ctx context.Context, pid int, path string, grace time.Duration) bool {
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(stopPoll)
	defer ticker.Stop()
	for {
		if !pidAlive(pid) {
			return true
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return !pidAlive(pid)
		case <-ticker.C:
		}
	}
}

// removeLockIfUnchanged removes the lock only when it still carries the
// line the caller read. A lock a new run rewrote in the meantime is that
// run's file, not this caller's to delete.
func removeLockIfUnchanged(path, line string) error {
	current, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if strings.TrimRight(string(current), "\n") != line {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
