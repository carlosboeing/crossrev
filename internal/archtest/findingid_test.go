package archtest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
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
				if excludeBoundaryEntry(path, true) {
					return filepath.SkipDir
				}
				return nil
			}
			if excludeBoundaryEntry(path, false) {
				return nil
			}

			relPath, _ := filepath.Rel(root, path)
			relSlash := filepath.ToSlash(relPath)
			dirSlash := filepath.ToSlash(filepath.Dir(relPath))

			// Conversion to FindingID is ONLY permitted inside internal/prstate
			isPermittedPackage := dirSlash == "internal/prstate"

			node, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("failed to parse %s: %v", path, err)
			}

			coreLocalNames, dotImportedCore := importLocalNames(node, "github.com/carlosboeing/crossrev/internal/core", "core")

			ast.Inspect(node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				var isFindingIDConversion bool

				// e.g. core.FindingID(...) or alias.FindingID(...)
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if ident, ok := sel.X.(*ast.Ident); ok {
						if coreLocalNames[ident.Name] && sel.Sel.Name == "FindingID" {
							isFindingIDConversion = true
						}
					}
				} else if ident, ok := call.Fun.(*ast.Ident); ok {
					// e.g. FindingID(...) inside internal/core or with dot import
					if ident.Name == "FindingID" && (dotImportedCore || dirSlash == "internal/core") {
						isFindingIDConversion = true
					}
				}

				if isFindingIDConversion && !isPermittedPackage {
					pos := fset.Position(call.Pos())
					t.Errorf("forbidden conversion to core.FindingID in %s:%d (FindingID conversion is only permitted in internal/prstate)", relSlash, pos.Line)
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
