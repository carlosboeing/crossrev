package exec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	osexec "os/exec"
	"time"
)

var _ Streamer = (*OSRunner)(nil)

// RunStream starts spec, hands its stdout to consume while the child runs,
// and returns once the child is reaped.
//
// It builds the child the way Run does — an argument array and never a
// command string, an exact environment, the caller's stdin bytes, and its
// own process group — and refuses the same Specs before anything starts: no
// program named, a merged stream, or a forge credential on a model-facing
// runner. Stderr is still captured; stdout is nobody's to keep, only to
// count.
//
// consume runs in the calling goroutine while the child writes. Once it
// returns, whatever it left unread is discarded beside the wait so a child
// blocked on a full pipe can still finish, and an orphan holding the pipe
// past the drain grace ends the wait the way it does for Run. A consume
// error kills the group first, so the child cannot outlive the caller that
// gave up on it.
func (r *OSRunner) RunStream(ctx context.Context, spec Spec, consume func(io.Reader) error) Result {
	started := time.Now()

	if spec.Path == "" {
		return Result{
			ExitCode: -1,
			Stdout:   []byte{},
			Stderr:   []byte{},
			Err:      &StartError{Path: spec.Path, Dir: spec.Dir, Err: errors.New("no program named")},
		}
	}

	if spec.Streams == StreamsCombined {
		return Result{
			ExitCode: -1,
			Stdout:   []byte{},
			Stderr:   []byte{},
			Err:      errors.New("exec: RunStream keeps the child's streams apart and cannot merge them"),
		}
	}

	// Before anything is started, and before any environment is built. See
	// Run for why the runner's own field is read rather than a field on Spec.
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
	cmd.Env = spec.Env
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	cmd.Stdin = bytes.NewReader(spec.Stdin)

	// The runner owns this pipe rather than asking os/exec for one
	// (Cmd.StdoutPipe): a pipe os/exec creates is closed by Wait when the
	// child exits, so unread buffered bytes raced that closure and could
	// return os.ErrClosed with trailing output lost. An owned pipe is
	// closed only below, after Wait, so the drain beside it cannot lose
	// bytes to the reap — and closing the read end there is what releases
	// the drain when an orphan still holds the write end.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return Result{
			ExitCode: -1,
			Stdout:   []byte{},
			Stderr:   []byte{},
			Err:      &StartError{Path: spec.Path, Dir: spec.Dir, Err: err},
		}
	}
	stderr := &capture{limit: spec.MaxOutputBytes}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderr

	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = pipeDrainGrace

	if err := cmd.Start(); err != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return Result{
			ExitCode: -1,
			Stdout:   []byte{},
			Stderr:   []byte{},
			Err:      &StartError{Path: spec.Path, Dir: spec.Dir, Err: err},
		}
	}
	// The child has its own descriptor now. The parent's write end must
	// close or the drain below never sees EOF.
	_ = stdoutW.Close()

	counted := &countReader{r: stdoutR}
	consumeErr := consume(counted)
	if consumeErr != nil {
		// The caller stopped reading: the child may be blocked writing
		// to a pipe nobody drains, so end the group before waiting.
		_ = killProcessGroup(cmd)
	}
	// Drain whatever the caller left unread beside the wait, not ahead of
	// it. Cmd.WaitDelay starts only once Wait observes the child exit, so
	// draining to EOF first would hang on an orphan past the grace and
	// leave ErrPipesAbandoned unreachable. Closing the read end after
	// Wait releases the drain even then; the drain is joined, never left
	// behind.
	draining := make(chan struct{})
	go func() {
		defer close(draining)
		_, _ = io.Copy(io.Discard, counted)
	}()
	waitErr := cmd.Wait()
	_ = stdoutR.Close()
	<-draining

	result := Result{Duration: time.Since(started)}
	result.Stdout = []byte{}
	result.StdoutBytes = counted.n
	result.Stderr, result.StderrBytes, result.StderrTruncated = stderr.state()

	result.Signal = signalOf(cmd.ProcessState)
	result.ExitCode = exitCodeOf(cmd.ProcessState, result.Signal)

	if errors.Is(waitErr, osexec.ErrWaitDelay) {
		result.Err = ErrPipesAbandoned
		return result
	}

	// A cancellation kills the group, so a consume error beside one is its
	// consequence rather than its cause: the context's error wins.
	if err := cancellationError(ctx.Err(), result.Signal); err != nil {
		result.Err = err
		return result
	}
	result.Err = consumeErr
	return result
}

// countReader counts the bytes read through it, so a streamed run still
// reports what the child wrote.
type countReader struct {
	r io.Reader
	n int
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}
