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
// # Four routes, one rule
//
// The os/exec import is the obvious one. os.StartProcess is not: it lives in os
// rather than os/exec, so an import rule naming one package cannot see it, and
// a zero ProcAttr inherits everything. syscall.StartProcess and syscall.Exec
// are the same fact one layer down.
//
// Confining the os/exec import also closes (*exec.Cmd).Environ(), which falls
// back to syscall.Environ whenever Cmd.Env is nil. That one is worth naming
// because no selector rule could ever reach it: the receiver is a variable, so
// there is no package identifier to match on. It is closed here only because a
// caller that cannot name the type cannot hold the value.
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
	// permittedDir is the one repo-relative directory allowed to hold it.
	permittedDir string
}

const processPermittedDir = "internal/exec"

var processRules = []processRule{
	{importPath: "os/exec", defaultName: "exec", permittedDir: processPermittedDir},
	{importPath: "os", defaultName: "os", symbol: "StartProcess", permittedDir: processPermittedDir},
	{importPath: "syscall", defaultName: "syscall", symbol: "StartProcess", permittedDir: processPermittedDir},
	{importPath: "syscall", defaultName: "syscall", symbol: "Exec", permittedDir: processPermittedDir},
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
		violations = append(violations, fmt.Sprintf(
			"%s:%d starts a child process through %s; only %s may, and every other caller goes through its Runner",
			relSlash, fset.Position(reference.pos).Line, reference.what, reference.permittedDir))
	}
	return violations, permitted, nil
}

type processReference struct {
	pos          token.Pos
	what         string
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
					what:             "an import of " + rule.importPath,
					permittedDir:     rule.permittedDir,
					grantsPermission: true,
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
						what:         qualified,
						permittedDir: rule.permittedDir,
					})
					return false
				}
			case *ast.Ident:
				if dotImported && expression.Name == rule.symbol {
					found = append(found, processReference{
						pos:          expression.Pos(),
						what:         qualified + " under a dot import",
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
