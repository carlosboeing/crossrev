package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// failingCommands fills every field of the table with a function that fails the
// test if it is ever called. A refusal must be printed by the parser, and help
// and version must not reach any other command, so nothing here may run.
func failingCommands(t *testing.T) Commands {
	t.Helper()
	var cmds Commands
	v := reflect.ValueOf(&cmds).Elem()
	for i := range v.NumField() {
		name := v.Type().Field(i).Name
		field := v.Field(i)
		field.Set(reflect.MakeFunc(field.Type(), func([]reflect.Value) []reflect.Value {
			t.Errorf("Commands.%s ran, and nothing in this test may reach a command", name)
			return []reflect.Value{reflect.ValueOf(ExitFailure), reflect.Zero(field.Type().Out(1))}
		}))
	}
	return cmds
}

// golden reads one of the files under testdata/help, each captured with
// `NO_COLOR=1 bash bin/crossrev …` in a checkout.
func golden(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "help", name))
	if err != nil {
		t.Fatalf("reading the golden: %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("testdata/help/%s is empty, so it proves nothing", name)
	}
	return string(raw)
}

// descriptorNames is the harness list the help block renders from: the compiled
// descriptor, which is lib/harnesses.json (internal/harness/assets.go).
func descriptorNames(t *testing.T) []string {
	t.Helper()
	document, err := harness.Descriptors()
	if err != nil {
		t.Fatalf("reading the compiled descriptor: %v", err)
	}
	names := document.Names()
	if len(names) == 0 {
		t.Fatal("the descriptor names no harness, so the help block would prove nothing")
	}
	return names
}

// TestHelpIsTheShellsUsageBlock pins `crossrev help` byte for byte.
//
// The names inside `--harness <one of: …>` are read from the compiled
// descriptor rather than written into the test, so a harness added to
// lib/harnesses.json fails this golden instead of quietly leaving the help
// behind (bin/crossrev:68-72, lib/harnesses.sh:125-129).
func TestHelpIsTheShellsUsageBlock(t *testing.T) {
	io, out, errs := captureIO()
	code, err := Help(io, descriptorNames(t))
	if err != nil {
		t.Fatalf("Help = %v", err)
	}
	if code != ExitOK {
		t.Errorf("exit = %d, want %d", code, ExitOK)
	}
	if got, want := out.String(), golden(t, "help.txt"); got != want {
		t.Errorf("stdout =\n%q\nwant\n%q", got, want)
	}
	if errs.Len() != 0 {
		t.Errorf("stderr = %q, and the shell prints the block on stdout", errs)
	}
}

// TestHelpWithoutAHarnessListPrintsTheFlagsShape pins the other arm of the same
// `if`: the shell prints `--harness <harness>` when jq is missing and the names
// cannot be read (bin/crossrev:68-72). Measured with a PATH holding no jq.
func TestHelpWithoutAHarnessListPrintsTheFlagsShape(t *testing.T) {
	io, out, _ := captureIO()
	if _, err := Help(io, nil); err != nil {
		t.Fatalf("Help = %v", err)
	}
	if got, want := out.String(), golden(t, "help-without-a-harness-list.txt"); got != want {
		t.Errorf("stdout =\n%q\nwant\n%q", got, want)
	}
}

// TestHelpNamesEveryDispatchableCommand fails when the help block stops naming
// a command bin/crossrev can dispatch to. `help` is left out because the shell's
// own COMMANDS list leaves it out.
func TestHelpNamesEveryDispatchableCommand(t *testing.T) {
	io, out, _ := captureIO()
	if _, err := Help(io, descriptorNames(t)); err != nil {
		t.Fatalf("Help = %v", err)
	}
	block := out.String()
	for _, name := range AllCommands() {
		if name == CommandHelp {
			continue
		}
		// `config show|backlog` and `config backlog` are one line in the
		// block, so the sub-command is looked for beside its parent rather
		// than as one string.
		want := strings.ReplaceAll(string(name), "config backlog", "config show|backlog")
		if !strings.Contains(block, want) {
			t.Errorf("the help block does not name %q", name)
		}
	}
}

