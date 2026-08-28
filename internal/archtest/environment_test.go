package archtest_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A bulk read of the process environment is confined to one file.
//
// internal/exec/env.go turns the environment into an allowlist (Inherit), and
// ADR 0001 makes that the boundary no GitHub credential crosses. A second
// reader anywhere would be a second policy, and its breach would be silent:
// nothing fails when a token reaches a model-facing process, it just arrives.
//
// # What counts as a bulk read, and what does not
//
// os.Environ and syscall.Environ are the same fact — os.Environ is a thin
// wrapper over syscall.Environ — and both hand back every variable including
// the ones nobody has thought of yet. Both are confined. syscall.Environ is
// confined nowhere at all rather than alongside os.Environ, because nothing
// needs the lower-level call and the higher-level one already has a home.
//
// os.Getenv and os.LookupEnv are deliberately NOT confined, and that is a
// finding rather than an omission. They read one variable the caller has
// already named, which is an allowlist of one by construction — the property
// Inherit exists to impose. They cannot leak a credential nobody wrote down,
// which is the failure mode the rule is for. Confining them would also break
// two files that read configuration correctly today: internal/config/load.go
// resolves XDG_CONFIG_HOME and HOME through them. syscall.Getenv is the same
// shape and is left alone for the same reason.
//
// /proc/self/environ is the kernel's own copy, and reading it would sidestep
// every rule above on the Linux that CI runs on. The literal path is caught
// below. That check is shallow on purpose and is documented as such: a path
// assembled from pieces at run time defeats it, and no test that reads source
// can close that.
//
// # What this test cannot close
//
//   - cgo. `extern char **environ` reaches the same array with no Go symbol to
//     match on. Adding cgo to a package here would be visible in review and in
//     the build, which is the only guard there is.
//   - A path to /proc built at run time, as above.
//   - go:linkname is caught as a source directive (below), but the guarantee is
//     the Go linker's rather than this test's: since Go 1.23 a pull-linkname to
//     an unexported-to-linkname standard library symbol fails to link at all.
//     Verified on this module: the link refuses with "invalid reference to
//     os.Environ" unless the build passes -ldflags=-checklinkname=0, which
//     nothing here does.
type environRule struct {
	// importPath and defaultName identify the package holding the symbol.
	importPath  string
	defaultName string
	// symbol is the function that reads the whole environment.
	symbol string
	// permittedIn is the one repo-relative file allowed to reference it. Empty
	// means no file may.
	permittedIn string
}

var environRules = []environRule{
	{importPath: "os", defaultName: "os", symbol: "Environ", permittedIn: "internal/exec/env.go"},
	{importPath: "syscall", defaultName: "syscall", symbol: "Environ"},
}

// procEnvironPath is spelled in pieces so this file does not match its own rule.
// The same trick is why scripts/lint.sh does not grep for the flag it forbids.
var procEnvironPath = "/proc/" + "self/" + "environ"

// environReference is one place a file reaches the whole environment.
type environReference struct {
	pos token.Pos
	// what names the route, for the failure message.
	what string
	// permittedIn is the file allowed to hold this reference, empty for none.
	permittedIn string
}

func TestEnvironReferenceBoundary(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()

	permittedSeen := make(map[string]bool)
	scannedDirs := make(map[string]bool)

	// Every directory in the module, not a hand-written pair. A rule that
	// applies to two directories is defeated by adding a third, and a package
	// outside cmd/ and internal/ compiles into the same binary.
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && excludeBoundaryEntry(path, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if excludeBoundaryEntry(path, false) {
			return nil
		}

		relPath, relErr := filepath.Rel(root, path)
		if relErr != nil {
			t.Fatalf("failed to locate %s under %s: %v", path, root, relErr)
		}
		relSlash := filepath.ToSlash(relPath)
		scannedDirs[filepath.ToSlash(filepath.Dir(relPath))] = true

		// ParseComments, because //go:linkname is a comment that the compiler
		// reads as a declaration.
		node, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			t.Fatalf("failed to parse %s: %v", relSlash, parseErr)
		}

		for _, reference := range environReferences(node) {
			if reference.permittedIn != "" && reference.permittedIn == relSlash {
				permittedSeen[relSlash] = true
				continue
			}
			position := fset.Position(reference.pos)
			where := "no file may hold one"
			if reference.permittedIn != "" {
				where = "only " + reference.permittedIn + " may hold one"
			}
			t.Errorf("%s:%d reaches the whole process environment through %s; %s",
				relSlash, position.Line, reference.what, where)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("error walking %s: %v", root, err)
	}

	// The rule must not pass because the scan found nothing. Both halves are
	// checked: the directories that hold the code, and the reference the one
	// permitted file is supposed to contain.
	for _, required := range []string{"cmd/crossrev", "internal/exec", "internal/archtest"} {
		if !scannedDirs[required] {
			t.Errorf("the scan never entered %s, so it proves nothing about it", required)
		}
	}
	for _, rule := range environRules {
		if rule.permittedIn == "" {
			continue
		}
		if !permittedSeen[rule.permittedIn] {
			t.Errorf("expected %s to reference %s.%s; the scan is not looking where it thinks it is",
				rule.permittedIn, rule.defaultName, rule.symbol)
		}
	}
}

