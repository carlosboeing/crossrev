package vcs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// removeLockIfUnchanged must close the gap between reading the lock and
// removing it. A takeover that starts in that gap would otherwise write a new
// lock which the removal then deletes, leaving the pull request unlocked while
// the new run believes it owns it.
//
// The hook runs inside that gap. The takeover waits for the guard until the
// removal has finished, then writes its own lock; the file must still exist
// and name the takeover when both have returned.
func TestRemoveLockIfUnchangedSerializesWithTakeover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pr-42.lock")
	line := "0 on local since 2026-01-01T00:00:00Z"
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("seed the lock: %v", err)
	}

	oldHook := beforeLockRemove
	t.Cleanup(func() { beforeLockRemove = oldHook })
	inComparison := make(chan struct{})
	allowRemoval := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(allowRemoval) }) }
	t.Cleanup(release)
	beforeLockRemove = func() {
		close(inComparison)
		<-allowRemoval
	}

	removed := make(chan error, 1)
	go func() { removed <- removeLockIfUnchanged(path, line) }()
	<-inComparison

	// This is the takeover AcquireRunLock makes, under the same guard.
	tookOver := make(chan error, 1)
	go func() {
		tookOver <- withLockGuard(dir, func() error {
			lock, err := takeRunLock(path, 42)
			if err != nil {
				return err
			}
			if !lock.Held() {
				return fmt.Errorf("the takeover did not take the lock")
			}
			return nil
		})
	}()

	// While the comparison-to-removal gap is open the takeover cannot hold
	// the guard. If it can, the removal below is free to delete its lock.
	select {
	case err := <-tookOver:
		t.Fatalf("a takeover completed inside the comparison-to-removal gap: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	release()
	if err := <-removed; err != nil {
		t.Fatalf("removeLockIfUnchanged: %v", err)
	}
	if err := <-tookOver; err != nil {
		t.Fatalf("takeover after the removal: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the takeover's lock was deleted: %v", err)
	}
	if got := ParseHolder(strings.TrimRight(string(raw), "\n")).PID; got != os.Getpid() {
		t.Errorf("the lock names pid %d, want the takeover's %d", got, os.Getpid())
	}
}
