//go:build linux || darwin

package vcs

import (
	"os"
	"path/filepath"
	"syscall"
)

// guardName is the file the guard is held on, beside the locks it guards.
// It is never removed: unlinking a file another process is about to lock
// would hand the two of them different files.
const guardName = "guard"

// withLockGuard runs fn while holding an exclusive advisory lock on the lock
// directory's guard file.
//
// A run lock is read, judged and then replaced or removed, and those are
// separate system calls. Two processes doing that to one lock file at once
// can each act on a line the other has already replaced — a stop removing
// the lock a new run took a moment earlier is the case that matters. Every
// read-then-write of a lock file goes through here, so no such sequence
// overlaps another.
//
// The guard is flock(2), which the kernel drops when the descriptor closes,
// so a process that dies inside fn cannot leave the guard held.
func withLockGuard(directory string, fn func() error) error {
	guard, err := os.OpenFile(filepath.Join(directory, guardName), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer guard.Close()
	for {
		err = syscall.Flock(int(guard.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		return err
	}
	return fn()
}
