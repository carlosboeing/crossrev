package archtest_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Starting a child process is internal/exec's job in production source. The
// process-start AST walk confines os/exec, os.StartProcess and the named
// syscall start helpers to that package. It does not cover syscall.Syscall.
//
// That package decides what environment a child receives (Spec.Env, set on
// every path so os/exec cannot inherit this process's own), whether a forge
// credential may travel with it (the runner instance), how its output is
// bounded, and what happens on cancellation. Every one of those properties is
// bypassed by four lines of os/exec somewhere else — a review built exactly that in
// internal/harness, and the child received the whole parent environment with
// the suite still green.
//
// # The routes, and the sweep that produced them
//
// The os/exec import is the obvious one. os.StartProcess is not: it lives in os
// rather than os/exec, so an import rule naming one package cannot see it, and
// a zero ProcAttr inherits everything. The syscall entries are the same fact one
// layer down.
//
// The syscall list is a sweep of the package's exported surface rather than the
// three names a review happened to think of — `go doc syscall` on darwin, linux,
// plan9, windows and js/wasm, filtered for anything that could start a child:
//
//	Exec, ForkExec, StartProcess     unix and plan9; ForkExec is what
//	                                 os.StartProcess itself calls
//	CreateProcess, CreateProcessAsUser
//	                                 windows only, and named anyway: this tree
//	                                 does not build there today, and a rule that
//	                                 waits for the port is a rule that is missing
//	                                 when the port lands
//
// What the same sweep found and left out: FindProcess, OpenProcess, Wait4,
// WaitProcess, TerminateProcess, ExitProcess, PtraceAttach and Process32First
// all act on a process that already exists. CloseOnExec sets a descriptor flag.
// os.Executable answers this process's own path.
//
// syscall.Syscall and its numbered siblings are deliberately absent, and that
// is a real gap rather than an oversight: SYS_EXECVE through Syscall6 starts a
// child and no name-based rule can tell it from an ioctl. internal/ui/terminal_unix.go:15
// makes exactly that call for TIOCGETA, so forbidding the family would forbid
// the terminal read as well. The gap is bounded by there being one such call in
// the tree and by it not naming a process constant.
//
// Confining the os/exec import also closes (*exec.Cmd).Environ(), which falls
// back to syscall.Environ whenever Cmd.Env is nil. That one is worth naming
// because no selector rule could ever reach it: the receiver is a variable, so
// there is no package identifier to match on. It is closed here only because a
// caller that cannot name the type cannot hold the value.
//
// # plugin.Open, which is here and is not this rule
//
// It starts no child, so folding it into a rule called "only internal/exec may
// start a process" would make the name say something the check does not — the
// fault this file was reviewed for elsewhere. It gets its own entry, with its
// own sentence, because the danger is real and worse in kind: a loaded plugin's
// init functions run inside the process that holds every credential, with this
// process's whole environment and no Spec between them. Nothing may load one,
// so it has no permitted directory at all.
//
// # unsafe, which is here and is also not this rule
//
// It starts no child either, and it is here for the same reason plugin is: the
// walk is already reading every production file's import list, and the danger
// is of a kind no other rule in this tree can see.
//
// OSRunner.orchestrator is unexported, so the only ordinary route to the
// forge-credential opt-out is NewOrchestratorRunner. The value is one bool, so
//
//	*(*bool)(unsafe.Pointer(&r)) = true
//
// still writes it, naming neither the field nor the constructor, and no rule
// that reads names can see that. Confining the import is what is left. A name
// scan catches accidents; code review catches a hostile commit; Run's refusal
// is the guard that executes.
//
// One directory needs it and keeps it. internal/ui/terminal_unix.go:16 passes
// unsafe.Pointer(&settings) to the SYS_IOCTL that asks the terminal driver for
// a file's line settings — the isatty every shell's `-t` is — and there is no
// Go without it. internal/ui is safe to grant because the tier DAG
// (dependencies_test.go) gives it no edge to internal/exec at all, so it cannot
// name the type it would be forging.
//
// What this does not reach: cgo, which gets at the same memory with no Go
// symbol to match on, and a go:linkname, which needs unsafe imported but is a
// comment rather than a selector. Both are refused elsewhere —
// environment_test.go rejects `import "C"` outright and reads linkname
// directives — and neither is closed by this entry.
//
// # What this does not cover, and why
//
// internal/testgen — see productionSource, which carries the reasoning.
//
// Test files are covered for the child-start rules and not for plugin and
// unsafe. For unsafe that exclusion is load-bearing rather than incidental:
// internal/ui/pty_linux_test.go and pty_darwin_test.go both build a
// pseudo-terminal through unsafe.Pointer, and a test file compiles into no
// released binary. For the child-start rules the exclusion was a hole: the
// whole-environment rule in environment_test.go already scans tests, and it
// caught cmd/crossrev/wiring_test.go's os.Environ, while an external test
// package next to it could import os/exec and pass. processTestPermitted closes
// that, by naming the files rather than the directories.
type processRule struct {
	// importPath, when set, forbids importing the package at all.
	importPath string
	// defaultName and symbol, when set, forbid one function in that package.
	defaultName string
	symbol      string
	// permittedDir is the one repo-relative directory allowed to hold it, or
	// empty when no directory may.
	permittedDir string
	// action opens the violation sentence, and is empty for the child-process
	// rules that share the default.
	action string
	// consequence completes the violation sentence after the semicolon.
	consequence string
	// provesTheScan marks the import whose presence in permittedDir shows the
	// scan reached the package it is supposed to be reading.
	provesTheScan bool
	// child marks a rule that forbids starting a child process. Those apply to
	// test files as well; plugin and unsafe do not.
	child bool
}

