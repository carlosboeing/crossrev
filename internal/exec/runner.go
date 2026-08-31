package exec

import (
	"context"
	"os"
	"time"
)

// Runner starts a child process and reports what it did.
//
// Every route from Go to a child process goes through this interface, so a test
// can substitute a recorder and a caller cannot reach os/exec by accident.
type Runner interface {
	// Run starts spec, waits for it, and returns once nothing is still writing
	// into the captured streams.
	//
	// It returns no error alongside the Result on purpose. A non-zero exit is
	// ordinary: lib/adapters/claude.sh:109 reads rc and turns it into a reported
	// harness failure rather than a crash, so the exit status is data. Result.Err
	// is reserved for the cases where the child produced no status at all — it
	// never started, or the context ended it.
	Run(ctx context.Context, spec Spec) Result
}

// Result is everything one invocation produced.
type Result struct {
	// ExitCode is the number a shell would report in $?.
	//
	// A child that exited carries its own status. A child killed by signal N
	// carries 128+N, which is where the 130 of an interrupted run comes from:
	// SIGINT is 2, and lib/run.sh:178 exits 130 for the same reason. A child
	// that never started carries -1.
	ExitCode int

	// Signal is the signal that ended the child, or nil if it exited on its own.
	Signal os.Signal

	// Stdout and Stderr are the captured streams, truncated to
	// Spec.MaxOutputBytes when the caller set one.
	Stdout []byte
	Stderr []byte

	// StdoutBytes and StderrBytes are what the child wrote, which is larger than
	// the captured slice when a cap discarded the tail.
	StdoutBytes int
	StderrBytes int

	// StdoutTruncated and StderrTruncated report that a cap discarded something.
	StdoutTruncated bool
	StderrTruncated bool

	// Duration is wall time from start to the last byte captured.
	Duration time.Duration

	// Err is set when the child produced no exit status of its own, or when the
	// status it produced does not describe the whole outcome:
	//
	//   - *CredentialError, when a model-facing runner was asked to start a
	//     child carrying a forge credential, and no child was started.
	//   - *StartError, when the child could not be started.
	//   - The context's error, when a cancellation or a deadline killed it.
	//   - ErrPipesAbandoned, when the child exited but its output was cut short
	//     because something it started still held the streams open.
	//
	// A non-zero ExitCode alone leaves this nil.
	Err error
}

// OK reports that the child started, finished on its own and exited zero.
func (r Result) OK() bool { return r.Err == nil && r.ExitCode == 0 }

// Signaled reports that a signal ended the child rather than a return from main.
func (r Result) Signaled() bool { return r.Signal != nil }
