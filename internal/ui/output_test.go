package ui_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// recorder is the pair of streams a test hands the voice, so nothing here
// writes to the terminal the suite is running on.
type recorder struct {
	out bytes.Buffer
	err bytes.Buffer
}

func (r *recorder) io(palette ui.Palette) *ui.IO {
	return &ui.IO{Out: &r.out, Err: &r.err, Palette: palette}
}

// The escape codes, spelled out once here rather than read from the package, so
// a change to the palette fails a test instead of changing what both sides say.
const (
	reset  = "\033[0m"
	dim    = "\033[2m"
	bold   = "\033[1m"
	red    = "\033[31m"
	yellow = "\033[33m"
	green  = "\033[32m"
	blue   = "\033[34m"
)

// TestColouredOutput pins the exact bytes of every output helper in
// lib/ui.sh:28-119, ui_section through ui_die.
// The box-drawing characters, the two-space gaps and the leading and trailing
// newlines are all load-bearing: they are what makes a block read as a block.
func TestColouredOutput(t *testing.T) {
	tests := []struct {
		name string
		say  func(*ui.IO)
		out  string
		err  string
	}{
		{
			name: "section",
			say:  func(o *ui.IO) { o.Section("Reviewing acme/widget#42") },
			out:  "\n" + blue + "◇  Reviewing acme/widget#42" + reset + "\n",
		},
		{
			name: "section state, converged",
			say:  func(o *ui.IO) { o.SectionState("#3", "converged", ui.StateOK, "") },
			out:  "\n" + blue + "◇  #3" + reset + " — " + green + "converged" + reset + "\n",
		},
		{
			name: "section state, failed",
			say:  func(o *ui.IO) { o.SectionState("#3", "failed", ui.StateBad, "") },
			out:  "\n" + blue + "◇  #3" + reset + " — " + red + "failed" + reset + "\n",
		},
		{
			name: "section state, warned with a qualifier",
			say:  func(o *ui.IO) { o.SectionState("#3", "stalled", ui.StateWarn, "(retried once)") },
			out:  "\n" + blue + "◇  #3" + reset + " — " + yellow + "stalled" + reset + " " + yellow + "(retried once)" + reset + "\n",
		},
		{
			name: "section state, neutral",
			say:  func(o *ui.IO) { o.SectionState("#3", "running", ui.StateNeutral, "") },
			out:  "\n" + blue + "◇  #3" + reset + " — " + blue + "running" + reset + "\n",
		},
		{
			name: "head",
			say:  func(o *ui.IO) { o.Head("NEXT") },
			out:  dim + "│" + reset + "  " + bold + "NEXT" + reset + "\n",
		},
		{
			name: "row, done",
			say:  func(o *ui.IO) { o.Row("2 ", ui.StepOK, "review") },
			out:  dim + "│" + reset + "  2 " + green + "✓" + reset + " review\n",
		},
		{
			name: "row, failed",
			say:  func(o *ui.IO) { o.Row("2 ", ui.StepNo, "review") },
			out:  dim + "│" + reset + "  2 " + red + "✗" + reset + " review\n",
		},
		{
			name: "row, working",
			say:  func(o *ui.IO) { o.Row("2 ", ui.StepRun, "review") },
			out:  dim + "│" + reset + "  2 " + blue + "◐" + reset + " review\n",
		},
		{
			name: "row, not started",
			say:  func(o *ui.IO) { o.Row("2 ", ui.StepIdle, "review") },
			out:  dim + "│" + reset + "  2 " + dim + "○" + reset + " review\n",
		},
		{
			name: "cmd",
			say:  func(o *ui.IO) { o.Cmd("crossrev review --pr 42") },
			out:  dim + "│" + reset + "  " + blue + "crossrev review --pr 42" + reset + "\n",
		},
		{
			name: "line",
			say:  func(o *ui.IO) { o.Line("2 findings") },
			out:  dim + "│" + reset + "  2 findings\n",
		},
		{
			name: "gap",
			say:  func(o *ui.IO) { o.Gap() },
			out:  dim + "│" + reset + "\n",
		},
		{
			name: "end",
			say:  func(o *ui.IO) { o.End("done") },
			out:  dim + "└" + reset + "  done\n\n",
		},
		{
			name: "ok",
			say:  func(o *ui.IO) { o.OK("created 5 labels on acme/widget") },
			out:  dim + "│" + reset + "  " + green + "✓" + reset + " created 5 labels on acme/widget\n",
		},
		{
			name: "no",
			say:  func(o *ui.IO) { o.No("gh is not installed") },
			out:  dim + "│" + reset + "  " + red + "✗" + reset + " gh is not installed\n",
		},
		{
			name: "opt",
			say:  func(o *ui.IO) { o.Opt("no operator config") },
			out:  dim + "│" + reset + "  " + dim + "○" + reset + " no operator config\n",
		},
		{
			name: "next",
			say:  func(o *ui.IO) { o.Next("run it again") },
			out:  dim + "│" + reset + "  " + blue + "→" + reset + " run it again\n",
		},
		{
			name: "say",
			say:  func(o *ui.IO) { o.Say("CrossRev 0.5.0") },
			out:  "  CrossRev 0.5.0\n",
		},
		{
			name: "warn goes to stderr",
			say:  func(o *ui.IO) { o.Warn("the label could not be applied", "the pass is not findable by label") },
			err:  "\n" + yellow + "⚠  the label could not be applied" + reset + "\n   the pass is not findable by label\n\n",
		},
		{
			name: "die goes to stderr",
			say: func(o *ui.IO) {
				o.Die("could not read acme/widget#42", "Check that the pull request exists and that gh is authenticated.")
			},
			err: "\n" + red + "error" + reset + "  could not read acme/widget#42\n       Check that the pull request exists and that gh is authenticated.\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r recorder
			tt.say(r.io(ui.Colour()))
			if got := r.out.String(); got != tt.out {
				t.Errorf("stdout = %q, want %q", got, tt.out)
			}
			if got := r.err.String(); got != tt.err {
				t.Errorf("stderr = %q, want %q", got, tt.err)
			}
		})
	}
}

