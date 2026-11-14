package vcs

import (
	"os"
	"syscall"
)

// PIDAlive reports whether pid names a process that is still running.
//
// It is `kill -0` with one correction. Signal zero delivers nothing and
// reports whether the process could be signalled, but a zombie — a process
// that has terminated whose parent has not yet reaped it — still has a
// process entry and still answers signal zero as alive. A local run killed
// hard enough that its cleanup never ran can therefore hold its lock
// forever from a parent that never reaps it, which is the incident behind
// this probe: the holder was `STAT Z` with no children, and every later run
// was refused as though it were still writing. A zombie is dead for every
// purpose a lock cares about, so it answers not alive here.
//
// A process owned by another user answers "permission denied" to signal
// zero, which the shell reads as a non-zero status and therefore as "not
// running" — and so does this. Reading it the other way would refuse a run
// because some unrelated process inherited a recycled pid.
//
// PIDAlive is exported so internal/cycle's status probe asks the same
// question the lock asks, rather than keeping a second copy that can drift
// back to the uncorrected probe — the drift that left status able to report
// a zombie leg as still running.
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return !isZombie(pid)
}

// pidAlive is PIDAlive under the name the lock has always used. It is a
// variable so that a test can answer for a pid without needing a process to
// exist at that number.
var pidAlive = PIDAlive
