package archtest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestEnvironReferenceBoundary(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()

	foundPermittedReference := false

	scanDirs := []string{"cmd", "internal"}

	for _, scanDir := range scanDirs {
		dirPath := filepath.Join(root, scanDir)
		err := filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if excludeBoundaryEntry(path, true) {
					return filepath.SkipDir
				}
				return nil
			}
			if excludeBoundaryEntry(path, false) {
				return nil
			}

			node, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("failed to parse %s: %v", path, err)
			}

			relPath, _ := filepath.Rel(root, path)
			relSlash := filepath.ToSlash(relPath)

			osLocalNames, dotImportedOS := importLocalNames(node, "os", "os")

			if len(osLocalNames) == 0 && !dotImportedOS {
				return nil
			}

			for _, pos := range environReferencePositions(node, osLocalNames, dotImportedOS) {
				if relSlash == "internal/exec/env.go" {
					foundPermittedReference = true
				} else {
					position := fset.Position(pos)
					t.Errorf("forbidden reference to os.Environ in %s:%d (os.Environ is only permitted in internal/exec/env.go)", relSlash, position.Line)
				}
			}

			return nil
		})

		if err != nil {
			t.Fatalf("error walking %s directory: %v", scanDir, err)
		}
	}

	if !foundPermittedReference {
		t.Errorf("expected to find permitted reference to os.Environ in internal/exec/env.go")
	}
}

func environReferencePositions(node *ast.File, osLocalNames map[string]bool, dotImportedOS bool) []token.Pos {
	var positions []token.Pos
	ast.Inspect(node, func(n ast.Node) bool {
		switch expression := n.(type) {
		case *ast.SelectorExpr:
			if ident, ok := expression.X.(*ast.Ident); ok && osLocalNames[ident.Name] && expression.Sel.Name == "Environ" {
				positions = append(positions, expression.Pos())
				return false
			}
		case *ast.Ident:
			if dotImportedOS && expression.Name == "Environ" {
				positions = append(positions, expression.Pos())
			}
		}
		return true
	})
	return positions
}

func TestEnvironBoundaryDetectsFunctionValues(t *testing.T) {
	tests := []struct {
		name          string
		source        string
		osLocalNames  map[string]bool
		dotImportedOS bool
	}{
		{
			name:         "aliased selector",
			source:       "package sample\nimport environment \"os\"\nvar inherit = environment.Environ\n",
			osLocalNames: map[string]bool{"environment": true},
		},
		{
			name:          "dot import",
			source:        "package sample\nimport . \"os\"\nvar inherit = Environ\n",
			dotImportedOS: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			node, err := parser.ParseFile(fset, "sample.go", tt.source, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			if got := len(environReferencePositions(node, tt.osLocalNames, tt.dotImportedOS)); got != 1 {
				t.Fatalf("environment references = %d, want 1", got)
			}
		})
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
