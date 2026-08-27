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

	scanDirs := []string{"cmd", "internal"}

	for _, scanDir := range scanDirs {
		dirPath := filepath.Join(root, scanDir)
		err := filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
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
			relSlash := filepath.ToSlash(relPath)

			// Resolve local name(s) bound to import path "os"
			osLocalNames := make(map[string]bool)
			dotImportedOS := false

			for _, imp := range node.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				if importPath == "os" {
					if imp.Name == nil {
						osLocalNames["os"] = true
					} else if imp.Name.Name == "." {
						dotImportedOS = true
					} else if imp.Name.Name != "_" {
						osLocalNames[imp.Name.Name] = true
					}
				}
			}

			if len(osLocalNames) == 0 && !dotImportedOS {
				return nil
			}

			ast.Inspect(node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				isEnvironCall := false

				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if ident, ok := sel.X.(*ast.Ident); ok {
						if osLocalNames[ident.Name] && sel.Sel.Name == "Environ" {
							isEnvironCall = true
						}
					}
				} else if ident, ok := call.Fun.(*ast.Ident); ok && dotImportedOS {
					if ident.Name == "Environ" {
						isEnvironCall = true
					}
				}

				if isEnvironCall {
					if relSlash == "internal/exec/env.go" {
						foundPermittedCall = true
					} else {
						pos := fset.Position(call.Pos())
						t.Errorf("forbidden call to os.Environ() in %s:%d (os.Environ is only permitted in internal/exec/env.go)", relSlash, pos.Line)
					}
				}
				return true
			})

			return nil
		})

		if err != nil {
			t.Fatalf("error walking %s directory: %v", scanDir, err)
		}
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
