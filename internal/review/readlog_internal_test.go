package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/runlog"
)

func openReadLogTestLog(t *testing.T, dir string) *runlog.Log {
	t.Helper()
	l, err := runlog.Open(runlog.Options{Dir: dir})
	if err != nil {
		t.Fatalf("runlog.Open: %v", err)
	}
	return l
}

// The call's read log is archived beside the transcripts and the scratch copy
// removed, so one call's reads do not leak into the next.
func TestCopyReadLogArchivesAndRemoves(t *testing.T) {
	tmp := t.TempDir()
	const body = "{\"event\":\"start\"}\n{\"event\":\"read\"}\n"
	if err := os.WriteFile(filepath.Join(tmp, "reads.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	run := t.TempDir()
	l := openReadLogTestLog(t, run)

	copyReadLog(l, tmp, 1)

	got, err := os.ReadFile(filepath.Join(run, "reads.call-1.jsonl"))
	if err != nil {
		t.Fatalf("the run directory holds no reads.call-1.jsonl: %v", err)
	}
	if string(got) != body {
		t.Errorf("reads.call-1.jsonl = %q, want %q", got, body)
	}
	if _, err := os.Stat(filepath.Join(tmp, "reads.jsonl")); !os.IsNotExist(err) {
		t.Errorf("the scratch reads.jsonl survives a successful copy: %v", err)
	}
}

// A copy that cannot land keeps the scratch copy and records the loss in the
// run log, in the same words WriteTranscript uses for a transcript it could
// not write. A directory standing where the target goes refuses the open on
// every platform without touching permissions.
func TestCopyReadLogKeepsTheSourceAndRecordsAFailedCopy(t *testing.T) {
	tmp := t.TempDir()
	const body = "{\"event\":\"start\"}\n"
	if err := os.WriteFile(filepath.Join(tmp, "reads.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	run := t.TempDir()
	l := openReadLogTestLog(t, run)
	target := filepath.Join(run, "reads.call-1.jsonl")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	copyReadLog(l, tmp, 1)

	if got, err := os.ReadFile(filepath.Join(tmp, "reads.jsonl")); err != nil || string(got) != body {
		t.Errorf("the scratch reads.jsonl was destroyed by a failed copy: %q, %v", got, err)
	}
	log, err := os.ReadFile(filepath.Join(run, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "could not write " + target; !strings.Contains(string(log), want) {
		t.Errorf("run.log carries no record of the failed copy, want %q:\n%s", want, log)
	}
}

// No read server ran, or it logged nothing: there is nothing to archive and
// nothing to record.
func TestCopyReadLogIgnoresAMissingOrEmptySource(t *testing.T) {
	run := t.TempDir()
	l := openReadLogTestLog(t, run)

	copyReadLog(l, t.TempDir(), 1)

	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "reads.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	copyReadLog(l, empty, 2)

	entries, err := os.ReadDir(run)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "reads.call-") {
			t.Errorf("a missing or empty source archived %s", entry.Name())
		}
	}
}
