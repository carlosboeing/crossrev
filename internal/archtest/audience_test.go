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

// audienceType and orchestratorName identify the one object this rule reads.
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
// So the name is confined to the packages whose whole subject is running one
// named program the orchestrator drives on its own behalf — `gh` in
// internal/forge/ghexec, git in internal/vcs — and adding a third consumer is a
// decision that has to be made here, against this comment, rather than in the
// file that wants it.
//
// # The type is the guard; this rule is the name check
//
// This used to scan for the VALUE as well as the name, because exec.Audience
// was an integer and the opt-out was the constant 1. Every spelling of that
// integer reached the opt-out with the name left off, and the list kept growing
// as reviews planted more:
//
//	exec.Audience(1)                      a conversion
//	exec.Spec{Audience: 1}                a keyed field
//	const orch exec.Audience = 1          a typed constant declaration
//	var orch exec.Audience = 1            a typed variable declaration
//	exec.AudienceModelFacing + 1          arithmetic on the strict sibling
//	s.Audience++                          an increment of the field
//	table[1]++                            an increment of an array element
//	json.Unmarshal([]byte("1"), &a)       a value computed at run time
//
// Two rounds of clauses caught the first seven; the eighth walked past all of
// them, and would walk past any ninth clause too — a syntactic scan cannot see
// a value that is computed. So exec.Audience is now a struct whose single field
// is unexported (internal/exec/spec.go). Every line above is a compile error
// outside internal/exec, and the unmarshal is a no-op there because
// encoding/json cannot reach an unexported field, so the clauses that looked
// for a written-out value are gone rather than kept as dead checks.
//
// What remains is the job a name check is actually good for: the value exists,
// it is reachable by name, and this says which directories may name it.
//
// # What it does not close
//
// The type closed construction, not every route to the value, and saying
// otherwise here would be worse than saying nothing — the next reviewer trusts
// the comment and stops looking. Three things are open.
//
//   - Capture. Every Spec internal/vcs and internal/forge/ghexec build carries
//     the opt-out and is handed to an injected Runner, so a fake Runner reads
//     the value straight off the Spec and names nothing.
//     TestAudienceFieldReferencesAreConfined below is the rule for that shape,
//     and it is a name scan: reflection over the exported field walks past it.
//   - unsafe. The value is one bool, so *(*bool)(unsafe.Pointer(&a)) = true
//     forges it. TestProcessStartBoundary in process_test.go refuses the
//     import outside internal/ui, and that rule reads import paths, so cgo or a
//     linkname is not covered by it.
//   - A Spec built inside internal/forge/ghexec and handed somewhere else, and
//     internal/exec itself. The first is bounded by that package exporting no
//     route to a Spec; the second is the declaration, which has to live
//     somewhere.
//
// # What this rule is for, stated exactly
//
// Every route above needs a Go file committed to this repository, and anybody
// who can add that file can add a directory to audienceOptOutPermitted in the
// same commit. These rules sit at the trust level of the code they police. They
// exist to make an accident visible in review — a Spec that picked up the
// opt-out on the way somewhere it should not go reads as one line of diff in a
// file the rule names — and they do not stop a committer who means it. Code
// review stops that.
//
// The guard that runs is exec.Run's refusal, which reads the field directly
// (internal/exec/osrunner.go) before it builds an environment or starts a
// child. It cannot tell a captured opt-out from an honest one, which is exactly
// why these rules are worth having and exactly why they must not be described
// as a boundary.
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

		// The one route left: the value, by name.
		for ident, obj := range pkg.TypesInfo.Uses {
			if !namesOrchestratorAudience(obj) {
				continue
			}
			if pos, dir, ok := locate(ident); ok {
				record(pos, "names exec."+orchestratorName, dir)
			}
		}
		for ident, obj := range pkg.TypesInfo.Defs {
			if !namesOrchestratorAudience(obj) {
				continue
			}
			if pos, dir, ok := locate(ident); ok {
				record(pos, "declares exec."+orchestratorName, dir)
			}
		}
	}

	// A rule that matched nothing proves nothing. The value is renamed or
	// the loader stops resolving it and every package passes vacuously.
	if permittedReferences == 0 {
		t.Fatal("the scan found no reference to the orchestrator audience at all, so it proves nothing")
	}

	// A declaration is both a Def and, in its own initialiser, a Use, so a
	// position reports once.
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

