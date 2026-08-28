package ui_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/ui"
)

func TestAssumeYes(t *testing.T) {
	for _, value := range []string{"", "0", "true", "yes", "y"} {
		if ui.AssumeYes(value) {
			t.Errorf("AssumeYes(%q) = true, want false", value)
		}
	}
	if !ui.AssumeYes("1") {
		t.Error(`AssumeYes("1") = false, want true`)
	}
}

// TestConfirmAnswers pins what counts as a yes. Everything else, an empty line
// included, is a no — a stray newline must not approve an outward-facing
// action (lib/ui.sh:169).
func TestConfirmAnswers(t *testing.T) {
	tests := []struct {
		answer string
		want   bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"Yes\n", true},
		{"  y  \n", true},
		{"\ty\t\n", true},
		{"\n", false},
		{"n\n", false},
		{"no\n", false},
		{"yeah\n", false},
		{"yy\n", false},
		{"y y\n", false},
		{"y\r\n", false},
		// Bash's read reports a source that ends without a newline as a failed
		// read, so this is a no however much it looks like a yes.
		{"y", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.answer, "\n", "\\n"), func(t *testing.T) {
			var r recorder
			o := r.io(ui.Plain())
			o.Input = &answers{text: tt.answer}

			got, err := o.Confirm("Create 5 labels on acme/widget?")
			if err != nil {
				t.Fatalf("Confirm: %v", err)
			}
			if got != tt.want {
				t.Errorf("Confirm(%q) = %t, want %t", tt.answer, got, tt.want)
			}
			if want := "◆  Create 5 labels on acme/widget?  [y/N] "; r.out.String() != want {
				t.Errorf("stdout = %q, want %q", r.out.String(), want)
			}
		})
	}
}

// TestConfirmWithAssumeYes: --yes answers without asking, and says so, so the
// transcript still records that something outward-facing happened.
func TestConfirmWithAssumeYes(t *testing.T) {
	var r recorder
	o := r.io(ui.Plain())
	o.AssumeYes = true
	source := &answers{text: "n\n"}
	o.Input = source

	got, err := o.Confirm("Create 5 labels on acme/widget?")
	if err != nil || !got {
		t.Fatalf("Confirm = %t, %v; want true, nil", got, err)
	}
	if want := "◆  Create 5 labels on acme/widget?  yes (--yes)\n"; r.out.String() != want {
		t.Errorf("stdout = %q, want %q", r.out.String(), want)
	}
	if source.opens != 0 {
		t.Errorf("the input was opened %d times with --yes set", source.opens)
	}
}

// TestPromptReadsOneValue: the question goes to stderr so the value can be
// captured from stdout, and only one line is taken.
func TestPromptReadsOneValue(t *testing.T) {
	var r recorder
	o := r.io(ui.Plain())
	o.Input = &answers{text: "acme/widget\nanother line\n"}

	got, err := o.Prompt("Which repository?")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got != "acme/widget" {
		t.Errorf("Prompt = %q, want %q", got, "acme/widget")
	}
	if r.out.String() != "" {
		t.Errorf("stdout = %q, want nothing — the value is what stdout is for", r.out.String())
	}
	if want := "◆  Which repository? › "; r.err.String() != want {
		t.Errorf("stderr = %q, want %q", r.err.String(), want)
	}
}

// TestPromptWithAnUnfinishedAnswer: a source that ends without a newline is a
// failed read, and the value is not handed back half-given.
func TestPromptWithAnUnfinishedAnswer(t *testing.T) {
	var r recorder
	o := r.io(ui.Plain())
	o.Input = &answers{text: "acme/widget"}

	got, err := o.Prompt("Which repository?")
	if got != "" {
		t.Errorf("Prompt = %q, want nothing", got)
	}
	if err == nil {
		t.Error("Prompt returned no error for an unfinished answer")
	}
}

// TestEachQuestionOpensTheSourceAgain, which is what the per-read redirection
// in lib/ui.sh does.
func TestEachQuestionOpensTheSourceAgain(t *testing.T) {
	var r recorder
	o := r.io(ui.Plain())
	source := &answers{text: "y\n"}
	o.Input = source

	for i := 0; i < 3; i++ {
		if _, err := o.Confirm("Again?"); err != nil {
			t.Fatalf("Confirm: %v", err)
		}
	}
	if source.opens != 3 {
		t.Errorf("the input was opened %d times, want 3", source.opens)
	}
}
