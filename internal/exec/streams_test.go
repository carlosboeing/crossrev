package exec_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// alternating is what the helper writes: one line to each stream in turn.
func alternating(pairs int) string {
	var b strings.Builder
	for i := 1; i <= pairs; i++ {
		fmt.Fprintf(&b, "out %d\nerr %d\n", i, i)
	}
	return b.String()
}

// The zero Spec keeps two separate captures, and every existing caller depends
// on that. Asked for nothing, a caller must get what it got before this field
// existed.
func TestStreamsSeparateIsTheZeroValue(t *testing.T) {
	result := run(t, helperSpec("alternate", "3"))
	if result.Err != nil {
		t.Fatalf("Run: %v", result.Err)
	}
	if got, want := string(result.Stdout), "out 1\nout 2\nout 3\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got, want := string(result.Stderr), "err 1\nerr 2\nerr 3\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

// StreamsCombined puts both descriptors on one pipe, so the capture is the
// child's own write order rather than an order reconstructed afterwards.
//
// The alternation is the evidence. Two pipes are drained by two goroutines and
// their relative order is whatever the scheduler decides, so exact alternation
// over many writes is a thing only one pipe can produce. The shell gets one
// stream the same way, at the descriptor: `2>&1` at lib/github.sh:481 for a
// commit, lib/github.sh:510 for a push and lib/run.sh:1891 for a worktree.
// The adapters are the other case and are left alone — lib/adapters/claude.sh:106
// sends the two streams to two files, and Spec's zero value is what matches it.
func TestStreamsCombinedInterleavesOnOnePipe(t *testing.T) {
	const pairs = 200

	spec := helperSpec("alternate", fmt.Sprint(pairs))
	spec.Streams = exec.StreamsCombined
	result := run(t, spec)
	if result.Err != nil {
		t.Fatalf("Run: %v", result.Err)
	}
	if got, want := string(result.Stdout), alternating(pairs); got != want {
		t.Errorf("the merged stream is not the child's write order\n want: %q\n  got: %q",
			truncate(want), truncate(got))
	}
	// Nothing is left on the other side, exactly as `2>&1` leaves stderr with
	// nothing to carry.
	if len(result.Stderr) != 0 {
		t.Errorf("stderr = %q, want it empty when the streams are combined", string(result.Stderr))
	}
	if result.StderrBytes != 0 {
		t.Errorf("StderrBytes = %d, want 0", result.StderrBytes)
	}
	if want := len(alternating(pairs)); result.StdoutBytes != want {
		t.Errorf("StdoutBytes = %d, want %d", result.StdoutBytes, want)
	}
}

// The cap applies once, to the merged stream, rather than once per descriptor.
func TestStreamsCombinedCapsTheMergedStream(t *testing.T) {
	spec := helperSpec("spew", "100", "100")
	spec.Streams = exec.StreamsCombined
	spec.MaxOutputBytes = 150
	result := run(t, spec)
	if result.Err != nil {
		t.Fatalf("Run: %v", result.Err)
	}
	if got := len(result.Stdout); got != 150 {
		t.Errorf("captured %d bytes, want 150", got)
	}
	if result.StdoutBytes != 200 {
		t.Errorf("StdoutBytes = %d, want 200", result.StdoutBytes)
	}
	if !result.StdoutTruncated {
		t.Error("the merged stream was capped and did not report it")
	}
	if result.StderrTruncated {
		t.Error("StderrTruncated is set for a stream that carries nothing")
	}
}

// The audience rule is unchanged by the new field: a model-facing child asking
// for a merged stream is still refused before anything starts.
func TestStreamsCombinedStillRefusesAForgeCredential(t *testing.T) {
	spec := helperSpec("alternate", "1")
	spec.Env = append(spec.Env, "GH_TOKEN=not-a-real-token")
	spec.Streams = exec.StreamsCombined
	result := run(t, spec)

	var refused *exec.CredentialError
	if !errors.As(result.Err, &refused) {
		t.Fatalf("Err = %v (%T), want a *exec.CredentialError", result.Err, result.Err)
	}
	if refused.Name != "GH_TOKEN" {
		t.Errorf("CredentialError.Name = %q, want GH_TOKEN", refused.Name)
	}
	if len(result.Stdout) != 0 {
		t.Errorf("a refused Spec produced output, so a child ran: %q", result.Stdout)
	}
}

func truncate(s string) string {
	if len(s) <= 120 {
		return s
	}
	return s[:60] + "…" + s[len(s)-60:]
}