// namesOrchestratorAudience matches the opt-out by name, package and type.
//
// The object kind is deliberately not asserted. It was a *types.Const and is now
// a *types.Var, because a struct value cannot be a Go constant, and a rule that
// pinned the kind would have gone quiet at that change rather than failing. The
// type check is what stops a same-named object of some other type from being
// mistaken for this one.
func namesOrchestratorAudience(obj types.Object) bool {
	if obj == nil || obj.Name() != orchestratorName {
		return false
	}
	if obj.Pkg() == nil || obj.Pkg().Path() != execPackage {
		return false
	}
	return obj.Type() != nil && obj.Type().String() == audienceType
}

// audienceOptOutPermitted names the three directories the audience may be
// spelled in, by directory rather than by package name: the external test
// package of each shares the directory and inherits the permission
// deliberately, because a test asserting the audience is set has to be able to
// say it.
//
// internal/exec declares the value, which has to live somewhere.
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

// audienceField is the Spec field the rule below reads.
const audienceField = "Audience"

// audienceFieldPermitted names the directories that may reference Spec.Audience
// at all, in either direction.
//
// The first three are audienceOptOutPermitted's: internal/exec decides on the
// field, internal/vcs and internal/forge/ghexec set it. internal/harness is the
// fourth and it is a deliberate widening rather than an oversight, so the
// reason is written down. internal/harness/adapter_test.go asserts that the
// Spec it builds for a model-facing child was left at AudienceModelFacing,
// which is a read this rule must not forbid — that assertion is the reason the
// harness cannot regress. It is safe to widen for because the tier DAG
// (dependencies_test.go) grants internal/harness edges to internal/exec,
// internal/cred and internal/runlog and to nothing else, so it can never hold a
// Spec that internal/vcs or internal/forge/ghexec built and therefore has no
// opt-out to capture.
func audienceFieldPermitted(dir string) bool {
	return audienceOptOutPermitted(dir) || dir == "internal/harness"
}

func TestAudienceFieldPermissionCoversTheFourDirectories(t *testing.T) {
	for _, dir := range []string{"internal/exec", "internal/forge/ghexec", "internal/vcs", "internal/harness"} {
		if !audienceFieldPermitted(dir) {
			t.Errorf("%s must keep the Spec.Audience reference permission", dir)
		}
	}
	for _, dir := range []string{
		"internal/cycle",
		"internal/review",
		"internal/resolve",
		"internal/app",
		"internal/sandbox",
		"internal/harness/nested",
		"cmd/crossrev",
	} {
		if audienceFieldPermitted(dir) {
			t.Errorf("%s must not inherit the Spec.Audience reference permission", dir)
		}
	}
}

