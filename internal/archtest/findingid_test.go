package archtest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindingIDConversionBoundary(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()

	dirs := []string{filepath.Join(root, "internal"), filepath.Join(root, "cmd")}

	for _, d := range dirs {
		err := filepath.WalkDir(d, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				name := entry.Name()
				if name == "archtest" || name == "testgen" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			relPath, _ := filepath.Rel(root, path)
			dirRel := filepath.Dir(relPath)

			// Conversion to FindingID is only permitted inside internal/prstate (and internal/core where type is defined)
			isPermittedPackage := dirRel == filepath.Join("internal", "prstate") || dirRel == filepath.Join("internal", "core")

			node, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("failed to parse %s: %v", path, err)
			}

			// Map local import aliases to package path
			coreAlias := "core"
			for _, imp := range node.Imports {
				if imp.Path != nil && strings.Contains(imp.Path.Value, "internal/core") {
					if imp.Name != nil {
						coreAlias = imp.Name.Name
					}
				}
			}

			ast.Inspect(node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				var isFindingIDConversion bool

				// e.g. core.FindingID(...)
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if ident, ok := sel.X.(*ast.Ident); ok {
						if ident.Name == coreAlias && sel.Sel.Name == "FindingID" {
							isFindingIDConversion = true
						}
					}
				} else if ident, ok := call.Fun.(*ast.Ident); ok {
					// e.g. FindingID(...) inside core package
					if ident.Name == "FindingID" && dirRel == filepath.Join("internal", "core") {
						isFindingIDConversion = true
					}
				}

				if isFindingIDConversion && !isPermittedPackage {
					pos := fset.Position(call.Pos())
					t.Errorf("forbidden conversion to core.FindingID in %s:%d (FindingID conversion is only permitted in internal/prstate)", relPath, pos.Line)
				}
				return true
			})

			return nil
		})
		if err != nil {
			t.Fatalf("walk failed for %s: %v", d, err)
		}
	}
}
