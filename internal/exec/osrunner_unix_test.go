//go:build unix

package exec_test

import (
	"context"
	"errors"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// A signalled child is reported as 128 plus the signal, which is what a shell
// puts in $?. SIGINT is 2, so an interrupted child is 130 — the same number
// lib/run.sh:178 exits with when the interrupt reaches crossrev itself.
func TestRunMapsASignalToOneHundredAndTwentyEightPlusIt(t *testing.T) {
	tests := []struct {
		name   string
		signal string
		want   int
		sig    syscall.Signal
	}{
		{name: "interrupt", signal: "INT", want: 130, sig: syscall.SIGINT},
		{name: "terminate", signal: "TERM", want: 143, sig: syscall.SIGTERM},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := run(t, helperSpec("raise", tt.signal))

			if result.ExitCode != tt.want {
				t.Errorf("ExitCode = %d, want %d", result.ExitCode, tt.want)
			}
			if result.Signal != tt.sig {
				t.Errorf("Signal = %v, want %v", result.Signal, tt.sig)
			}
			if !result.Signaled() {
				t.Error("Signaled reported false for a signalled child")
			}
			// Nothing cancelled this run, so the exit status is the whole story.
			if result.Err != nil {
				t.Errorf("Err = %v, want nil for a child that died on its own signal", result.Err)
			}
		})
	}
}

// The child runs in its own process group so a cancellation can kill everything
// it started, not only the process the runner holds a handle on.
func TestRunPutsTheChildInItsOwnProcessGroup(t *testing.T) {
	result := run(t, helperSpec("pgid"))

	if !result.OK() {
		t.Fatalf("helper failed: exit=%d err=%v stderr=%q", result.ExitCode, result.Err, result.Stderr)
	}
	childGroup, err := strconv.Atoi(string(result.Stdout))
	if err != nil {
		t.Fatalf("child printed %q, not a process group: %v", result.Stdout, err)
	}
	ownGroup, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatalf("reading this process's group: %v", err)
	}
	if childGroup == ownGroup {
		t.Errorf("child ran in this process's group %d; a group kill would take the test down with it", ownGroup)
	}
}

// Cancelling kills the group, so a grandchild the harness started dies too. A
// harness that spawns a language server or a sandbox helper would otherwise
// outlive the leg that cancelled it.
func TestCancellationKillsTheChildsGrandchild(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan exec.Result, 1)
	go func() { done <- exec.NewOSRunner().Run(ctx, helperSpec("spawn", "60000")) }()

	// Give the helper time to fork before cancelling, so the pid is on stdout.
	time.Sleep(500 * time.Millisecond)
	cancel()

	var result exec.Result
	select {
	case result = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}

	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", result.Err)
	}
	grandchild, err := strconv.Atoi(string(result.Stdout))
	if err != nil {
		t.Fatalf("helper printed %q, not a pid: %v (stderr %q)", result.Stdout, err, result.Stderr)
	}

	// Signal 0 tests for the process without touching it. Polling rather than
	// checking once: the kill and the reap are not instantaneous, and a pid is
	// reusable, so a short window keeps the answer meaningful.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(grandchild, 0); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return
			}
			t.Fatalf("probing pid %d: %v", grandchild, err)
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(grandchild, syscall.SIGKILL)
			t.Fatalf("grandchild %d outlived the cancelled child", grandchild)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
