package runlog_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/runlog"
)

func fixedClock(at string) func() time.Time {
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return when }
}

func openLog(t *testing.T, opts runlog.Options) *runlog.Log {
	t.Helper()
	if opts.Dir == "" {
		opts.Dir = filepath.Join(t.TempDir(), "acme-widget", "pr-7", "local-1")
	}
	if opts.Now == nil {
		opts.Now = fixedClock("2026-08-29T01:02:03Z")
	}
	l, err := runlog.Open(opts)
	if err != nil {
		t.Fatalf("opening the log: %v", err)
	}
	return l
}

func readLog(t *testing.T, l *runlog.Log) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(l.Dir(), "run.log"))
	if err != nil {
		t.Fatalf("reading the run log: %v", err)
	}
	return string(raw)
}

// TestOpenCreatesThePrivateRunDirectory covers what log_init does
// (lib/log.sh:64): the directory, owner-only, and the opening event that names
// the repository and the pull request.
func TestOpenCreatesThePrivateRunDirectory(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7"})

	assertMode(t, l.Dir(), 0o700)
	if got, want := readLog(t, l), "2026-08-29T01:02:03Z run start repo=acme/widget pr=7\n"; got != want {
		t.Errorf("run log = %q, want %q", got, want)
	}
	assertMode(t, filepath.Join(l.Dir(), "run.log"), 0o600)
}

// TestOpenIgnoresTheProcessUmask: the mode is requested at creation, so a
// permissive umask cannot widen it and a restrictive one cannot narrow it past
// what is asked for.
func TestOpenIgnoresTheProcessUmask(t *testing.T) {
	previous := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(previous) })

	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7"})
	assertMode(t, l.Dir(), 0o700)
	assertMode(t, filepath.Join(l.Dir(), "run.log"), 0o600)
}

// TestOpenWithNoDirectory: a run directory that cannot be made leaves the
// caller a nil *Log it can go on using, which is the Bash library's fallback to
// an empty CROSSREV_RUN_DIR (lib/log.sh:68).
func TestOpenWithNoDirectory(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	l, err := runlog.Open(runlog.Options{Dir: filepath.Join(blocked, "runs")})
	if err == nil {
		t.Fatal("Open returned no error for a directory it could not create")
	}
	if l != nil {
		t.Fatalf("Open returned a usable log: %v", l)
	}

	// Every method tolerates it, because the callers are the paths whose own
	// failure is what is being recorded.
	l.Event("run", "start")
	l.SetLeg("review")
	l.ClearTranscripts("")
	l.RedactFile(blocked)
	if got := l.Dir(); got != "" {
		t.Errorf("Dir() = %q, want empty", got)
	}
	if _, ok := l.TranscriptBase(1); ok {
		t.Error("TranscriptBase reported a stem with no run directory")
	}
	if l.TranscriptsKept() {
		t.Error("TranscriptsKept is true with no run directory")
	}
}

// TestEventCollapsesNewlines holds the one-line-per-event invariant. Callers
// pass git tails and die reasons that already contain newlines
// (lib/log.sh:82-85).
func TestEventCollapsesNewlines(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7"})
	l.Event("exit", "code=1 reason=first\nsecond\r\nthird")

	lines := strings.Count(readLog(t, l), "\n")
	if lines != 2 {
		t.Errorf("run log holds %d lines, want 2:\n%s", lines, readLog(t, l))
	}
	if !strings.Contains(readLog(t, l), "exit code=1 reason=first second  third\n") {
		t.Errorf("the detail was not collapsed:\n%s", readLog(t, l))
	}
}

// TestEventRedactsWhatItWrites: the rule is that nothing reaches the file
// unfiltered, with no exception to remember, even though every caller builds
// its detail from names and exit codes (lib/log.sh:74-77).
func TestEventRedactsWhatItWrites(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7"})
	l.Event("harness", "claude said ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	got := readLog(t, l)
	if strings.Contains(got, "ghp_AAAAAAAAAAAA") {
		t.Errorf("the run log carries the credential:\n%s", got)
	}
	if !strings.Contains(got, "ghp_AAAAAA…[redacted]") {
		t.Errorf("the run log does not carry the mask:\n%s", got)
	}
}

// TestEventAppends: the log grows rather than being rewritten, and an existing
// file is never truncated by the create that guards its mode.
func TestEventAppends(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7"})
	l.Event("leg", "review start")
	l.Event("leg", "review done")

	if got, want := strings.Count(readLog(t, l), "\n"), 3; got != want {
		t.Errorf("run log holds %d lines, want %d:\n%s", got, want, readLog(t, l))
	}
}

// TestEventConcurrently: the Bash library cannot write two events at once and
// Go can, so the file is guarded. Run under -race, this is the test that says
// so.
func TestEventConcurrently(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7"})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Event("leg", "attempt")
		}()
	}
	wg.Wait()

	if got, want := strings.Count(readLog(t, l), "\n"), 9; got != want {
		t.Errorf("run log holds %d lines, want %d", got, want)
	}
}
