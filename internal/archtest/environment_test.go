package archtest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironCallBoundary(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()

	foundPermittedCall := false

	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "archtest" || name == "testgen" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		node, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", path, err)
		}

		relPath, _ := filepath.Rel(root, path)

		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}

			if ident.Name == "os" && sel.Sel.Name == "Environ" {
				if relPath == filepath.Join("internal", "exec", "env.go") || filepath.Dir(relPath) == filepath.Join("internal", "exec") {
					foundPermittedCall = true
				} else {
					pos := fset.Position(call.Pos())
					t.Errorf("forbidden call to os.Environ() in %s:%d (os.Environ is only permitted in internal/exec/env.go)", relPath, pos.Line)
				}
			}
			return true
		})

		return nil
	})

	if err != nil {
		t.Fatalf("error walking internal directory: %v", err)
	}

	if !foundPermittedCall {
		t.Errorf("expected to find permitted call to os.Environ() in internal/exec/env.go")
	}
}

func findRepoRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not find repo root with go.mod from %s", dir)
	return "."
}
