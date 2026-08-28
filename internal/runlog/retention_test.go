package runlog_test

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
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
		{"+7", 14},
		// Decimal, not octal. find reads the argument as decimal and so must
		// this: measured, the shell deletes a forty-year-old run for 007 and
		// keeps a fresh one, which is seven days exactly.
		{"007", 7},
		{"00000000000000000000014", 14},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := runlog.RetentionDays(tt.value); got != tt.want {
				t.Errorf("RetentionDays(%q) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

// unrepresentable are windows that pass the Bash regex and are too large for an
// int. Measured against lib/log.sh on this platform: each one leaves a
// forty-year-old run directory in place, because BSD find accepts the argument
// and matches nothing while GNU find refuses it and lib/log.sh:205 swallows the
// refusal. Neither deletes anything.
var unrepresentable = []string{
	"9223372036854775808",
	"18446744073709551616",
	"99999999999999999999999999",
}

// TestRetentionDaysOverflow: a window too large to represent becomes
// KeepEverything, not a wrapped number.
//
// Accumulating the digits by hand wraps, and the wrap goes negative: the three
// values below produced -9223372036854775808, 0 and -2537764290115403777. A
// negative window makes every run directory older than the window, including
// the one the current run is writing into.
func TestRetentionDaysOverflow(t *testing.T) {
	for _, value := range unrepresentable {
		t.Run(value, func(t *testing.T) {
			got := runlog.RetentionDays(value)
			if got != runlog.KeepEverything {
				t.Errorf("RetentionDays(%q) = %d, want KeepEverything (%d)", value, got, runlog.KeepEverything)
			}
			if got >= 0 {
				t.Errorf("RetentionDays(%q) = %d, which authorises a sweep", value, got)
			}
		})
	}
}

// TestRetentionOverflowIsNotTheDefault is the test that catches the obvious
// wrong fix.
//
// Falling back to DefaultRetentionDays for an unrepresentable window looks like
// the safe reading and is a data-loss bug: it deletes every run past a
// fortnight for an input the shell deletes nothing at all for. Measured, the
// shell keeps a forty-year-old run directory for each of these. Anyone changing
// the overflow answer to the default has to delete this test to do it.
func TestRetentionOverflowIsNotTheDefault(t *testing.T) {
	for _, value := range unrepresentable {
		if got := runlog.RetentionDays(value); got == runlog.DefaultRetentionDays {
			t.Errorf("RetentionDays(%q) = %d, the default; the shell sweeps nothing for this value, and the default sweeps everything past a fortnight",
				value, got)
		}
	}
}

// TestRetentionDaysBoundary pins the largest window that fits and the first one
// that does not, rather than approaching them. Both are built from math.MaxInt
// so the boundary is the running platform's, not a 64-bit assumption.
func TestRetentionDaysBoundary(t *testing.T) {
	largest := strconv.Itoa(math.MaxInt)
	if got := runlog.RetentionDays(largest); got != math.MaxInt {
		t.Errorf("RetentionDays(%q) = %d, want %d", largest, got, math.MaxInt)
	}

	onePast := strconv.FormatUint(uint64(math.MaxInt)+1, 10)
	if got := runlog.RetentionDays(onePast); got != runlog.KeepEverything {
		t.Errorf("RetentionDays(%q) = %d, want KeepEverything (%d)", onePast, got, runlog.KeepEverything)
	}
}

// TestSweepKeepsAFreshRunForEveryConfiguredWindow is the invariant underneath
// all of it: measured against lib/log.sh, no value of
// CROSSREV_LOG_RETENTION_DAYS deletes a run directory created a moment ago.
// Not the default, not zero, not a typo, and not a number too large to
// represent.
func TestSweepKeepsAFreshRunForEveryConfiguredWindow(t *testing.T) {
	windows := append([]string{"", "14", "0", "007", "1e3", "-1", "not-a-number", "365"}, unrepresentable...)
	for _, value := range windows {
		t.Run(value, func(t *testing.T) {
			now := time.Now()
			base := filepath.Join(t.TempDir(), "runs")
			run := filepath.Join(base, "acme-widget", "pr-7", "local-1")
			if err := os.MkdirAll(run, 0o700); err != nil {
				t.Fatal(err)
			}
			// One millisecond old, which is the age of a run that is still
			// being written into.
			when := now.Add(-time.Millisecond)
			if err := os.Chtimes(run, when, when); err != nil {
				t.Fatal(err)
			}

			runlog.Sweep(base, runlog.RetentionDays(value), now)

			if !exists(run) {
				t.Errorf("a run created one millisecond ago was deleted for retention %q, which parsed to %d",
					value, runlog.RetentionDays(value))
			}
		})
	}
}

// TestSweepRefusesANegativeWindow: a window that says nothing meaningful must
// not authorise a deletion. No configured value produces one, so this is the
// guard against one arriving from anywhere else.
func TestSweepRefusesANegativeWindow(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	base := filepath.Join(t.TempDir(), "runs")
	old := filepath.Join(base, "acme-widget", "pr-7", "local-1")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	age(t, old, 4000, now)

	for _, days := range []int{runlog.KeepEverything, -14, math.MinInt} {
		runlog.Sweep(base, days, now)
		if !exists(old) {
			t.Fatalf("a window of %d deleted a run directory", days)
		}
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
