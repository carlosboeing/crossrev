package ui_test

import (
	"bytes"
	"testing"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// The three helpers a leg's lines are printed through, byte for byte.
//
// Measured against the shell in this checkout:
//
//	$ NO_COLOR=1 bash -c 'source lib/ui.sh; ui_say "S"; ui_ok "O"; ui_warn "C" "Q"'
//	stdout: "  S\n"  "│  ✓ O\n"
//	stderr: "\n⚠  C\n   Q\n\n"
//
// The split matters as much as the bytes: a warning goes to stderr, so a caller
// capturing a leg's stdout gets the report without it, and a reader still sees
// why. Printing every line the same way lost that.
func TestPrintRendersEachKindTheWayItsHelperDoes(t *testing.T) {
	for _, row := range []struct {
		name       string
		line       ui.Line
		wantStdout string
		wantStderr string
	}{
		{
			name:       "ui_say",
			line:       ui.Say("S"),
			wantStdout: "  S\n",
		},
		{
			name:       "ui_ok",
			line:       ui.OK("O"),
			wantStdout: "│  ✓ O\n",
		},
		{
			name:       "ui_warn",
			line:       ui.Warn("C", "Q"),
			wantStderr: "\n⚠  C\n   Q\n\n",
		},
		{
			// The legs print several `printf '\n'` blank lines of their own —
			// above the run header (lib/run.sh:1066) and below the closing
			// report (lib/run.sh:2434). ui_gap is NOT this line: it prints the
			// dim gutter rule, which is a section's spacing and not a blank.
			name:       "a bare printf newline",
			line:       ui.Blank(),
			wantStdout: "\n",
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			var out, err bytes.Buffer
			io := &ui.IO{Out: &out, Err: &err, Palette: ui.Plain()}

			io.Print(row.line)

			if out.String() != row.wantStdout {
				t.Errorf("stdout = %q, want %q", out.String(), row.wantStdout)
			}
			if err.String() != row.wantStderr {
				t.Errorf("stderr = %q, want %q", err.String(), row.wantStderr)
			}
		})
	}
}

// The zero Line is a ui_say of nothing, not a panic. Several sites answer one
// only when there is something to say and leave it zero otherwise, and the
// check they make is on Text.
func TestPrintTheZeroLineSaysNothingInTheSayShape(t *testing.T) {
	var out, err bytes.Buffer
	io := &ui.IO{Out: &out, Err: &err, Palette: ui.Plain()}

	io.Print(ui.Line{})

	if out.String() != "  \n" {
		t.Errorf("stdout = %q, want the empty ui_say line", out.String())
	}
	if err.String() != "" {
		t.Errorf("stderr = %q, want nothing", err.String())
	}
}

// PrintAll keeps the order a leg reported in, and keeps each line's own stream.
func TestPrintAllKeepsOrderAndStream(t *testing.T) {
	var out, err bytes.Buffer
	io := &ui.IO{Out: &out, Err: &err, Palette: ui.Plain()}

	io.PrintAll([]ui.Line{ui.Say("one"), ui.Warn("two", "because"), ui.OK("three")})

	if want := "  one\n│  ✓ three\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if want := "\n⚠  two\n   because\n\n"; err.String() != want {
		t.Errorf("stderr = %q, want %q", err.String(), want)
	}
}

// String is the shape ui.Warning built before Line existed, so an assertion
// about what a leg reported reads the same either way.
func TestStringJoinsAWarningsTwoHalves(t *testing.T) {
	if got, want := ui.Warn("C", "Q").String(), ui.Warning("C", "Q"); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if got := ui.Say("plain").String(); got != "plain" {
		t.Errorf("String() = %q, want the text alone", got)
	}
	if got := ui.Joined([]ui.Line{ui.Say("a"), ui.OK("b")}); got != "a\nb" {
		t.Errorf("Joined = %q", got)
	}
}

// A blank line contributes nothing to the joined report, so an assertion about
// what a leg said reads the same whether the run carries one or not.
func TestBlankRendersAsNoTextWhenJoined(t *testing.T) {
	if got := ui.Joined([]ui.Line{ui.Say("a"), ui.Blank(), ui.Say("b")}); got != "a\n\nb" {
		t.Errorf("Joined = %q, want the blank between them", got)
	}
	if got := ui.Blank().String(); got != "" {
		t.Errorf("String() = %q, want the empty string", got)
	}
}
