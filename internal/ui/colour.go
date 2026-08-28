package ui

// Palette is the set of escape codes the helpers write around what they print.
//
// It is a value rather than a package variable, because the Bash library
// decides colour once when it is sourced (lib/ui.sh:19-27) and that is exactly
// what makes it hard to test: a suite that captures output through a pipe
// freezes one answer and a session at a terminal freezes the other. Deciding
// once is right; deciding once inside a global is what a caller cannot undo.
type Palette struct {
	Reset  string
	Dim    string
	Bold   string
	Red    string
	Yellow string
	Green  string
	Blue   string
}

// Colour is the palette a terminal gets.
func Colour() Palette {
	return Palette{
		Reset:  "\033[0m",
		Dim:    "\033[2m",
		Bold:   "\033[1m",
		Red:    "\033[31m",
		Yellow: "\033[33m",
		Green:  "\033[32m",
		Blue:   "\033[34m",
	}
}

// Plain is the palette everything else gets, and it is the zero Palette: a
// caller that never sets one prints no escape codes rather than a broken half
// of them.
func Plain() Palette { return Palette{} }

// PaletteFor decides colour the way lib/ui.sh:19 does: on when stdout is a
// terminal and NO_COLOR says nothing, off otherwise. Both halves matter,
// because this runs in CI as often as in a shell.
//
// noColor is the value of NO_COLOR (https://no-color.org). An unset and an
// empty variable are the same answer here — the Bash test is `-z` — so one
// string carries both.
func PaletteFor(stdoutIsTerminal bool, noColor string) Palette {
	if stdoutIsTerminal && noColor == "" {
		return Colour()
	}
	return Plain()
}
