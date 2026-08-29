package archtest_test

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// execPackage is the package that declares the audience, named as a string so
// that this rule does not import the package it polices.
const execPackage = "github.com/carlosboeing/crossrev/internal/exec"

// audienceType and orchestratorConst are the two routes to the opt-out.
const (
	audienceType     = execPackage + ".Audience"
	orchestratorName = "AudienceOrchestrator"
)

// The orchestrator audience may be asked for in two packages.
//
// # What it opts out of
//
// exec.Run refuses to start a model-facing child whose environment names a
// forge credential, and Spec.Audience decides which children are model-facing.
// The zero value is the strict one, so forgetting the field fails closed: the
// run is refused and the message names the variable. AudienceOrchestrator is
// the opt-out, and it fails OPEN — a child that is not `gh`, started with a
// token in its environment, is exactly what ADR 0001 says must never happen,
// and nothing in the runner can tell that child from `gh`.
//
// # Why a rule rather than a review
//
// The breach is silent. Nothing errors when a model-facing process receives a
// token; it just arrives, and the leg it arrives in looks like every other one.
// A reviewer reading a diff sees one field set to a named constant, which is
// what a correct call also looks like. The difference between the two is which
// program is on the other end of Spec.Path, and that is not visible at the line
// where the constant is written.
//
// So the constant is confined to the packages whose whole subject is running
// one named program the orchestrator drives on its own behalf — `gh` in
// internal/forge/ghexec, git in internal/vcs — and adding a third consumer is a
// decision that has to be made here, against this comment, rather than in the
// file that wants it.
//
// # Both routes
//
// Naming the constant is one route. `exec.Audience(1)` is the other: it is the
// same value with the name left off, and a rule that read only the constant
// would answer nothing about it. Both are checked, which is the shape
// TestFindingIDMintingBoundary uses for the same reason.
//
// # What it does not close
//
// It says nothing about a Spec built inside internal/forge/ghexec and handed
// somewhere else, and nothing about internal/exec itself. The first is bounded
// by that package having no exported route to a Spec; the second is the
// declaration, which has to live somewhere.
func TestOrchestratorAudienceIsConfinedToTwoPackages(t *testing.T) {
	root := findRepoRoot(t)
	pkgs := loadModulePackages(t)

	type violation struct {
		where string
		what  string
	}
	var found []violation
	permittedReferences := 0

	record := func(pos string, what string, dir string) {
		if audienceOptOutPermitted(dir) {
			permittedReferences++
			return
		}
		found = append(found, violation{where: pos, what: what})
	}

	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}

		locate := func(node ast.Node) (pos string, dir string, ok bool) {
			position := pkg.Fset.Position(node.Pos())
			relPath, err := filepath.Rel(root, position.Filename)
			if err != nil || strings.HasPrefix(relPath, "..") {
				// A generated test main lives outside the checkout.
				return "", "", false
			}
			return fmt.Sprintf("%s:%d", filepath.ToSlash(relPath), position.Line),
				filepath.ToSlash(filepath.Dir(relPath)), true
		}

		// Route one: the constant, by name.
		for ident, obj := range pkg.TypesInfo.Uses {
			if !isOrchestratorConst(obj) {
				continue
			}
			if pos, dir, ok := locate(ident); ok {
				record(pos, "names exec."+orchestratorName, dir)
			}
		}
		for ident, obj := range pkg.TypesInfo.Defs {
			if !isOrchestratorConst(obj) {
				continue
			}
			if pos, dir, ok := locate(ident); ok {
				record(pos, "declares exec."+orchestratorName, dir)
			}
		}

		// Route two: the type, converted or constant, which reaches the same
		// value without naming it.
		for expr, tv := range pkg.TypesInfo.Types {
			if tv.IsType() || tv.Type == nil || tv.Type.String() != audienceType {
				continue
			}
			what := ""
			if call, ok := expr.(*ast.CallExpr); ok {
				if ftv, ok := pkg.TypesInfo.Types[call.Fun]; ok && ftv.IsType() {
					what = "converts to exec.Audience"
				}
			}
			if what == "" {
				continue
			}
			if pos, dir, ok := locate(expr); ok {
				record(pos, what, dir)
			}
		}
	}

	// A rule that matched nothing proves nothing. The constant is renamed or
	// the loader stops resolving it and every package passes vacuously.
	if permittedReferences == 0 {
		t.Fatal("the scan found no reference to the orchestrator audience at all, so it proves nothing")
	}

	// One expression is recorded under both routes, so a position reports once.
	sort.Slice(found, func(i, j int) bool { return found[i].where < found[j].where })
	var last string
	for _, v := range found {
		if v.where == last {
			continue
		}
		last = v.where
		t.Errorf("%s %s outside internal/forge/ghexec and internal/vcs, the only packages that may hand a forge credential to a child", v.where, v.what)
	}
}

func isOrchestratorConst(obj types.Object) bool {
	konst, ok := obj.(*types.Const)
	if !ok || konst.Name() != orchestratorName {
		return false
	}
	return konst.Pkg() != nil && konst.Pkg().Path() == execPackage
}

// audienceOptOutPermitted names the three directories the audience may be
// spelled in, by directory rather than by package name: the external test
// package of each shares the directory and inherits the permission
// deliberately, because a test asserting the audience is set has to be able to
// say it.
//
// internal/exec declares the constant, which has to live somewhere.
// internal/forge/ghexec runs `gh`, which cannot authenticate without the
// credential. internal/vcs runs git, which pushes over whatever credential
// helper the environment configures — on a GitHub-hosted runner that is the
// ambient token, and internal/vcs/git.go:93-107 states the case. Neither `gh`
// nor git reads attacker-controlled text, which is the process ADR 0001 is
// about.
func audienceOptOutPermitted(dir string) bool {
	return dir == "internal/exec" || dir == "internal/forge/ghexec" || dir == "internal/vcs"
}

