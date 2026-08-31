package archtest_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// execPackage is the package that declares the runner, named as a string so
// that this rule does not import the package it polices.
const execPackage = "github.com/carlosboeing/crossrev/internal/exec"

const orchestratorRunnerName = "NewOrchestratorRunner"

// The orchestrator runner may be asked for in three directories.
//
// # What it opts out of
//
// NewOSRunner refuses to start a child whose environment names a forge
// credential. NewOrchestratorRunner is the opt-out: it may start a child
// that holds one. Review and resolve will hold a GitHub client and a
// process start in one type, so the opt-out lives on the runner instance
// rather than on Spec — a fake Runner cannot copy it from one spec onto
// another.
//
// # Why a rule rather than a review
//
// The breach is silent. Nothing errors when a model-facing process receives
// a token; it just arrives, and the leg it arrives in looks like every
// other one. A reviewer reading a diff sees a constructor call, which is
// what a correct call also looks like. The difference between the two is
// which program is on the other end of Spec.Path, and that is not visible
// at the line where the constructor is written.
//
// So the name is confined to the packages whose whole subject is running
// one named program the orchestrator drives on its own behalf — `gh` in
// internal/forge/ghexec, git in internal/vcs — plus the declaration in
// internal/exec and its tests. Adding a fourth consumer is a decision that
// has to be made here, against this comment, rather than in the file that
// wants it.
//
// # What it does not close
//
// A name scan cannot see a Runner that was constructed in a permitted
// directory and handed to a wrapper elsewhere. Review and resolve will
// hold a GitHub client and a process start in one type; if that type is
// given the orchestrator runner, it can start a model-facing child with a
// forge credential. unsafe and reflection can still write the unexported
// bool on OSRunner. The constructor scan uses default build tags, so a
// file excluded by those tags is not read. testdata, race/windows tags,
// /proc assembly and syscall.Syscall are not closed here. The name check
// catches an accident that writes NewOrchestratorRunner in the wrong
// package. Code review catches a hostile commit. Run's refusal is the
// guard that executes: NewOSRunner still refuses a forge credential,
// whatever wrapper holds it.
//
// # What this rule is for, stated exactly
//
// Every route above needs a Go file committed to this repository, and
// anybody who can add that file can add a directory to
// orchestratorRunnerPermitted in the same commit. These rules sit at the
// trust level of the code they police. They exist to make an accident
// visible in review, and they do not stop a committer who means it.
func TestOrchestratorRunnerIsConfined(t *testing.T) {
	root := findRepoRoot(t)
	pkgs := loadModulePackages(t)

	var found []orchestratorRunnerViolation
	permittedReferences := 0

	record := func(pos string, what string, dir string) {
		if permitted, v := recordOrchestratorRunner(pos, what, dir); permitted {
			permittedReferences++
			return
		} else if v != nil {
			found = append(found, *v)
		}
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

		for ident, obj := range pkg.TypesInfo.Uses {
			if !namesNewOrchestratorRunner(obj) {
				continue
			}
			if pos, dir, ok := locate(ident); ok {
				record(pos, "names exec."+orchestratorRunnerName, dir)
			}
		}
		for ident, obj := range pkg.TypesInfo.Defs {
			if !namesNewOrchestratorRunner(obj) {
				continue
			}
			if pos, dir, ok := locate(ident); ok {
				record(pos, "declares exec."+orchestratorRunnerName, dir)
			}
		}
	}

	// A rule that matched nothing proves nothing. The value is renamed or
	// the loader stops resolving it and every package passes vacuously.
	if permittedReferences == 0 {
		t.Fatal("the scan found no reference to NewOrchestratorRunner at all, so it proves nothing")
	}

	sort.Slice(found, func(i, j int) bool { return found[i].where < found[j].where })
	var last string
	for _, v := range found {
		if v.where == last {
			continue
		}
		last = v.where
		t.Errorf("%s %s outside internal/vcs, internal/forge/ghexec and internal/exec, the only directories that may start a child with a forge credential", v.where, v.what)
	}
}

// namesNewOrchestratorRunner matches the opt-out by name and package.
//
// The object kind is deliberately not asserted. A rule that pinned the
// kind would go quiet if the constructor became a variable rather than
// failing. The package check is what stops a same-named object elsewhere
// from being mistaken for this one.
func namesNewOrchestratorRunner(obj types.Object) bool {
	if obj == nil || obj.Name() != orchestratorRunnerName {
		return false
	}
	return obj.Pkg() != nil && obj.Pkg().Path() == execPackage
}

