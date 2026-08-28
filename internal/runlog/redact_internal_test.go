package runlog

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var errFilterDied = errors.New("the filter died")

func failingLog(t *testing.T, dir string) *Log {
	t.Helper()
	l, err := Open(Options{Dir: dir, Repo: "acme/widget", PR: "7"})
	if err != nil {
		t.Fatalf("opening the log: %v", err)
	}
	l.redact = func([]byte) ([]byte, error) { return nil, errFilterDied }
	return l
}

// TestPublishFailsClosed is the branch the security promise rests on: a body
// that could not be filtered is exactly the body that might carry the
// credential, so the text is withheld and the caller is told (lib/log.sh:143-157).
func TestPublishFailsClosed(t *testing.T) {
	dir := t.TempDir()
	l := failingLog(t, dir)

	got, err := l.Publish("the token is ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if !errors.Is(err, errFilterDied) {
		t.Fatalf("Publish err = %v, want the filter's own error", err)
	}
	if got != WithheldText {
		t.Errorf("Publish = %q, want the withheld notice %q", got, WithheldText)
	}
	if strings.Contains(got, "ghp_") {
		t.Errorf("the withheld body still carries the credential: %q", got)
	}

	log, err := os.ReadFile(filepath.Join(dir, logFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "redact publish filter failed; body withheld") {
		t.Errorf("the run log does not record the withheld body:\n%s", log)
	}
}

// TestRedactFileFailsClosed: a filter error must not leave the original on
// disk, so the unredacted copy is replaced with a notice (lib/log.sh:180-185).
func TestRedactFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	l := failingLog(t, dir)
	path := filepath.Join(dir, "review.attempt-1.stdout")
	if err := os.WriteFile(path, []byte("ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	l.RedactFile(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "redaction failed; original discarded\n" {
		t.Errorf("file = %q, want the discard notice", got)
	}
	log, err := os.ReadFile(filepath.Join(dir, logFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "redact failed "+path) {
		t.Errorf("the run log does not record the failure:\n%s", log)
	}
}

// TestNothingInThisPackageChangesAMode is how "never briefly wider" is checked
// rather than asserted.
//
// A final stat can only ever say what the mode ended up as, and a
// create-then-narrow implementation ends up at the same 0600 as a create-at-0600
// one. The difference between them is a chmod, and there is exactly one way to
// widen a file and then narrow it: call one. So the check is that no chmod is
// reachable from this package's production source at all — every file and
// directory here gets its mode from the create call that makes it, which the
// kernel applies as the inode appears.
func TestNothingInThisPackageChangesAMode(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			selector, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if strings.Contains(selector.Sel.Name, "Chmod") {
				t.Errorf("%s references %s; a mode set after creation is a window in which the file was wider",
					name, selector.Sel.Name)
			}
			return true
		})
	}
	// The scan must not pass because it looked at nothing.
	if scanned < 4 {
		t.Errorf("scanned %d production files, expected the whole package", scanned)
	}
}