// environReferences finds every route in one file from source to the whole
// process environment.
func environReferences(node *ast.File) []environReference {
	var found []environReference

	for _, rule := range environRules {
		localNames, dotImported := importLocalNames(node, rule.importPath, rule.defaultName)
		if len(localNames) == 0 && !dotImported {
			continue
		}
		qualified := rule.defaultName + "." + rule.symbol

		ast.Inspect(node, func(n ast.Node) bool {
			switch expression := n.(type) {
			case *ast.SelectorExpr:
				// Covers the call, the function value, the method-expression
				// spelling and every alias of the import, because all of them
				// are the same selector to the parser.
				if ident, ok := expression.X.(*ast.Ident); ok &&
					localNames[ident.Name] && expression.Sel.Name == rule.symbol {
					found = append(found, environReference{
						pos:         expression.Pos(),
						what:        qualified,
						permittedIn: rule.permittedIn,
					})
					return false
				}
			case *ast.Ident:
				if dotImported && expression.Name == rule.symbol {
					found = append(found, environReference{
						pos:         expression.Pos(),
						what:        qualified + " under a dot import",
						permittedIn: rule.permittedIn,
					})
				}
			}
			return true
		})
	}

	found = append(found, environLinknameReferences(node)...)
	found = append(found, environLiteralReferences(node)...)

	sort.Slice(found, func(i, j int) bool { return found[i].pos < found[j].pos })
	return found
}

// environLinknameReferences finds a linker directive that binds a local
// declaration to a confined symbol. The import list says nothing about it — the
// file imports unsafe and names the target in a comment — so the selector scan
// above cannot see it.
func environLinknameReferences(node *ast.File) []environReference {
	var found []environReference
	for _, group := range node.Comments {
		for _, comment := range group.List {
			text := comment.Text
			if !strings.HasPrefix(text, "//go:linkname") {
				continue
			}
			for _, rule := range environRules {
				target := rule.defaultName + "." + rule.symbol
				if !slices.Contains(strings.Fields(text), target) {
					continue
				}
				found = append(found, environReference{
					pos:  comment.Pos(),
					what: "a linker directive bound to " + target,
					// A directive is never the permitted call, whatever file it
					// sits in: env.go reaches the symbol by importing os.
				})
			}
		}
	}
	return found
}

// environLiteralReferences finds the kernel's own copy of the environment named
// as a path. Shallow by construction — see the note on the rule type.
func environLiteralReferences(node *ast.File) []environReference {
	var found []environReference
	ast.Inspect(node, func(n ast.Node) bool {
		literal, ok := n.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			return true
		}
		if strings.Contains(value, procEnvironPath) {
			found = append(found, environReference{
				pos:  literal.Pos(),
				what: "the kernel's copy of the environment at " + procEnvironPath,
			})
		}
		return true
	})
	return found
}