const processPermittedDir = "internal/exec"

// unsafePermittedDir holds the one production call that needs unsafe.Pointer.
const unsafePermittedDir = "internal/ui"

const startsAChild = "only " + processPermittedDir + " may, and every other caller goes through its Runner"

var processRules = []processRule{
	{importPath: "os/exec", defaultName: "exec", permittedDir: processPermittedDir, consequence: startsAChild, provesTheScan: true, child: true},
	{importPath: "os", defaultName: "os", symbol: "StartProcess", permittedDir: processPermittedDir, consequence: startsAChild, child: true},
	{importPath: "syscall", defaultName: "syscall", symbol: "StartProcess", permittedDir: processPermittedDir, consequence: startsAChild, child: true},
	{importPath: "syscall", defaultName: "syscall", symbol: "Exec", permittedDir: processPermittedDir, consequence: startsAChild, child: true},
	{importPath: "syscall", defaultName: "syscall", symbol: "ForkExec", permittedDir: processPermittedDir, consequence: startsAChild, child: true},
	{importPath: "syscall", defaultName: "syscall", symbol: "CreateProcess", permittedDir: processPermittedDir, consequence: startsAChild, child: true},
	{importPath: "syscall", defaultName: "syscall", symbol: "CreateProcessAsUser", permittedDir: processPermittedDir, consequence: startsAChild, child: true},
	{importPath: "plugin", defaultName: "plugin",
		action:      "loads native code into this process through",
		consequence: "no directory may, because a plugin's init functions run inside the process that holds every credential"},
	{importPath: "unsafe", defaultName: "unsafe", permittedDir: unsafePermittedDir,
		action:      "can forge a value the type system protects, through",
		consequence: "only " + unsafePermittedDir + " may, for the ioctl that asks whether a file is a terminal"},
}