func TestAudienceOptOutPermissionCoversTheThreeDirectories(t *testing.T) {
	for _, dir := range []string{"internal/exec", "internal/forge/ghexec", "internal/vcs"} {
		if !audienceOptOutPermitted(dir) {
			t.Errorf("%s must keep the audience opt-out", dir)
		}
	}
	for _, dir := range []string{
		"internal/harness",
		"internal/forge",
		"internal/cred",
		"internal/sandbox",
		"internal/forge/ghexec/nested",
		"internal/vcs/nested",
		"cmd/crossrev",
	} {
		if audienceOptOutPermitted(dir) {
			t.Errorf("%s must not inherit the audience opt-out", dir)
		}
	}
}

// ghClientDir and ghClientFile are where a Spec may be built for `gh`.
const (
	ghClientDir  = "internal/forge/ghexec"
	ghClientFile = ghClientDir + "/client.go"
	specType     = execPackage + ".Spec"
)

// Inside the package that holds the opt-out, a Spec is built in one file.
//
// The audience rule above says which package may ask for the opt-out. This says
// where inside it, and the two together are what make "every spec carries the
// orchestrator audience" a fact rather than a description of the code as
// written. Without it a method added to any of the six operation files can
// build its own Spec, and the one that forgets the audience is refused — which
// is the safe half — while the one that sets it is not checked by anything.
//
// It reads types rather than syntax, which is what closes the five routes past
// a scan of the text. An aliased import, a dot import, a local type alias, a
// `var s exec.Spec` with no literal at all, and an element of a []exec.Spec
// whose literal carries no type of its own all produce an expression whose TYPE
// is this one, and none of them is a selector spelled `exec.Spec`.
//
// The sixth route is not closed and cannot be by any scan of one package: a
// Spec built elsewhere and handed in. What bounds it is that this package
// exports no route to a Spec, and that the audience rule above stops the
// elsewhere from setting the field that matters.
func TestSpecConstructionIsConfinedToTheGhClient(t *testing.T) {
	root := findRepoRoot(t)
	pkgs := loadModulePackages(t)

	seen := map[string]bool{}
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		for expr, tv := range pkg.TypesInfo.Types {
			if tv.IsType() || tv.Type == nil || tv.Type.String() != specType {
				continue
			}
			position := pkg.Fset.Position(expr.Pos())
			relPath, err := filepath.Rel(root, position.Filename)
			if err != nil || strings.HasPrefix(relPath, "..") {
				continue
			}
			relSlash := filepath.ToSlash(relPath)
			if filepath.ToSlash(filepath.Dir(relSlash)) != ghClientDir {
				// Another package's Specs are its own business; this rule is
				// about the package that holds the credential opt-out.
				continue
			}
			if !productionSource(relSlash) || relSlash == ghClientFile {
				seen[relSlash] = true
				continue
			}
			t.Errorf("%s:%d names exec.Spec; inside %s only client.go may build one, so the audience is set in one place",
				relSlash, position.Line, ghClientDir)
		}
	}

	if !seen[ghClientFile] {
		t.Fatalf("the scan found no exec.Spec in %s, so it proves nothing", ghClientFile)
	}
}

// Every Spec that package builds runs `gh`, with the audience set.
//
// This is the half a rule about WHERE cannot state. The opt-out is safe because
// the child is `gh` and not a model, so a Spec built in the permitted file with
// a Path the caller chose would satisfy every other rule here and still be the
// leak ADR 0001 forbids. Pinning the field to the package's own constant is
// what ties the credential to the one program that may hold it.
func TestEveryGhSpecRunsTheGhProgram(t *testing.T) {
	root := findRepoRoot(t)
	pkgs := loadModulePackages(t)

	literals := 0
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			position := pkg.Fset.Position(file.Pos())
			relPath, err := filepath.Rel(root, position.Filename)
			if err != nil || filepath.ToSlash(relPath) != ghClientFile {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || pkg.TypesInfo == nil {
					return true
				}
				tv, ok := pkg.TypesInfo.Types[lit]
				if !ok || tv.Type == nil || tv.Type.String() != specType {
					return true
				}
				literals++
				fields := specFields(lit)

				path, ok := fields["Path"].(*ast.Ident)
				if !ok || path.Name != "program" {
					t.Errorf("%s:%d builds a Spec whose Path is not the package's own `program` constant",
						ghClientFile, pkg.Fset.Position(lit.Pos()).Line)
				}
				audience, ok := fields["Audience"].(*ast.SelectorExpr)
				if !ok || audience.Sel.Name != orchestratorName {
					t.Errorf("%s:%d builds a Spec that does not set the orchestrator audience",
						ghClientFile, pkg.Fset.Position(lit.Pos()).Line)
				}
				return true
			})
		}
	}

	if literals == 0 {
		t.Fatalf("no Spec literal was found in %s, so this rule checked nothing", ghClientFile)
	}
}

// specFields is the keyed fields of a composite literal, by name. A Spec built
// positionally has none, which the callers above report as a missing field.
func specFields(lit *ast.CompositeLit) map[string]ast.Expr {
	fields := map[string]ast.Expr{}
	for _, element := range lit.Elts {
		kv, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok {
			fields[key.Name] = kv.Value
		}
	}
	return fields
}