// orchestratorRunnerPermitted names the three directories the constructor
// may be spelled in, by directory rather than by package name: the
// external test package of each shares the directory and inherits the
// permission deliberately, because a test asserting the runner is
// constructed has to be able to say it.
//
// internal/exec declares the value, which has to live somewhere.
// internal/forge/ghexec runs `gh`, which cannot authenticate without the
// credential. internal/vcs runs git, which pushes over whatever credential
// helper the environment configures — on a GitHub-hosted runner that is the
// ambient token. Neither `gh` nor git reads attacker-controlled text, which
// is the process ADR 0001 is about.
func orchestratorRunnerPermitted(dir string) bool {
	return dir == "internal/exec" || dir == "internal/forge/ghexec" || dir == "internal/vcs"
}

type orchestratorRunnerViolation struct {
	where string
	what  string
}

// recordOrchestratorRunner is the decision both the live scan and the
// fixture table use. Exact directory equality: vendored/internal/exec is
// not internal/exec.
func recordOrchestratorRunner(pos, what, dir string) (permitted bool, v *orchestratorRunnerViolation) {
	if orchestratorRunnerPermitted(dir) {
		return true, nil
	}
	return false, &orchestratorRunnerViolation{where: pos, what: what}
}

func auditOrchestratorRunnerSource(relSlash, source string) (violations []string, permitted bool, err error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, relSlash, source, 0)
	if err != nil {
		return nil, false, err
	}
	dir := filepath.ToSlash(filepath.Dir(relSlash))
	ast.Inspect(node, func(n ast.Node) bool {
		var pos token.Pos
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if x.Sel == nil || x.Sel.Name != orchestratorRunnerName {
				return true
			}
			pos = x.Sel.Pos()
		case *ast.FuncDecl:
			if x.Name == nil || x.Name.Name != orchestratorRunnerName {
				return true
			}
			pos = x.Name.Pos()
		default:
			return true
		}
		where := fmt.Sprintf("%s:%d", relSlash, fset.Position(pos).Line)
		if ok, v := recordOrchestratorRunner(where, "names exec."+orchestratorRunnerName, dir); ok {
			permitted = true
		} else if v != nil {
			violations = append(violations, v.where+" "+v.what)
		}
		return true
	})
	return violations, permitted, nil
}

func TestOrchestratorRunnerAuditVerdict(t *testing.T) {
	tests := []struct {
		name          string
		file          string
		source        string
		wantViolation bool
		wantPermitted bool
	}{
		{
			name:          "names the constructor in internal/harness",
			file:          "internal/harness/adapter.go",
			source:        "package harness\nimport crexec \"github.com/carlosboeing/crossrev/internal/exec\"\nvar v = crexec.NewOrchestratorRunner\n",
			wantViolation: true,
		},
		{
			name:          "a directory whose path merely ends with the permitted one",
			file:          "vendored/internal/exec/runner.go",
			source:        "package exec\nimport crexec \"github.com/carlosboeing/crossrev/internal/exec\"\nvar v = crexec.NewOrchestratorRunner\n",
			wantViolation: true,
		},
		{
			name:          "the permitted exec package names it",
			file:          "internal/exec/osrunner.go",
			source:        "package exec\nfunc NewOrchestratorRunner() {}\n",
			wantPermitted: true,
		},
		{
			name:          "the permitted vcs package names it",
			file:          "internal/vcs/git.go",
			source:        "package vcs\nimport crexec \"github.com/carlosboeing/crossrev/internal/exec\"\nvar v = crexec.NewOrchestratorRunner\n",
			wantPermitted: true,
		},
		{
			name:   "internal/harness without the constructor",
			file:   "internal/harness/adapter.go",
			source: "package harness\nimport crexec \"github.com/carlosboeing/crossrev/internal/exec\"\nvar v = crexec.NewOSRunner\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations, permitted, err := auditOrchestratorRunnerSource(tt.file, tt.source)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			if got := len(violations) > 0; got != tt.wantViolation {
				t.Fatalf("violations = %v, want a violation: %t", violations, tt.wantViolation)
			}
			if permitted != tt.wantPermitted {
				t.Errorf("permitted = %t, want %t", permitted, tt.wantPermitted)
			}
		})
	}
}

