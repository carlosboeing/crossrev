package archtest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// excludeBoundaryEntry decides what a source-boundary scan skips.
//
// Directories: only the ones no Go binary can be built from. Dot-prefixed
// covers .git, and covers .worktrees, which holds other branches' checkouts —
// not this branch's code to police, and scripts/lint.sh prunes the same path
// for the same reason. Underscore-prefixed and testdata are what the go tool
// itself ignores, so nothing in them is ever compiled.
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
