package archtest_test

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// `gh api -F key=value` reads the value from a file when it starts with `@`,
// and from stdin when it is exactly `@-`. `-f` does not: it sends the text as
// written. Measured against the installed `gh`, with a file holding a marker:
//
//	-f probe=@/tmp/probe  ->  ?probe=%40%2Ftmp%2Fprobe
//	-F probe=@/tmp/probe  ->  ?probe=MARKER-SECRET-VALUE
//
// Every value the port sends under `-F` today is a number or a repository
// owner and name, none of which a model writes and none of which may hold an
// `@`. Every value a model does write — a comment body, an issue title, a
// review comment's path — goes under `-f`.
//
// That is a property of the argument each call site chose, and nothing held it
// there. Moving one body from `-f` to `-F` would make the orchestrator read a
// local credential file and publish its bytes, because the process composing
// that body is the one that read the pull request, and the `gh` child holds
// the four forge tokens. This is the rule that keeps them apart.
//
// The shell has the same property at lib/github.sh:75, :120 and :216, where
// every `-F` is a page number, a line number or a repository field.
func TestNoTypedGhFieldTakesTextAModelWrote(t *testing.T) {
	root := findRepoRoot(t)
	pkgs := loadModulePackages(t)

	// The complete list of keys the port may send as a typed field. Each is a
	// number or a repository identifier; a repository name cannot hold an `@`.
	allowed := map[string]bool{
		"line": true, "per_page": true, "page": true,
		"owner": true, "name": true, "number": true,
	}

	typedFields := 0
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			position := pkg.Fset.Position(file.Pos())
			relPath, err := filepath.Rel(root, position.Filename)
			if err != nil {
				continue
			}
			path := filepath.ToSlash(relPath)
			if !strings.HasPrefix(path, ghClientDir+"/") || strings.HasSuffix(path, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				list, ok := n.(*ast.CallExpr)
				var args []ast.Expr
				if ok {
					args = list.Args
				} else if composite, isLit := n.(*ast.CompositeLit); isLit {
					args = composite.Elts
				} else {
					return true
				}
				for at, arg := range args {
					if literalText(arg) != "-F" {
						continue
					}
					typedFields++
					line := pkg.Fset.Position(arg.Pos()).Line
					if at+1 >= len(args) {
						t.Errorf("%s:%d passes -F with nothing after it", path, line)
						continue
					}
					key := typedFieldKey(args[at+1])
					if key == "" {
						t.Errorf("%s:%d passes -F a key this rule cannot read; "+
							"write it as a literal `key=` prefix so the rule can", path, line)
						continue
					}
					if !allowed[key] {
						t.Errorf("%s:%d sends %q as a typed field. `gh api -F` reads a value "+
							"beginning with `@` from a file and `@-` from stdin, so a typed field "+
							"may only take a number or a repository identifier. Use -f.",
							path, line, key)
					}
				}
				return true
			})
		}
	}

	// A rule that scanned nothing proves nothing.
	if typedFields == 0 {
		t.Fatalf("found no -F argument under %s; the scan did not reach the client", ghClientDir)
	}
}

// literalText answers an unquoted string literal, or the empty string.
func literalText(expr ast.Expr) string {
	basic, ok := expr.(*ast.BasicLit)
	if !ok || basic.Kind != token.STRING {
		return ""
	}
	unquoted, err := strconv.Unquote(basic.Value)
	if err != nil {
		return ""
	}
	return unquoted
}

// typedFieldKey answers the `key` of a `key=…` argument, whether it is written
// whole or built by concatenation. An expression whose leftmost part is not a
// literal answers the empty string, which the caller reports rather than skips.
func typedFieldKey(expr ast.Expr) string {
	for {
		binary, ok := expr.(*ast.BinaryExpr)
		if !ok {
			break
		}
		expr = binary.X
	}
	text := literalText(expr)
	at := strings.Index(text, "=")
	if at < 0 {
		return ""
	}
	return text[:at]
}