func TestOrchestratorRunnerPermissionCoversTheThreeDirectories(t *testing.T) {
	for _, dir := range []string{"internal/exec", "internal/forge/ghexec", "internal/vcs"} {
		if !orchestratorRunnerPermitted(dir) {
			t.Errorf("%s must keep the orchestrator runner", dir)
		}
	}
	for _, dir := range []string{
		"internal/harness",
		"internal/review",
		"internal/forge",
		"internal/cred",
		"internal/sandbox",
		"internal/forge/ghexec/nested",
		"internal/vcs/nested",
		"internal/exec/nested",
		"cmd/crossrev",
	} {
		if orchestratorRunnerPermitted(dir) {
			t.Errorf("%s must not inherit the orchestrator runner", dir)
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
// The runner rule above says which package may ask for the opt-out. This says
// where inside it a Spec for `gh` may be built. Without it a method added to
// any of the operation files can build its own Spec with a Path the caller
// chose.
//
// It reads types rather than syntax, which is what closes the five routes past
// a scan of the text. An aliased import, a dot import, a local type alias, a
// `var s exec.Spec` with no literal at all, and an element of a []exec.Spec
// whose literal carries no type of its own all produce an expression whose TYPE
// is this one, and none of them is a selector spelled `exec.Spec`.
//
// The sixth route is not closed and cannot be by any scan of one package: a
// Spec built elsewhere and handed in. What bounds it is that this package
// exports no route to a Spec.
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
			t.Errorf("%s:%d names exec.Spec; inside %s only client.go may build one",
				relSlash, position.Line, ghClientDir)
		}
	}

	if !seen[ghClientFile] {
		t.Fatalf("the scan found no exec.Spec in %s, so it proves nothing", ghClientFile)
	}
}

// Every Spec that package builds runs `gh`.
//
// This is the half a rule about WHERE cannot state. The opt-out is safe because
// the child is `gh` and not a model, so a Spec built in the permitted file with
// a Path the caller chose would satisfy every other rule here and still be the
// leak ADR 0001 forbids. Pinning Path to the package's own constant is what
// ties the credential to the one program that may hold it.
//
// # Two shapes, because a literal is not the only way to set a field
//
// A review planted both of the ones a composite-literal walk cannot see, and
// both passed:
//
//	s := exec.Spec{Path: program, …}; s.Path = elsewhere   set, then overwritten
//	var s exec.Spec; s.Path = elsewhere                   never a literal
//
// So the assignments are read as well. Inside client.go, a write to the Path
// of anything of this type has to be the same value the literal is held to.
// The check is the field rather than the variable, which is what makes a Spec
// that never appears in a literal reach it.
//
// # What it still does not check
//
// The value on the right is matched by name — the identifier `program`. A
// `const program = "curl"` would satisfy it, and so would a local variable
// shadowing the constant. That is not a scan's job to catch: the constant is
// declared in this file, above the only Spec, and the diff that changed it
// would say so.
func TestEveryGhSpecRunsTheGhProgram(t *testing.T) {
	root := findRepoRoot(t)
	pkgs := loadModulePackages(t)

	// literals and writes together are what proves the scan reached something.
	literals, writes := 0, 0
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			position := pkg.Fset.Position(file.Pos())
			relPath, err := filepath.Rel(root, position.Filename)
			if err != nil || filepath.ToSlash(relPath) != ghClientFile {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if pkg.TypesInfo == nil {
					return true
				}
				switch node := n.(type) {
				case *ast.CompositeLit:
					tv, ok := pkg.TypesInfo.Types[node]
					if !ok || tv.Type == nil || tv.Type.String() != specType {
						return true
					}
					literals++
					line := pkg.Fset.Position(node.Pos()).Line
					fields := specFields(node)
					if !isGhProgram(fields["Path"]) {
						t.Errorf("%s:%d builds a Spec whose Path is not the package's own `program` constant",
							ghClientFile, line)
					}
				case *ast.AssignStmt:
					for at, target := range node.Lhs {
						field, ok := target.(*ast.SelectorExpr)
						if !ok || !isSpecTyped(pkg.TypesInfo, field.X) {
							continue
						}
						if at >= len(node.Rhs) {
							// A multi-value right-hand side assigns something
							// this scan cannot name, so it is reported rather
							// than skipped.
							t.Errorf("%s:%d writes a Spec field from a call this rule cannot read",
								ghClientFile, pkg.Fset.Position(target.Pos()).Line)
							continue
						}
						value := node.Rhs[at]
						line := pkg.Fset.Position(target.Pos()).Line
						if field.Sel.Name == "Path" {
							writes++
							if !isGhProgram(value) {
								t.Errorf("%s:%d sets a Spec's Path to something other than the package's own `program` constant",
									ghClientFile, line)
							}
						}
					}
				}
				return true
			})
		}
	}

	if literals == 0 && writes == 0 {
		t.Fatalf("no Spec literal or field write was found in %s, so this rule checked nothing", ghClientFile)
	}
}

// isGhProgram reports the package's own `program` constant, by name.
func isGhProgram(value ast.Expr) bool {
	ident, ok := value.(*ast.Ident)
	return ok && ident.Name == "program"
}

// isSpecTyped reports an expression whose type is exec.Spec, whether it is the
// value or a pointer to one.
func isSpecTyped(info *types.Info, expr ast.Expr) bool {
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	name := tv.Type.String()
	return name == specType || name == "*"+specType
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
