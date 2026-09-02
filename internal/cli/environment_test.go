package cli

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// --- the walk ----------------------------------------------------------------

// envReadArgument is where a call keeps the variable's name.
//
// The map is the whole definition of "a named environment read", and it is
// deliberately short. os.Getenv and os.LookupEnv are the process environment.
// Getenv and Lookup unqualified are the two injected readers this module has —
// app.Environment.Getenv (internal/app/path.go:26) and cred.Environment.Lookup
// (internal/cred/staging.go:60) — and nothing else in the module declares a
// method by either name. lookupEnv and envValueSet read the environment a leg
// was started with (internal/resolve/invoke.go:424,
// internal/review/review.go:246), which is the same contract one process later.
//
// Set and Unset are left out. Both write a name for a child rather than reading
// one, and the only name either is ever given is the staging variable, which
// comes from the descriptor and is never a literal.
var envReadArgument = map[string]int{
	"Getenv":      0,
	"LookupEnv":   0,
	"Lookup":      0,
	"lookupEnv":   1,
	"envValueSet": 1,
}

// envRead is one place a production source names a variable and asks for it.
type envRead struct {
	// pkg is the module-relative package directory, e.g. "internal/config".
	pkg string
	// where is "<file>:<line>", for the failure message.
	where string
	// name is the variable, when the argument resolved to one.
	name string
	// resolved is false for a call whose name is computed at run time.
	resolved bool
	// how names the call, for the failure message.
	how string
}

// excludeContractEntry decides what the contract scan skips.
//
// It is source_helpers_test.go's excludeBoundaryEntry with one addition: test
// files. The contract is what the shipped binary reads, and a test may set a
// probe variable of its own — internal/cred/staging_test.go:617 reads
// CROSSREV_CRED_PROBE, which is not a variable CrossRev has. Including test
// files would put that name in the table and say the tool reads it.
//
// internal/testgen goes for the reason productionSource gives: nothing imports
// it, and `go run ./internal/testgen/policy` is a generator rather than the
// tool.
func excludeContractEntry(path string, isDir bool) bool {
	if isDir {
		name := filepath.Base(path)
		switch {
		case strings.HasPrefix(name, "."), strings.HasPrefix(name, "_"):
			return true
		case name == "testdata", name == "vendor", name == "node_modules":
			return true
		case name == "testgen":
			return true
		}
		return false
	}
	return !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go")
}

func TestContractScannerSkipsWhatIsNotShipped(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		isDir bool
		want  bool
	}{
		{name: "a package in the binary", path: "internal/config", isDir: true},
		{name: "the entrypoint", path: "cmd/crossrev", isDir: true},
		{name: "the generator", path: "internal/testgen", isDir: true, want: true},
		{name: "another branch's checkout", path: ".worktrees", isDir: true, want: true},
		{name: "fixtures", path: "internal/cli/testdata", isDir: true, want: true},
		{name: "a shipped file", path: "internal/config/load.go"},
		{name: "a test file", path: "internal/cred/staging_test.go", want: true},
		{name: "a file that is not Go", path: "internal/cli/testdata/matrix.json", want: true},
		{name: "a file named like a test but not one", path: "internal/cli/pretest.go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := excludeContractEntry(tt.path, tt.isDir); got != tt.want {
				t.Fatalf("excludeContractEntry(%q, %t) = %t, want %t", tt.path, tt.isDir, got, tt.want)
			}
		})
	}
}

// contractRoot is the module root, found by walking up to go.mod.
func contractRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// walkProductionSources parses every shipped Go file under the module root and
// hands it to audit with its package directory.
//
// It returns the package directories it entered. A scan that entered nothing
// proves nothing, and the caller checks that.
func walkProductionSources(t *testing.T, root string, audit func(pkg, relSlash string, fset *token.FileSet, node *ast.File)) map[string]bool {
	t.Helper()

	entered := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && excludeContractEntry(path, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if excludeContractEntry(path, false) {
			return nil
		}
		relPath, relErr := filepath.Rel(root, path)
		if relErr != nil {
			t.Fatalf("locate %s under %s: %v", path, root, relErr)
		}
		relSlash := filepath.ToSlash(relPath)
		pkg := filepath.ToSlash(filepath.Dir(relPath))
		entered[pkg] = true

		fset := token.NewFileSet()
		node, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", relSlash, parseErr)
		}
		audit(pkg, relSlash, fset, node)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return entered
}

