package preflight_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/preflight"
)

// A state directory with no worktrees under it says nothing
// (lib/preflight.sh:330-335).
func TestReportWorktreesIsSilentWithNothingToReport(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	io, buf := capture()
	c := &preflight.Checker{IO: io}
	c.ReportWorktrees()
	if buf.Len() != 0 {
		t.Errorf("printed %q with no base directory, want nothing", buf)
	}

	if err := os.MkdirAll(filepath.Join(state, "crossrev", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	io, buf = capture()
	c = &preflight.Checker{IO: io}
	c.ReportWorktrees()
	if buf.Len() != 0 {
		t.Errorf("printed %q with an empty base directory, want nothing", buf)
	}
}

// A clean resolve run removes its worktree; a failed run leaves it behind. The
// accumulation is reported so it is discoverable rather than silent
// (lib/preflight.sh:328-342).
func TestReportWorktreesNamesEveryLeftoverWorktree(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	base := filepath.Join(state, "crossrev", "worktrees")
	for _, dir := range []string{"acme-widget/pr-42", "acme-widget/pr-7", "zz-other/pr-1"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Only directories are worktrees: `find -type d` skips a loose file, and a
	// worktree's own contents are a level too deep to be listed.
	if err := os.WriteFile(filepath.Join(base, "acme-widget", "loose-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "acme-widget", "pr-42", "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	io, buf := capture()
	c := &preflight.Checker{IO: io}
	c.ReportWorktrees()

	want := "\n◇  Tool-owned worktrees\n" +
		"│  ○ " + base + "/acme-widget/pr-42\n" +
		"│  ○ " + base + "/acme-widget/pr-7\n" +
		"│  ○ " + base + "/zz-other/pr-1\n" +
		"│     Left behind by failed resolve runs. Safe to remove if no run is in progress.\n"
	if got := buf.String(); got != want {
		t.Errorf("report =\n%q\nwant\n%q", got, want)
	}
}

// The base is built by concatenation, so a trailing slash on XDG_STATE_HOME
// survives into the reported path the way it does in vcs.WorktreeDir.
func TestReportWorktreesKeepsTheStateHomeAsItIsGiven(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state+"/")
	if err := os.MkdirAll(filepath.Join(state, "crossrev", "worktrees", "acme-widget", "pr-1"), 0o755); err != nil {
		t.Fatal(err)
	}

	io, buf := capture()
	c := &preflight.Checker{IO: io}
	c.ReportWorktrees()

	want := "│  ○ " + state + "//crossrev/worktrees/acme-widget/pr-1\n"
	if got := buf.String(); !strings.Contains(got, want) {
		t.Errorf("report =\n%q\nwant a line %q", got, want)
	}
}

// An empty XDG_STATE_HOME falls back to HOME, which is the `:-` form: it takes
// the fallback on an empty value as well as an unset one
// (lib/preflight.sh:329).
func TestReportWorktreesFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".local", "state", "crossrev", "worktrees", "acme-widget", "pr-3"), 0o755); err != nil {
		t.Fatal(err)
	}

	io, buf := capture()
	c := &preflight.Checker{IO: io}
	c.ReportWorktrees()

	want := "│  ○ " + home + "/.local/state/crossrev/worktrees/acme-widget/pr-3\n"
	if got := buf.String(); !strings.Contains(got, want) {
		t.Errorf("report =\n%q\nwant a line %q", got, want)
	}
}