// processTestPermitted is every test file allowed to start a child process, by
// path and not by directory.
//
// By path because a directory allowance would be no rule at all here: five test
// files in internal/harness run the Bash oracle, so allowing that directory
// would let a sixth do anything it liked, and the same for cmd/crossrev, where
// wiring_test.go builds and runs the binary and nothing else there may.
//
// The list was measured rather than guessed: `grep -rl '"os/exec"' --include
// '*_test.go'` over the module, minus the three files that only carry the
// string inside a fixture. Three reasons cover all of it — the package under
// test, the one test that runs the built binary, and the parity tests that run
// the Bash oracle or a helper child of this test binary and compare.
//
// An entry nothing needs is a rule that has quietly stopped meaning anything,
// so TestProcessStartBoundary fails on a name here that starts no child.
var processTestPermitted = map[string]string{
	"internal/exec/helper_unix_test.go": "the package under test; it re-runs this test binary as its own child helper",

	"cmd/crossrev/wiring_test.go": "builds the binary with `go build` and runs it, which is the only end-to-end check of the composition root",

	"internal/config/merge_test.go":              "runs this test binary as a helper child for the config merge oracle",
	"internal/prompt/shell_test.go":              "runs bash and git to compare the prompt against the shell that wrote it",
	"internal/review/helper_test.go":             "runs bash for the review leg's oracle",
	"internal/forge/ghexec/stub_test.go":         "exec.LookPath, to skip when the stub's tools are not installed",
	"internal/harness/alternatives_test.go":      "runs bash for the descriptor oracle",
	"internal/harness/argv_test.go":              "runs bash for the argv oracle",
	"internal/harness/descriptor_parity_test.go": "runs bash for the descriptor oracle",
	"internal/harness/descriptor_test.go":        "exec.LookPath, to skip when jq is not installed",
	"internal/harness/errors_test.go":            "runs bash for the adapter-error oracle",
	"internal/harness/invocation_test.go":        "runs bash for the invocation oracle",
	"internal/vcs/lock_test.go":                  "runs sh and sleep to hold a lock from another process",
	"internal/vcs/push_streams_test.go":          "runs bash for the push oracle",
	"internal/vcs/tail_shell_test.go":            "runs bash for the tail oracle",
}

// verb opens the violation sentence for one rule.
func (r processRule) verb() string {
	if r.action != "" {
		return r.action
	}
	return "starts a child process through"
}

func TestProcessStartBoundary(t *testing.T) {
	root := findRepoRoot(t)

	permittedSeen := false
	sawChild := map[string]bool{}
	walkBoundarySources(t, root, func(relSlash string, src []byte) {
		violations, permitted, err := auditProcessSource(relSlash, src)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", relSlash, err)
		}
		for _, violation := range violations {
			t.Error(violation)
		}
		if permitted {
			permittedSeen = true
		}
		if startsAChildSomewhere(t, relSlash, src) {
			sawChild[relSlash] = true
		}
	})

	// internal/exec is supposed to be the package that starts processes. If it
	// holds no os/exec import, the scan is not reading what it thinks it is.
	if !permittedSeen {
		t.Errorf("expected a production file in %s to import os/exec; the scan is not looking where it thinks it is",
			processPermittedDir)
	}

	// An allowance nothing needs is what lets the list only ever grow.
	for path, why := range processTestPermitted {
		if !sawChild[path] {
			t.Errorf("processTestPermitted names %s (%q) and it starts no child there; drop the entry", path, why)
		}
	}
}

// startsAChildSomewhere answers whether a file holds any child-start reference
// at all, which is what makes an allowance for it load-bearing.
func startsAChildSomewhere(t *testing.T, relSlash string, src []byte) bool {
	t.Helper()
	node, err := parser.ParseFile(token.NewFileSet(), relSlash, src, 0)
	if err != nil {
		t.Fatalf("failed to parse %s: %v", relSlash, err)
	}
	for _, reference := range processReferences(node) {
		if reference.child {
			return true
		}
	}
	return false
}

