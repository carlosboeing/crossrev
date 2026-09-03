package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/cli"
	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/runlog"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// ---------------------------------------------------------------------------
// The base revision
// ---------------------------------------------------------------------------

// scriptedGit answers one `git cat-file` pair and records every call.
//
// It is the whole of the seam deps.show needs: vcs.New takes an exec.Runner, so
// a repository can be built over an answer table rather than over a checkout,
// and what the caller asked git for is then readable.
type scriptedGit struct {
	// blob is what `cat-file blob <spec>` answers, keyed on the spec.
	blob map[string]string
	// calls is every argument vector, in order.
	calls [][]string
}

func (g *scriptedGit) Run(_ context.Context, spec exec.Spec) exec.Result {
	g.calls = append(g.calls, slices.Clone(spec.Args))
	if len(spec.Args) != 3 || spec.Args[0] != "cat-file" {
		return exec.Result{ExitCode: 1}
	}
	body, known := g.blob[spec.Args[2]]
	if !known {
		return exec.Result{ExitCode: 128, Stderr: []byte("fatal: path does not exist\n")}
	}
	if spec.Args[1] == "-t" {
		return exec.Result{Stdout: []byte("blob\n")}
	}
	return exec.Result{Stdout: []byte(body)}
}

// Policy is read at the revision it was asked for, and never from the working
// tree (ADR 0003, and `git show "$rev:$path"` at lib/config.sh:95).
//
// deps.show is the one function that decides this for every configuration read
// in the binary. Handing vcs.Repository.Show a zero revision is not a smaller
// version of the same read: vcs/show.go:66-68 routes a zero revision to the
// working tree, so a pull request would be reviewed under the policy its own
// head proposes rather than the policy its base already carries.
func TestPolicyIsReadAtTheRevisionRatherThanFromTheWorkingTree(t *testing.T) {
	const path = ".github/crossrev.yml"
	const atTheBase = "mode: local\n"
	const inTheWorkingTree = "mode: automated\n"

	// The working tree says one thing on disk...
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, path), []byte(inTheWorkingTree), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Chdir(dir)

	// ...and the revision says another.
	base, err := core.NewRevision(strings.Repeat("ab", 20))
	if err != nil {
		t.Fatalf("NewRevision: %v", err)
	}
	git := &scriptedGit{blob: map[string]string{base.SHA() + ":" + path: atTheBase}}
	d := &deps{repo: vcs.New(git, nil).At("")}

	body, status, err := d.show()(context.Background(), base, path)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if got := string(body); got != atTheBase {
		t.Errorf("deps.show read %q, want the base revision's %q — a zero revision reads the working tree (ADR 0003)",
			got, atTheBase)
	}
	if want := config.FileStatus(vcs.IsFile); status != want {
		t.Errorf("status = %d, want %d: the path is a regular blob at that revision", status, want)
	}
	if len(git.calls) == 0 {
		t.Fatal("deps.show asked git for nothing, so it read the working tree (ADR 0003)")
	}
	want := base.SHA() + ":" + path
	for _, call := range git.calls {
		if call[len(call)-1] != want {
			t.Errorf("git %v does not name the base revision %q (ADR 0003)", call, want)
		}
	}
}

// ---------------------------------------------------------------------------
// The command table
// ---------------------------------------------------------------------------

// commandTable is one row per field of cli.Commands: what the field is called,
// and one string only that field's own handler prints or returns.
//
// Two mutations are what the markers are for. A field wired to another field's
// handler answers the wrong marker — the ConfigRequest pair is the only place
// in the table where two fields share a signature, so it is the only place such
// a swap compiles. A field stubbed to `return 0, nil` prints nothing and
// answers the wrong status.
//
// Cycle, Review, Resolve and Status share the refusal a command with no
// repository makes. Each takes its own request type, so no one of them can be
// wired to another's handler and still build; the marker is there to prove the
// closure reaches a real handler rather than a stub.
func commandTable(t *testing.T) []struct {
	field      string
	wantStatus int
	marker     string
} {
	t.Helper()
	return []struct {
		field      string
		wantStatus int
		marker     string
	}{
		{"Cycle", cli.ExitFailure, "Run crossrev from a checkout with a GitHub remote"},
		{"Review", cli.ExitFailure, "Run crossrev from a checkout with a GitHub remote"},
		{"Resolve", cli.ExitFailure, "Run crossrev from a checkout with a GitHub remote"},
		{"Status", cli.ExitFailure, "Run crossrev from a checkout with a GitHub remote"},
		{"Init", cli.ExitFailure, `a repository slug must be owner/name`},
		{"Watchdog", cli.ExitFailure, "could not work out which repository to watch"},
		{"ConfigShow", cli.ExitOK, `"min_fix_severity": "medium"`},
		{"ConfigBacklog", cli.ExitOK, "deferred work would go to:"},
		{"AuthStatus", cli.ExitOK, "none configured"},
		{"AuthLogin", cli.ExitFailure, "no terminal is attached"},
		{"AuthInstall", cli.ExitFailure, "Register one first: crossrev auth login"},
		{"AuthRotate", cli.ExitFailure, "There is nothing to rotate"},
		{"AuthRefresh", cli.ExitFailure, "there is no credential to refresh"},
		{"Doctor", cli.ExitFailure, "Fix what is marked ✗ above, then run this again."},
		{"Help", cli.ExitOK, "crossrev — cross-model PR review loop"},
		{"Version", cli.ExitOK, installedVersion(t)},
	}
}