// stringConstants collects every package-level identifier bound to a string
// literal, so a read given a named constant resolves to the name it holds.
//
// internal/cred/staging.go:34 is why this exists: the runner signal is read as
// `o.env().Lookup(runnerEnvironment)`, and a scan that took literals only would
// report that internal/cred reads nothing.
func stringConstants(node *ast.File, into map[string]string) {
	for _, decl := range node.Decls {
		general, ok := decl.(*ast.GenDecl)
		if !ok || (general.Tok != token.CONST && general.Tok != token.VAR) {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != len(value.Values) {
				continue
			}
			for i, name := range value.Names {
				literal, ok := value.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				text, err := strconv.Unquote(literal.Value)
				if err != nil {
					continue
				}
				into[name.Name] = text
			}
		}
	}
}

// callName answers the function name of a call expression, unqualified.
func callName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	}
	return ""
}

// namedEnvReads finds every environment read in one file.
func namedEnvReads(pkg, relSlash string, fset *token.FileSet, node *ast.File, constants map[string]string) []envRead {
	var found []envRead

	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn := callName(call)
		index, watched := envReadArgument[fn]
		if !watched || len(call.Args) <= index {
			return true
		}
		read := envRead{pkg: pkg, where: fmt.Sprintf("%s:%d", relSlash, fset.Position(call.Pos()).Line), how: fn}
		switch argument := call.Args[index].(type) {
		case *ast.BasicLit:
			if argument.Kind == token.STRING {
				if text, err := strconv.Unquote(argument.Value); err == nil {
					read.name, read.resolved = text, true
				}
			}
		case *ast.Ident:
			if text, ok := constants[argument.Name]; ok {
				read.name, read.resolved = text, true
				read.how = fn + "(" + argument.Name + ")"
			}
		}
		found = append(found, read)
		return true
	})
	return found
}

// mentionedNames answers every variable in want that this file names as a
// string literal, either alone or as the `NAME=` half of an assignment written
// onto a child's invocation.
func mentionedNames(node *ast.File, want map[string]bool) []string {
	var found []string
	ast.Inspect(node, func(n ast.Node) bool {
		literal, ok := n.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(literal.Value)
		if err != nil {
			return true
		}
		for name := range want {
			if text == name || strings.HasPrefix(text, name+"=") {
				found = append(found, name)
			}
		}
		return true
	})
	return found
}

