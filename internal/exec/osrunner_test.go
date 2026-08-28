package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/exec"
)

func helperSpec(command string, args ...string) exec.Spec {
	return exec.Spec{
		Path: os.Args[0],
		Args: helperSpecArgs(command, args...),
		Env:  helperEnv(nil),
	}
}

func run(t *testing.T, spec exec.Spec) exec.Result {
	t.Helper()
	return exec.NewOSRunner().Run(t.Context(), spec)
}

// Each element of Spec.Args reaches the child as one argv entry. The Bash
// adapters pass a whole prompt as a single argument — `"$(cat "$prompt_file")"`
// at lib/adapters/claude.sh:106 — so a prompt full of newlines and quotes has
// to survive intact with no quoting rules of its own.
func TestRunPassesArgumentsAsAnArray(t *testing.T) {
	arguments := []string{
		"plain",
		"two words",
		"a\nnewline\nand\nanother",
		"",
		`quotes " and ' and a $DOLLAR and a ; semicolon`,
		"trailing space ",
		"a\ttab",
		"--looks-like-a-flag",
	}

	result := run(t, helperSpec("argv", arguments...))

	if !result.OK() {
		t.Fatalf("helper failed: exit=%d err=%v stderr=%q", result.ExitCode, result.Err, result.Stderr)
	}
	var got []string
	if err := json.Unmarshal(result.Stdout, &got); err != nil {
		t.Fatalf("decoding the child's argv: %v (stdout %q)", err, result.Stdout)
	}
	if !slices.Equal(got, arguments) {
		t.Errorf("child argv = %q, want %q", got, arguments)
	}
}

// Ruling of the environment boundary: the child gets exactly Spec.Env.
// /usr/bin/env with no arguments prints one line per variable and nothing else,
// which makes an empty environment provable rather than merely likely. It is
// POSIX and takes no flags, so it behaves the same on Darwin and on Linux, and
// the adapters already depend on it (lib/adapters/claude.sh:67).
func TestRunGivesAnEmptySpecEnvAnEmptyChildEnvironment(t *testing.T) {
	const envBinary = "/usr/bin/env"
	if _, err := os.Stat(envBinary); err != nil {
		t.Skipf("%s is not on this host: %v", envBinary, err)
	}

	// Set in the parent, so an inherited environment would be visible.
	t.Setenv("GH_TOKEN", "a-token-that-must-not-travel")
	t.Setenv("CROSSREV_PARENT_ONLY", "parent")

	for _, tt := range []struct {
		name string
		env  []string
	}{
		{name: "nil Env", env: nil},
		{name: "empty Env", env: []string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := run(t, exec.Spec{Path: envBinary, Env: tt.env})

			if !result.OK() {
				t.Fatalf("env failed: exit=%d err=%v stderr=%q", result.ExitCode, result.Err, result.Stderr)
			}
			if len(result.Stdout) != 0 {
				// Names only. The values here would be the parent's real
				// environment, and a failing run prints its output into a CI log.
				var names []string
				for _, entry := range strings.Split(strings.TrimRight(string(result.Stdout), "\n"), "\n") {
					name, _, _ := strings.Cut(entry, "=")
					names = append(names, name)
				}
				t.Fatalf("child inherited %d variables: %s", len(names), strings.Join(names, " "))
			}
		})
	}
}

func TestRunGivesTheChildExactlySpecEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "a-token-that-must-not-travel")
	t.Setenv("GITHUB_TOKEN", "another-one")
	t.Setenv("GH_ENTERPRISE_TOKEN", "a-third")
	t.Setenv("PATH", "/usr/bin:/bin")

	spec := helperSpec("lookup", "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "PATH", "ANTHROPIC_BASE_URL")
	spec.Env = helperEnv(map[string]string{"ANTHROPIC_BASE_URL": "https://endpoint.example/v1"})

	result := run(t, spec)

	if !result.OK() {
		t.Fatalf("helper failed: exit=%d err=%v stderr=%q", result.ExitCode, result.Err, result.Stderr)
	}
	want := strings.Join([]string{
		"GH_TOKEN<unset>",
		"GITHUB_TOKEN<unset>",
		"GH_ENTERPRISE_TOKEN<unset>",
		"PATH<unset>",
		"ANTHROPIC_BASE_URL=https://endpoint.example/v1",
	}, "\n") + "\n"
	if string(result.Stdout) != want {
		t.Errorf("child saw:\n%s\nwant:\n%s", result.Stdout, want)
	}
}