// Every command in the table is wired, and every one reaches its own handler.
//
// internal/cli refuses a nil field rather than panicking (commands.go:50-53),
// so an unwired command is a message on stderr at the moment somebody runs it
// and nothing at all before then. `auth status` and `auth login` have no
// CLI-driven shell suite behind them either, so dropping either was invisible
// everywhere.
//
// The table is checked against the struct's own fields, so a command added to
// cli.Commands with no row here fails this test rather than joining the set of
// things nothing looks at.
func TestEveryCommandIsWiredAndReachesItsOwnHandler(t *testing.T) {
	table := commandTable(t)
	fields := reflect.TypeFor[cli.Commands]()
	if fields.NumField() != len(table) {
		t.Fatalf("cli.Commands has %d fields and the table has %d rows: a command was added without one",
			fields.NumField(), len(table))
	}
	for at := range fields.NumField() {
		if name := fields.Field(at).Name; name != table[at].field {
			t.Fatalf("cli.Commands field %d is %s and the table row is %s", at, name, table[at].field)
		}
	}

	doc, err := harness.Descriptors()
	if err != nil {
		t.Fatalf("reading the compiled-in descriptor: %v", err)
	}

	// A command answers here rather than reaching a network: the sandbox is a
	// directory that is not a checkout, with the five core tools stubbed so
	// the two config commands get past preflight_require_yq.
	sandboxPATH(t, coreToolStubs())

	answers := map[string]string{}
	for _, row := range table {
		t.Run(row.field, func(t *testing.T) {
			var out, errOut bytes.Buffer
			io := &ui.IO{Out: &out, Err: &errOut, Palette: ui.Plain()}
			table, _ := compose(io, doc)

			field := reflect.ValueOf(table).FieldByName(row.field)
			if field.IsNil() {
				t.Fatalf("compose left cli.Commands.%s nil, so that command refuses as an unwired build",
					row.field)
			}
			request := reflect.New(field.Type().In(1)).Elem()
			returned := field.Call([]reflect.Value{reflect.ValueOf(context.Background()), request})

			said := out.String() + errOut.String()
			if returned[1].IsValid() && !returned[1].IsNil() {
				said += returned[1].Interface().(error).Error()
			}
			answers[row.field] = said

			if status := int(returned[0].Int()); status != row.wantStatus {
				t.Errorf("%s answered status %d, want %d — it does not reach its own handler\n%s",
					row.field, status, row.wantStatus, said)
			}
			if !strings.Contains(said, row.marker) {
				t.Errorf("%s said nothing its own handler says (%q):\n%s", row.field, row.marker, said)
			}
		})
	}

	// The one pair that shares a request type, so the one pair a wrong handler
	// can be wired to and still build.
	if show, backlog := answers["ConfigShow"], answers["ConfigBacklog"]; show != "" && show == backlog {
		t.Errorf("config show and config backlog answered the same thing, so one is wired to the other's handler:\n%s", show)
	}
}

// ---------------------------------------------------------------------------
// The two runners' filter, and --yes
// ---------------------------------------------------------------------------

// An issue title is masked on its way to the forge (log_redact_str,
// lib/log.sh:116).
//
// forge.Publisher's two halves are not interchangeable: Filter may refuse a
// body it could not read, and Mask cannot, which is why a title gets Mask. A
// Mask that returns its argument publishes the credential in the title of an
// issue anybody can read, and the run log's own filtering never sees it.
func TestAnIssueTitleIsMaskedBeforeItIsPublished(t *testing.T) {
	const credential = "ghp_abcdef0123456789"

	got := publisher{}.Mask("the harness echoed " + credential)
	if strings.Contains(got, credential) {
		t.Errorf("Mask published the credential in the clear: %q", got)
	}
	if want := runlog.Redact("the harness echoed " + credential); got != want {
		t.Errorf("Mask = %q, want runlog.Redact's %q", got, want)
	}
}

