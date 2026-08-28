package runlog_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/runlog"
)

func TestRetentionDays(t *testing.T) {
	tests := []struct {
		value string
		want  int
	}{
		{"", 14},
		{"7", 7},
		{"0", 0},
		{"365", 365},
		{"not-a-number", 14},
		{"-1", 14},
		{"7.5", 14},
		{" 7", 14},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := runlog.RetentionDays(tt.value); got != tt.want {
				t.Errorf("RetentionDays(%q) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

// age makes a directory read as n days old.
func age(t *testing.T, dir string, days int, now time.Time) {
	t.Helper()
	when := now.AddDate(0, 0, -days)
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestSweepRemovesRunsPastTheWindow covers the first pass, at exactly depth
// three (lib/log.sh:198-201).
func TestSweepRemovesRunsPastTheWindow(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	base := filepath.Join(t.TempDir(), "runs")

	old := filepath.Join(base, "acme-widget", "pr-7", "local-99999")
	fresh := filepath.Join(base, "acme-widget", "pr-7", "local-88888")
	for _, dir := range []string{old, fresh} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "run.log"), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	age(t, old, 40, now)

	runlog.Sweep(base, 14, now)

	if exists(old) {
		t.Error("the sweep left a run directory older than the window")
	}
	if !exists(filepath.Join(fresh, "run.log")) {
		t.Error("the sweep removed a recent run directory")
	}
}

// TestSweepAgeBoundary pins find's own arithmetic: `-mtime +14` is the age in
// whole days, truncated, being greater than 14. A run at exactly the window
// survives; the next whole day past it does not.
func TestSweepAgeBoundary(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		hours   int
		removed bool
	}{
		{"fourteen days", 14 * 24, false},
		{"one hour short of fifteen", 15*24 - 1, false},
		{"fifteen days", 15 * 24, true},
		{"one hour past fifteen", 15*24 + 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "runs")
			run := filepath.Join(base, "acme-widget", "pr-7", "local-1")
			if err := os.MkdirAll(run, 0o700); err != nil {
				t.Fatal(err)
			}
			when := now.Add(-time.Duration(tt.hours) * time.Hour)
			if err := os.Chtimes(run, when, when); err != nil {
				t.Fatal(err)
			}

			runlog.Sweep(base, 14, now)

			if removed := !exists(run); removed != tt.removed {
				t.Errorf("removed = %t at %d hours old, want %t", removed, tt.hours, tt.removed)
			}
		})
	}
}

// TestSweepNeverRemovesTheLevelsAboveARun is why the first pass is pinned to
// exactly depth three. A slug directory holding one expired run must lose the
// run and keep itself; it is emptied by the removal, so its mtime is now and
// the second pass leaves it for the next sweep.
func TestSweepNeverRemovesTheLevelsAboveARun(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	base := filepath.Join(t.TempDir(), "runs")
	slug := filepath.Join(base, "acme-widget")
	pr := filepath.Join(slug, "pr-7")
	run := filepath.Join(pr, "local-1")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{run, pr, slug} {
		age(t, dir, 40, now)
	}

	runlog.Sweep(base, 14, now)

	if exists(run) {
		t.Error("the expired run survived")
	}
	if !exists(pr) || !exists(slug) {
		t.Error("the sweep removed a level above the run in the same pass")
	}
	if !exists(base) {
		t.Error("the sweep removed the base it was given")
	}
}

// TestSweepRemovesDirectoriesLeftEmptyByAnEarlierSweep covers the second pass
// (lib/log.sh:203-204): a pull-request directory emptied by a previous run of
// the sweep, and old enough, goes.
func TestSweepRemovesDirectoriesLeftEmptyByAnEarlierSweep(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	base := filepath.Join(t.TempDir(), "runs")
	slug := filepath.Join(base, "acme-widget")
	pr := filepath.Join(slug, "pr-7")
	if err := os.MkdirAll(pr, 0o700); err != nil {
		t.Fatal(err)
	}
	age(t, pr, 40, now)
	age(t, slug, 40, now)

	runlog.Sweep(base, 14, now)

	if exists(pr) {
		t.Error("an empty pull-request directory past the window survived")
	}
	// The slug directory was emptied by this very sweep, so its mtime is now
	// and it survives until the next one.
	if !exists(slug) {
		t.Error("a directory emptied by this sweep was removed in the same pass")
	}
}

// TestSweepLeavesAnEmptyDirectoryInsideTheWindow: a directory another process
// has just made and not yet written into must survive.
func TestSweepLeavesAnEmptyDirectoryInsideTheWindow(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	base := filepath.Join(t.TempDir(), "runs")
	pr := filepath.Join(base, "acme-widget", "pr-7")
	if err := os.MkdirAll(pr, 0o700); err != nil {
		t.Fatal(err)
	}

	runlog.Sweep(base, 14, now)

	if !exists(pr) {
		t.Error("a fresh empty directory was swept")
	}
}

// TestSweepSurvivesWhatItCannotRead: the Bash sweep runs from log_init and
// returns zero whatever happens.
func TestSweepSurvivesWhatItCannotRead(t *testing.T) {
	now := time.Now()
	runlog.Sweep("", 14, now)
	runlog.Sweep(filepath.Join(t.TempDir(), "no-such-base"), 14, now)

	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runlog.Sweep(file, 14, now)
	if !exists(file) {
		t.Error("the sweep removed the file it was pointed at")
	}
}
