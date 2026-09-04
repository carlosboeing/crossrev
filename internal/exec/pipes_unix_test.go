//go:build unix

package exec

import (
	"context"
	"errors"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// A child can exit cleanly and leave its output streams held open by something
// it started. The shell never meets this: lib/adapters/claude.sh:106 redirects
// to a file, and a file needs no reader, so an orphan writing into it after the
// parent has gone costs the shell nothing. A pipe does need one, so Run stops
// waiting and says the capture was cut short.
//
// This test is in the package rather than beside it so it can shrink
// pipeDrainGrace. At its production value the case would cost ten seconds a run.
func TestRunReportsPipesTheChildLeftHeldOpen(t *testing.T) {
	restore := pipeDrainGrace
	pipeDrainGrace = 300 * time.Millisecond
	t.Cleanup(func() { pipeDrainGrace = restore })

	const holdFor = 30 * time.Second

	spec := Spec{
		Path: os.Args[0],
		Args: []string{"-test.run=TestHelperProcess", "--", "orphan", strconv.Itoa(int(holdFor.Milliseconds()))},
		Env:  []string{"CROSSREV_EXEC_HELPER=1"},
	}

	started := time.Now()
	result := NewOSRunner().Run(context.Background(), spec)
	elapsed := time.Since(started)

	// Whatever the kill reached, the grandchild must not outlive the test.
	t.Cleanup(func() {
		if pid, err := strconv.Atoi(string(result.Stdout)); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	if !errors.Is(result.Err, ErrPipesAbandoned) {
		t.Fatalf("Err = %v, want ErrPipesAbandoned (stdout %q, stderr %q)", result.Err, result.Stdout, result.Stderr)
	}
	if result.OK() {
		t.Error("OK reported true for a capture that was cut short; a truncated payload would read as a success")
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want the 0 the child itself exited with", result.ExitCode)
	}

	// Bounded, and bounded well below what the grandchild holds for. Without
	// Cmd.WaitDelay this waits the full thirty seconds.
	if elapsed > 10*time.Second {
		t.Errorf("Run took %s to give up on the held pipes, want roughly %s", elapsed, pipeDrainGrace)
	}
}

// The predicate behind Result.Err, tested directly because the condition it
// decides has a microsecond-wide window in a live run.
func TestCancellationErrorAsksAboutTheSignal(t *testing.T) {
	tests := []struct {
		name   string
		ctxErr error
		signal os.Signal
		want   error
	}{
		{
			name:   "nothing happened",
			ctxErr: nil,
			signal: nil,
			want:   nil,
		},
		{
			name:   "cancelled and killed",
			ctxErr: context.Canceled,
			signal: syscall.SIGKILL,
			want:   context.Canceled,
		},
		{
			name:   "deadline passed and killed",
			ctxErr: context.DeadlineExceeded,
			signal: syscall.SIGKILL,
			want:   context.DeadlineExceeded,
		},
		{
			// The case the exit-status form got wrong: the child answered on
			// its own, non-zero, as the deadline fired. Its status is the whole
			// story and Result.Err stays nil.
			name:   "deadline passed but the child answered first",
			ctxErr: context.DeadlineExceeded,
			signal: nil,
			want:   nil,
		},
		{
			// A child that killed itself. Nothing cancelled the run, so the
			// 128+signal exit code says everything there is to say.
			name:   "signalled with no cancellation",
			ctxErr: nil,
			signal: syscall.SIGINT,
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cancellationError(tt.ctxErr, tt.signal); !errors.Is(got, tt.want) || (tt.want == nil && got != nil) {
				t.Errorf("cancellationError(%v, %v) = %v, want %v", tt.ctxErr, tt.signal, got, tt.want)
			}
		})
	}
}
