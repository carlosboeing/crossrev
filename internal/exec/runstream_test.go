package exec_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/exec"
)

func runStream(t *testing.T, spec exec.Spec, consume func(io.Reader) error) exec.Result {
	t.Helper()
	return exec.NewOSRunner().RunStream(t.Context(), spec, consume)
}

// The streamed contract in one case: consume observes the child's first
// bytes while the child is still running. A buffered runner could only
// deliver them after exit, so a child that never exits on its own would
// hang it; here consume reads "ready" and stops the child with an error.
func TestRunStreamDeliversOutputBeforeTheChildExits(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	sentinel := errors.New("stop after ready")
	var got []byte
	result := exec.NewOSRunner().RunStream(ctx, helperSpec("sleep", "600000"), func(rd io.Reader) error {
		var err error
		got, err = io.ReadAll(io.LimitReader(rd, int64(len("ready"))))
		if err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(result.Err, sentinel) {
		t.Fatalf("Err = %v, want the consume error (output never arrived before exit)", result.Err)
	}
	if string(got) != "ready" {
		t.Errorf("consume read %q, want %q", got, "ready")
	}
}

func TestRunStreamCountsStdoutAndKeepsStderr(t *testing.T) {
	var stdout []byte
	result := runStream(t, helperSpec("alternate", "3"), func(rd io.Reader) error {
		var err error
		stdout, err = io.ReadAll(rd)
		return err
	})
	if !result.OK() {
		t.Fatalf("helper failed: exit=%d err=%v stderr=%q", result.ExitCode, result.Err, result.Stderr)
	}
	if want := "out 1\nout 2\nout 3\n"; string(stdout) != want {
		t.Errorf("consumed stdout = %q, want %q", stdout, want)
	}
	if len(result.Stdout) != 0 {
		t.Errorf("Result.Stdout holds %d bytes, want nothing retained", len(result.Stdout))
	}
	if result.StdoutBytes != len(stdout) {
		t.Errorf("StdoutBytes = %d, want the %d written", result.StdoutBytes, len(stdout))
	}
	if want := "err 1\nerr 2\nerr 3\n"; string(result.Stderr) != want {
		t.Errorf("Result.Stderr = %q, want %q", result.Stderr, want)
	}
}

func TestRunStreamReportsANonZeroExit(t *testing.T) {
	result := runStream(t, helperSpec("exit", "3"), func(rd io.Reader) error {
		_, err := io.Copy(io.Discard, rd)
		return err
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil (a non-zero exit is data)", result.Err)
	}
	if result.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", result.ExitCode)
	}
	if result.OK() {
		t.Error("OK is true after exit 3, want false")
	}
}

// A consume that returns early must not hang the child: the megabyte the
// child keeps writing past the pipe buffer is discarded so it can finish.
func TestRunStreamDiscardsWhatConsumeLeavesUnread(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var head [10]byte
	result := exec.NewOSRunner().RunStream(ctx, helperSpec("spew", "1000000", "0"), func(rd io.Reader) error {
		_, err := io.ReadFull(rd, head[:])
		return err
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.StdoutBytes != 1000000 {
		t.Errorf("StdoutBytes = %d, want the 1000000 written (read or discarded)", result.StdoutBytes)
	}
}

func TestRunStreamRefusesCombinedStreams(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("touch", "started")
	spec.Dir = dir
	spec.Streams = exec.StreamsCombined
	result := exec.NewOSRunner().RunStream(t.Context(), spec, func(rd io.Reader) error { return nil })
	if result.Err == nil {
		t.Fatal("Err is nil, want a refusal (merged bytes are not stdout's)")
	}
	if result.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", result.ExitCode)
	}
	if _, err := os.Stat(filepath.Join(dir, "started")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the sentinel exists, so the child started before the refusal")
	}
}

func TestRunStreamRefusesAForgeCredential(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("touch", "started")
	spec.Dir = dir
	spec.Env = append(spec.Env, "GH_TOKEN=secret")
	consumed := false
	result := exec.NewOSRunner().RunStream(t.Context(), spec, func(rd io.Reader) error {
		consumed = true
		return nil
	})
	if !errors.Is(result.Err, exec.ErrForgeCredential) {
		t.Fatalf("Err = %v, want a forge-credential refusal", result.Err)
	}
	if consumed {
		t.Error("consume ran, want no read from a refused child")
	}
	if _, err := os.Stat(filepath.Join(dir, "started")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the sentinel exists, so the child started before the refusal")
	}
}

func TestRunStreamCancellationKillsTheChild(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var got []byte
	result := exec.NewOSRunner().RunStream(ctx, helperSpec("sleep", "30000"), func(rd io.Reader) error {
		head := make([]byte, len("ready"))
		if _, err := io.ReadFull(rd, head); err != nil {
			return err
		}
		got = head
		cancel()
		_, err := io.Copy(io.Discard, rd)
		return err
	})
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", result.Err)
	}
	if string(got) != "ready" {
		t.Errorf("consume read %q, want %q", got, "ready")
	}
	if !result.Signaled() {
		t.Error("Signaled is false, want the kill the cancellation sends")
	}
}

func TestRunStreamRejectsAnEmptyPath(t *testing.T) {
	spec := helperSpec("exit", "0")
	spec.Path = ""
	result := exec.NewOSRunner().RunStream(t.Context(), spec, func(rd io.Reader) error { return nil })
	var start *exec.StartError
	if !errors.As(result.Err, &start) {
		t.Fatalf("Err = %v, want a StartError", result.Err)
	}
}