// auditProcessSource is the whole verdict for one file: the scope decision, the
// parse, the routes and the permitted directory.
func auditProcessSource(relSlash string, src []byte) (violations []string, permitted bool, err error) {
	testFile := strings.HasSuffix(relSlash, "_test.go")
	// internal/testgen is out whether the file is a test or not, for the reason
	// productionSource gives: nothing imports the generator.
	if strings.HasPrefix(relSlash, "internal/testgen/") {
		return nil, false, nil
	}
	if !testFile && !productionSource(relSlash) {
		return nil, false, nil
	}

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, relSlash, src, 0)
	if err != nil {
		return nil, false, err
	}

	// Exact equality on the directory, never a prefix or a suffix.
	// "vendored/internal/exec" ends with the permitted name and is not it, and
	// "internal/execute" starts with it and is not it either.
	dir := filepath.ToSlash(filepath.Dir(relSlash))
	_, testAllowed := processTestPermitted[relSlash]

	for _, reference := range processReferences(node) {
		if testFile {
			// plugin and unsafe keep the test exclusion; the child-start
			// rules do not.
			if !reference.child || testAllowed {
				continue
			}
			violations = append(violations, fmt.Sprintf("%s:%d %s; %s",
				relSlash, fset.Position(reference.pos).Line, reference.what,
				"a test file may start a child only where processTestPermitted names it and says why"))
			continue
		}
		if dir == reference.permittedDir {
			permitted = permitted || reference.grantsPermission
			continue
		}
		violations = append(violations, fmt.Sprintf("%s:%d %s; %s",
			relSlash, fset.Position(reference.pos).Line, reference.what, reference.consequence))
	}
	return violations, permitted, nil
}

type processReference struct {
	pos          token.Pos
	what         string
	consequence  string
	permittedDir string
	// child marks a reference to a route that starts a child process, which is
	// the half of these rules test files are in scope for.
	child bool
	// grantsPermission marks the import that proves the scan reached the
	// permitted package, so the rule cannot pass vacuously.
	grantsPermission bool
}

func processReferences(node *ast.File) []processReference {
	var found []processReference

	for _, rule := range processRules {
		if rule.symbol == "" {
			for _, imported := range node.Imports {
				path, err := strconv.Unquote(imported.Path.Value)
				if err != nil || path != rule.importPath {
					continue
				}
				found = append(found, processReference{
					pos:              imported.Pos(),
					what:             rule.verb() + " an import of " + rule.importPath,
					consequence:      rule.consequence,
					permittedDir:     rule.permittedDir,
					grantsPermission: rule.provesTheScan,
					child:            rule.child,
				})
			}
			continue
		}

		localNames, dotImported := importLocalNames(node, rule.importPath, rule.defaultName)
		if len(localNames) == 0 && !dotImported {
			continue
		}
		qualified := rule.defaultName + "." + rule.symbol

		ast.Inspect(node, func(n ast.Node) bool {
			switch expression := n.(type) {
			case *ast.SelectorExpr:
				if ident, ok := expression.X.(*ast.Ident); ok &&
					localNames[ident.Name] && expression.Sel.Name == rule.symbol {
					found = append(found, processReference{
						pos:          expression.Pos(),
						what:         rule.verb() + " " + qualified,
						consequence:  rule.consequence,
						permittedDir: rule.permittedDir,
						child:        rule.child,
					})
					return false
				}
			case *ast.Ident:
				if dotImported && expression.Name == rule.symbol {
					found = append(found, processReference{
						pos:          expression.Pos(),
						what:         rule.verb() + " " + qualified + " under a dot import",
						consequence:  rule.consequence,
						permittedDir: rule.permittedDir,
						child:        rule.child,
					})
				}
			}
			return true
		})
	}
	return found
}

