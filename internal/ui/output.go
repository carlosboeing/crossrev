package ui

import (
	"fmt"
	"io"
)

// IO is where the voice writes, what it writes in, and where it reads an answer
// from. Every one of those is handed in, so a test captures the output it
// asserts against and never reaches the terminal the suite runs on.
//
// The zero value writes nowhere and prints no colour. That is deliberate:
// every helper here runs from a command that is already reporting something
// else, so a half-wired IO must discard rather than panic.
type IO struct {
	// Out is where the block goes: the sections, the rows, the confirmations.
	Out io.Writer
	// Err is where a warning, an error and a value prompt go, so a caller can
	// capture a printed value from stdout while the reader still sees why.
	Err io.Writer
	// Palette is decided once by the command, from PaletteFor.
	Palette Palette
	// Input is where an answer is read from. Nil means there is none, which is
	// the third arm of _ui_input_source and the one that dies with a message.
	Input Input
	// AssumeYes answers every confirmation without asking, which is --yes.
	AssumeYes bool
}

func (o *IO) out() io.Writer {
	if o == nil || o.Out == nil {
		return io.Discard
	}
	return o.Out
}

func (o *IO) err() io.Writer {
	if o == nil || o.Err == nil {
		return io.Discard
	}
	return o.Err
}

func (o *IO) palette() Palette {
	if o == nil {
		return Plain()
	}
	return o.Palette
}

// State colours the state word in a section heading.
type State int

const (
	// StateNeutral is the zero value and the neutral blue, which is what the
	// Bash version gives any word it was not told how to colour.
	StateNeutral State = iota
	StateOK
	StateBad
	StateWarn
)

// Step is the glyph on a leg row.
type Step int

const (
	// StepIdle is the zero value: a leg that has not started.
	StepIdle Step = iota
	StepOK
	StepNo
	// StepRun is a leg that is working right now, and it is deliberately
	// neither of the other two: a tick would read as finished and a cross as
	// failed, and it is neither. Blue rather than green for the same reason —
	// the outcome is not in yet, and only the two settled glyphs get a verdict
	// colour (lib/ui.sh:55-58).
	StepRun
)

// Section opens a block. The body lines under it are prefixed with a rule.
func (o *IO) Section(title string) {
	p := o.palette()
	fmt.Fprintf(o.out(), "\n%s◇  %s%s\n", p.Blue, title, p.Reset)
}

// SectionState opens a block whose subject carries a state word — "#3 —
// converged".
//
// The word answers the first question before the reader parses anything else,
// which is the whole job when they are checking a dozen pull requests in a row.
// The note is an optional qualifier, such as "(retried once)".
func (o *IO) SectionState(title, word string, state State, note string) {
	p := o.palette()
	var colour string
	switch state {
	case StateOK:
		colour = p.Green
	case StateBad:
		colour = p.Red
	case StateWarn:
		colour = p.Yellow
	default:
		colour = p.Blue
	}
	fmt.Fprintf(o.out(), "\n%s◇  %s%s — %s%s%s", p.Blue, title, p.Reset, colour, word, p.Reset)
	if note != "" {
		fmt.Fprintf(o.out(), " %s%s%s", p.Yellow, note, p.Reset)
	}
	fmt.Fprint(o.out(), "\n")
}

// Head is a heading inside a section, grouping the lines under it.
func (o *IO) Head(text string) {
	p := o.palette()
	fmt.Fprintf(o.out(), "%s│%s  %s%s%s\n", p.Dim, p.Reset, p.Bold, text, p.Reset)
}

// Row is a leg line, with a gutter before the glyph so the pass number sits to
// its left.
func (o *IO) Row(gutter string, step Step, text string) {
	p := o.palette()
	var glyph string
	switch step {
	case StepOK:
		glyph = p.Green + "✓" + p.Reset
	case StepNo:
		glyph = p.Red + "✗" + p.Reset
	case StepRun:
		glyph = p.Blue + "◐" + p.Reset
	default:
		glyph = p.Dim + "○" + p.Reset
	}
	fmt.Fprintf(o.out(), "%s│%s  %s%s %s\n", p.Dim, p.Reset, gutter, glyph, text)
}

// Cmd is a command the reader can type. No glyph: under a NEXT heading the
// arrow is redundant, and the line reads as something to copy rather than as
// narration.
func (o *IO) Cmd(command string) {
	p := o.palette()
	fmt.Fprintf(o.out(), "%s│%s  %s%s%s\n", p.Dim, p.Reset, p.Blue, command, p.Reset)
}

// Line is a body line inside a section.
func (o *IO) Line(text string) {
	p := o.palette()
	fmt.Fprintf(o.out(), "%s│%s  %s\n", p.Dim, p.Reset, text)
}

// Gap is a blank rule, for spacing inside a section.
func (o *IO) Gap() {
	p := o.palette()
	fmt.Fprintf(o.out(), "%s│%s\n", p.Dim, p.Reset)
}

// End is the terminal line, closing a block.
func (o *IO) End(text string) {
	p := o.palette()
	fmt.Fprintf(o.out(), "%s└%s  %s\n\n", p.Dim, p.Reset, text)
}

// OK reports verified success. Only call it once the thing is actually true —
// rule 5.
func (o *IO) OK(text string) {
	p := o.palette()
	fmt.Fprintf(o.out(), "%s│%s  %s✓%s %s\n", p.Dim, p.Reset, p.Green, p.Reset, text)
}

// No reports a thing that is absent or failed.
func (o *IO) No(text string) {
	p := o.palette()
	fmt.Fprintf(o.out(), "%s│%s  %s✗%s %s\n", p.Dim, p.Reset, p.Red, p.Reset, text)
}

// Opt reports a thing that is absent but optional.
func (o *IO) Opt(text string) {
	p := o.palette()
	fmt.Fprintf(o.out(), "%s│%s  %s○%s %s\n", p.Dim, p.Reset, p.Dim, p.Reset, text)
}

// Next is the next action.
func (o *IO) Next(text string) {
	p := o.palette()
	fmt.Fprintf(o.out(), "%s│%s  %s→%s %s\n", p.Dim, p.Reset, p.Blue, p.Reset, text)
}

// Say is a plain informational line outside a section.
func (o *IO) Say(text string) {
	fmt.Fprintf(o.out(), "  %s\n", text)
}

// Warn states a condition and what it costs — rule 3. Both are required,
// because a warning that names a condition without its consequence is the thing
// this project keeps trying not to print.
func (o *IO) Warn(condition, consequence string) {
	p := o.palette()
	fmt.Fprintf(o.err(), "\n%s⚠  %s%s\n", p.Yellow, condition, p.Reset)
	fmt.Fprintf(o.err(), "   %s\n\n", consequence)
}

// Die prints what went wrong and what to do about it — rule 4 — and returns the
// error the caller sends up. It does not end the process; see FatalError for
// why, and for what replaces CROSSREV_DIE_REASON.
func (o *IO) Die(reason, action string) error {
	p := o.palette()
	fmt.Fprintf(o.err(), "\n%serror%s  %s\n", p.Red, p.Reset, reason)
	fmt.Fprintf(o.err(), "       %s\n\n", action)
	return &FatalError{Reason: reason, Action: action}
}

// Warning joins a condition to its consequence with the newline and three-space
// indent IO.Warn prints between them. A caller that answers one string rather
// than two — every leg does, because Result.Messages is a []string — builds the
// bytes here so a renderer arriving later has them. Split the pair apart at
// that point and let Warn do the joining.
func Warning(condition, consequence string) string {
	return condition + "\n   " + consequence
}
