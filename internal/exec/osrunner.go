package exec

import (
	"bytes"
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"time"
)

// pipeDrainGrace bounds how long Run waits for the captured streams after the
// child is already gone, or after a cancellation should have ended it.
//
// It is not a timeout on the child, and Ruling A is untouched by it: a Spec
// with no Timeout still lets a child run for as long as it likes. It only
// covers the two ways Wait can hang once the child's own life is over — an
// orphan holding the pipes open, and a process that ignored the kill. The Bash
// adapters cannot hit either, because they redirect to files
// (lib/adapters/claude.sh:106) and a file needs no reader.
const pipeDrainGrace = 10 * time.Second

// OSRunner starts real processes. It is the only place in the codebase that
// calls os/exec.
type OSRunner struct{}

// NewOSRunner returns the runner that starts real processes.
func NewOSRunner() *OSRunner { return &OSRunner{} }

var _ Runner = (*OSRunner)(nil)

// Run starts spec and waits for it.
//
// It builds an argument array and never a command string. Nothing here is
// handed to a shell, so a prompt containing a semicolon, a backtick or a
// newline is one argument and not an injection — which is the whole reason the
// Bash side builds `local -a run=(...)` arrays rather than concatenating
// (lib/adapters/claude.sh:57).
func (r *OSRunner) Run(ctx context.Context, spec Spec) Result {
	started := time.Now()

	if spec.Path == "" {
		return Result{
			ExitCode: -1,
			Stdout:   []byte{},
			Stderr:   []byte{},
			Err:      &StartError{Path: spec.Path, Dir: spec.Dir, Err: errors.New("no program named")},
		}
	}

	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	cmd := osexec.CommandContext(ctx, spec.Path, spec.Args...)
	cmd.Dir = spec.Dir

	// Set unconditionally. os/exec inherits the parent's environment when
	// Cmd.Env is nil, so leaving it nil for an empty Spec.Env would hand a
	// model-facing process every credential this one holds. An empty child
	// environment is the fail-closed answer (ADR 0001, SECURITY.md).
	cmd.Env = spec.Env
	if cmd.Env == nil {
		cmd.Env = []string{}
	}

	// A reader over the caller's bytes, empty when there are none. os/exec
	// gives the child a closed descriptor when Stdin is nil, and an empty
	// reader when it is set; either way the child sees EOF at once rather than
	// a terminal, which is the `</dev/null` of every adapter.
	cmd.Stdin = bytes.NewReader(spec.Stdin)

	stdout := &capture{limit: spec.MaxOutputBytes}
	stderr := &capture{limit: spec.MaxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// The child leads its own process group so a cancellation can kill
	// everything it started. A harness that spawns helpers would otherwise
	// leave them running after the leg that cancelled it is gone.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = pipeDrainGrace

	err := cmd.Run()

	result := Result{Duration: time.Since(started)}
	result.Stdout, result.StdoutBytes, result.StdoutTruncated = stdout.state()
	result.Stderr, result.StderrBytes, result.StderrTruncated = stderr.state()

	if cmd.ProcessState == nil {
		// No child ever ran: an unresolvable program, a working directory that
		// is not there, a fork the kernel refused.
		result.ExitCode = -1
		result.Err = &StartError{Path: spec.Path, Dir: spec.Dir, Err: err}
		return result
	}

	result.Signal = signalOf(cmd.ProcessState)
	result.ExitCode = exitCodeOf(cmd.ProcessState, result.Signal)

	if errors.Is(err, osexec.ErrWaitDelay) {
		result.Err = ErrPipesAbandoned
		return result
	}

	// A cancellation is reported only when it actually stopped the child. A
	// child that finished on its own in the same instant the context expired
	// produced a real answer, and calling that a cancellation would discard it.
	if ctxErr := ctx.Err(); ctxErr != nil && !cmd.ProcessState.Success() {
		result.Err = ctxErr
	}
	return result
}

// exitCodeOf maps a finished child onto the number a shell reports in $?.
//
// A signalled child becomes 128 plus the signal, which is where the 130 of an
// interrupted run comes from. os.ProcessState.ExitCode answers -1 for a
// signalled child, which would collide with the code for a child that never
// started.
func exitCodeOf(state *os.ProcessState, signal os.Signal) int {
	if number, ok := signalNumber(signal); ok {
		return 128 + number
	}
	return state.ExitCode()
}