// TestVersionIsTheVersionFileWithItsWhitespaceRemoved pins `crossrev version`
// against the VERSION file this checkout carries.
//
// The shell is `tr -d '[:space:]' <"$ROOT/VERSION"` followed by `echo`
// (bin/crossrev:64, :124): every whitespace byte is deleted, and one newline is
// added back.
func TestVersionIsTheVersionFileWithItsWhitespaceRemoved(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatalf("reading VERSION: %v", err)
	}
	io, out, errs := captureIO()
	code, err := Version(io, string(raw))
	if err != nil {
		t.Fatalf("Version = %v", err)
	}
	if code != ExitOK {
		t.Errorf("exit = %d, want %d", code, ExitOK)
	}
	if got, want := out.String(), golden(t, "version.txt"); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if errs.Len() != 0 {
		t.Errorf("stderr = %q, and the shell prints the version on stdout", errs)
	}
}

// TestVersionDeletesEveryWhitespaceByte pins the `tr -d` rather than a trim: it
// removes whitespace from the middle of the file too, which a TrimSpace would
// leave in place.
func TestVersionDeletesEveryWhitespaceByte(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"0.5.0\n", "0.5.0\n"},
		{"0.5.0", "0.5.0\n"},
		{"  0.5.0  \n\n", "0.5.0\n"},
		{"\t0.5.0\r\n", "0.5.0\n"},
		{"0. 5 .0\n", "0.5.0\n"},
		{"1.2.3-rc.1\n", "1.2.3-rc.1\n"},
	} {
		t.Run(fmt.Sprintf("%q", tc.raw), func(t *testing.T) {
			io, out, _ := captureIO()
			if _, err := Version(io, tc.raw); err != nil {
				t.Fatalf("Version = %v", err)
			}
			if got := out.String(); got != tc.want {
				t.Errorf("stdout = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestVersionRefusesABuildThatCarriesNoVersion pins what happens when there is
// no version to print. The shell cannot reach `echo` at all — `tr` fails to
// open the file and `set -e` ends the process at status 1 with Bash's own
// message, which names an absolute path. This says the same thing in the
// tool's voice at the same status.
func TestVersionRefusesABuildThatCarriesNoVersion(t *testing.T) {
	for _, raw := range []string{"", "\n", "   \t\n"} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			io, out, errs := captureIO()
			code, err := Version(io, raw)
			if code != ExitFailure {
				t.Errorf("exit = %d, want %d", code, ExitFailure)
			}
			var fatal *ui.FatalError
			if !errors.As(err, &fatal) {
				t.Fatalf("Version = %#v, want a refusal", err)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, and a build with no version prints none", out)
			}
			if errs.Len() == 0 {
				t.Error("the refusal printed nothing")
			}
		})
	}
}

// TestHelpAndVersionAnswerWithNothingInstalled runs the eight spellings through
// Run with an empty PATH.
//
// The shell special-cases both ahead of everything it sources, so they answer
// on a machine with no gh, no jq and no harness (bin/crossrev:122-132). Here
// the harness names come off the compiled descriptor rather than off PATH, so
// an empty PATH changes nothing — which is the property being pinned. Every
// other field of the table fails the test if it is reached.
func TestHelpAndVersionAnswerWithNothingInstalled(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatalf("reading VERSION: %v", err)
	}
	names := descriptorNames(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, golden(t, "help.txt")},
		{[]string{""}, golden(t, "help.txt")},
		{[]string{"help"}, golden(t, "help.txt")},
		{[]string{"--help"}, golden(t, "help.txt")},
		{[]string{"-h"}, golden(t, "help.txt")},
		{[]string{"version"}, golden(t, "version.txt")},
		{[]string{"--version"}, golden(t, "version.txt")},
		{[]string{"-v"}, golden(t, "version.txt")},
	} {
		t.Run(fmt.Sprintf("%q", tc.args), func(t *testing.T) {
			t.Setenv("PATH", "")
			io, out, errs := captureIO()
			cmds := failingCommands(t)
			cmds.Help = func(context.Context, HelpRequest) (int, error) { return Help(io, names) }
			cmds.Version = func(context.Context, VersionRequest) (int, error) { return Version(io, string(raw)) }

			if code := Run(context.Background(), tc.args, cmds, io, names); code != ExitOK {
				t.Fatalf("Run = %d, want %d", code, ExitOK)
			}
			if got := out.String(); got != tc.want {
				t.Errorf("stdout = %q, want %q", got, tc.want)
			}
			if errs.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", errs)
			}
		})
	}
}