// A zero Spec closes stdin. Every adapter redirects `</dev/null`, and
// lib/adapters/opencode.sh:47 records why: with stdin held open the CLI blocks
// on it and produces nothing on either stream.
func TestRunClosesStdinByDefault(t *testing.T) {
	// The runner's own stdin is filled first, so a child wired to it would read
	// these bytes. Without this the test proves nothing under `go test`, whose
	// stdin is already at EOF — the same blind spot CLAUDE.md records for an
	// interactive suite run.
	withParentStdin(t, "bytes the child must not see")

	result := run(t, helperSpec("stdin"))

	if !result.OK() {
		t.Fatalf("helper failed: exit=%d err=%v stderr=%q", result.ExitCode, result.Err, result.Stderr)
	}
	if len(result.Stdout) != 0 {
		t.Errorf("child read %q from a stdin nobody filled", result.Stdout)
	}
	if string(result.Stderr) != "0" {
		t.Errorf("child counted %q bytes on stdin, want 0", result.Stderr)
	}
}

func TestRunDeliversStdinWhenTheCallerSetsIt(t *testing.T) {
	payload := "first\nsecond\n\x00binary\xff\n"
	spec := helperSpec("stdin")
	spec.Stdin = []byte(payload)

	result := run(t, spec)

	if !result.OK() {
		t.Fatalf("helper failed: exit=%d err=%v stderr=%q", result.ExitCode, result.Err, result.Stderr)
	}
	if string(result.Stdout) != payload {
		t.Errorf("child read %q, want %q", result.Stdout, payload)
	}
}

func TestRunHonoursTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving the temporary directory: %v", err)
	}

	spec := helperSpec("pwd")
	spec.Dir = dir

	result := run(t, spec)

	if !result.OK() {
		t.Fatalf("helper failed: exit=%d err=%v stderr=%q", result.ExitCode, result.Err, result.Stderr)
	}
	if got := string(result.Stdout); got != resolved && got != dir {
		t.Errorf("child ran in %q, want %q", got, resolved)
	}
}

func TestRunReportsAWorkingDirectoryThatDoesNotExist(t *testing.T) {
	spec := helperSpec("pwd")
	spec.Dir = filepath.Join(t.TempDir(), "no-such-directory")

	result := run(t, spec)

	var start *exec.StartError
	if !errors.As(result.Err, &start) {
		t.Fatalf("Err = %v (%T), want a *exec.StartError", result.Err, result.Err)
	}
	if start.Dir != spec.Dir {
		t.Errorf("StartError.Dir = %q, want %q", start.Dir, spec.Dir)
	}
	if result.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 for a child that never started", result.ExitCode)
	}
	if exec.IsNotFound(result.Err) {
		t.Error("a missing working directory was reported as a missing program")
	}
}

// The Bash guard is `command -v claude >/dev/null 2>&1 || ui_die` at
// lib/adapters/claude.sh:19, which names the install page. Telling this apart
// from any other start failure is what lets the caller print that.
func TestRunReportsAProgramThatIsNotOnPath(t *testing.T) {
	result := run(t, exec.Spec{Path: "crossrev-no-such-program-4f1c9a", Env: helperEnv(nil)})

	if !exec.IsNotFound(result.Err) {
		t.Fatalf("Err = %v, want a not-found error", result.Err)
	}
	var start *exec.StartError
	if !errors.As(result.Err, &start) {
		t.Fatalf("Err = %v (%T), want a *exec.StartError", result.Err, result.Err)
	}
	if result.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 for a child that never started", result.ExitCode)
	}
}

func TestRunReportsAnEmptyPath(t *testing.T) {
	result := run(t, exec.Spec{})

	var start *exec.StartError
	if !errors.As(result.Err, &start) {
		t.Fatalf("Err = %v (%T), want a *exec.StartError", result.Err, result.Err)
	}
	if result.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", result.ExitCode)
	}
}

// A non-zero exit is data, not an error. lib/adapters/claude.sh:107 reads rc and
// reports it; nothing about it is exceptional.
func TestRunReportsANonZeroExitWithoutAnError(t *testing.T) {
	for _, code := range []int{1, 2, 42, 127} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			result := run(t, helperSpec("exit", strconv.Itoa(code)))

			if result.Err != nil {
				t.Errorf("Err = %v, want nil for an ordinary non-zero exit", result.Err)
			}
			if result.ExitCode != code {
				t.Errorf("ExitCode = %d, want %d", result.ExitCode, code)
			}
			if result.Signaled() {
				t.Error("an exiting child was reported as signalled")
			}
			if result.OK() {
				t.Error("OK reported true for a non-zero exit")
			}
		})
	}
}

