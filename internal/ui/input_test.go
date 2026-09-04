package ui_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// answers is an Input backed by a string, so no test in this package opens the
// terminal the suite is running on.
type answers struct {
	text  string
	opens int
	fail  error
}

func (a *answers) Open() (io.ReadCloser, error) {
	if a.fail != nil {
		return nil, a.fail
	}
	a.opens++
	return io.NopCloser(strings.NewReader(a.text)), nil
}

// tty writes text to a file and points a Terminal at it, which exercises the
// first arm of _ui_input_source without ever opening /dev/tty.
func tty(t *testing.T, text string) ui.Terminal {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tty")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return ui.Terminal{TTYPath: path}
}

// TestTerminalPrefersTheControllingTerminal: with both arms available, the
// controlling terminal is the one taken.
//
// Both arms have to be real for this to measure the order. The second is
// guarded by IsTerminal, and under `go test` the process's stdin is not one, so
// handing over os.Stdin exercises the first arm alone and passes just as
// happily with the two swapped. Stdin here is a pseudo-terminal, which answers
// the same ioctl a real one does.
//
// It asserts which source was chosen rather than reading from it: reading the
// wrong one would block on an idle terminal rather than fail.
func TestTerminalPrefersTheControllingTerminal(t *testing.T) {
	stdin := openPTY(t)
	if stdin == nil {
		t.Skip("no pseudo-terminal on this platform, so only one arm is reachable")
	}
	if !ui.IsTerminal(stdin) {
		t.Fatal("the pseudo-terminal does not read as a terminal")
	}
	source := tty(t, "from the terminal\n")
	source.Stdin = stdin

	reader, err := source.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()

	chosen, ok := reader.(*os.File)
	if !ok || chosen.Name() != source.TTYPath {
		t.Fatalf("Open chose %T, want the controlling terminal at %s", reader, source.TTYPath)
	}
	got, err := io.ReadAll(chosen)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from the terminal\n" {
		t.Errorf("read %q from the wrong source", got)
	}
}

// TestTerminalRefusesAStdinThatIsNotATerminal is what protects `curl … | sh`:
// the script is on stdin there, and it must never be read as an answer. The
// guard rather than the arm order is what does it — a swapped Open refuses the
// pipe just the same.
func TestTerminalRefusesAStdinThatIsNotATerminal(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if _, err := writer.WriteString("y\n"); err != nil {
		t.Fatal(err)
	}

	source := ui.Terminal{
		TTYPath: filepath.Join(t.TempDir(), "no-such-terminal"),
		Stdin:   reader,
	}
	if _, err := source.Open(); !errors.Is(err, ui.ErrNoInput) {
		t.Errorf("Open err = %v, want ErrNoInput; a piped script is not an answer", err)
	}
}

// TestTerminalDefaultsToTheControllingTerminal: an empty TTYPath is
// DefaultTTYPath, and every test sets the field, so nothing else pins the
// fallback. The two must reach the same place whether or not this machine has a
// controlling terminal, which is what makes the check the same measurement on a
// runner and in a session.
func TestTerminalDefaultsToTheControllingTerminal(t *testing.T) {
	if ui.DefaultTTYPath != "/dev/tty" {
		t.Errorf("DefaultTTYPath = %q, want /dev/tty", ui.DefaultTTYPath)
	}

	unset, errUnset := ui.Terminal{}.Open()
	if unset != nil {
		unset.Close()
	}
	named, errNamed := ui.Terminal{TTYPath: ui.DefaultTTYPath}.Open()
	if named != nil {
		named.Close()
	}
	if (errUnset == nil) != (errNamed == nil) {
		t.Errorf("an empty TTYPath answered %v and DefaultTTYPath answered %v", errUnset, errNamed)
	}
}

// TestTerminalRefusesWithNeitherArm is the path a CI runner takes: no
// controlling terminal, and a stdin that is not one either.
func TestTerminalRefusesWithNeitherArm(t *testing.T) {
	notATerminal, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer notATerminal.Close()

	source := ui.Terminal{
		TTYPath: filepath.Join(t.TempDir(), "no-such-terminal"),
		Stdin:   notATerminal,
	}
	if _, err := source.Open(); !errors.Is(err, ui.ErrNoInput) {
		t.Errorf("Open err = %v, want ErrNoInput", err)
	}

	source.Stdin = nil
	if _, err := source.Open(); !errors.Is(err, ui.ErrNoInput) {
		t.Errorf("Open with no stdin err = %v, want ErrNoInput", err)
	}
}

// TestNoInputRefusesRatherThanBlocking is the case CLAUDE.md warns about: a
// session at a terminal takes an arm a runner cannot, so the runner's path has
// to be the one under test. Nothing blocks and the message names the cause.
func TestNoInputRefusesRatherThanBlocking(t *testing.T) {
	var r recorder
	o := r.io(ui.Plain())
	o.Input = &answers{fail: ui.ErrNoInput}

	confirmed, err := o.Confirm("Create 5 labels on acme/widget?")
	if confirmed {
		t.Error("Confirm said yes with no input")
	}
	want := "CrossRev needs to ask you something, but no terminal is attached"
	if ui.Reason(err) != want {
		t.Errorf("die reason = %q, want %q", ui.Reason(err), want)
	}
	if got := r.err.String(); !strings.Contains(got, want) {
		t.Errorf("stderr = %q, want it to carry the refusal", got)
	}
	if got := r.err.String(); !strings.Contains(got, "Editor-embedded and captured shells often have no controlling terminal") {
		t.Errorf("stderr = %q, want it to say what to do", got)
	}
	// Nothing was asked, because there was nowhere to hear the answer.
	if got := r.out.String(); got != "" {
		t.Errorf("stdout = %q, want nothing printed", got)
	}
}

// TestPromptRefusesWithNoInput is the same refusal on the other reader.
func TestPromptRefusesWithNoInput(t *testing.T) {
	var r recorder
	o := r.io(ui.Plain())
	o.Input = &answers{fail: ui.ErrNoInput}

	value, err := o.Prompt("Which repository?")
	if value != "" {
		t.Errorf("Prompt returned %q with no input", value)
	}
	if ui.Reason(err) == "" {
		t.Errorf("Prompt err = %v, want a fatal error carrying a reason", err)
	}
}
