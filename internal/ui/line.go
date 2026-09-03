package ui

import "strings"

// Kind is which of the voice's helpers printed a line.
//
// A leg answers its lines rather than printing them, because the leg packages
// are tier 3 and hold no terminal. The kind travels with the text so the
// composition root can print each line through the helper the shell used at
// that site: measured with NO_COLOR=1, `ui_say "S"` is `  S` on stdout,
// `ui_ok "O"` is `│  ✓ O` on stdout, and `ui_warn "C" "Q"` is a blank line,
// `⚠  C`, `   Q` and another blank line on STDERR (lib/ui.sh:83, :95, :101-104).
//
// Without it every line printed the same way, and a caller reading a leg's
// output could not tell a verified success from a warning — rule 5 of the
// voice, stated at the top of this package, made unenforceable.
type Kind int

const (
	// KindSay is ui_say: a plain informational line outside a section
	// (lib/ui.sh:95). It is the commonest, so it is the zero value.
	KindSay Kind = iota
	// KindOK is ui_ok: a verified success (lib/ui.sh:83). Only for something
	// that is actually true.
	KindOK
	// KindWarn is ui_warn: a condition and what it costs you (lib/ui.sh:101).
	// It goes to stderr, and both halves are required.
	KindWarn
	// KindBlank is a bare `printf '\n'`. Several leg sites print one directly
	// rather than through a helper: above the run header (lib/run.sh:1072) and
	// below the closing report (lib/run.sh:2440, :2438). ui_gap is a different
	// line — it prints the dim gutter rule that spaces a section — so a blank
	// cannot be spelled with it.
	KindBlank
)

// Line is one line a leg reported, with the helper that must print it.
//
// Action is the consequence half of a warning and is empty for every other
// kind. Keeping the pair apart is what lets Warn do its own joining, rather
// than a caller building the bytes and a renderer having to take them apart
// again.
type Line struct {
	Kind   Kind
	Text   string
	Action string
}

// Say, OK and Warn build one line each. They are functions rather than methods
// so a package with no IO — every leg — can answer a Line without holding a
// terminal.
func Say(text string) Line { return Line{Kind: KindSay, Text: text} }

// OK is a verified success. Rule 5: only once the thing is actually true.
func OK(text string) Line { return Line{Kind: KindOK, Text: text} }

// Warn takes both halves because a warning that names a condition without its
// consequence is the thing this project keeps trying not to print (rule 3).
func Warn(condition, consequence string) Line {
	return Line{Kind: KindWarn, Text: condition, Action: consequence}
}

// Blank is the bare newline the legs print between blocks. Its Text is empty
// and is never read.
func Blank() Line { return Line{Kind: KindBlank} }

// SayLines turns a run of plain lines into Lines, for a site that answers
// several at once.
func SayLines(texts ...string) []Line {
	lines := make([]Line, 0, len(texts))
	for _, text := range texts {
		lines = append(lines, Say(text))
	}
	return lines
}

// Print writes one line through the helper its kind names.
func (o *IO) Print(line Line) {
	switch line.Kind {
	case KindOK:
		o.OK(line.Text)
	case KindWarn:
		o.Warn(line.Text, line.Action)
	case KindBlank:
		o.Blank()
	default:
		o.Say(line.Text)
	}
}

// PrintAll writes a leg's whole report, in order.
func (o *IO) PrintAll(lines []Line) {
	for _, line := range lines {
		o.Print(line)
	}
}

// String renders one line as a single string: a warning as the condition, the
// newline and the three-space indent, then the consequence, and anything else
// as its own text.
//
// It is the shape Warning built before Line existed, so an assertion about what
// a leg reported reads the same whether it looks at one line or at the joined
// run.
func (l Line) String() string {
	if l.Kind == KindBlank {
		return ""
	}
	if l.Kind == KindWarn && l.Action != "" {
		return l.Text + "\n   " + l.Action
	}
	return l.Text
}

// Texts renders a run of lines, one string each.
func Texts(lines []Line) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, line.String())
	}
	return out
}

// Joined is every line of a run, newline-separated. It is what a test asserting
// on a leg's whole report reads.
func Joined(lines []Line) string { return strings.Join(Texts(lines), "\n") }