// TestHelpAndVersionStartNoProcess reads this package's own sources.
//
// Neither command may reach a child process, which is why the shell answers
// both before it sources an adapter. There is no runner to hand a fake to,
// because the package holds none: internal/exec is the only route from Go to a
// child (internal/exec/runner.go), and nothing here imports it or os/exec. This
// fails if that changes, which is the point — a fake runner would only prove
// the path the test happened to take.
func TestHelpAndVersionStartNoProcess(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package: %v", err)
	}
	banned := map[string]bool{
		`"os/exec"`: true,
		`"github.com/carlosboeing/crossrev/internal/exec"`: true,
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		checked++
		for _, imported := range file.Imports {
			if banned[imported.Path.Value] {
				t.Errorf("%s imports %s, and no command in this package may start a process", name, imported.Path.Value)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no source file was read, so nothing was checked")
	}
}

// refusalRow is one measured command line: what the shell printed, and what
// this package prints where it cannot reproduce that byte for byte.
type refusalRow struct {
	Name   string   `json:"name"`
	Args   []string `json:"args"`
	Shell  *stream  `json:"shell"`
	Native *stream  `json:"native"`
	Note   string   `json:"note"`
}

type stream struct {
	Exit   int    `json:"exit"`
	Stderr string `json:"stderr"`
}

func loadRefusals(t *testing.T) []refusalRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "help", "refusals.json"))
	if err != nil {
		t.Fatalf("reading the refusals: %v", err)
	}
	var file struct {
		Rows []refusalRow `json:"rows"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parsing the refusals: %v", err)
	}
	if len(file.Rows) == 0 {
		t.Fatal("the refusal file is empty, so it proves nothing")
	}
	return file.Rows
}

// TestRefusalsArePrintedTheShellsWay walks every measured refusal through Run
// and compares the bytes on stderr and the process status.
//
// Each row was captured with `NO_COLOR=1 bash bin/crossrev …`. A row carrying a
// native block is one this package cannot reproduce and says why; every other
// row must match the shell exactly.
func TestRefusalsArePrintedTheShellsWay(t *testing.T) {
	names := descriptorNames(t)
	for _, row := range loadRefusals(t) {
		t.Run(row.Name, func(t *testing.T) {
			want := row.Shell
			if row.Native != nil {
				if row.Note == "" {
					t.Fatal("a row that diverges from the shell must say why")
				}
				want = row.Native
			}
			if want == nil {
				t.Fatal("a row must carry the shell's bytes or the native ones")
			}

			io, out, errs := captureIO()
			code := Run(context.Background(), row.Args, failingCommands(t), io, names)

			if code != want.Exit {
				t.Errorf("exit = %d, want %d", code, want.Exit)
			}
			if got := errs.String(); got != want.Stderr {
				t.Errorf("stderr = %q, want %q", got, want.Stderr)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, and a refusal prints nothing there", out)
			}
		})
	}
}

// TestRefusalsCoverEveryRefusedMatrixRow keeps the two files together: a
// refusal added to matrix.json with no measured bytes beside it would otherwise
// be asserted only as a reason and an action, never as what a reader sees.
func TestRefusalsCoverEveryRefusedMatrixRow(t *testing.T) {
	measured := make(map[string]bool)
	for _, row := range loadRefusals(t) {
		measured[strings.Join(row.Args, "\x00")] = true
	}
	for _, row := range loadMatrix(t).Rows {
		if row.Request != nil {
			continue
		}
		if !measured[strings.Join(row.Args, "\x00")] {
			t.Errorf("matrix row %q has no measured bytes in refusals.json", row.Name)
		}
	}
}

// TestTheCompositionRootHasBothHalvesOfHelpAndVersion pins what cmd/crossrev
// builds the table out of: the harness names come from the descriptor rather
// than a literal, and the version comes off the embedded file rather than
// nothing.
//
// The table itself is built in cmd/crossrev, which this package may not import
// — it is the composition root and holds every tier-3 package. What can be
// pinned from here is that both halves answer.
func TestTheCompositionRootHasBothHalvesOfHelpAndVersion(t *testing.T) {
	io, out, _ := captureIO()

	document, err := harness.Descriptors()
	if err != nil {
		t.Fatalf("harness.Descriptors: %v", err)
	}
	names := document.Names()
	if got, want := strings.Join(names, "|"), strings.Join(descriptorNames(t), "|"); got != want {
		t.Errorf("harnesses = %q, want the descriptor's %q", got, want)
	}
	if InstalledVersion() == "" {
		t.Error("InstalledVersion answers nothing, so `crossrev version` refuses in every build")
	}

	if _, err := Help(io, names); err != nil {
		t.Fatalf("Help = %v", err)
	}
	if got, want := out.String(), golden(t, "help.txt"); got != want {
		t.Errorf("stdout =\n%q\nwant\n%q", got, want)
	}
}
