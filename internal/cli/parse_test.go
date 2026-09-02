package cli

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// TestParseRuleMatrix walks every row of testdata/matrix.json: one command line
// in, one parsed request or one refusal out.
//
// The rows are the shell's parse rule (bin/crossrev:111-190) and every argument
// loop it dispatches to (lib/run.sh:913, :1730, :2895, :3034, :3666,
// lib/init.sh:34, lib/auth.sh:511, :792, :879, :998).
//
// Most rows were measured with `NO_COLOR=1 bash bin/crossrev …` before they
// were written down, and testdata/help/refusals.json holds the bytes. Two
// groups were not, both by a recorded ruling that doc.go's "Where this diverges
// from the shell, and why" lists:
//
//   - `review --pr abc`, `review --repo garbage` and `init --repo garbage` have
//     no shell refusal to measure. Bash keeps the value as a string and lets a
//     later `gh` call fail on it; these request types hold an int and a
//     core.Slug, so the parser refuses at the flag. Their refusals.json rows
//     carry a native block and a null shell one.
//   - `init --owner`, `init --repo ""` and every `auth` flag using `${2:?…}`
//     were measured, but the reason and action here are the port's own words.
//     The shell prints bash's `lib/auth.sh: line N: 2: …` frame, which names a
//     library file and a line number to an operator who has neither.
//
// A third divergence was removed rather than recorded: `watchdog --timeout abc`
// parses, because the shell does not read the value until it compares against
// it (lib/run.sh:3671, :3719).
func TestParseRuleMatrix(t *testing.T) {
	file := loadMatrix(t)
	for _, row := range file.Rows {
		t.Run(row.Name, func(t *testing.T) {
			io, out, errs := captureIO()
			got, err := Parse(row.Args, io, file.Harnesses)

			switch {
			case row.Silent:
				if !errors.Is(err, errNoValue) {
					t.Fatalf("Parse(%q) = %v, want the silent stop", row.Args, err)
				}
				if out.Len() != 0 || errs.Len() != 0 {
					t.Errorf("the silent stop printed %q / %q, and the shell prints nothing", out, errs)
				}

			case row.Reason != "":
				var fatal *ui.FatalError
				if !errors.As(err, &fatal) {
					t.Fatalf("Parse(%q) = %#v, want a refusal", row.Args, err)
				}
				if fatal.Reason != row.Reason {
					t.Errorf("reason = %q, want %q", fatal.Reason, row.Reason)
				}
				if fatal.Action != row.Action {
					t.Errorf("action = %q, want %q", fatal.Action, row.Action)
				}
				// ui_die prints both lines before it exits, so a refusal
				// that returned the right value and printed nothing would
				// still be wrong.
				printed := errs.String()
				if !strings.Contains(printed, row.Reason) || !strings.Contains(printed, row.Action) {
					t.Errorf("stderr = %q, want it to carry both lines", printed)
				}

			default:
				if err != nil {
					t.Fatalf("Parse(%q) = %v, want the %s command", row.Args, err, row.Command)
				}
				if string(got.Command) != row.Command {
					t.Errorf("command = %q, want %q", got.Command, row.Command)
				}
				fields := fieldStrings(t, got.Request)
				if !reflect.DeepEqual(fields, row.Request) {
					t.Errorf("request = %#v, want %#v", fields, row.Request)
				}
				if out.Len() != 0 || errs.Len() != 0 {
					t.Errorf("a parse that succeeded printed %q / %q", out, errs)
				}
			}
		})
	}
}

// TestParseRuleCoversEveryCommand fails when a command reachable from
// bin/crossrev has no row at all. A matrix that quietly stopped covering
// `auth rotate` would otherwise still pass.
func TestParseRuleCoversEveryCommand(t *testing.T) {
	file := loadMatrix(t)
	seen := make(map[string]bool)
	for _, row := range file.Rows {
		if row.Command != "" {
			seen[row.Command] = true
		}
	}
	for _, name := range AllCommands() {
		if !seen[string(name)] {
			t.Errorf("no matrix row parses to %q", name)
		}
	}
}

// TestParseRuleDashArgumentKeepsItsOwnOptions pins the one line of the parse
// rule a comment can get wrong: the default cycle does not shift, because the
// options belong to the cycle (bin/crossrev:135-139).
func TestParseRuleDashArgumentKeepsItsOwnOptions(t *testing.T) {
	io, _, _ := captureIO()
	got, err := Parse([]string{"--pr", "42", "--no-tips"}, io, nil)
	if err != nil {
		t.Fatalf("Parse = %v", err)
	}
	if got.Command != CommandCycle {
		t.Fatalf("command = %q, want cycle", got.Command)
	}
	req, ok := got.Request.(CycleRequest)
	if !ok {
		t.Fatalf("request = %T, want CycleRequest", got.Request)
	}
	if req.PR != 42 || !req.NoTips {
		t.Errorf("request = %#v, want the leading options read as the cycle's own", req)
	}
}

// TestParseRuleHelpAndVersionNeedNothingInstalled pins the special cases ahead
// of the rule (bin/crossrev:122-125): they are the two dash-arguments nobody
// means as a cycle, and they answer with no harness list to render from.
func TestParseRuleHelpAndVersionNeedNothingInstalled(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want Command
	}{
		{nil, CommandHelp},
		{[]string{""}, CommandHelp},
		{[]string{"help"}, CommandHelp},
		{[]string{"--help"}, CommandHelp},
		{[]string{"-h"}, CommandHelp},
		{[]string{"version"}, CommandVersion},
		{[]string{"--version"}, CommandVersion},
		{[]string{"-v"}, CommandVersion},
	} {
		t.Run(fmt.Sprintf("%q", tc.args), func(t *testing.T) {
			io, _, _ := captureIO()
			got, err := Parse(tc.args, io, nil)
			if err != nil {
				t.Fatalf("Parse = %v", err)
			}
			if got.Command != tc.want {
				t.Errorf("command = %q, want %q", got.Command, tc.want)
			}
		})
	}
}
