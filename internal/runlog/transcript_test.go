package runlog_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/carlosboeing/crossrev/internal/runlog"
)

func TestKeepTranscripts(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"", false},
		{"0", false},
		{"true", false},
		{"yes", false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := runlog.KeepTranscripts(tt.value); got != tt.want {
				t.Errorf("KeepTranscripts(%q) = %t, want %t", tt.value, got, tt.want)
			}
		})
	}
}

// TestTranscriptBasePreCreatesPrivateFiles: the three files exist at 0600
// before an adapter's redirect opens them, so the redirect inherits the mode
// rather than the process umask (lib/log.sh:221-225).
func TestTranscriptBasePreCreatesPrivateFiles(t *testing.T) {
	previous := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(previous) })

	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7", Leg: "review"})
	base, ok := l.TranscriptBase(2)
	if !ok {
		t.Fatal("TranscriptBase reported no stem")
	}
	if want := filepath.Join(l.Dir(), "review.attempt-2"); base != want {
		t.Errorf("base = %q, want %q", base, want)
	}
	for _, stream := range []string{".stdout", ".stderr", ".payload"} {
		assertMode(t, base+stream, 0o600)
	}
}

// TestTranscriptBaseNeedsALeg: with no leg the adapters fall back to anonymous
// temporary files, which is the behaviour every caller of an adapter outside a
// run already has (lib/log.sh:218).
func TestTranscriptBaseNeedsALeg(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7"})
	if base, ok := l.TranscriptBase(1); ok {
		t.Errorf("TranscriptBase reported %q with no leg set", base)
	}

	l.SetLeg("resolve")
	base, ok := l.TranscriptBase(1)
	if !ok {
		t.Fatal("TranscriptBase reported no stem after the leg was named")
	}
	if want := filepath.Join(l.Dir(), "resolve.attempt-1"); base != want {
		t.Errorf("base = %q, want %q", base, want)
	}
}

// TestClearTranscriptsOneAttempt removes the stem it is given and nothing else.
func TestClearTranscriptsOneAttempt(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7", Leg: "review"})
	first, _ := l.TranscriptBase(1)
	second, _ := l.TranscriptBase(2)

	l.ClearTranscripts(first)

	if exists(first + ".stdout") {
		t.Error("the cleared attempt survived")
	}
	if !exists(second + ".stdout") {
		t.Error("a different attempt was cleared too")
	}
	if !exists(filepath.Join(l.Dir(), "run.log")) {
		t.Error("the run log was cleared")
	}
}

// TestClearTranscriptsWholeLeg removes every attempt of the current leg and
// leaves the other leg's alone.
func TestClearTranscriptsWholeLeg(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7", Leg: "review"})
	first, _ := l.TranscriptBase(1)
	second, _ := l.TranscriptBase(2)
	l.SetLeg("resolve")
	other, _ := l.TranscriptBase(1)
	l.SetLeg("review")

	l.ClearTranscripts("")

	for _, base := range []string{first, second} {
		if exists(base + ".stdout") {
			t.Errorf("%s survived a whole-leg clear", base)
		}
	}
	if !exists(other + ".stdout") {
		t.Error("the other leg's transcript was cleared")
	}
}

// TestClearTranscriptsKeepsThemWhenAsked: --keep-transcripts and
// logs.keep_transcripts both land here (lib/log.sh:235).
func TestClearTranscriptsKeepsThemWhenAsked(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7", Leg: "review", KeepTranscripts: true})
	base, _ := l.TranscriptBase(1)

	l.ClearTranscripts(base)

	if !exists(base + ".stdout") {
		t.Error("the transcript was cleared with KeepTranscripts set")
	}
	if !l.TranscriptsKept() {
		t.Error("TranscriptsKept is false with KeepTranscripts set")
	}
}

// TestTranscriptBaseTruncatesAStaleFile: the Bash helper opens the stem with a
// truncating redirect, so an attempt reusing a stem starts empty.
func TestTranscriptBaseTruncatesAStaleFile(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7", Leg: "review"})
	base, _ := l.TranscriptBase(1)
	if err := os.WriteFile(base+".stdout", []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok := l.TranscriptBase(1); !ok {
		t.Fatal("TranscriptBase reported no stem")
	}

	got, err := os.ReadFile(base + ".stdout")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("the stem was not truncated: %q", got)
	}
}
