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
// (lib/adapters/claude.sh:111) and a file needs no reader.
// A var rather than a const only so the abandoned-pipe test can shrink it;
// nothing outside this package can reach it, and nothing changes it at run time.
var pipeDrainGrace = 10 * time.Second

// OSRunner starts real processes. Production process start is confined to
// this package by the process-start AST walk in internal/archtest, which
// does not cover syscall.Syscall.
//
// orchestrator is the credential decision. Unexported, so a Spec cannot carry
// it between packages and a fake Runner cannot copy it off one Spec onto
// another. unsafe and reflection can still write the bool; a name scan
// catches accidents, and code review catches a hostile commit. Run's
// refusal is the guard that executes.
type OSRunner struct {
	orchestrator bool
}

// NewOSRunner returns a model-facing runner. It refuses a forge credential
// in Spec.Env or Spec.Args.
func NewOSRunner() *OSRunner { return &OSRunner{} }

// NewOrchestratorRunner returns a runner that may start a child holding a
// forge credential. Production construction of git and gh uses this.
func NewOrchestratorRunner() *OSRunner { return &OSRunner{orchestrator: true} }

var _ Runner = (*OSRunner)(nil)

// Run starts spec and waits for it.
//
// It builds an argument array and never a command string. Nothing here is
// handed to a shell, so a prompt containing a semicolon, a backtick or a
// newline is one argument and not an injection. lib/adapters/codex.sh:79-82
// says why that matters: the harness is "the process that reads
// attacker-controlled text".
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

	// Before anything is started, and before any environment is built. A
	// refusal after Start would already have handed the token over.
	//
	// The runner's own field is read rather than a field on Spec. A Spec
	// that travelled through a fake Runner cannot carry the opt-out with it,
	// and nothing package-level can switch this check off for every instance.
	if !r.orchestrator {
		if name, found := forgeCredentialIn(spec.Env); found {
			return Result{
				ExitCode: -1,
				Stdout:   []byte{},
				Stderr:   []byte{},
				Err:      &CredentialError{Name: name, Path: spec.Path},
			}
		}
		if name, found := forgeCredentialIn(spec.Args); found {
			return Result{
				ExitCode: -1,
				Stdout:   []byte{},
				Stderr:   []byte{},
				Err:      &CredentialError{Name: name, Path: spec.Path},
			}
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

	// A reader over the caller's bytes, empty when there are none.
	//
	// Set on every path rather than left nil. os/exec opens os.DevNull for a
	// nil Stdin, which would answer the same way, but saying it here is what
	// keeps the rule readable at the one place it matters. One difference from
	// Bash worth naming: the adapters give the child the literal /dev/null
	// (lib/adapters/claude.sh:111) and this gives it a pipe closed at once. A
	// child cannot tell the two apart — both read EOF on the first call — but
	// they are not the same file.
	cmd.Stdin = bytes.NewReader(spec.Stdin)

	stdout := &capture{limit: spec.MaxOutputBytes}
	stderr := &capture{limit: spec.MaxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if spec.Streams == StreamsCombined {
		// One capture on both, which os/exec turns into one pipe: its stderr
		// descriptor is the stdout one whenever the two writers are the same
		// interface value. So the interleaving happens in the kernel, in the
		// order the child wrote, and not in a merge here that would have to
		// invent one.
		//
		// The capture's mutex is what makes this safe whatever os/exec decides:
		// one pipe means one copier goroutine today, and a second one writing
		// into the same buffer would still be serialised.
		stderr = stdout
		cmd.Stderr = stdout
	}

	// The child leads its own process group so a cancellation can kill
	// everything it started. A harness that spawns helpers would otherwise
	// leave them running after the leg that cancelled it is gone.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = pipeDrainGrace

	err := cmd.Run()

	result := Result{Duration: time.Since(started)}
	result.Stdout, result.StdoutBytes, result.StdoutTruncated = stdout.state()
	if spec.Streams == StreamsCombined {
		// Reading the same capture twice would report the merged byte count on
		// both sides and say the stderr that carries nothing was truncated.
		result.Stderr = []byte{}
	} else {
		result.Stderr, result.StderrBytes, result.StderrTruncated = stderr.state()
	}

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

	result.Err = cancellationError(ctx.Err(), result.Signal)
	return result
}

// cancellationError decides whether a finished child was stopped by the context
// or answered on its own.
//
// The test is the signal, not the exit status. A cancellation always kills the
// group with SIGKILL, which cannot be caught, so a cancelled child is always
// signalled; a child that returned from main never is. Asking instead whether
// the child failed would label a child that exited non-zero in the same instant
// the deadline fired a timeout, and Result.Err is documented as covering only
// the cases where the child produced no status of its own.
func cancellationError(ctxErr error, signal os.Signal) error {
	if ctxErr != nil && signal != nil {
		return ctxErr
	}
	return nil
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