func TestMentionedNamesSeesBothSpellings(t *testing.T) {
	source := "package sample\n" +
		"var plain = \"XDG_STATE_HOME\"\n" +
		"var assignment = []string{\"GIT_INDEX_FILE=\" + path}\n" +
		"var longer = \"CROSSREV_CODEX_AUTH_OLD\"\n" +
		"var path = \"/tmp/x\"\n"
	node, err := parser.ParseFile(token.NewFileSet(), "sample.go", source, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	want := map[string]bool{"XDG_STATE_HOME": true, "GIT_INDEX_FILE": true, "CROSSREV_CODEX_AUTH": true}
	got := mentionedNames(node, want)
	sort.Strings(got)
	if strings.Join(got, " ") != "GIT_INDEX_FILE XDG_STATE_HOME" {
		t.Fatalf("mentioned = %v, want the plain name and the assignment and not the longer name", got)
	}
}

func TestNamedEnvReadsResolvesAConstant(t *testing.T) {
	source := "package sample\n" +
		"const runnerEnvironment = \"RUNNER_ENVIRONMENT\"\n" +
		"var a, _ = env.Lookup(runnerEnvironment)\n" +
		"var b = os.Getenv(\"HOME\")\n" +
		"var c = lookupEnv(list, \"ANTHROPIC_API_KEY\")\n" +
		"var d = os.Getenv(computed)\n"
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "sample.go", source, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	constants := map[string]string{}
	stringConstants(node, constants)

	reads := namedEnvReads("internal/sample", "sample.go", fset, node, constants)
	if len(reads) != 4 {
		t.Fatalf("reads = %d %v, want 4", len(reads), reads)
	}
	var names []string
	unresolved := 0
	for _, read := range reads {
		if !read.resolved {
			unresolved++
			continue
		}
		names = append(names, read.name)
	}
	sort.Strings(names)
	if strings.Join(names, " ") != "ANTHROPIC_API_KEY HOME RUNNER_ENVIRONMENT" {
		t.Errorf("resolved names = %v, want the constant, the literal and the second argument", names)
	}
	if unresolved != 1 {
		t.Errorf("unresolved = %d, want the one computed read", unresolved)
	}
}

// --- the measured Bash side ---------------------------------------------------

type shellInventory struct {
	Count     int `json:"count"`
	Classes   []string
	Variables []shellVariable `json:"variables"`
}

type shellVariable struct {
	Name       string   `json:"name"`
	Class      string   `json:"class"`
	Descriptor bool     `json:"descriptor"`
	Spelled    bool     `json:"spelled"`
	Shell      []string `json:"shell"`
	Why        string   `json:"why"`
}

// shellDefaultRead is one place the Bash reads a name through a default
// operator, and one place it assigns one.
type shellDefaultRead struct {
	// name is the variable.
	name string
	// where is "<file>:<line>", for the failure message.
	where string
}

// shellDefaultOperator matches `${NAME:-`, `${NAME:=`, `${NAME:?` and `${NAME+`.
//
// Those four are the whole of "read this, and here is what to do when it is
// unset". A read written `$NAME` or `${NAME}` with no operator is not in scope:
// the shell sets most of its own globals unconditionally before reading them,
// and a bare read says nothing about where the value came from.
var shellDefaultOperator = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)(?::-|:=|:\?|\+)`)

// shellAssignment matches `NAME=` at the start of a word, which covers a plain
// assignment, `local NAME=`, `export NAME=`, and a one-command prefix such as
// `LC_ALL=C awk …`. The leading class is what stops `CROSSREV_X=` also counting
// as an assignment of `X`.
var shellAssignment = regexp.MustCompile(`(?:^|[\s;(&|])([A-Z_][A-Z0-9_]*)=`)

// shellSources is every Bash file the tool ships.
func shellSources(t *testing.T, root string) []string {
	t.Helper()
	files := []string{filepath.Join(root, "bin", "crossrev")}
	for _, pattern := range []string{
		filepath.Join(root, "lib", "*.sh"),
		filepath.Join(root, "lib", "adapters", "*.sh"),
	} {
		matched, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		files = append(files, matched...)
	}
	if len(files) < 10 {
		t.Fatalf("found %d shell sources, which is too few to be looking at this tool", len(files))
	}
	return files
}

// inheritedShellReads answers every name the Bash reads through a default
// operator and assigns nowhere.
//
// That pair is the contract's own scan rule for "this value can only have come
// from outside the process". A name the shell assigns somewhere — the CTX_*
// block, CROSSREV_RUN_DIR, ROOT — is a global the process sets for itself, and
// an inherited value never survives to be read.
func inheritedShellReads(t *testing.T, root string) []shellDefaultRead {
	t.Helper()

	var reads []shellDefaultRead
	assigned := map[string]bool{}
	for _, path := range shellSources(t, root) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("locate %s under %s: %v", path, root, err)
		}
		relSlash := filepath.ToSlash(rel)
		for i, line := range strings.Split(string(raw), "\n") {
			for _, m := range shellDefaultOperator.FindAllStringSubmatch(line, -1) {
				reads = append(reads, shellDefaultRead{name: m[1], where: fmt.Sprintf("%s:%d", relSlash, i+1)})
			}
			for _, m := range shellAssignment.FindAllStringSubmatch(line, -1) {
				assigned[m[1]] = true
			}
		}
	}

	var inherited []shellDefaultRead
	for _, read := range reads {
		if !assigned[read.name] {
			inherited = append(inherited, read)
		}
	}
	return inherited
}

func TestInheritedShellReadsSeparatesTheTwoKinds(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{filepath.Join("lib", "adapters"), "bin"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("make the fixture tree: %v", err)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	// Nine library files, because shellSources refuses fewer than ten sources.
	for i := 0; i < 8; i++ {
		write(filepath.Join("lib", fmt.Sprintf("filler%d.sh", i)), "\n")
	}
	write(filepath.Join("lib", "sample.sh"),
		"CROSSREV_RUN_DIR=\"\"\n"+
			"printf '%s' \"${CROSSREV_RUN_DIR:-none}\"\n"+
			"printf '%s' \"${TMPDIR:-/tmp}\"\n"+
			"printf '%s' \"$HOME/x\"\n")
	write(filepath.Join("lib", "adapters", "sample.sh"),
		"  local dir=\"${XDG_STATE_HOME:-$HOME/.local/state}\"\n")

	write(filepath.Join("bin", "crossrev"), "LC_ALL=C sort\n")

	var names []string
	for _, read := range inheritedShellReads(t, dir) {
		names = append(names, read.name+" "+read.where)
	}
	sort.Strings(names)
	got := strings.Join(names, ", ")
	want := "TMPDIR lib/sample.sh:3, XDG_STATE_HOME lib/adapters/sample.sh:1"
	if got != want {
		t.Fatalf("inherited = %q, want %q: the assigned global and the bare $HOME are not inherited reads", got, want)
	}
}

func loadShellInventory(t *testing.T) shellInventory {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "environment", "shell-inventory.json"))
	if err != nil {
		t.Fatalf("read the measured inventory: %v", err)
	}
	var inventory shellInventory
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatalf("parse the measured inventory: %v", err)
	}
	if len(inventory.Variables) == 0 {
		t.Fatal("the measured inventory is empty, so it proves nothing")
	}
	return inventory
}

// --- the contract --------------------------------------------------------------

func TestEnvironmentContract(t *testing.T) {
	root := contractRoot(t)
	table := Environment()

	byName := make(map[string]Variable, len(table))
	for _, v := range table {
		byName[v.Name] = v
	}
	wanted := make(map[string]bool, len(table))
	for _, v := range table {
		wanted[v.Name] = true
	}

	// One walk, two audits. The reads decide whether anything implicit is
	// left; the mentions decide whether a reader named in the table still
	// names the variable.
	var reads []envRead
	mentions := make(map[string]map[string]bool)
	entered := walkProductionSources(t, root, func(pkg, relSlash string, fset *token.FileSet, node *ast.File) {
		constants := map[string]string{}
		stringConstants(node, constants)
		reads = append(reads, namedEnvReads(pkg, relSlash, fset, node, constants)...)
		for _, name := range mentionedNames(node, wanted) {
			if mentions[name] == nil {
				mentions[name] = map[string]bool{}
			}
			mentions[name][pkg] = true
		}
	})

	t.Run("the scan entered the packages that read the environment", func(t *testing.T) {
		// A scan that entered nothing passes every rule below. These five are
		// named so that moving a package cannot quietly shrink the contract.
		for _, required := range []string{
			"internal/cli", "internal/config", "internal/cred",
			"internal/preflight", "internal/runlog",
		} {
			if !entered[required] {
				t.Errorf("the scan never entered %s, so it proves nothing about it", required)
			}
		}
		if len(reads) < len(table)/4 {
			t.Errorf("the scan found %d environment reads, which is too few to be looking at this module", len(reads))
		}
	})

	t.Run("the walk found each shape of read", func(t *testing.T) {
		// Four reads, one per shape the walk has to handle, each pinned to the
		// package it lives in. Without these the walk can stop resolving a
		// shape and every rule below still passes, because a read it never
		// finds is a read it never has to place.
		//
		//   a plain literal          internal/cli/cli.go:38
		//   a named constant         internal/cred/staging.go:367
		//   the second argument      internal/review/review.go:210
		//   an injected reader       internal/app/path.go:50
		shapes := []struct{ name, pkg string }{
			{"NO_COLOR", "cmd/crossrev"},
			{"CROSSREV_ASSUME_YES", "cmd/crossrev"},
			{"RUNNER_ENVIRONMENT", "internal/cred"},
			{"ANTHROPIC_API_KEY", "internal/review"},
			{"XDG_CONFIG_HOME", "internal/app"},
		}
		for _, shape := range shapes {
			found := false
			for _, read := range reads {
				if read.resolved && read.name == shape.name && read.pkg == shape.pkg {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("the walk never found %s read in %s", shape.name, shape.pkg)
			}
		}
	})

	t.Run("no implicit environment read is left", func(t *testing.T) {
		for _, read := range reads {
			if !read.resolved {
				// A computed name is the endpoint token and the descriptor's
				// secret, both named in environment.go. There is no table that
				// could hold either.
				continue
			}
			entry, held := byName[read.name]
			if !held {
				t.Errorf("%s reads %s through %s, and the contract does not carry it",
					read.where, read.name, read.how)
				continue
			}
			if !slices.Contains(entry.Readers, read.pkg) {
				t.Errorf("%s reads %s, and %s is not among its readers %v",
					read.where, read.name, read.pkg, entry.Readers)
			}
		}
	})

	t.Run("every reader named in the contract still names its variable", func(t *testing.T) {
		for _, v := range table {
			for _, reader := range v.Readers {
				if !mentions[v.Name][reader] {
					t.Errorf("%s names %s as a reader of %s, and no shipped source there names it",
						"the contract", reader, v.Name)
				}
			}
		}
	})

	t.Run("a variable Go reads has at least one reader", func(t *testing.T) {
		for _, read := range reads {
			if !read.resolved {
				continue
			}
			if entry, held := byName[read.name]; held && len(entry.Readers) == 0 {
				t.Errorf("%s is read at %s and the contract names no reader for it", read.name, read.where)
			}
		}
	})

	t.Run("a package naming a variable is one of its readers", func(t *testing.T) {
		for name, packages := range mentions {
			entry := byName[name]
			for pkg := range packages {
				if pkg == "internal/cli" {
					// environment.go and this test name every variable in the
					// table, which is what the table is. internal/cli reads
					// none of them: the process's own reads moved to
					// cmd/crossrev with the composition root, which is where a
					// process's environment is decided.
					continue
				}
				if !slices.Contains(entry.Readers, pkg) {
					t.Errorf("%s names %s and is not among its readers %v", pkg, name, entry.Readers)
				}
			}
		}
	})

	t.Run("the contract is sorted and holds each name once", func(t *testing.T) {
		for i := 1; i < len(table); i++ {
			if table[i-1].Name >= table[i].Name {
				t.Errorf("%s follows %s: the contract is sorted by name", table[i].Name, table[i-1].Name)
			}
		}
	})

	t.Run("every class is one of the six", func(t *testing.T) {
		known := map[Class]bool{
			ClassOperatorInput: true, ClassRunnerSignal: true, ClassPathOverride: true,
			ClassCredential: true, ClassEndpoint: true, ClassChildOutput: true,
		}
		for _, v := range table {
			if !known[v.Class] {
				t.Errorf("%s is classified %q, which is not one of the six", v.Name, v.Class)
			}
		}
	})

	t.Run("the contract is the measured Bash inventory", func(t *testing.T) {
		inventory := loadShellInventory(t)
		if inventory.Count != len(inventory.Variables) {
			t.Fatalf("the inventory says %d and carries %d", inventory.Count, len(inventory.Variables))
		}
		if len(table) != inventory.Count {
			t.Fatalf("the contract carries %d variables and the measured inventory %d", len(table), inventory.Count)
		}
		for _, measured := range inventory.Variables {
			entry, held := byName[measured.Name]
			if !held {
				t.Errorf("the shell touches %s and the contract does not carry it", measured.Name)
				continue
			}
			if string(entry.Class) != measured.Class {
				t.Errorf("%s is %q here and %q in the measured inventory", measured.Name, entry.Class, measured.Class)
			}
			if entry.Descriptor != measured.Descriptor {
				t.Errorf("%s: descriptor is %t here and %t in the measured inventory",
					measured.Name, entry.Descriptor, measured.Descriptor)
			}
			if len(measured.Shell) == 0 {
				t.Errorf("%s carries no measured shell site", measured.Name)
			}
			if measured.Why == "" {
				t.Errorf("%s carries no reason", measured.Name)
			}
		}
	})

	t.Run("a name the shell reads with a default and never assigns is in the contract", func(t *testing.T) {
		// The rule that catches a variable arriving on the Bash side. The Go
		// walk above cannot: a name the port has not reached yet is read by no
		// Go source, and the inventory is hand-written, so nothing else reads
		// the shell back. TMPDIR arrived at lib/auth.sh:626 with the login
		// fix and no rule failed.
		inherited := inheritedShellReads(t, root)
		if len(inherited) < len(table)/4 {
			t.Fatalf("the shell scan found %d inherited reads, which is too few to be looking at this tool", len(inherited))
		}
		for _, read := range inherited {
			if _, held := byName[read.name]; !held {
				t.Errorf("%s reads %s with a default and the shell assigns it nowhere, and the contract does not carry it",
					read.where, read.name)
			}
		}
	})

	t.Run("every measured shell site names its variable", func(t *testing.T) {
		// The half a hand-written table gets wrong. A citation is a claim about
		// a line in the shipped Bash, and this reads the line back.
		inventory := loadShellInventory(t)
		for _, measured := range inventory.Variables {
			if !measured.Spelled {
				continue
			}
			for _, site := range measured.Shell {
				file, line, ok := strings.Cut(site, ":")
				if !ok {
					t.Errorf("%s: %q is not file:line", measured.Name, site)
					continue
				}
				number, err := strconv.Atoi(line)
				if err != nil || number < 1 {
					t.Errorf("%s: %q has no line number", measured.Name, site)
					continue
				}
				raw, err := os.ReadFile(filepath.Join(root, file))
				if err != nil {
					t.Errorf("%s: %v", measured.Name, err)
					continue
				}
				lines := strings.Split(string(raw), "\n")
				if number > len(lines) {
					t.Errorf("%s: %s has only %d lines", measured.Name, file, len(lines))
					continue
				}
				if !strings.Contains(lines[number-1], measured.Name) {
					t.Errorf("%s: %s does not name it, it reads %q", measured.Name, site, lines[number-1])
				}
			}
		}
	})

	t.Run("the descriptor-named variables are the descriptor's", func(t *testing.T) {
		// The other half a hand-written table gets wrong. A name marked as the
		// descriptor's has to be in lib/harnesses.json, and every name in
		// lib/harnesses.json has to be marked.
		document, err := harness.Descriptors()
		if err != nil {
			t.Fatalf("read the descriptor: %v", err)
		}
		fromDescriptor := map[string]bool{}
		for _, name := range document.Names() {
			entry, ok := document.For(name)
			if !ok {
				t.Fatalf("the descriptor names %s and does not carry it", name)
			}
			credential := entry.Credential
			for _, declared := range append(
				append([]string{credential.Secret, credential.Staging.Env}, credential.EnvNames...),
				credential.EnvKeep...,
			) {
				if declared != "" {
					fromDescriptor[declared] = true
				}
			}
		}
		if len(fromDescriptor) == 0 {
			t.Fatal("the descriptor declared no environment name, so this rule proves nothing")
		}

		for _, v := range table {
			if v.Descriptor && !fromDescriptor[v.Name] {
				t.Errorf("%s is marked as the descriptor's and lib/harnesses.json does not declare it", v.Name)
			}
			if !v.Descriptor && fromDescriptor[v.Name] {
				t.Errorf("lib/harnesses.json declares %s and the contract does not mark it as the descriptor's", v.Name)
			}
		}
		for name := range fromDescriptor {
			if _, held := byName[name]; !held {
				t.Errorf("lib/harnesses.json declares %s and the contract does not carry it", name)
			}
		}
	})

	t.Run("the four forge credentials are classified as credentials", func(t *testing.T) {
		// The ADR 0001 boundary, stated as a rule rather than left to the
		// reader of a table: a reclassification is what would let one of these
		// out of the strip list without anything failing.
		for _, name := range []string{
			"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
		} {
			entry, held := byName[name]
			if !held {
				t.Errorf("the contract does not carry %s", name)
				continue
			}
			if entry.Class != ClassCredential {
				t.Errorf("%s is classified %q, and it is a credential to strip", name, entry.Class)
			}
			if !slices.Contains(entry.Readers, "internal/exec") {
				t.Errorf("%s does not name internal/exec, which is where the strip list lives", name)
			}
		}
	})

	t.Run("EnvironmentFor answers what the table holds", func(t *testing.T) {
		if _, held := EnvironmentFor("CROSSREV_NOT_A_VARIABLE"); held {
			t.Error("EnvironmentFor answered a name the contract does not carry")
		}
		got, held := EnvironmentFor("NO_COLOR")
		if !held || got.Class != ClassOperatorInput {
			t.Errorf("EnvironmentFor(NO_COLOR) = %+v, %t", got, held)
		}
	})

	t.Run("the answer is a copy", func(t *testing.T) {
		first := Environment()
		if len(first) == 0 {
			t.Fatal("the contract is empty")
		}
		first[0].Name = "MUTATED"
		if len(first[0].Readers) > 0 {
			first[0].Readers[0] = "mutated"
		}
		second := Environment()
		if second[0].Name == "MUTATED" {
			t.Error("a caller rewrote the contract's first name")
		}
		if len(second[0].Readers) > 0 && second[0].Readers[0] == "mutated" {
			t.Error("a caller rewrote the contract's first reader list")
		}
	})
}
