package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// TestExitCodesAreOnlyEverThree pins the whole range. The shell exits 0, 1 or
// 130 and nothing else: `ui_die` ends the process with 1 (lib/ui.sh:113-119),
// `run_checkpoint` ends it with 130 on an interrupt (lib/run.sh:178), and every
// other return is a success.
func TestExitCodesAreOnlyEverThree(t *testing.T) {
	file := loadMatrix(t)
	for _, row := range file.Rows {
		t.Run(row.Name, func(t *testing.T) {
			cmds, _ := recordingCommands(t, 0, nil)
			io, _, _ := captureIO()
			code := Run(context.Background(), row.Args, cmds, io, file.Harnesses)
			want := ExitOK
			if row.Reason != "" || row.Silent {
				want = ExitFailure
			}
			if code != want {
				t.Errorf("Run(%q) = %d, want %d", row.Args, code, want)
			}
		})
	}
}

// TestExitCodesFromACommand pins what Run makes of what a command answers.
func TestExitCodesFromACommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		err  error
		want int
	}{
		{"a command that succeeded", 0, nil, ExitOK},
		{"an interrupt", 0, ErrInterrupted, ExitInterrupted},
		{"an interrupt reported as a code", ExitInterrupted, nil, ExitInterrupted},
		{"an interrupt wrapped on the way up", 0, errors.Join(errors.New("resolve"), ErrInterrupted), ExitInterrupted},
		{"a cancelled context", 0, context.Canceled, ExitInterrupted},
		{"a refusal", 0, &ui.FatalError{Reason: "no", Action: "do this"}, ExitFailure},
		{"a plain error", 0, errors.New("boom"), ExitFailure},
		{"a code the shell never produces", 7, nil, ExitFailure},
		{"a negative code", -1, nil, ExitFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmds, seen := recordingCommands(t, tc.code, tc.err)
			io, _, _ := captureIO()
			got := Run(context.Background(), []string{"status", "--pr", "3"}, cmds, io, nil)
			if len(*seen) != 1 {
				t.Fatalf("Run made %d calls, want one", len(*seen))
			}
			if got != tc.want {
				t.Errorf("Run = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestExitCodesSilentStopSaysNothing pins the one refusal that prints nothing.
//
// The shell's argument loops reach `shift 2` with one argument left. `shift`
// fails, `set -euo pipefail` ends the process, and the reader gets an empty
// terminal and status 1 (bin/crossrev:12, lib/run.sh:923). It is a defect of
// the shell rather than a design, and it is reproduced because it is
// observable.
func TestExitCodesSilentStopSaysNothing(t *testing.T) {
	cmds, seen := recordingCommands(t, 0, nil)
	io, out, errs := captureIO()
	code := Run(context.Background(), []string{"review", "--pr"}, cmds, io, nil)
	if code != ExitFailure {
		t.Errorf("Run = %d, want %d", code, ExitFailure)
	}
	if out.Len() != 0 || errs.Len() != 0 {
		t.Errorf("printed %q / %q, want nothing at all", out, errs)
	}
	if len(*seen) != 0 {
		t.Errorf("a command ran after a refusal: %v", *seen)
	}
}

// TestExitCodesRefusalPrintsBeforeItReturns keeps Run from swallowing the two
// lines ui_die prints, which is the only thing that tells the reader why.
func TestExitCodesRefusalPrintsBeforeItReturns(t *testing.T) {
	cmds, seen := recordingCommands(t, 0, nil)
	io, _, errs := captureIO()
	code := Run(context.Background(), []string{"reviw"}, cmds, io, nil)
	if code != ExitFailure {
		t.Errorf("Run = %d, want %d", code, ExitFailure)
	}
	if got := errs.String(); got == "" {
		t.Error("Run refused the command and printed nothing")
	}
	if len(*seen) != 0 {
		t.Errorf("a command ran after a refusal: %v", *seen)
	}
}
