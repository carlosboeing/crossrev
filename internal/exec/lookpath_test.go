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
// The eighth shape is a file carrying an execute bit that is not the caller's.
// bash asks access(2), not the mode, so a mode-0010 file the caller owns is not
// executable to the caller and never takes the preferred slot. Planted as xg:
//
//	$ PATH=xg    bash -c 'command -v tool'  → xg/tool, exit 0   (the fallback)
//	$ PATH=nd:xg bash -c 'command -v tool'  → nothing, exit 1
//	$ PATH=xg:xo bash -c 'command -v tool'  → xo/tool, exit 0   (xo is mode 0100)
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
	// groupOnly holds a yq whose only execute bit belongs to the group, which
	// the caller does not get because the caller owns the file: access(2) stops
	// at the owner class. It is executable to the mode and not to the caller.
	groupOnly := filepath.Join(root, "xg")
	for path, mode := range map[string]os.FileMode{unreadable: 0o644, program: 0o755, groupOnly: 0o010} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("make %s: %v", path, err)
		}
		if err := os.WriteFile(filepath.Join(path, "yq"), []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatalf("write %s/yq: %v", path, err)
		}
		// os.WriteFile subtracts the umask, and 0o010 is exactly what the
		// umask usually removes.
		if err := os.Chmod(filepath.Join(path, "yq"), mode); err != nil {
			t.Fatalf("chmod %s/yq: %v", path, err)
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
		{"a file whose execute bit is not the caller's", list(groupOnly), filepath.Join(groupOnly, "yq")},
		{"the directory ahead of the file the caller may not run", list(directory, groupOnly), ""},
		{"that file ahead of one the caller may run", list(groupOnly, program), filepath.Join(program, "yq")},
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
// with the directory itself. Measured, GNU bash 3.2.57: `PATH="x1" command -v
// ""` and `PATH="x1:" command -v ""` both exit 1.
//
// The early return is not the only reason this refuses, and no test can make it
// be. shellJoin("", name) with an empty name answers a path ending in a
// separator, os.Stat of that resolves to a directory or fails with ENOTDIR, and
// neither is a regular file — so the fallback slot is never filled and the
// search refuses on its own. Removing the guard is behaviour-preserving, which
// is why this test passes with and without it.
func TestLookPathRefusesTheEmptyName(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if got, err := exec.LookPath(""); err == nil {
		t.Errorf(`LookPath("") = %q, want a refusal`, got)
	}
}

// An empty PATH element is the current directory, and the bytes bash answers
// with are the ones it built: `./name`, not the `name` filepath.Join would
// clean it down to. A literal `.` element answers the same way, which is what
// makes the two one rule rather than two.
//
// Measured from a directory holding an executable `tool` and a mode-0644
// `plain`:
//
//	$ PATH=":/nonexistent" bash -c 'command -v tool'   → ./tool,  exit 0
//	$ PATH="/nonexistent:" bash -c 'command -v tool'   → ./tool,  exit 0
//	$ PATH="."             bash -c 'command -v tool'   → ./tool,  exit 0
//	$ PATH=":"             bash -c 'command -v plain'  → ./plain, exit 0
//
// The last row is the fallback slot reached through an empty element: the
// current directory holds a regular file with no execute bit, so it answers.
func TestLookPathReadsAnEmptyPathElementAsTheCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write plain: %v", err)
	}
	t.Chdir(dir)

	sep := string(os.PathListSeparator)
	for _, row := range []struct{ name, path, tool, want string }{
		{"an empty element ahead of a missing directory", sep + "/nonexistent", "tool", "./tool"},
		{"an empty element behind one", "/nonexistent" + sep, "tool", "./tool"},
		{"a dot element", ".", "tool", "./tool"},
		{"the fallback slot reached through an empty element", sep, "plain", "./plain"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Setenv("PATH", row.path)
			got, err := exec.LookPath(row.tool)
			if err != nil {
				t.Fatalf("LookPath(%q) with PATH=%q = %v, want %q", row.tool, row.path, err, row.want)
			}
			if got != row.want {
				t.Errorf("LookPath(%q) with PATH=%q = %q, want %q", row.tool, row.path, got, row.want)
			}
		})
	}
}

