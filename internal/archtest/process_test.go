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

// Starting a child process is internal/exec's job, and nothing else's.
//
// That package decides what environment a child receives (Spec.Env, set on
// every path so os/exec cannot inherit this process's own), whether a forge
// credential may travel with it (Spec.Audience), how its output is bounded, and
// what happens on cancellation. Every one of those properties is bypassed by
// four lines of os/exec somewhere else — a review built exactly that in
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
// # What this does not cover, and why
//
// Test files and internal/testgen — see productionSource, which carries the
// reasoning and the counter-argument.
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
}

const processPermittedDir = "internal/exec"

const startsAChild = "only " + processPermittedDir + " may, and every other caller goes through its Runner"

var processRules = []processRule{
	{importPath: "os/exec", defaultName: "exec", permittedDir: processPermittedDir, consequence: startsAChild, provesTheScan: true},
	{importPath: "os", defaultName: "os", symbol: "StartProcess", permittedDir: processPermittedDir, consequence: startsAChild},
	{importPath: "syscall", defaultName: "syscall", symbol: "StartProcess", permittedDir: processPermittedDir, consequence: startsAChild},
	{importPath: "syscall", defaultName: "syscall", symbol: "Exec", permittedDir: processPermittedDir, consequence: startsAChild},
	{importPath: "syscall", defaultName: "syscall", symbol: "ForkExec", permittedDir: processPermittedDir, consequence: startsAChild},
	{importPath: "syscall", defaultName: "syscall", symbol: "CreateProcess", permittedDir: processPermittedDir, consequence: startsAChild},
	{importPath: "syscall", defaultName: "syscall", symbol: "CreateProcessAsUser", permittedDir: processPermittedDir, consequence: startsAChild},
	{importPath: "plugin", defaultName: "plugin",
		action:      "loads native code into this process through",
		consequence: "no directory may, because a plugin's init functions run inside the process that holds every credential"},
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
	})

	// internal/exec is supposed to be the package that starts processes. If it
	// holds no os/exec import, the scan is not reading what it thinks it is.
	if !permittedSeen {
		t.Errorf("expected a production file in %s to import os/exec; the scan is not looking where it thinks it is",
			processPermittedDir)
	}
}

// auditProcessSource is the whole verdict for one file: the scope decision, the
// parse, the routes and the permitted directory.
func auditProcessSource(relSlash string, src []byte) (violations []string, permitted bool, err error) {
	if !productionSource(relSlash) {
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

	for _, reference := range processReferences(node) {
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
			name:   "a test file anywhere",
			file:   "internal/prompt/shell_test.go",
			source: "package prompt\nimport \"os/exec\"\nvar v = exec.Command\n",
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
