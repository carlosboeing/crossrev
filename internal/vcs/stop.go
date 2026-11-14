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

// beforeLockRemove runs between the comparison and the removal in
// removeLockIfUnchanged. It does nothing; it is a variable so a test can
// attempt a takeover at the one moment the guard exists to close.
var beforeLockRemove = func() {}

// holderStartSlack is how far after the lock's own timestamp a process may
// have started and still be taken for the run that wrote it. The two times
// reach this code by different routes — the lock line from the wall clock,
// the start time from the process table, on Linux as ticks after a boot
// time held in whole seconds — so a comparison exact to the second could
// refuse a real holder on rounding alone. A reused pid starts after its
// previous owner died, which no pid allocator brings round in two seconds.
const holderStartSlack = 2 * time.Second

// StopRunLock stops the local run holding one pull request, or clears the
// lock it left when it is already dead.
//
// A lock on another host is refused with the lock left in place: the pid in
// it names a process on that machine, and signalling a local process that
// happens to carry the same number would stop a stranger. A live holder
// whose line names no host is refused for the same reason. A holder that is
// already dead — a zombie counts as dead, which is the whole reason
// PIDAlive exists — is cleared without a signal. A live process carrying
// the holder's pid is signalled only when it started before the lock was
// taken; see refuseUnidentifiedHolder. It gets SIGTERM first, because that
// is the signal a CrossRev run turns into an orderly unwind, and SIGKILL
// only after the term grace expires. The lock is removed only once the
// holder is dead, and only if the file still carries the line this call
// read, so a lock a new run took in the meantime is never the file this
// stop deletes.
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
	if refusal := refuseUnidentifiedHolder(pr, holder, path); refusal != nil {
		return result, refusal
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

// refuseUnidentifiedHolder answers a refusal unless the live process
// carrying the holder's pid can be shown to be the run that took the lock.
//
// A lock outlives a holder that died without releasing it, and the kernel
// hands that pid to a later process. The host, the liveness probe and the
// unchanged lock line all still pass then, and a signal would stop a
// stranger. What tells the two apart is when the process started: a process
// keeps its pid from start to exit, so one that started before the lock was
// written held that pid when it was written, and is the writer. One that
// started afterwards cannot be.
//
// Nothing is signalled and the lock stays in place in all four refusals: a
// lock line that names no host, one with no readable timestamp, a process
// whose start time this machine will not give, and a process that started
// after the lock was taken. The last is not cleared as a dead holder's lock
// would be, because a wall clock stepped forward since the lock was taken
// reads the same way for a holder that is still running.
func refuseUnidentifiedHolder(pr int, holder Holder, path string) *Refusal {
	hint := fmt.Sprintf("Nothing was signalled and the lock was left in place. Check the process with: ps -p %d. If no CrossRev run is working on the pull request, remove %s", holder.PID, path)
	if holder.Host == "" {
		return &Refusal{
			Message: fmt.Sprintf("cannot tell which machine took the lock on pull request %d, because the holder line names no host — %s", pr, holder.Raw),
			Hint:    hint,
		}
	}
	since, err := time.Parse(time.RFC3339, holder.Since)
	if err != nil {
		return &Refusal{
			Message: fmt.Sprintf("cannot tell whether pid %d is the run that took the lock on pull request %d, because the lock does not say when it was taken — %s", holder.PID, pr, holder.Raw),
			Hint:    hint,
		}
	}
	started, ok := processStartTime(holder.PID)
	if !ok {
		return &Refusal{
			Message: fmt.Sprintf("cannot tell whether pid %d is the run that took the lock on pull request %d, because its start time could not be read — %s", holder.PID, pr, holder.Raw),
			Hint:    hint,
		}
	}
	if started.After(since.Add(holderStartSlack)) {
		return &Refusal{
			Message: fmt.Sprintf("pid %d is not the run that took the lock on pull request %d: it started at %s, after the lock was taken — %s", holder.PID, pr, started.UTC().Format(time.RFC3339), holder.Raw),
			Hint:    hint,
		}
	}
	return nil
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
//
// The comparison and the removal run under the guard AcquireRunLock takes a
// lock under, so no run can replace the file between the two. A lock, or a
// lock directory, that is already gone leaves nothing to remove.
func removeLockIfUnchanged(path, line string) error {
	err := withLockGuard(filepath.Dir(path), func() error {
		current, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.TrimRight(string(current), "\n") != line {
			return nil
		}
		beforeLockRemove()
		return os.Remove(path)
	})
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