// One case per route the attack pass found, so a later simplification of the
// scanner cannot quietly reopen one.
func TestEnvironBoundaryDetectsEveryRoute(t *testing.T) {
	linkname := "//go:" + "linkname pulled os.Environ"
	tests := []struct {
		name        string
		source      string
		wantCount   int
		wantWhat    string
		permittedIn string
	}{
		{
			name:        "plain call",
			source:      "package sample\nimport \"os\"\nvar v = os.Environ()\n",
			wantCount:   1,
			wantWhat:    "os.Environ",
			permittedIn: "internal/exec/env.go",
		},
		{
			name:        "aliased import",
			source:      "package sample\nimport environment \"os\"\nvar v = environment.Environ()\n",
			wantCount:   1,
			wantWhat:    "os.Environ",
			permittedIn: "internal/exec/env.go",
		},
		{
			name:        "dot import",
			source:      "package sample\nimport . \"os\"\nvar v = Environ()\n",
			wantCount:   1,
			wantWhat:    "os.Environ under a dot import",
			permittedIn: "internal/exec/env.go",
		},
		{
			name:        "function value",
			source:      "package sample\nimport \"os\"\nvar read = os.Environ\n",
			wantCount:   1,
			wantWhat:    "os.Environ",
			permittedIn: "internal/exec/env.go",
		},
		{
			name:        "function value in a composite literal",
			source:      "package sample\nimport \"os\"\ntype box struct{ read func() []string }\nvar b = box{read: os.Environ}\n",
			wantCount:   1,
			wantWhat:    "os.Environ",
			permittedIn: "internal/exec/env.go",
		},
		{
			name:        "handed to reflect",
			source:      "package sample\nimport (\n\t\"os\"\n\t\"reflect\"\n)\n\nvar v = reflect.ValueOf(os.Environ)\n",
			wantCount:   1,
			wantWhat:    "os.Environ",
			permittedIn: "internal/exec/env.go",
		},
		{
			name:      "the syscall underneath",
			source:    "package sample\nimport \"syscall\"\nvar v = syscall.Environ()\n",
			wantCount: 1,
			wantWhat:  "syscall.Environ",
		},
		{
			name:      "aliased syscall",
			source:    "package sample\nimport sys \"syscall\"\nvar v = sys.Environ\n",
			wantCount: 1,
			wantWhat:  "syscall.Environ",
		},
		{
			name:      "linker directive",
			source:    "package sample\n\nimport _ \"unsafe\"\n\n" + linkname + "\nfunc pulled() []string\n",
			wantCount: 1,
			wantWhat:  "a linker directive bound to os.Environ",
		},
		{
			name:      "the kernel's copy",
			source:    "package sample\nimport \"os\"\nvar v, _ = os.ReadFile(\"" + procEnvironPath + "\")\n",
			wantCount: 1,
			wantWhat:  "the kernel's copy of the environment at " + procEnvironPath,
		},
		{
			name:      "a build-tagged file the toolchain would skip",
			source:    "//go:build linux\n\npackage sample\n\nimport \"os\"\n\nvar v = os.Environ()\n",
			wantCount: 1,
			wantWhat:  "os.Environ",
			// Parsed whatever the host's tags say, which is why this scanner
			// stays an AST walk rather than becoming a go/packages load: a load
			// resolves build constraints and would never see this file on
			// Darwin.
			permittedIn: "internal/exec/env.go",
		},
		{
			name:      "a named single read is not a bulk read",
			source:    "package sample\nimport \"os\"\nvar v = os.Getenv(\"GH_TOKEN\") + os.LookupEnvName\n",
			wantCount: 0,
		},
		{
			name:      "a local method that happens to be called Environ",
			source:    "package sample\ntype box struct{}\nfunc (box) Environ() []string { return nil }\nvar v = box{}.Environ()\n",
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, err := parser.ParseFile(token.NewFileSet(), "sample.go", tt.source, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}

			found := environReferences(node)
			if len(found) != tt.wantCount {
				t.Fatalf("environment references = %d %v, want %d", len(found), describe(found), tt.wantCount)
			}
			if tt.wantCount == 0 {
				return
			}
			if found[0].what != tt.wantWhat {
				t.Errorf("what = %q, want %q", found[0].what, tt.wantWhat)
			}
			if found[0].permittedIn != tt.permittedIn {
				t.Errorf("permittedIn = %q, want %q", found[0].permittedIn, tt.permittedIn)
			}
		})
	}
}

func describe(references []environReference) []string {
	out := make([]string, 0, len(references))
	for _, reference := range references {
		out = append(out, fmt.Sprintf("%s(permitted in %q)", reference.what, reference.permittedIn))
	}
	return out
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
