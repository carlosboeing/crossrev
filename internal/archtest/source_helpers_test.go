package archtest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

func excludeBoundaryEntry(path string, isDir bool) bool {
	if isDir {
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
		{name: "Go test file", path: "internal/example/example_test.go", want: false},
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
