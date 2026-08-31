package ui

import (
	"fmt"
	"regexp"
)

// yes is what counts as one. Anything else, including an empty line, is a no.
var yes = regexp.MustCompile(`^[Yy]([Ee][Ss])?$`)

// AssumeYes reads the raw value of CROSSREV_ASSUME_YES, which --yes sets. Only
// "1" answers every question; unset and anything else ask (lib/ui.sh:145).
func AssumeYes(value string) bool { return value == "1" }

// Confirm asks before an outward-facing action — rule 6. The caller explains
// first; this only collects the answer (ui_confirm, lib/ui.sh:144-154).
//
// Defaults to no, so a stray newline cannot approve something, and so does a
// source that ends before the reader answers.
//
// The source is resolved before the question is printed, which is the order the
// Bash function uses and the one that matters: with nowhere to read from,
// nothing is asked and the refusal is the only thing printed.
func (o *IO) Confirm(question string) (bool, error) {
	p := o.palette()
	if o != nil && o.AssumeYes {
		fmt.Fprintf(o.out(), "%s◆  %s%s  yes (--yes)\n", p.Bold, question, p.Reset)
		return true, nil
	}
	source, err := o.open()
	if err != nil {
		return false, o.noInput()
	}
	defer source.Close()

	fmt.Fprintf(o.out(), "%s◆  %s%s  [y/N] ", p.Bold, question, p.Reset)
	answer, err := readAnswer(source)
	if err != nil {
		return false, nil
	}
	return yes.MatchString(answer), nil
}

// Prompt reads one value. The question goes to stderr so the value can be
// captured from stdout (ui_prompt, lib/ui.sh:157-163).
func (o *IO) Prompt(question string) (string, error) {
	source, err := o.open()
	if err != nil {
		return "", o.noInput()
	}
	defer source.Close()

	p := o.palette()
	fmt.Fprintf(o.err(), "%s◆  %s%s › ", p.Bold, question, p.Reset)
	answer, err := readAnswer(source)
	if err != nil {
		return "", err
	}
	return answer, nil
}
