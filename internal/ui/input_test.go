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

// TestTerminalPrefersTheControllingTerminal: the first arm wins even when
// stdin is available, because `curl … | sh` leaves the script on stdin.
func TestTerminalPrefersTheControllingTerminal(t *testing.T) {
	source := tty(t, "from the terminal\n")
	source.Stdin = os.Stdin

	reader, err := source.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from the terminal\n" {
		t.Errorf("read %q from the wrong source", got)
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