// A name carrying a separator must name a regular file. A directory carries an
// execute bit and is not a program, so access(2) alone is not the test:
// isProgram asks for a regular file first.
//
// Measured with an `adir/` planted as a mode-0755 directory, GNU bash 3.2.57:
//
//	$ bash -c 'command -v ./adir'        → nothing, exit 1
//	$ bash -c 'command -v /abs/adir'     → nothing, exit 1
//
// The same fact is what lib/run.sh:530's `command -v` answers for a directory
// named like a tool, and lookpath.go:36-40 records it for the search half.
func TestLookPathRefusesADirectoryNamedWithASeparator(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "adir")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("make %s: %v", directory, err)
	}
	// A PATH holding a program of the same base name must not rescue it.
	rescue := filepath.Join(root, "rescue")
	if err := os.MkdirAll(rescue, 0o755); err != nil {
		t.Fatalf("make %s: %v", rescue, err)
	}
	if err := os.WriteFile(filepath.Join(rescue, "adir"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write %s/adir: %v", rescue, err)
	}
	t.Setenv("PATH", rescue)

	if got, err := exec.LookPath(directory); err == nil {
		t.Errorf("LookPath(%q) = %q, want a refusal: a directory is not a program", directory, got)
	}
	t.Chdir(root)
	if got, err := exec.LookPath("./adir"); err == nil {
		t.Errorf(`LookPath("./adir") = %q, want a refusal: a directory is not a program`, got)
	}
}

// A name carrying a separator is answered with the bytes the caller passed, not
// with a cleaned path. bash hands back what it was given, and a caller that
// prints the answer or compares it against its own argument sees the difference.
//
// Measured, GNU bash 3.2.57, from a directory holding a mode-0755 `x` and
// `x1/zzt`:
//
//	$ bash -c 'command -v ./x'              → ./x
//	$ bash -c 'command -v ./x1/../x1/zzt'   → ./x1/../x1/zzt
//
// lookpath.go:57-58 records the refusal half of the same measurement.
func TestLookPathAnswersANameWithASeparatorVerbatim(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "x1"), 0o755); err != nil {
		t.Fatalf("make x1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "x"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write x: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "x1", "zzt"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write x1/zzt: %v", err)
	}
	t.Chdir(root)
	t.Setenv("PATH", "/nonexistent")

	for _, name := range []string{"./x", "./x1/../x1/zzt"} {
		got, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("LookPath(%q) = %v, want %q", name, err, name)
		}
		if got != name {
			t.Errorf("LookPath(%q) = %q, want the bytes back unchanged", name, got)
		}
	}
}

// bash drops one trailing separator from a PATH element and glues the name on,
// so the separators the element carries past the first survive into the answer.
//
// Measured, GNU bash 3.2.57, from a directory holding `x1/zzt` at mode 0755:
//
//	$ bash -c 'PATH="x1/"  command -v zzt'  → x1/zzt
//	$ bash -c 'PATH="x1//" command -v zzt'  → x1//zzt
//
// lookpath.go:95-99 records the same measurement for shellJoin.
func TestLookPathKeepsTheSeparatorsAPathElementCarries(t *testing.T) {
	root := t.TempDir()
	program := filepath.Join(root, "x1")
	if err := os.MkdirAll(program, 0o755); err != nil {
		t.Fatalf("make x1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(program, "yq"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write x1/yq: %v", err)
	}

	separator := string(os.PathSeparator)
	for _, row := range []struct{ name, path, want string }{
		{"one trailing separator", program + separator, program + separator + "yq"},
		{"two trailing separators", program + separator + separator, program + separator + separator + "yq"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Setenv("PATH", row.path)
			got, err := exec.LookPath("yq")
			if err != nil {
				t.Fatalf("LookPath with PATH=%q = %v, want %q", row.path, err, row.want)
			}
			if got != row.want {
				t.Errorf("LookPath with PATH=%q = %q, want %q", row.path, got, row.want)
			}
		})
	}
}

// With an executable in more than one PATH entry, the first entry in PATH order
// wins. The search is executable-preferred over the whole list and first-match
// among the executables, which is one rule and not two.
//
// Measured, GNU bash 3.2.57, with `zzt` at mode 0755 in both x1 and x2:
//
//	$ bash -c 'PATH="x1:x2" command -v zzt'  → x1/zzt
//	$ bash -c 'PATH="x2:x1" command -v zzt'  → x2/zzt
func TestLookPathAnswersTheFirstExecutableInPathOrder(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "x1")
	second := filepath.Join(root, "x2")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "yq"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write %s/yq: %v", dir, err)
		}
	}

	separator := string(os.PathListSeparator)
	for _, row := range []struct{ name, path, want string }{
		{"the first entry holds it", first + separator + second, filepath.Join(first, "yq")},
		{"the order reversed", second + separator + first, filepath.Join(second, "yq")},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Setenv("PATH", row.path)
			got, err := exec.LookPath("yq")
			if err != nil {
				t.Fatalf("LookPath with PATH=%q = %v, want %q", row.path, err, row.want)
			}
			if got != row.want {
				t.Errorf("LookPath with PATH=%q = %q, want %q", row.path, got, row.want)
			}
		})
	}
}
