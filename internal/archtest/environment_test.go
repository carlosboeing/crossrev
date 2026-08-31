package archtest_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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
// os.Getenv and os.LookupEnv are deliberately NOT confined. Calling that an
// allowlist of one would overstate it: an allowlist of one is safe only when
// the one is not a credential, and os.LookupEnv("GH_TOKEN") is exactly the
// route a review found. The reason to leave them alone is that the guard
// belongs at the destination rather than the read. The shell itself does a
// named single read — lib/adapters/claude.sh:82 reads an endpoint token out of
// the environment and :91 puts it into the child — so a rule forbidding the
// read would be stricter than the thing being ported, and would break
// internal/config/load.go, which resolves XDG_CONFIG_HOME and HOME this way.
// What stops a named credential reaching a harness is NewOSRunner, which
// refuses it at Run. syscall.Getenv is the same shape and left alone for the
// same reason.
//
// /proc/self/environ is the kernel's own copy, and reading it would sidestep
// every rule above on the Linux that CI runs on. The literal path is caught
// below. That check is shallow on purpose and is documented as such: a path
// assembled from pieces at run time defeats it, and no test that reads source
// can close that.
//
// # What this test cannot close
//
//   - What cgo does once admitted. `extern char **environ` reaches the same
//     array with no Go symbol to match on. Whether cgo is admitted at all IS
//     closable, and is closed below: `import "C"` is an ordinary import.
//   - A path to /proc built at run time, as above.
//   - Nothing about go:linkname, in the end. The directive is caught below, but
//     the real guard is the Go linker: since Go 1.23 a pull-linkname to a
//     standard library symbol that is not marked linkname-able fails to link.
//     Verified on this module — the link refuses with "invalid reference to
//     os.Environ" unless the build passes -ldflags=-checklinkname=0, which
//     nothing here does. The source check is a second guard that fires at test
//     time instead of link time, and it is deliberately literal: a directive
//     naming some other lowercase symbol would pass it, and the linker would
//     still refuse.
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

	permittedSeen := make(map[string]bool)
	walkBoundarySources(t, root, func(relSlash string, src []byte) {
		violations, permitted, err := auditEnvironSource(relSlash, src)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", relSlash, err)
		}
		for _, violation := range violations {
			t.Error(violation)
		}
		for _, held := range permitted {
			permittedSeen[held] = true
		}
	})

	// The rule must not pass because the scan found nothing. The permitted file
	// is supposed to hold the reference the rule exists to allow; if it does
	// not, the scanner is not looking where it thinks it is.
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

// auditEnvironSource is the whole verdict for one file: the parse, the routes,
// and the decision about which file may hold which.
//
// It takes source rather than a parsed file so that the parse mode is inside
// the function its own fixtures exercise. A file parsed without
// parser.ParseComments holds no linker directives, and a scanner that lost the
// mode would report a clean module.
func auditEnvironSource(relSlash string, src []byte) (violations []string, permitted []string, err error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, relSlash, src, parser.ParseComments)
	if err != nil {
		return nil, nil, err
	}

	for _, reference := range environReferences(node) {
		// Exact equality, never a suffix. "vendor/internal/exec/env.go" ends
		// with the permitted name and is a different file.
		if reference.permittedIn != "" && reference.permittedIn == relSlash {
			permitted = append(permitted, relSlash)
			continue
		}
		where := "no file may hold one"
		if reference.permittedIn != "" {
			where = "only " + reference.permittedIn + " may hold one"
		}
		violations = append(violations, fmt.Sprintf(
			"%s:%d reaches the whole process environment through %s; %s",
			relSlash, fset.Position(reference.pos).Line, reference.what, where))
	}
	return violations, permitted, nil
}

