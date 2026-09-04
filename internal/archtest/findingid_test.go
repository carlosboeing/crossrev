package archtest_test

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// findingIDType is the type whose values only internal/prstate may mint.
// Compared by string rather than by identity: `Tests: true` type-checks
// internal/core twice — once on its own and once with its test files — and the
// two runs produce different type objects for the same declaration.
const findingIDType = "github.com/carlosboeing/crossrev/internal/core.FindingID"

// A FindingID may only be minted in internal/prstate. An id derived by a second
// implementation would look like a new finding on the next pass and get posted
// again, so the rule covers every route from a plain string to this type, not
// just the conversion:
//
//   - core.FindingID(s), the conversion itself
//   - var id core.FindingID = "zzz", and the same as a const
//   - Finding{ID: "zzz"}, a struct field whose type is this one
//   - f.ID = "zzz", an assignment to one
//   - []core.FindingID{"zzz"} and map[string]core.FindingID{"k": "zzz"}
//
// All five are the same fact to go/types: an expression whose type is
// core.FindingID and which is either a conversion or a constant. Nothing else
// can turn a string into an id, and passing an id that already exists is
// neither.
func TestFindingIDMintingBoundary(t *testing.T) {
	root := findRepoRoot(t)
	pkgs := loadModulePackages(t)

	type violation struct {
		where string
		what  string
	}
	var found []violation

	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		for expr, tv := range pkg.TypesInfo.Types {
			if tv.IsType() || tv.Type == nil || tv.Type.String() != findingIDType {
				continue
			}
			// A conversion is a call whose callee is the type itself. go/types
			// records the callee with IsType, which is what separates
			// core.FindingID(s) from a call returning one.
			var what string
			if call, ok := expr.(*ast.CallExpr); ok {
				if ftv, ok := pkg.TypesInfo.Types[call.Fun]; ok && ftv.IsType() {
					what = "a conversion to core.FindingID"
				}
			}
			if what == "" && tv.Value != nil {
				what = "a constant of type core.FindingID"
			}
			if what == "" {
				continue
			}

			pos := pkg.Fset.Position(expr.Pos())
			relPath, err := filepath.Rel(root, pos.Filename)
			if err != nil || strings.HasPrefix(relPath, "..") {
				// A generated test main lives outside the checkout.
				continue
			}
			relSlash := filepath.ToSlash(relPath)
			if findingIDMintPermitted(filepath.ToSlash(filepath.Dir(relPath)), pkg.Name) {
				continue
			}
			found = append(found, violation{
				where: fmt.Sprintf("%s:%d", relSlash, pos.Line),
				what:  what,
			})
		}
	}

	// One expression can be recorded twice — the operand of a conversion carries
	// the converted type too — so a position is reported once.
	sort.Slice(found, func(i, j int) bool { return found[i].where < found[j].where })
	var last string
	for _, v := range found {
		if v.where == last {
			continue
		}
		last = v.where
		t.Errorf("%s is %s outside internal/prstate, which is the only package that may mint one", v.where, v.what)
	}
}

// loadModulePackages type-checks every package in the module, test files
// included. The test files are not exempt: a test that mints an id is a second
// implementation of the derivation whatever it is for.
func loadModulePackages(t *testing.T) []*packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "github.com/carlosboeing/crossrev/...")
	if err != nil {
		t.Fatalf("failed to load packages: %v", err)
	}
	// A package that fails to type-check loads with an empty Types map, so every
	// rule below would pass on it vacuously.
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatal("packages failed to load; the minting boundary cannot be checked against them")
	}
	return pkgs
}

func findingIDMintPermitted(dir, packageName string) bool {
	return dir == "internal/prstate" && packageName == "prstate"
}

func TestFindingIDPermissionExcludesExternalTestPackage(t *testing.T) {
	if !findingIDMintPermitted("internal/prstate", "prstate") {
		t.Fatal("package prstate must retain FindingID construction authority")
	}
	if findingIDMintPermitted("internal/prstate", "prstate_test") {
		t.Fatal("external package prstate_test must not inherit FindingID construction authority from its directory")
	}
}
