package preflight_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/preflight"
)

// A checkout with no quarantine in it has nothing to say
// (lib/preflight.sh:310).
func TestCheckQuarantineIsSilentWhenThereIsNone(t *testing.T) {
	io, buf := capture()
	c := &preflight.Checker{IO: io, Dir: t.TempDir()}
	if !c.CheckQuarantine() {
		t.Errorf("CheckQuarantine = false, want true")
	}
	if buf.Len() != 0 {
		t.Errorf("printed %q, want nothing", buf)
	}
}

// A quarantine directory that exists is stranded whether or not anything is
// left in it, and an empty one names no files (lib/preflight.sh:315-320).
func TestCheckQuarantineReportsAnEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".crossrev-quarantine"), 0o755); err != nil {
		t.Fatal(err)
	}

	io, buf := capture()
	c := &preflight.Checker{IO: io, Dir: dir}
	if c.CheckQuarantine() {
		t.Errorf("CheckQuarantine = true, want false")
	}
	want := "│  ✗ stranded quarantine found at .crossrev-quarantine\n" +
		"│     A previous run died before restoring the checkout. Move them back to restore your files.\n"
	if got := buf.String(); got != want {
		t.Errorf("report =\n%q\nwant\n%q", got, want)
	}
}

// Everything inside is named, at every depth, relative to the quarantine and in
// order (lib/preflight.sh:312-317).
func TestCheckQuarantineNamesWhatIsInside(t *testing.T) {
	dir := t.TempDir()
	quarantine := filepath.Join(dir, ".crossrev-quarantine")
	if err := os.MkdirAll(filepath.Join(quarantine, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"AGENTS.md", ".mcp.json", ".claude/settings.json"} {
		if err := os.WriteFile(filepath.Join(quarantine, path), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	io, buf := capture()
	c := &preflight.Checker{IO: io, Dir: dir}
	if c.CheckQuarantine() {
		t.Errorf("CheckQuarantine = true, want false")
	}
	want := "│  ✗ stranded quarantine found at .crossrev-quarantine\n" +
		"│     Files inside: .claude .claude/settings.json .mcp.json AGENTS.md\n" +
		"│     A previous run died before restoring the checkout. Move them back to restore your files.\n"
	if got := buf.String(); got != want {
		t.Errorf("report =\n%q\nwant\n%q", got, want)
	}
}

// The quarantine is looked for beside the checkout rather than beside the
// binary, and it is named in the report by the relative path the shell uses
// whatever directory the command was given (lib/preflight.sh:309).
func TestCheckQuarantineLooksInTheCheckoutItWasGiven(t *testing.T) {
	elsewhere := t.TempDir()
	if err := os.Mkdir(filepath.Join(elsewhere, ".crossrev-quarantine"), 0o755); err != nil {
		t.Fatal(err)
	}

	io, buf := capture()
	c := &preflight.Checker{IO: io, Dir: t.TempDir()}
	if !c.CheckQuarantine() {
		t.Errorf("a quarantine in another directory was reported: %q", buf)
	}

	io, buf = capture()
	c = &preflight.Checker{IO: io, Dir: elsewhere}
	if c.CheckQuarantine() {
		t.Errorf("the quarantine in the named checkout was missed")
	}
	if got := buf.String(); got[:len("│  ✗ stranded quarantine found at .crossrev-quarantine\n")] !=
		"│  ✗ stranded quarantine found at .crossrev-quarantine\n" {
		t.Errorf("report named an absolute path:\n%s", got)
	}
}

// With no directory named the probe reads the working directory, which is
// where the Bash relative path resolves. Every case above passes Dir, so
// without this the default is never taken.
func TestCheckQuarantineDefaultsToTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".crossrev-quarantine"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	io, buf := capture()
	c := &preflight.Checker{IO: io}
	if c.CheckQuarantine() {
		t.Errorf("CheckQuarantine = true, want false")
	}
	if buf.Len() == 0 {
		t.Errorf("the quarantine in the working directory was not reported")
	}
}