func TestRunSeparatesTheTwoStreams(t *testing.T) {
	result := run(t, helperSpec("spew", "10", "4"))

	if !result.OK() {
		t.Fatalf("helper failed: exit=%d err=%v", result.ExitCode, result.Err)
	}
	if string(result.Stdout) != strings.Repeat("o", 10) {
		t.Errorf("Stdout = %q", result.Stdout)
	}
	if string(result.Stderr) != strings.Repeat("e", 4) {
		t.Errorf("Stderr = %q", result.Stderr)
	}
}

// The zero Spec caps nothing. No adapter truncates its capture file, and a
// default cap here would cut a model payload in half.
func TestRunCapsNothingByDefault(t *testing.T) {
	const size = 1 << 20

	result := run(t, helperSpec("spew", strconv.Itoa(size), "0"))

	if !result.OK() {
		t.Fatalf("helper failed: exit=%d err=%v", result.ExitCode, result.Err)
	}
	if len(result.Stdout) != size {
		t.Fatalf("captured %d bytes, want the whole %d", len(result.Stdout), size)
	}
	if result.StdoutTruncated {
		t.Error("an uncapped run reported truncation")
	}
}

func TestRunCapsEachStreamWhenTheCallerAsks(t *testing.T) {
	spec := helperSpec("spew", "5000", "3000")
	spec.MaxOutputBytes = 100

	result := run(t, spec)

	if !result.OK() {
		t.Fatalf("helper failed: exit=%d err=%v", result.ExitCode, result.Err)
	}
	if len(result.Stdout) != 100 || len(result.Stderr) != 100 {
		t.Fatalf("captured %d/%d bytes, want 100 of each", len(result.Stdout), len(result.Stderr))
	}
	if !result.StdoutTruncated || !result.StderrTruncated {
		t.Error("a capped run did not report truncation on both streams")
	}
	if result.StdoutBytes != 5000 || result.StderrBytes != 3000 {
		t.Errorf("byte counts = %d/%d, want the 5000/3000 the child wrote", result.StdoutBytes, result.StderrBytes)
	}
}

// The capture must be complete when Run returns. A copier still writing into
// the buffer after the caller has the Result is the classic bug here, and it
// shows up as a short read on a large payload.
func TestRunReturnsOnlyAfterTheCaptureIsComplete(t *testing.T) {
	const size = 1 << 20

	for attempt := 0; attempt < 5; attempt++ {
		result := run(t, helperSpec("spew", strconv.Itoa(size), strconv.Itoa(size)))

		if !result.OK() {
			t.Fatalf("helper failed: exit=%d err=%v", result.ExitCode, result.Err)
		}
		if len(result.Stdout) != size || len(result.Stderr) != size {
			t.Fatalf("attempt %d captured %d/%d bytes, want %d of each",
				attempt, len(result.Stdout), len(result.Stderr), size)
		}
	}
}

func TestRunReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := time.Now()

	done := make(chan exec.Result, 1)
	go func() { done <- exec.NewOSRunner().Run(ctx, helperSpec("sleep", "60000")) }()

	// Cancelling before the child is up would test the start path instead.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case result := <-done:
		if !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("Err = %v, want context.Canceled", result.Err)
		}
		if !result.Signaled() {
			t.Errorf("a cancelled child was not reported as signalled: exit=%d", result.ExitCode)
		}
		if result.ExitCode <= 128 {
			t.Errorf("ExitCode = %d, want 128 plus the killing signal", result.ExitCode)
		}
		if elapsed := time.Since(started); elapsed > 30*time.Second {
			t.Errorf("Run took %s to notice the cancellation", elapsed)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

func TestRunHonoursSpecTimeout(t *testing.T) {
	spec := helperSpec("sleep", "60000")
	spec.Timeout = 250 * time.Millisecond

	started := time.Now()
	result := run(t, spec)
	elapsed := time.Since(started)

	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("Err = %v, want context.DeadlineExceeded", result.Err)
	}
	if elapsed > 30*time.Second {
		t.Errorf("Run took %s for a 250ms timeout", elapsed)
	}
}

func TestRunOfAZeroTimeoutWaitsForTheChild(t *testing.T) {
	spec := helperSpec("sleep", "300")

	result := run(t, spec)

	if !result.OK() {
		t.Fatalf("a child with no deadline was cut short: exit=%d err=%v", result.ExitCode, result.Err)
	}
	if string(result.Stdout) != "ready" {
		t.Errorf("Stdout = %q, want the child's own output", result.Stdout)
	}
}

// withParentStdin replaces this process's stdin with a file holding content,
// and restores it when the test ends. No test in this package runs in parallel,
// so the global is not shared while it is swapped.
func withParentStdin(t *testing.T, content string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the replacement stdin: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening the replacement stdin: %v", err)
	}

	original := os.Stdin
	os.Stdin = file
	t.Cleanup(func() {
		os.Stdin = original
		file.Close()
	})
}