// CROSSREV_ASSUME_YES answers every confirmation on its own, which is the
// environment half of --yes (lib/ui.sh:145).
//
// The shell reads the variable inside ui_confirm, so an inherited 1 answers
// whether or not the flag was passed. Dropping the read here leaves a run that
// exported it hanging on a question in a place that has no terminal to answer
// from.
func TestTheEnvironmentAnswersEveryConfirmationOnItsOwn(t *testing.T) {
	t.Setenv("CROSSREV_ASSUME_YES", "1")
	if !newIO(false).AssumeYes {
		t.Error("CROSSREV_ASSUME_YES=1 did not answer the confirmations, and lib/ui.sh:145 says it does")
	}

	// The flag alone still does, and neither alone is the default.
	t.Setenv("CROSSREV_ASSUME_YES", "")
	if !newIO(true).AssumeYes {
		t.Error("--yes did not answer the confirmations")
	}
	if newIO(false).AssumeYes {
		t.Error("nothing was set and the confirmations were answered anyway")
	}
}

// ---------------------------------------------------------------------------
// The signals
// ---------------------------------------------------------------------------

// Both signals the shell traps cancel the run's context
// (run_trap_install, lib/run.sh:88: `trap 'CROSSREV_INTERRUPTED=1' INT TERM`).
//
// The context is what every child is started with, and internal/cli turns its
// error into status 130. A signal missing from the registration is a run that
// carries on with the operator's Ctrl-C or the runner's shutdown ignored.
func TestInterruptibleCancelsOnBothSignalsTheShellTraps(t *testing.T) {
	for _, signalled := range []struct {
		name string
		send syscall.Signal
	}{
		{"INT", syscall.SIGINT},
		{"TERM", syscall.SIGTERM},
	} {
		t.Run(signalled.name, func(t *testing.T) {
			// A second registration of our own, so the default action cannot
			// end this process when interruptible registered nothing. It is
			// the reason a missing signal reads as a failed assertion here
			// rather than as a test binary killed mid-run.
			guard := make(chan os.Signal, 1)
			signal.Notify(guard, signalled.send)
			defer signal.Stop(guard)

			ctx, stop := interruptible()
			defer stop()

			self, err := os.FindProcess(os.Getpid())
			if err != nil {
				t.Fatalf("finding this process: %v", err)
			}
			if err := self.Signal(signalled.send); err != nil {
				t.Fatalf("signalling this process: %v", err)
			}
			<-guard

			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Errorf("SIG%s did not cancel the run's context, and lib/run.sh:88 traps INT and TERM",
					signalled.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The call sites with no runtime observable
// ---------------------------------------------------------------------------

// sourceRule is one call in cmd/crossrev/main.go and what it must be written
// with.
type sourceRule struct {
	// name is what the failure calls this rule.
	name string
	// function is the enclosing function in main.go.
	function string
	// call is the callee as it is written in the source.
	call string
	// deferred requires the call to be the target of a defer statement.
	deferred bool
	// args are the arguments the call must name, in order. Nil asks for none
	// in particular.
	args []string
	// why is what goes wrong when the call is written some other way.
	why string
}

// These two call sites decide something real and observe nothing.
//
// **This test reads the source rather than running it, and that is weaker than
// a behavioural test.** It is here because neither call has a runtime
// observable at all, and a rule nothing checks is a rule that drifts:
//
//   - `defer stop()` unregisters the signal channel signal.NotifyContext
//     opened. Go exposes no way to ask whether a signal is registered, and the
//     only difference the omission makes is whether the NEXT signal after run
//     returns finds a handler — so a test asserting the correct behaviour would
//     have to let the default action kill its own test binary.
//   - newIO(false) is the root IO's answer to every confirmation. AssumeYes is
//     read in one place, ui.IO.Confirm, and the root IO reaches one only
//     through `auth login` or `auth rotate`. newIO builds its input as
//     ui.Terminal{Stdin: os.Stdin} with no TTYPath, so Terminal.Open tries
//     /dev/tty first (internal/ui/input.go:53-58, the port of _ui_input_source
//     at lib/ui.sh:129-134). Where a controlling terminal exists the correct
//     code blocks on a read and only the mutant answers, so a behavioural test
//     would hang on the code being right.
//
// If either becomes observable, replace its row with the behavioural test. The
// third row is not in that position: the signals are covered at runtime by
// TestInterruptibleCancelsOnBothSignalsTheShellTraps, which is the better
// proof, and this row is a second, cheaper reading of the same rule.
var sourceRules = []sourceRule{
	{
		name:     "the root IO answers nothing on its own",
		function: "run",
		call:     "newIO",
		args:     []string{"false"},
		why: "the composition root would assume yes for every confirmation, so `auth login` " +
			"and `auth rotate` would open a browser without asking (lib/ui.sh:144-147 asks)",
	},
	{
		name:     "the signal registration is released",
		function: "run",
		call:     "stop",
		deferred: true,
		why:      "the signal registration signal.NotifyContext opened would outlive the run",
	},
	{
		name:     "both signals the shell traps are registered",
		function: "interruptible",
		call:     "signal.NotifyContext",
		args:     []string{"context.Background()", "os.Interrupt", "syscall.SIGTERM"},
		why:      "run_trap_install traps INT and TERM (lib/run.sh:88)",
	},
}

func TestTheCallSitesWithNoRuntimeObservableAreWhatTheySay(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}

	for _, rule := range sourceRules {
		t.Run(rule.name, func(t *testing.T) {
			function := functionNamed(t, parsed, rule.function)
			call, found := callWritten(function, rule.call, rule.deferred)
			if !found {
				written := rule.call + "(…)"
				if rule.deferred {
					written = "defer " + written
				}
				t.Fatalf("%s does not contain `%s`: %s", rule.function, written, rule.why)
			}
			if rule.args == nil {
				return
			}
			var written []string
			for _, argument := range call.Args {
				written = append(written, types.ExprString(argument))
			}
			if !slices.Equal(written, rule.args) {
				t.Errorf("%s calls %s(%s), want %s(%s): %s",
					rule.function, rule.call, strings.Join(written, ", "),
					rule.call, strings.Join(rule.args, ", "), rule.why)
			}
		})
	}
}

// functionNamed is the top-level function declaration of that name.
func functionNamed(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, declaration := range file.Decls {
		function, isFunction := declaration.(*ast.FuncDecl)
		if isFunction && function.Recv == nil && function.Name.Name == name {
			return function
		}
	}
	t.Fatalf("main.go declares no function %s", name)
	return nil
}

// callWritten is the first call to name inside the function, optionally
// requiring that a defer statement is what makes it.
func callWritten(function *ast.FuncDecl, name string, deferred bool) (*ast.CallExpr, bool) {
	var found *ast.CallExpr
	ast.Inspect(function, func(node ast.Node) bool {
		if found != nil {
			return false
		}
		call, isCall := node.(*ast.CallExpr)
		if deferred {
			statement, isDefer := node.(*ast.DeferStmt)
			if !isDefer {
				return true
			}
			call, isCall = statement.Call, true
		}
		if isCall && types.ExprString(call.Fun) == name {
			found = call
			return false
		}
		return true
	})
	return found, found != nil
}

// ---------------------------------------------------------------------------
// Shared fixture
// ---------------------------------------------------------------------------

// installedVersion is the version the binary prints, read from the file
// internal/cli embeds. It is read before the sandbox moves the process.
func installedVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatalf("reading VERSION: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

// stub is one program on the sandbox PATH: a name and the shell that answers
// for it.
type stub struct {
	name string
	body string
}

// coreToolStubs are the five preflight probes (lib/preflight.sh:86), each
// answering a version, with gh also answering the first identity probe.
func coreToolStubs() []stub {
	stubs := []stub{{
		name: "gh",
		body: "#!/bin/sh\ncase \"$*\" in\n" +
			"  '--version') echo 'gh version 2.40.0' ;;\n" +
			"  'api user --jq .login') echo tester ;;\n" +
			"  'repo view'*) echo '{}' ;;\n" +
			"  *) exit 1 ;;\nesac\n",
	}}
	for _, tool := range []string{"git", "jq", "yq", "openssl"} {
		stubs = append(stubs, stub{name: tool, body: "#!/bin/sh\necho '" + tool + " 1.2.3'\n"})
	}
	return stubs
}

// sandboxPATH stands the process in an empty directory that is not a checkout,
// with PATH holding only the stubs named. It returns that directory.
//
// Every path the commands read is redirected with it, so nothing here reads or
// writes the developer's own configuration.
func sandboxPATH(t *testing.T, stubs []stub) string {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	work := filepath.Join(root, "work")
	for _, dir := range []string{bin, work} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for _, program := range stubs {
		if err := os.WriteFile(filepath.Join(bin, program.name), []byte(program.body), 0o755); err != nil { //nolint:gosec // a stub has to be executable
			t.Fatalf("write %s: %v", program.name, err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Chdir(work)
	return work
}
