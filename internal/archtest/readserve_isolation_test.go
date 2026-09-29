package archtest_test

import (
	"go/ast"
	"testing"

	"golang.org/x/tools/go/packages"
)

// loadReadServeSyntax loads the non-test syntax trees of internal/readserve.
func loadReadServeSyntax(t *testing.T) *packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedSyntax,
		Tests: false,
	}

	pkgs, err := packages.Load(cfg, "github.com/carlosboeing/crossrev/internal/readserve")
	if err != nil {
		t.Fatalf("failed to load packages: %v", err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatalf("package loading reported errors")
	}
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package, got %d", len(pkgs))
	}
	return pkgs[0]
}

// osCalls calls f for every `os.X(...)` call in the package syntax.
func osCalls(pkg *packages.Package, f func(sel *ast.SelectorExpr, call *ast.CallExpr, pos string)) {
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Name != "os" {
				return true
			}
			f(sel, call, pkg.Fset.Position(sel.Pos()).String())
			return true
		})
	}
}

// The server is configured by argument, never by environment: no read of
// the process environment may appear in the package.
func TestReadServeReadsNoEnvironment(t *testing.T) {
	pkg := loadReadServeSyntax(t)

	reads := map[string]bool{"Getenv": true, "LookupEnv": true, "Environ": true}
	osCalls(pkg, func(sel *ast.SelectorExpr, _ *ast.CallExpr, pos string) {
		if reads[sel.Sel.Name] {
			t.Errorf("internal/readserve reads the environment via os.%s at %s", sel.Sel.Name, pos)
		}
	})
}

// The server's only write is the configured log path: the only file
// operations in the package are creating the log's parent directory and
// opening that path for appending, and nothing else in os creates,
// truncates or writes a file.
func TestReadServeWritesOnlyItsLog(t *testing.T) {
	pkg := loadReadServeSyntax(t)

	allowed := map[string]bool{"MkdirAll": true, "OpenFile": true}
	opens := 0
	osCalls(pkg, func(sel *ast.SelectorExpr, call *ast.CallExpr, pos string) {
		if !allowed[sel.Sel.Name] {
			t.Errorf("internal/readserve uses os.%s at %s, outside the log-path write", sel.Sel.Name, pos)
			return
		}
		switch sel.Sel.Name {
		case "MkdirAll":
			// The one directory creation parents the log path: the
			// argument shape is MkdirAll(filepath.Dir(cfg.LogPath), ...).
			if !isDirOfLogPath(call) {
				t.Errorf("os.MkdirAll at %s does not create the log path's parent directory", pos)
			}
		case "OpenFile":
			if len(call.Args) == 0 {
				t.Errorf("os.OpenFile at %s takes no path argument", pos)
				return
			}
			// The one open must name the configured log path.
			path, ok := call.Args[0].(*ast.SelectorExpr)
			if !ok {
				t.Errorf("os.OpenFile at %s opens a path that is not cfg.LogPath", pos)
				return
			}
			id, ok := path.X.(*ast.Ident)
			if !ok || id.Name != "cfg" || path.Sel.Name != "LogPath" {
				t.Errorf("os.OpenFile at %s opens a path that is not cfg.LogPath", pos)
				return
			}
			opens++
		}
	})
	if opens != 1 {
		t.Errorf("internal/readserve opens %d file paths, want exactly cfg.LogPath", opens)
	}
}

// isDirOfLogPath reports whether call is filepath.Dir(cfg.LogPath).
func isDirOfLogPath(call *ast.CallExpr) bool {
	if len(call.Args) == 0 {
		return false
	}
	dir, ok := call.Args[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := dir.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != "filepath" || sel.Sel.Name != "Dir" {
		return false
	}
	if len(dir.Args) != 1 {
		return false
	}
	path, ok := dir.Args[0].(*ast.SelectorExpr)
	if !ok {
		return false
	}
	cfg, ok := path.X.(*ast.Ident)
	return ok && cfg.Name == "cfg" && path.Sel.Name == "LogPath"
}
