package ui_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// TestDieReturnsTheReasonRatherThanExiting is the whole shape decision: the
// Bash function ends the process, and this one hands the reason back so the
// caller's deferred cleanup runs and one place decides the exit status.
func TestDieReturnsTheReasonRatherThanExiting(t *testing.T) {
	var r recorder
	err := r.io(ui.Plain()).Die("could not read acme/widget#42", "Check that gh is authenticated.")

	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("Die returned %T, want a *ui.FatalError", err)
	}
	if fatal.Reason != "could not read acme/widget#42" {
		t.Errorf("Reason = %q", fatal.Reason)
	}
	if fatal.Action != "Check that gh is authenticated." {
		t.Errorf("Action = %q", fatal.Action)
	}
	if err.Error() != fatal.Reason {
		t.Errorf("Error() = %q, want the reason", err.Error())
	}
}

// TestReasonSurvivesWrapping: the reason is what a pull request marker carries
// when a leg dies, and it has to be readable however far up the stack it was
// wrapped on the way (lib/run.sh:103, :145).
func TestReasonSurvivesWrapping(t *testing.T) {
	var r recorder
	err := r.io(ui.Plain()).Die("the harness produced no findings", "Run it again with --keep-transcripts.")
	wrapped := fmt.Errorf("resolve leg: %w", fmt.Errorf("attempt 2: %w", err))

	if got, want := ui.Reason(wrapped), "the harness produced no findings"; got != want {
		t.Errorf("Reason(wrapped) = %q, want %q", got, want)
	}
}

// TestReasonOfSomethingElse: a run that ended for a reason nobody recorded
// reads as none, which is what lets lib/run.sh:145 substitute its own text.
func TestReasonOfSomethingElse(t *testing.T) {
	if got := ui.Reason(errors.New("exit status 1")); got != "" {
		t.Errorf("Reason = %q, want empty", got)
	}
	if got := ui.Reason(nil); got != "" {
		t.Errorf("Reason(nil) = %q, want empty", got)
	}
}

// A fatal minted under a Kind matches it under errors.Is, so a caller
// degrades on one failure and propagates another; the message stays the
// reason alone, and a fatal minted before the field matches nothing.
func TestFatalErrorMatchesItsKind(t *testing.T) {
	kind := errors.New("the harness failed instead of answering")
	fatal := &ui.FatalError{Reason: "the claude harness failed: gone", Kind: kind}
	if !errors.Is(fatal, kind) {
		t.Error("the fatal does not match the kind it was minted under")
	}
	if errors.Is(fatal, errors.New("another sentinel")) {
		t.Error("the fatal matches a sentinel it was not minted under")
	}
	if fatal.Error() != "the claude harness failed: gone" {
		t.Errorf("Error() = %q, want the reason alone", fatal.Error())
	}
	plain := &ui.FatalError{Reason: "the review leg ran a command"}
	if errors.Is(plain, kind) {
		t.Error("a fatal with no kind matches one")
	}
}
