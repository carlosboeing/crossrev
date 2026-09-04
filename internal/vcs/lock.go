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

// AutomatedMode is the mode word that takes no lock. lib/run.sh:191 tests for
// this exact string and every other value falls through to the local path.
const AutomatedMode = "automated"

// unknownHost is what the holder line carries when the machine will not name
// itself. The shell writes it as `$(hostname 2>/dev/null || echo local)`
// (lib/run.sh:214), so a failed lookup becomes a literal rather than an empty
// field that would make the line unparseable.
const unknownHost = "local"

// Holder is the line a run lock carries: `<pid> on <hostname> since <iso8601>`.
type Holder struct {
	// PID is the process that took the lock, or zero when the first field is
	// not a number.
	PID int
	// Host is the machine it took it on.
	Host string
	// Since is the instant it was taken, in UTC.
	Since string
	// Raw is the line as it was read, which is what the messages quote.
	Raw string
}

// String renders the line run_lock_acquire writes (lib/run.sh:213-215).
func (h Holder) String() string {
	return strconv.Itoa(h.PID) + " on " + h.Host + " since " + h.Since
}

// ParseHolder reads a holder line the way the shell reads it: the pid is
// everything before the first space, and the host is everything between the
// first ` on ` and the first ` since ` (lib/run.sh:197-198 and :3321-3323).
//
// A line that does not have the shape yields what it can rather than an error.
// The shell has no way to report one, and every use of the result is a
// comparison that a partial value simply fails.
func ParseHolder(line string) Holder {
	holder := Holder{Raw: line}
	first, _, _ := strings.Cut(line, " ")
	if pid, err := strconv.Atoi(first); err == nil {
		holder.PID = pid
	}
	if _, rest, found := strings.Cut(line, " on "); found {
		host, since, split := strings.Cut(rest, " since ")
		holder.Host = host
		if split {
			holder.Since = since
		}
	}
	return holder
}

// RunLock is what an acquisition produced.
type RunLock struct {
	// Path is the lock file, empty when nothing was locked.
	Path string
	// Warning is set when a dead holder's lock was taken over.
	Warning *Warning

	held bool
}

// Held reports that this call is the one that wrote the lock, and so the one
// that releases it.
func (l RunLock) Held() bool { return l.held }

// Release removes the lock, and does nothing at all for a lock this call did
// not take.
//
// The no-op matters. `crossrev run` drives review and resolve one after the
// other in one process; the second leg re-enters the acquisition holding what
// the first took, and a release there would drop the lock mid-loop and open a
// window for a second terminal to start a pass halfway through this one
// (lib/run.sh:199-205).
func (l RunLock) Release() error {
	if !l.held || l.Path == "" {
		return nil
	}
	if err := os.Remove(l.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// AcquireRunLock takes the local run lock for one pull request. It is
// run_lock_acquire at lib/run.sh:189-217.
//
// # Why the shared git directory
//
// Automated mode uses one concurrency group per pull request. Locally, two runs
// against the same pull request would interleave writes, so a lock is taken and
// its holder named rather than failing opaquely. The lock keys on the clone's
// shared git directory rather than the working tree's private one: every
// working tree of a clone must find the same lock, whichever tree took it.
//
// # Three outcomes
//
//   - No lock at all: automated mode, or a directory that is not a repository.
//     Both return a zero RunLock and no error, which is the shell's `return 0`.
//   - A lock held by this same process: kept, not re-taken. See Release.
//   - A lock held by a dead process: taken over, with a warning that says so.
//
// A lock held by a live process is a refusal. `kill -0` is what the shell asks,
// and a process this one may not signal answers the same way a process that is
// gone does — the probe cannot tell them apart, and neither can this.
func (r *Repository) AcquireRunLock(ctx context.Context, pr int, mode string) (RunLock, error) {
	if mode == AutomatedMode {
		return RunLock{}, nil
	}

	common, err := r.CommonDir(ctx)
	if err != nil || common == "" {
		// `git rev-parse --git-common-dir || return 0`. Nothing to key a lock
		// on is not a failure to lock; it is a run with no lock.
		return RunLock{}, nil
	}

	directory := filepath.Join(common, "crossrev")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return RunLock{}, err
	}
	path := filepath.Join(directory, "pr-"+strconv.Itoa(pr)+".lock")

	var warning *Warning
	if raw, readErr := os.ReadFile(path); readErr == nil {
		holder := ParseHolder(strings.TrimRight(string(raw), "\n"))
		if holder.PID == os.Getpid() {
			return RunLock{Path: path}, nil
		}
		if holder.PID != 0 && pidAlive(holder.PID) {
			return RunLock{}, &Refusal{
				Message: fmt.Sprintf("another CrossRev run already holds pull request %d — %s", pr, holder.Raw),
				Hint:    "Two runs writing the same pull request would interleave comments and replies. Wait for it to finish, or stop that process.",
			}
		}
		warning = &Warning{
			Message: fmt.Sprintf("a previous run left a lock on pull request %d held by %s, which is no longer running", pr, holder.Raw),
			Hint:    "Taking it over. If that run was killed mid-write, its marker records how far it got and this run posts only the difference.",
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return RunLock{}, err
		}
	}

	line := Holder{PID: os.Getpid(), Host: hostname(), Since: time.Now().UTC().Format("2006-01-02T15:04:05Z")}
	if err := os.WriteFile(path, []byte(line.String()+"\n"), 0o644); err != nil {
		return RunLock{}, err
	}
	return RunLock{Path: path, Warning: warning, held: true}, nil
}

// hostname is `hostname 2>/dev/null || echo local`.
func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return unknownHost
	}
	return name
}

// pidAlive is `kill -0 "$pid" 2>/dev/null`.
//
// Signal zero delivers nothing and reports whether the process could be
// signalled. A process owned by another user answers "permission denied", which
// the shell reads as a non-zero status and therefore as "not running" — and so
// does this. Reading it the other way would refuse a run because some unrelated
// process inherited a recycled pid.
//
// A variable so that a test can answer for a pid without needing a process to
// exist at that number.
var pidAlive = func(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
