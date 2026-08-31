package archtest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// excludeBoundaryEntry decides what a source-boundary scan skips.
//
// Directories. Dot-prefixed covers .git, and covers .worktrees, which holds
// other branches' checkouts — not this branch's code to police, and
// scripts/lint.sh prunes the same path for the same reason.
// Underscore-prefixed and testdata are what the go tool leaves out of a
// wildcard pattern like ./... — which is NOT the same as never compiling them.
// A package under testdata is importable and does compile; one was built during
// review that read the environment and this scan did not see it. What stops it
// reaching a binary is TestPackageDependencies, which reports the import as an
// illegal dependency because no tier grants it. That is defence in depth rather
// than the reason, and saying otherwise would overstate what this scan proves.
//
// Files: Go source, test files included. A test that reaches past a boundary is
// a second implementation of whatever the boundary protects.
func excludeBoundaryEntry(path string, isDir bool) bool {
	if isDir {
		name := filepath.Base(path)
		switch {
		case strings.HasPrefix(name, "."), strings.HasPrefix(name, "_"):
			return true
		case name == "testdata", name == "vendor", name == "node_modules":
			return true
		}
		return false
	}
	return !strings.HasSuffix(path, ".go")
}

func importLocalNames(node *ast.File, wantedPath, defaultName string) (map[string]bool, bool) {
	localNames := make(map[string]bool)
	dotImported := false
	for _, imp := range node.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if importPath != wantedPath {
			continue
		}
		if imp.Name == nil {
			localNames[defaultName] = true
		} else if imp.Name.Name == "." {
			dotImported = true
		} else if imp.Name.Name != "_" {
			localNames[imp.Name.Name] = true
		}
	}
	return localNames, dotImported
}

func TestBoundaryScannerIncludesEveryGoFile(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		isDir bool
		want  bool
	}{
		{name: "architecture test directory", path: "internal/archtest", isDir: true, want: false},
		{name: "generator directory", path: "internal/testgen", isDir: true, want: false},
		{name: "a package outside cmd and internal", path: "pkg/anything", isDir: true, want: false},
		{name: "another branch's checkout", path: ".worktrees", isDir: true, want: true},
		{name: "the git directory", path: ".git", isDir: true, want: true},
		{name: "an underscore directory the go tool ignores", path: "_scratch", isDir: true, want: true},
		{name: "test fixtures the go tool ignores", path: "internal/example/testdata", isDir: true, want: true},
		{name: "Go test file", path: "internal/example/example_test.go", want: false},
		{name: "build-tagged Go file", path: "internal/example/example_linux.go", want: false},
		{name: "non-Go file", path: "internal/example/fixture.json", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := excludeBoundaryEntry(tt.path, tt.isDir); got != tt.want {
				t.Fatalf("excludeBoundaryEntry() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestImportLocalNamesUnquotesImportPaths(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "raw string", source: "package sample\nimport platform `os`\n"},
		{name: "escaped string", source: "package sample\nimport platform \"o\\x73\"\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, err := parser.ParseFile(token.NewFileSet(), "sample.go", tt.source, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			localNames, dotImported := importLocalNames(node, "os", "os")
			if dotImported || !localNames["platform"] || len(localNames) != 1 {
				t.Fatalf("import names = %v, dot imported = %t; want platform alias", localNames, dotImported)
			}
		})
	}
}

// walkBoundarySources hands every Go source file a boundary rule applies to to
// audit, as raw bytes, and returns the repo-relative directories it entered.
//
// The bytes rather than a parsed file: each rule parses for itself, so the parse
// mode a rule depends on is inside the function its own fixtures exercise. A
// shared parse here would put that mode where nothing tests it.
func walkBoundarySources(t *testing.T, root string, audit func(relSlash string, src []byte)) map[string]bool {
	t.Helper()

	scannedDirs := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && excludeBoundaryEntry(path, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if excludeBoundaryEntry(path, false) {
			return nil
		}

		relPath, relErr := filepath.Rel(root, path)
		if relErr != nil {
			t.Fatalf("failed to locate %s under %s: %v", path, root, relErr)
		}
		relSlash := filepath.ToSlash(relPath)
		scannedDirs[filepath.ToSlash(filepath.Dir(relPath))] = true

		src, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("failed to read %s: %v", relSlash, readErr)
		}
		audit(relSlash, src)
		return nil
	})
	if err != nil {
		t.Fatalf("error walking %s: %v", root, err)
	}

	// A scan that entered nothing proves nothing. Both halves of the module are
	// named so that moving a package cannot quietly shrink the rule.
	for _, required := range []string{"cmd/crossrev", "internal/exec", "internal/archtest"} {
		if !scannedDirs[required] {
			t.Errorf("the scan never entered %s, so it proves nothing about it", required)
		}
	}
	return scannedDirs
}

// productionSource reports whether a file compiles into the crossrev binary.
//
// Two kinds of Go file in this module do not, and both are out of scope for the
// process-start rule below.
//
//   - A _test.go file. `go build` leaves it out and npm packs no Go source at
//     all, so nothing it does reaches a released binary.
//   - internal/testgen, which is run with `go run ./internal/testgen/policy` to
//     regenerate parity vectors from the Bash tables. Nothing imports it:
//     `go list -deps ./cmd/crossrev` does not name it.
//
// The counter-argument is real and worth stating rather than hiding. A test file
// can start an unbounded child with the whole parent environment, and on a CI
// runner that environment holds a forge credential. This rule does not close
// that, and calling it closed would be false.
//
// It is left open because the parity method of this port is to run the Bash
// oracle and compare against it: internal/config/merge_test.go and
// internal/prompt/shell_test.go both start bash to do exactly that, and every
// parity test after them will need to. A rule that each of those must edit is a
// rule that gets edited without being read. What limits the damage is that the
// boundary exists for the process that reads attacker-controlled text
// (lib/adapters/codex.sh:79-82), and a test child is not one.
func productionSource(relSlash string) bool {
	if strings.HasSuffix(relSlash, "_test.go") {
		return false
	}
	return relSlash != "internal/testgen" &&
		!strings.HasPrefix(relSlash, "internal/testgen/")
}

func TestProductionSourceScope(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "a package in the binary", path: "internal/harness/harness.go", want: true},
		{name: "the entrypoint", path: "cmd/crossrev/main.go", want: true},
		{name: "a test file", path: "internal/harness/harness_test.go", want: false},
		{name: "a test file in the exec package", path: "internal/exec/helper_unix_test.go", want: false},
		{name: "the parity generator", path: "internal/testgen/policy/main.go", want: false},
		{name: "a file merely starting with the same letters", path: "internal/testgenerator/main.go", want: true},
		{name: "a file named like a test but not one", path: "internal/harness/pretest.go", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := productionSource(tt.path); got != tt.want {
				t.Fatalf("productionSource(%q) = %t, want %t", tt.path, got, tt.want)
			}
		})
	}
}
