//go:build unix

package exec

import (
	"context"
	"errors"
	"io"
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

// A streamed run meets the same orphan a buffered one does: the child exits
// at once and a grandchild outside the process group holds the streams. The
// unread stdout must drain beside the wait rather than ahead of it —
// Cmd.WaitDelay starts only once Wait observes the child exit, so draining
// to EOF first hangs on the grandchild past the grace and ErrPipesAbandoned
// is unreachable.
//
// This test is in the package rather than beside it so it can shrink
// pipeDrainGrace. At its production value the case would cost ten seconds a run.
func TestRunStreamReportsPipesTheChildLeftHeldOpen(t *testing.T) {
	restore := pipeDrainGrace
	pipeDrainGrace = 300 * time.Millisecond
	t.Cleanup(func() { pipeDrainGrace = restore })

	const holdFor = 30 * time.Second

	spec := Spec{
		Path: os.Args[0],
		Args: []string{"-test.run=TestHelperProcess", "--", "orphan", strconv.Itoa(int(holdFor.Milliseconds()))},
		Env:  []string{"CROSSREV_EXEC_HELPER=1"},
	}

	// One read only: the orphan's pid arrives at once, and reading to EOF
	// here would wait on the grandchild the test is about.
	var pidText []byte
	started := time.Now()
	result := NewOSRunner().RunStream(context.Background(), spec, func(rd io.Reader) error {
		buf := make([]byte, 32)
		n, _ := rd.Read(buf)
		pidText = append(pidText, buf[:n]...)
		return nil
	})
	elapsed := time.Since(started)

	// Whatever the kill reached, the grandchild must not outlive the test.
	t.Cleanup(func() {
		if pid, err := strconv.Atoi(string(pidText)); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	if !errors.Is(result.Err, ErrPipesAbandoned) {
		t.Fatalf("Err = %v, want ErrPipesAbandoned (stdout bytes %d, stderr %q)", result.Err, result.StdoutBytes, result.Stderr)
	}
	if result.OK() {
		t.Error("OK reported true for a capture that was cut short; a truncated payload would read as a success")
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want the 0 the child itself exited with", result.ExitCode)
	}

	// Bounded, and bounded well below what the grandchild holds for. Without
	// the drain beside the wait this holds the full thirty seconds.
	if elapsed > 10*time.Second {
		t.Errorf("RunStream took %s to give up on the held pipes, want roughly %s", elapsed, pipeDrainGrace)
	}
}

// A grandchild holding only stdout is invisible to Cmd.WaitDelay: os/exec
// connects an *os.File stdout directly instead of copying it, so no copier
// exists for the grace to bound and Wait returns nil at once. Only the drain
// beside Wait can report this orphan, by still being stuck when the grace
// elapses. The both-streams orphan above cannot cover it: there the
// grandchild also holds stderr, and the stderr copy is what trips
// ErrWaitDelay.
//
// This test is in the package rather than beside it so it can shrink
// pipeDrainGrace. At its production value the case would cost ten seconds a run.
func TestRunStreamReportsAStdoutOnlyOrphan(t *testing.T) {
	restore := pipeDrainGrace
	pipeDrainGrace = 300 * time.Millisecond
	t.Cleanup(func() { pipeDrainGrace = restore })

	const holdFor = 30 * time.Second

	spec := Spec{
		Path: os.Args[0],
		Args: []string{"-test.run=TestHelperProcess", "--", "orphan-stdout", strconv.Itoa(int(holdFor.Milliseconds()))},
		Env:  []string{"CROSSREV_EXEC_HELPER=1"},
	}

	// One read only: the orphan's pid arrives at once, and reading to EOF
	// here would wait on the grandchild the test is about.
	var pidText []byte
	started := time.Now()
	result := NewOSRunner().RunStream(context.Background(), spec, func(rd io.Reader) error {
		buf := make([]byte, 32)
		n, _ := rd.Read(buf)
		pidText = append(pidText, buf[:n]...)
		return nil
	})
	elapsed := time.Since(started)

	// Whatever the kill reached, the grandchild must not outlive the test.
	t.Cleanup(func() {
		if pid, err := strconv.Atoi(string(pidText)); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	if !errors.Is(result.Err, ErrPipesAbandoned) {
		t.Fatalf("Err = %v, want ErrPipesAbandoned (stdout bytes %d, stderr %q)", result.Err, result.StdoutBytes, result.Stderr)
	}
	if result.OK() {
		t.Error("OK reported true for a capture that was cut short; a truncated payload would read as a success")
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want the 0 the child itself exited with", result.ExitCode)
	}

	// Bounded, and bounded well below what the grandchild holds for. Without
	// the grace bounding the stuck drain this holds the full thirty seconds.
	if elapsed > 10*time.Second {
		t.Errorf("RunStream took %s to give up on the held stdout, want roughly %s", elapsed, pipeDrainGrace)
	}
}

// The drain-and-wait error matrix: every way a streamed child can finish —
// exit zero, exit non-zero, context cancel, deadline — crossed with every
// way a grandchild can hold the pipes. The rule under test is that the
// first real error wins (Wait's, then the context's, then consume's) and
// the abandoned-pipe error is added only when there is no other: a
// signalled wait still reaches cancellationError, and a non-zero exit
// still stands on its status the way it does for Run.
//
// Five cells already have their own tests and are not repeated here:
// exit 0 with no orphan (TestRunStreamCountsStdoutAndKeepsStderr), exit 0
// with stdout held (TestRunStreamReportsAStdoutOnlyOrphan) and both held
// (TestRunStreamReportsPipesTheChildLeftHeldOpen), exit 3 with no orphan
// (TestRunStreamReportsANonZeroExit), and cancel with no orphan
// (TestRunStreamCancellationKillsTheChild). The eleven rows below fill the
// rest, plus a consume error beside a held stdout, which the same fix
// keeps from being overwritten.
//
// Every row also asserts the byte count, which is the no-output-lost
// half of the sweep: the grandchild writes nothing and the child writes
// once, so StdoutBytes must equal exactly what the first read saw. A
// close under a live drain would show up here as a short count.
//
// This test is in the package rather than beside it so it can shrink
// pipeDrainGrace. At its production value the orphan rows would cost ten
// seconds each.
func TestRunStreamDrainAndWaitMatrix(t *testing.T) {
	restore := pipeDrainGrace
	pipeDrainGrace = 300 * time.Millisecond
	t.Cleanup(func() { pipeDrainGrace = restore })

	const holdFor = 30 * time.Second
	holdMs := strconv.Itoa(int(holdFor.Milliseconds()))
	const childSleepMs = "30000"

	sentinel := errors.New("stop after the first read")
	tests := []struct {
		name string
		// Helper command after the "--".
		args []string
		// Spec.Timeout; zero for none.
		timeout time.Duration
		// Cancel the context once the first read lands.
		cancelAfterRead bool
		// What consume returns after the first read; nil reads on.
		consumeErr error
		// wantFirst pins the first read when it is fixed ("ready");
		// empty wants the grandchild pid, which also feeds the cleanup kill.
		wantFirst    string
		wantErr      error
		wantExit     int
		wantSignaled bool
	}{
		{
			name:    "exit 0/stderr held",
			args:    []string{"orphan-stderr", holdMs},
			wantErr: ErrPipesAbandoned, wantExit: 0,
		},
		{
			name:     "exit 3/stdout held",
			args:     []string{"orphan-exit", holdMs, "stdout", "3"},
			wantExit: 3,
		},
		{
			name:     "exit 3/stderr held",
			args:     []string{"orphan-exit", holdMs, "stderr", "3"},
			wantExit: 3,
		},
		{
			name:     "exit 3/both held",
			args:     []string{"orphan-exit", holdMs, "both", "3"},
			wantExit: 3,
		},
		{
			name:            "cancel/stdout held",
			args:            []string{"orphan-sleep", holdMs, "stdout", childSleepMs},
			cancelAfterRead: true,
			wantErr:         context.Canceled, wantExit: 137, wantSignaled: true,
		},
		{
			name:            "cancel/stderr held",
			args:            []string{"orphan-sleep", holdMs, "stderr", childSleepMs},
			cancelAfterRead: true,
			wantErr:         context.Canceled, wantExit: 137, wantSignaled: true,
		},
		{
			name:            "cancel/both held",
			args:            []string{"orphan-sleep", holdMs, "both", childSleepMs},
			cancelAfterRead: true,
			wantErr:         context.Canceled, wantExit: 137, wantSignaled: true,
		},
		{
			name:      "timeout/no orphan",
			args:      []string{"sleep", childSleepMs},
			timeout:   500 * time.Millisecond,
			wantFirst: "ready",
			wantErr:   context.DeadlineExceeded, wantExit: 137, wantSignaled: true,
		},
		{
			name:    "timeout/stdout held",
			args:    []string{"orphan-sleep", holdMs, "stdout", childSleepMs},
			timeout: 500 * time.Millisecond,
			wantErr: context.DeadlineExceeded, wantExit: 137, wantSignaled: true,
		},
		{
			name:    "timeout/stderr held",
			args:    []string{"orphan-sleep", holdMs, "stderr", childSleepMs},
			timeout: 500 * time.Millisecond,
			wantErr: context.DeadlineExceeded, wantExit: 137, wantSignaled: true,
		},
		{
			name:    "timeout/both held",
			args:    []string{"orphan-sleep", holdMs, "both", childSleepMs},
			timeout: 500 * time.Millisecond,
			wantErr: context.DeadlineExceeded, wantExit: 137, wantSignaled: true,
		},
		{
			name:       "consume error/stdout held",
			args:       []string{"orphan-sleep", holdMs, "stdout", childSleepMs},
			consumeErr: sentinel,
			wantErr:    sentinel, wantExit: 137, wantSignaled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			spec := Spec{
				Path:    os.Args[0],
				Args:    append([]string{"-test.run=TestHelperProcess", "--"}, tt.args...),
				Env:     []string{"CROSSREV_EXEC_HELPER=1"},
				Timeout: tt.timeout,
			}

			// One read only: the marker arrives at once, and reading to
			// EOF here would wait on the grandchild the row is about.
			var first []byte
			started := time.Now()
			result := NewOSRunner().RunStream(ctx, spec, func(rd io.Reader) error {
				buf := make([]byte, 64)
				n, _ := rd.Read(buf)
				first = append(first, buf[:n]...)
				if tt.cancelAfterRead {
					cancel()
				}
				return tt.consumeErr
			})
			elapsed := time.Since(started)

			// Whatever the kill reached, the grandchild must not outlive
			// the test. A fixed marker parses to nothing and skips this.
			t.Cleanup(func() {
				if pid, err := strconv.Atoi(string(first)); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})

			if tt.wantFirst != "" {
				if string(first) != tt.wantFirst {
					t.Errorf("first read = %q, want %q", first, tt.wantFirst)
				}
			} else if _, err := strconv.Atoi(string(first)); err != nil {
				t.Errorf("first read = %q, want the grandchild pid", first)
			}
			if !errors.Is(result.Err, tt.wantErr) || (tt.wantErr == nil && result.Err != nil) {
				t.Errorf("Err = %v, want %v (stdout bytes %d, stderr %q)", result.Err, tt.wantErr, result.StdoutBytes, result.Stderr)
			}
			if result.ExitCode != tt.wantExit {
				t.Errorf("ExitCode = %d, want %d", result.ExitCode, tt.wantExit)
			}
			if result.Signaled() != tt.wantSignaled {
				t.Errorf("Signaled = %v, want %v", result.Signaled(), tt.wantSignaled)
			}
			// Every row is a failure by construction: an error, a
			// non-zero exit, or both. A cut-short capture must never
			// read as a success.
			if result.OK() {
				t.Error("OK reported true; a truncated or failed run would read as a success")
			}
			// The grandchild writes nothing and the child writes once,
			// so the count must match the first read exactly. Less is
			// output lost to a close under the drain; more is a byte
			// counted twice.
			if result.StdoutBytes != len(first) {
				t.Errorf("StdoutBytes = %d, want the %d the first read saw", result.StdoutBytes, len(first))
			}

			// Bounded, and bounded well below what the grandchild holds
			// for. Without the grace this holds the full thirty seconds.
			if elapsed > 10*time.Second {
				t.Errorf("RunStream took %s, want roughly the grace", elapsed)
			}
		})
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