func TestProcessAuditVerdict(t *testing.T) {
	tests := []struct {
		name          string
		file          string
		source        string
		wantViolation bool
		wantPermitted bool
		mustMention   string
	}{
		{
			name:          "the permitted package imports os/exec",
			file:          "internal/exec/osrunner.go",
			source:        "package exec\nimport osexec \"os/exec\"\nvar v = osexec.Command\n",
			wantPermitted: true,
		},
		{
			name:          "any other package imports it",
			file:          "internal/harness/harness.go",
			source:        "package harness\nimport \"os/exec\"\nvar v = exec.Command\n",
			wantViolation: true,
			mustMention:   "an import of os/exec",
		},
		{
			// The route an import rule naming os/exec cannot see.
			name:          "os.StartProcess",
			file:          "internal/harness/harness.go",
			source:        "package harness\nimport \"os\"\nvar v, _ = os.StartProcess(\"/bin/sh\", nil, &os.ProcAttr{})\n",
			wantViolation: true,
			mustMention:   "os.StartProcess",
		},
		{
			name:          "syscall.StartProcess",
			file:          "internal/harness/harness.go",
			source:        "package harness\nimport \"syscall\"\nvar v, _, _ = syscall.StartProcess(\"/bin/sh\", nil, nil)\n",
			wantViolation: true,
			mustMention:   "syscall.StartProcess",
		},
		{
			name:          "syscall.Exec",
			file:          "internal/harness/harness.go",
			source:        "package harness\nimport \"syscall\"\nvar v = syscall.Exec\n",
			wantViolation: true,
			mustMention:   "syscall.Exec",
		},
		{
			// os.StartProcess calls this, so a caller can skip a layer and
			// reach the same child.
			name:          "syscall.ForkExec",
			file:          "internal/harness/harness.go",
			source:        "package harness\nimport \"syscall\"\nvar v, _ = syscall.ForkExec(\"/bin/sh\", nil, nil)\n",
			wantViolation: true,
			mustMention:   "syscall.ForkExec",
		},
		{
			// Windows only. This tree does not build there, and the rule is
			// written for the port rather than after it.
			name:          "syscall.CreateProcess",
			file:          "internal/harness/harness.go",
			source:        "package harness\nimport \"syscall\"\nvar v = syscall.CreateProcess\n",
			wantViolation: true,
			mustMention:   "syscall.CreateProcess",
		},
		{
			name:          "syscall.CreateProcessAsUser",
			file:          "internal/harness/harness.go",
			source:        "package harness\nimport \"syscall\"\nvar v = syscall.CreateProcessAsUser\n",
			wantViolation: true,
			mustMention:   "syscall.CreateProcessAsUser",
		},
		{
			// A different rule sharing the walk: no child, and no permitted
			// directory either.
			name:          "an import of plugin",
			file:          "internal/harness/harness.go",
			source:        "package harness\nimport \"plugin\"\nvar v = plugin.Open\n",
			wantViolation: true,
			mustMention:   "loads native code into this process",
		},
		{
			name:          "an import of plugin inside the permitted directory",
			file:          "internal/exec/osrunner.go",
			source:        "package exec\nimport \"plugin\"\nvar v = plugin.Open\n",
			wantViolation: true,
			mustMention:   "no directory may",
		},
		{
			// The third rule sharing the walk: no child either, and one
			// permitted directory rather than none.
			name:          "an import of unsafe",
			file:          "internal/cycle/leg.go",
			source:        "package cycle\nimport \"unsafe\"\nvar v unsafe.Pointer\n",
			wantViolation: true,
			mustMention:   "can forge a value the type system protects",
		},
		{
			name:   "an import of unsafe in the directory that needs it",
			file:   "internal/ui/terminal_unix.go",
			source: "package ui\nimport \"unsafe\"\nvar v unsafe.Pointer\n",
		},
		{
			// The permission is the directory, not the package that starts
			// processes: internal/exec has no business forging one.
			name:          "an import of unsafe in the process-start directory",
			file:          "internal/exec/osrunner.go",
			source:        "package exec\nimport \"unsafe\"\nvar v unsafe.Pointer\n",
			wantViolation: true,
			mustMention:   "only internal/ui may",
		},
		{
			// A sibling in the same package that acts on a process which
			// already exists is not a start.
			name:   "syscall.Wait4",
			file:   "internal/harness/harness.go",
			source: "package harness\nimport \"syscall\"\nvar v = syscall.Wait4\n",
		},
		{
			name:          "an aliased os/exec import",
			file:          "internal/harness/harness.go",
			source:        "package harness\nimport sub \"os/exec\"\nvar v = sub.Command\n",
			wantViolation: true,
		},
		{
			name:          "a blank os/exec import",
			file:          "internal/harness/harness.go",
			source:        "package harness\nimport _ \"os/exec\"\n",
			wantViolation: true,
		},
		{
			// A directory that ends with the permitted name is not it.
			name:          "a directory whose path merely ends with the permitted one",
			file:          "vendored/internal/exec/runner.go",
			source:        "package exec\nimport \"os/exec\"\nvar v = exec.Command\n",
			wantViolation: true,
		},
		{
			// And one that starts with it is not it either.
			name:          "a directory whose path merely starts with the permitted one",
			file:          "internal/execute/runner.go",
			source:        "package execute\nimport \"os/exec\"\nvar v = exec.Command\n",
			wantViolation: true,
		},
		{
			// A subdirectory of the permitted one is a different package.
			name:          "a subpackage of the permitted directory",
			file:          "internal/exec/inner/runner.go",
			source:        "package inner\nimport \"os/exec\"\nvar v = exec.Command\n",
			wantViolation: true,
		},
		{
			// Not "a test file anywhere" any more: this one is named in
			// processTestPermitted, because it runs bash to compare against.
			name:   "a test file the allowance names",
			file:   "internal/prompt/shell_test.go",
			source: "package prompt\nimport \"os/exec\"\nvar v = exec.Command\n",
		},
		{
			// The hole this rule was extended to close. An external test
			// package sits in the same directory as five files that may run
			// the oracle, and a directory allowance would let it through.
			name:          "an external test package in a directory whose other tests may",
			file:          "internal/harness/scratch_test.go",
			source:        "package harness_test\nimport \"os/exec\"\nvar x = exec.Command\n",
			wantViolation: true,
			mustMention:   "processTestPermitted names it",
		},
		{
			// And the same beside the one test that builds and runs the
			// binary. wiring_test.go may; nothing else in cmd/crossrev does.
			name:          "an external test package beside the wiring test",
			file:          "cmd/crossrev/scratch_test.go",
			source:        "package main_test\nimport \"os/exec\"\nvar x = exec.Command\n",
			wantViolation: true,
			mustMention:   "processTestPermitted names it",
		},
		{
			// os.StartProcess is the same rule, so a test file reaches it too.
			name:          "a test file starting a child through os.StartProcess",
			file:          "internal/cycle/scratch_test.go",
			source:        "package cycle\nimport \"os\"\nvar v, _ = os.StartProcess(\"/bin/sh\", nil, &os.ProcAttr{})\n",
			wantViolation: true,
			mustMention:   "os.StartProcess",
		},
		{
			// unsafe is not that rule: the pseudo-terminal tests need it, and
			// a test file compiles into no released binary.
			name:   "a test file importing unsafe",
			file:   "internal/cycle/scratch_test.go",
			source: "package cycle\nimport \"unsafe\"\nvar v unsafe.Pointer\n",
		},
		{
			name:   "a test file for the parity generator",
			file:   "internal/testgen/policy/main_test.go",
			source: "package main\nimport \"os/exec\"\nvar v = exec.Command\n",
		},
		{
			name:   "the parity generator",
			file:   "internal/testgen/policy/main.go",
			source: "package main\nimport \"os/exec\"\nvar v = exec.Command\n",
		},
		{
			name:   "a package that starts nothing",
			file:   "internal/policy/guards.go",
			source: "package policy\nimport \"strings\"\nvar v = strings.TrimSpace\n",
		},
		{
			// os.Getenv is not confined, and this rule must not confine it by
			// accident: only StartProcess is named in the os package.
			name:   "another function in the os package",
			file:   "internal/config/load.go",
			source: "package config\nimport \"os\"\nvar v = os.Getenv(\"HOME\")\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations, permitted, err := auditProcessSource(tt.file, []byte(tt.source))
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}

			if got := len(violations) > 0; got != tt.wantViolation {
				t.Fatalf("violations = %v, want a violation: %t", violations, tt.wantViolation)
			}
			if permitted != tt.wantPermitted {
				t.Errorf("permitted = %t, want %t", permitted, tt.wantPermitted)
			}
			if tt.mustMention == "" {
				return
			}
			if !strings.Contains(violations[0], tt.mustMention) {
				t.Errorf("violation %q does not mention %q", violations[0], tt.mustMention)
			}
			if !strings.HasPrefix(violations[0], tt.file+":") {
				t.Errorf("violation %q does not name the file and line", violations[0])
			}
		})
	}
}
