package ui_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// TestPaletteFor pins the decision at lib/ui.sh:19. NO_COLOR set to the empty
// string is the same as unset, because the Bash test is `-z` rather than a
// presence check; a port that read presence would print no colour for anyone
// who exports the variable empty.
func TestPaletteFor(t *testing.T) {
	tests := []struct {
		name       string
		isTerminal bool
		noColor    string
		want       ui.Palette
	}{
		{"a terminal with nothing said", true, "", ui.Colour()},
		{"a terminal with NO_COLOR set", true, "1", ui.Plain()},
		{"a terminal with NO_COLOR set to anything", true, "false", ui.Plain()},
		{"a terminal with NO_COLOR set empty", true, "", ui.Colour()},
		{"a pipe", false, "", ui.Plain()},
		{"a pipe with NO_COLOR set", false, "1", ui.Plain()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ui.PaletteFor(tt.isTerminal, tt.noColor); got != tt.want {
				t.Errorf("PaletteFor(%t, %q) = %+v, want %+v", tt.isTerminal, tt.noColor, got, tt.want)
			}
		})
	}
}

// TestPlainIsTheZeroPalette: a caller that never sets one prints no escape
// codes rather than a broken half of them.
func TestPlainIsTheZeroPalette(t *testing.T) {
	var zero ui.Palette
	if ui.Plain() != zero {
		t.Errorf("Plain() = %+v, want the zero Palette", ui.Plain())
	}
}

// TestIsTerminal covers the answers this suite can produce without a terminal
// of its own. /dev/null is the case that decides the implementation: it is a
// character device, so the usual Go shortcut calls it a terminal, and a run
// whose stdin comes from it is exactly the CI case the input fallback exists to
// refuse.
func TestIsTerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-terminal")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()

	pipeRead, pipeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipeRead.Close()
	defer pipeWrite.Close()

	tests := []struct {
		name string
		file *os.File
	}{
		{"a regular file", file},
		{"the null device", null},
		{"a pipe", pipeRead},
		{"nothing at all", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ui.IsTerminal(tt.file) {
				t.Error("reported as a terminal")
			}
		})
	}
}