// TestPlainOutput is the same helpers with the palette off, which is what a
// pipe and a CI runner get. Every escape code goes and nothing else moves.
func TestPlainOutput(t *testing.T) {
	tests := []struct {
		name string
		say  func(*ui.IO)
		out  string
		err  string
	}{
		{"section", func(o *ui.IO) { o.Section("Reviewing") }, "\n◇  Reviewing\n", ""},
		{"section state", func(o *ui.IO) { o.SectionState("#3", "converged", ui.StateOK, "(retried once)") }, "\n◇  #3 — converged (retried once)\n", ""},
		{"head", func(o *ui.IO) { o.Head("NEXT") }, "│  NEXT\n", ""},
		{"row", func(o *ui.IO) { o.Row("2 ", ui.StepOK, "review") }, "│  2 ✓ review\n", ""},
		{"cmd", func(o *ui.IO) { o.Cmd("crossrev review --pr 42") }, "│  crossrev review --pr 42\n", ""},
		{"line", func(o *ui.IO) { o.Line("2 findings") }, "│  2 findings\n", ""},
		{"gap", func(o *ui.IO) { o.Gap() }, "│\n", ""},
		{"end", func(o *ui.IO) { o.End("done") }, "└  done\n\n", ""},
		{"ok", func(o *ui.IO) { o.OK("created") }, "│  ✓ created\n", ""},
		{"no", func(o *ui.IO) { o.No("absent") }, "│  ✗ absent\n", ""},
		{"opt", func(o *ui.IO) { o.Opt("absent") }, "│  ○ absent\n", ""},
		{"next", func(o *ui.IO) { o.Next("run it") }, "│  → run it\n", ""},
		{"say", func(o *ui.IO) { o.Say("hello") }, "  hello\n", ""},
		{"warn", func(o *ui.IO) { o.Warn("condition", "consequence") }, "", "\n⚠  condition\n   consequence\n\n"},
		{"die", func(o *ui.IO) { o.Die("wrong", "next action") }, "", "\nerror  wrong\n       next action\n\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r recorder
			tt.say(r.io(ui.Plain()))
			if got := r.out.String(); got != tt.out {
				t.Errorf("stdout = %q, want %q", got, tt.out)
			}
			if got := r.err.String(); got != tt.err {
				t.Errorf("stderr = %q, want %q", got, tt.err)
			}
			if strings.Contains(r.out.String()+r.err.String(), "\033") {
				t.Error("the plain palette emitted an escape code")
			}
		})
	}
}

// TestZeroIODiscards: a caller that has not wired the streams must not panic.
// Every path here runs from a command that is already reporting something else.
func TestZeroIODiscards(t *testing.T) {
	var o ui.IO
	o.Section("nothing is attached")
	o.Warn("nor here", "still nothing")
	if err := o.Die("reason", "action"); err == nil {
		t.Error("Die returned no error")
	}
}

// TestNilIOTolerates is the tolerance the package doc promises, measured.
//
// The zero IO above is a value; this is the pointer, and the two are different
// receivers. Every helper reads the palette through a nil check, Confirm reads
// AssumeYes through another, and a command that gives up before wiring its
// output holds nil rather than a zero value. Nothing here may panic, because
// each of these runs from a path that is already reporting a failure.
func TestNilIOTolerates(t *testing.T) {
	var o *ui.IO

	o.Section("a section")
	o.SectionState("#3", "converged", ui.StateOK, "(retried once)")
	o.Head("a heading")
	o.Row("3 ", ui.StepOK, "review")
	o.Cmd("crossrev review --pr 42")
	o.Line("a line")
	o.Gap()
	o.End("done")
	o.OK("ok")
	o.No("no")
	o.Opt("opt")
	o.Next("next")
	o.Say("say")
	o.Warn("a condition", "what it costs")
	if err := o.Die("reason", "action"); ui.Reason(err) != "reason" {
		t.Errorf("Die on a nil IO returned %v, want a fatal error carrying the reason", err)
	}

	// Confirm reads AssumeYes before it reads anything else, and there is no
	// struct to read it from.
	confirmed, err := o.Confirm("Create 5 labels on acme/widget?")
	if confirmed {
		t.Error("Confirm said yes on a nil IO")
	}
	if ui.Reason(err) == "" {
		t.Errorf("Confirm err = %v, want the no-terminal refusal", err)
	}
	if value, err := o.Prompt("Which repository?"); value != "" || ui.Reason(err) == "" {
		t.Errorf("Prompt = %q, %v; want nothing and the no-terminal refusal", value, err)
	}
}

// TestIOWithNoInputRefuses: an IO that is wired for output and not for answers
// is the third arm of _ui_input_source, and it refuses rather than panicking on
// the interface it was never given.
func TestIOWithNoInputRefuses(t *testing.T) {
	var r recorder
	o := r.io(ui.Plain())
	o.Input = nil

	confirmed, err := o.Confirm("Create 5 labels on acme/widget?")
	if confirmed {
		t.Error("Confirm said yes with no Input")
	}
	want := "CrossRev needs to ask you something, but no terminal is attached"
	if ui.Reason(err) != want {
		t.Errorf("die reason = %q, want %q", ui.Reason(err), want)
	}
	if r.out.String() != "" {
		t.Errorf("stdout = %q, want nothing asked", r.out.String())
	}
}