// The verdict itself, over sources held in memory. TestEnvironBoundaryDetectsEveryRoute
// below checks the detector; this checks what the walk does with what the
// detector found, which is the half that decides whether anything is reported
// at all.
func TestEnvironAuditVerdict(t *testing.T) {
	linkname := "//go:" + "linkname pulled os.Environ"

	tests := []struct {
		name          string
		file          string
		source        string
		wantViolation bool
		wantPermitted bool
		mustMention   string
	}{
		{
			name:          "the permitted file holds the permitted reference",
			file:          "internal/exec/env.go",
			source:        "package exec\nimport \"os\"\nvar v = os.Environ()\n",
			wantPermitted: true,
		},
		{
			name:          "the same reference anywhere else",
			file:          "internal/harness/harness.go",
			source:        "package harness\nimport \"os\"\nvar v = os.Environ()\n",
			wantViolation: true,
			mustMention:   "only internal/exec/env.go may hold one",
		},
		{
			// A path that ends with the permitted name is a different file. A
			// suffix match here would grant every one of them the exemption.
			name:          "a file whose path merely ends with the permitted one",
			file:          "vendored/internal/exec/env.go",
			source:        "package exec\nimport \"os\"\nvar v = os.Environ()\n",
			wantViolation: true,
		},
		{
			// Needs parser.ParseComments. Without it this file is clean.
			name:          "a linker directive",
			file:          "internal/harness/link.go",
			source:        "package harness\n\nimport _ \"unsafe\"\n\n" + linkname + "\nfunc pulled() []string\n",
			wantViolation: true,
			mustMention:   "linker directive",
		},
		{
			// Needs strings.Contains. An equality test misses every path that
			// merely holds the needle.
			name:          "the kernel's copy inside a longer path",
			file:          "internal/harness/host.go",
			source:        "package harness\nimport \"os\"\nvar v, _ = os.ReadFile(\"/host" + procEnvironPath + "\")\n",
			wantViolation: true,
		},
		{
			name:          "cgo",
			file:          "internal/harness/bridge.go",
			source:        "package harness\n\nimport \"C\"\n\nvar v = 1\n",
			wantViolation: true,
			mustMention:   "cgo",
		},
		{
			name:   "a file with nothing to say",
			file:   "internal/harness/plain.go",
			source: "package harness\nimport \"strings\"\nvar v = strings.TrimSpace(\" x \")\n",
		},
		{
			// Even in the permitted file: syscall.Environ is permitted nowhere.
			name:          "the syscall underneath, in the permitted file",
			file:          "internal/exec/env.go",
			source:        "package exec\nimport \"syscall\"\nvar v = syscall.Environ()\n",
			wantViolation: true,
			mustMention:   "no file may hold one",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations, permitted, err := auditEnvironSource(tt.file, []byte(tt.source))
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}

			if got := len(violations) > 0; got != tt.wantViolation {
				t.Fatalf("violations = %v, want a violation: %t", violations, tt.wantViolation)
			}
			if got := len(permitted) > 0; got != tt.wantPermitted {
				t.Errorf("permitted = %v, want a permitted reference: %t", permitted, tt.wantPermitted)
			}
			if tt.mustMention == "" {
				return
			}
			if !strings.Contains(violations[0], tt.mustMention) {
				t.Errorf("violation %q does not mention %q", violations[0], tt.mustMention)
			}
			if !strings.HasPrefix(violations[0], tt.file+":") {
				t.Errorf("violation %q does not name the file and line", violations[0])
			}
		})
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
	found = append(found, cgoImportReferences(node)...)

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

// cgoImportReferences finds a file that admits cgo.
//
// `extern char **environ` is a C global that every rule above is blind to, and
// no test that reads Go source can follow what a cgo preamble does. What it can
// do is refuse the door: `import "C"` is an ordinary import declaration, and
// nothing in this module holds one — `grep -rln 'import "C"' --include='*.go'`
// returns nothing — so the rule costs no exemption. The tree-sitter dependency
// is a separate module and is unaffected.
func cgoImportReferences(node *ast.File) []environReference {
	var found []environReference
	for _, imported := range node.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil || path != "C" {
			continue
		}
		found = append(found, environReference{
			pos:  imported.Pos(),
			what: `cgo, which reaches the environment as the C global "environ"`,
		})
	}
	return found
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