// Spec.Audience is referenced in four directories and nowhere else.
//
// # The shape this catches
//
// The rule above confines the NAME AudienceOrchestrator. It does not confine
// the value once a Spec is carrying it, and every Spec internal/vcs and
// internal/forge/ghexec build is carrying it. Both packages hand their Specs to
// an injected exec.Runner, which is what makes them testable, and a package
// that can supply that Runner takes the value off the Spec without writing the
// name down:
//
//	func (t *thief) Run(_ context.Context, spec exec.Spec) exec.Result {
//		t.stolen = spec.Audience
//		return exec.Result{}
//	}
//
// A tier-3 package may import internal/vcs and internal/forge/ghexec, so it may
// supply that Runner. The stolen value then goes into a Spec of its own with a
// forge credential in the environment and a harness on Path, and exec.Run has
// nothing left to refuse with — the field is the whole decision.
//
// # It is a name scan, and reflection walks past it
//
// This reads type information for identifiers spelled Audience whose object is
// the field declared on exec.Spec. Every ordinary reference is one of those,
// including the assignment form and the read above. None of the following is,
// and none is closed here:
//
//	reflect.ValueOf(spec).FieldByName("Audience")   the field by string
//	reflect.ValueOf(spec).Field(4)                  the field by index
//	*(*bool)(unsafe.Pointer(&a)) = true             the value forged outright
//
// The unsafe line is refused separately, by import path, in process_test.go.
// The reflect lines are not refused at all. Saying this rule closes capture
// would be false; it catches the shape a capture is actually written in, which
// is a review aid, and internal/exec/spec.go states what that is worth.
//
// # Both directions, deliberately
//
// A write is a reference too, so `s.Audience = exec.AudienceModelFacing` in an
// unpermitted directory fails this rule even though it sets the strict value.
// That write is redundant — the zero value is already that — and treating the
// field as untouchable outside the four directories is one sentence, where a
// direction-aware rule is a second thing to get right.
func TestAudienceFieldReferencesAreConfined(t *testing.T) {
	root := findRepoRoot(t)
	pkgs := loadModulePackages(t)

	type violation struct {
		where string
		what  string
	}
	var found []violation
	permittedReferences := 0

	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		for ident, obj := range pkg.TypesInfo.Uses {
			if !namesAudienceField(obj) {
				continue
			}
			position := pkg.Fset.Position(ident.Pos())
			relPath, err := filepath.Rel(root, position.Filename)
			if err != nil || strings.HasPrefix(relPath, "..") {
				// A generated test main lives outside the checkout.
				continue
			}
			relSlash := filepath.ToSlash(relPath)
			if audienceFieldPermitted(filepath.ToSlash(filepath.Dir(relSlash))) {
				permittedReferences++
				continue
			}
			found = append(found, violation{
				where: fmt.Sprintf("%s:%d", relSlash, position.Line),
				what:  "references exec.Spec." + audienceField,
			})
		}
	}

	// A rule that matched nothing proves nothing: rename the field, or lose the
	// type information, and every package passes vacuously.
	if permittedReferences == 0 {
		t.Fatal("the scan found no reference to exec.Spec.Audience at all, so it proves nothing")
	}

	sort.Slice(found, func(i, j int) bool { return found[i].where < found[j].where })
	var last string
	for _, v := range found {
		if v.where == last {
			continue
		}
		last = v.where
		t.Errorf("%s %s outside internal/exec, internal/vcs, internal/forge/ghexec and internal/harness; a Spec built elsewhere carries the orchestrator opt-out, and copying it off the Spec never spells %s",
			v.where, v.what, orchestratorName)
	}
}

// namesAudienceField matches the Audience field declared on exec.Spec.
//
// The declaring package and the field's own type are both asserted, so a field
// of the same name on some unrelated struct in this module is not mistaken for
// it, and IsField distinguishes the field from the package-level variables,
// which are Vars of the same type in the same package.
func namesAudienceField(obj types.Object) bool {
	variable, ok := obj.(*types.Var)
	if !ok || !variable.IsField() || variable.Name() != audienceField {
		return false
	}
	if variable.Pkg() == nil || variable.Pkg().Path() != execPackage {
		return false
	}
	return variable.Type() != nil && variable.Type().String() == audienceType
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
//
// # Two shapes, because a literal is not the only way to set a field
//
// A review planted both of the ones a composite-literal walk cannot see, and
// both passed:
//
//	s := exec.Spec{Path: program, …}; s.Path = elsewhere   set, then overwritten
//	var s exec.Spec; s.Path = elsewhere; s.Audience = …    never a literal
//
// So the assignments are read as well. Inside client.go, a write to the Path or
// the Audience of anything of this type has to be the same two values the
// literal is held to. The check is the field rather than the variable, which is
// what makes a Spec that never appears in a literal reach it.
//
// # What it still does not check
//
// The value on the right is matched by name — the identifier `program`, and a
// selector ending in AudienceOrchestrator. A `const program = "curl"` would
// satisfy it, and so would a local variable shadowing the constant. That is not
// a scan's job to catch: the constant is declared in this file, fifteen lines
// above the only Spec, and the diff that changed it would say so.
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
					if !isOrchestratorAudience(fields["Audience"]) {
						t.Errorf("%s:%d builds a Spec that does not set the orchestrator audience",
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
						switch field.Sel.Name {
						case "Path":
							writes++
							if !isGhProgram(value) {
								t.Errorf("%s:%d sets a Spec's Path to something other than the package's own `program` constant",
									ghClientFile, line)
							}
						case "Audience":
							writes++
							if !isOrchestratorAudience(value) {
								t.Errorf("%s:%d sets a Spec's Audience to something other than the orchestrator audience",
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

// isOrchestratorAudience reports exec.AudienceOrchestrator, by name.
func isOrchestratorAudience(value ast.Expr) bool {
	selector, ok := value.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == orchestratorName
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
