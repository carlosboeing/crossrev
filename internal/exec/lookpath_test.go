package exec_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// The whole contract of the PATH search, in one table.
//
// bash's search is executable-preferred, not first-match, and it does not skip
// a directory named like the tool — the directory takes the fallback slot and
// then loses it, because the fallback is an answer only when it is a regular
// file. Every row was measured with `zzt` planted three ways: a directory (nd),
// a mode-0644 file (f1) and a mode-0755 file (x1).
//
//	$ PATH=nd    bash -c 'command -v zzt'  → nothing, exit 1
//	$ PATH=f1    bash -c 'command -v zzt'  → f1/zzt, exit 0
//	$ PATH=nd:f1 bash -c 'command -v zzt'  → nothing, exit 1
//	$ PATH=f1:nd bash -c 'command -v zzt'  → f1/zzt, exit 0
//	$ PATH=nd:x1 bash -c 'command -v zzt'  → x1/zzt
//	$ PATH=f1:x1 bash -c 'command -v zzt'  → x1/zzt
//	$ PATH=x1:f1 bash -c 'command -v zzt'  → x1/zzt
//
// This is the one copy. internal/preflight, internal/review, internal/resolve
// and internal/initcmd each had a private search; three of the four accepted a
// directory named like the tool, and none of the three had a row for it.
func TestLookPathPrefersAnExecutableAndFallsBackToTheFirstFile(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "nd")
	unreadable := filepath.Join(root, "f1")
	program := filepath.Join(root, "x1")
	if err := os.MkdirAll(filepath.Join(directory, "yq"), 0o755); err != nil {
		t.Fatalf("make the directory named like the tool: %v", err)
	}
	for path, mode := range map[string]os.FileMode{unreadable: 0o644, program: 0o755} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("make %s: %v", path, err)
		}
		if err := os.WriteFile(filepath.Join(path, "yq"), []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatalf("write %s/yq: %v", path, err)
		}
	}

	// A symlink to a program is a program, which is why the search stats
	// through the link rather than at it.
	linked := filepath.Join(root, "sl")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatalf("make %s: %v", linked, err)
	}
	if err := os.Symlink(filepath.Join(program, "yq"), filepath.Join(linked, "yq")); err != nil {
		t.Fatalf("link yq: %v", err)
	}

	list := func(dirs ...string) string { return strings.Join(dirs, string(os.PathListSeparator)) }
	for _, row := range []struct {
		name string
		path string
		// want is the path the search must answer with, empty for a refusal.
		// The shared function returns it, which the four private copies did
		// not let any caller observe.
		want string
	}{
		{"a directory named like the tool", list(directory), ""},
		{"a file with no execute bit", list(unreadable), filepath.Join(unreadable, "yq")},
		{"the directory ahead of that file", list(directory, unreadable), ""},
		{"that file ahead of the directory", list(unreadable, directory), filepath.Join(unreadable, "yq")},
		{"an executable behind a directory", list(directory, program), filepath.Join(program, "yq")},
		{"an executable behind a file with no execute bit", list(unreadable, program), filepath.Join(program, "yq")},
		{"an executable ahead of both", list(program, unreadable, directory), filepath.Join(program, "yq")},
		{"a symlink to an executable", list(linked), filepath.Join(linked, "yq")},
		{"an empty PATH", "", ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Setenv("PATH", row.path)
			got, err := exec.LookPath("yq")
			if row.want == "" {
				if err == nil {
					t.Fatalf("LookPath with PATH=%q = %q, want a refusal", row.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("LookPath with PATH=%q = %v, want %q", row.path, err, row.want)
			}
			if got != row.want {
				t.Errorf("LookPath with PATH=%q = %q, want %q", row.path, got, row.want)
			}
		})
	}
}

// A name carrying a separator is not searched and takes no fallback: measured,
// `command -v ./f` on a mode-0644 file exits 1 where `./x` answers `./x`.
func TestLookPathDoesNotSearchAName(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "f")
	program := filepath.Join(root, "x")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", plain, err)
	}
	if err := os.WriteFile(program, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", program, err)
	}
	// A PATH holding an executable of the same base name must not rescue the
	// non-executable path, which is what proves the search is skipped.
	rescue := filepath.Join(root, "rescue")
	if err := os.MkdirAll(rescue, 0o755); err != nil {
		t.Fatalf("make %s: %v", rescue, err)
	}
	if err := os.WriteFile(filepath.Join(rescue, "f"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write %s/f: %v", rescue, err)
	}
	t.Setenv("PATH", rescue)

	if got, err := exec.LookPath(plain); err == nil {
		t.Errorf("LookPath(%q) = %q, want a refusal: it has no execute bit", plain, got)
	}
	if got, err := exec.LookPath(program); err != nil || got != program {
		t.Errorf("LookPath(%q) = %q, %v; want the path back", program, got, err)
	}
	if got, err := exec.LookPath(filepath.Join(root, "nothing")); err == nil {
		t.Errorf("LookPath on a missing path = %q, want a refusal", got)
	}
}

// The empty name is not a search over PATH for "": every entry would answer
// with the directory itself.
func TestLookPathRefusesTheEmptyName(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if got, err := exec.LookPath(""); err == nil {
		t.Errorf(`LookPath("") = %q, want a refusal`, got)
	}
}
