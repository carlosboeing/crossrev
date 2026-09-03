package app_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/app"
)

// fakeEnv answers the two variables the paths are built from, without touching
// the environment the test binary runs in.
type fakeEnv map[string]string

func (e fakeEnv) Getenv(name string) string { return e[name] }

func TestDirReadsXDGConfigHome(t *testing.T) {
	got := app.Dir(fakeEnv{"XDG_CONFIG_HOME": "/x", "HOME": "/h"})
	if want := "/x/crossrev/apps"; got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

func TestDirFallsBackToHomeConfig(t *testing.T) {
	got := app.Dir(fakeEnv{"HOME": "/h"})
	if want := "/h/.config/crossrev/apps"; got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

// `${XDG_CONFIG_HOME:-...}` takes the fallback when the variable is set and
// empty, not only when it is unset. `${XDG_CONFIG_HOME-...}` would not.
func TestDirTreatsEmptyXDGAsUnset(t *testing.T) {
	got := app.Dir(fakeEnv{"XDG_CONFIG_HOME": "", "HOME": "/h"})
	if want := "/h/.config/crossrev/apps"; got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

// The shell concatenates, so a trailing separator survives into the path
// `auth status` prints beside the key. filepath.Join would clean it away and
// print a path the shell never printed. Measured: XDG_CONFIG_HOME=/x/ gives
// /x//crossrev/apps.
func TestDirKeepsADoubledSeparator(t *testing.T) {
	got := app.Dir(fakeEnv{"XDG_CONFIG_HOME": "/x/"})
	if want := "/x//crossrev/apps"; got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

// With neither set the shell builds "/.config/crossrev/apps" rather than
// refusing. Nothing depends on that being useful; it is here so the port does
// not invent a check the shell does not make.
func TestDirWithNothingSet(t *testing.T) {
	got := app.Dir(fakeEnv{})
	if want := "/.config/crossrev/apps"; got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

func TestTokensPath(t *testing.T) {
	if got, want := app.TokensPath(fakeEnv{"XDG_CONFIG_HOME": "/x"}), "/x/crossrev/tokens.json"; got != want {
		t.Fatalf("TokensPath = %q, want %q", got, want)
	}
	// The ledger sits beside the apps directory, not inside it.
	if got, want := app.TokensPath(fakeEnv{"HOME": "/h"}), "/h/.config/crossrev/tokens.json"; got != want {
		t.Fatalf("TokensPath = %q, want %q", got, want)
	}
}

// touch creates an empty regular file, which is what `-f` tests for.
func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestPEMPathPrefersTheRoledName(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "acme.loop.pem"))
	touch(t, filepath.Join(dir, "acme.pem"))

	got := app.PEMPath(dir, "acme", "loop")
	if want := filepath.Join(dir, "acme.loop.pem"); got != want {
		t.Fatalf("PEMPath = %q, want %q", got, want)
	}
}

// A key registered before roles existed sits at <owner>.pem and belongs to the
// loop. The loop role reads it when the roled name is absent, so an existing
// install keeps working with no migration step.
func TestPEMPathFallsBackToTheLegacyName(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "acme.pem"))

	got := app.PEMPath(dir, "acme", "loop")
	if want := filepath.Join(dir, "acme.pem"); got != want {
		t.Fatalf("PEMPath = %q, want %q", got, want)
	}
}

func TestPEMPathWithNeitherOnDiskNamesTheRoledOne(t *testing.T) {
	dir := t.TempDir()
	got := app.PEMPath(dir, "acme", "loop")
	if want := filepath.Join(dir, "acme.loop.pem"); got != want {
		t.Fatalf("PEMPath = %q, want %q", got, want)
	}
}

// The fallback is the loop's alone. A refresher key never had a legacy name, so
// finding <owner>.pem must not divert the refresher onto the loop's key — that
// is the privilege separation the two Apps exist to draw.
func TestPEMPathDoesNotFallBackForTheRefresher(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "acme.pem"))

	got := app.PEMPath(dir, "acme", "refresher")
	if want := filepath.Join(dir, "acme.refresher.pem"); got != want {
		t.Fatalf("PEMPath = %q, want %q", got, want)
	}
}

// `${2:-loop}`: the role argument is optional and defaults to the loop.
func TestPEMPathDefaultsTheRole(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "acme.pem"))

	if got, want := app.PEMPath(dir, "acme", ""), filepath.Join(dir, "acme.pem"); got != want {
		t.Fatalf("PEMPath = %q, want %q", got, want)
	}
	if got, want := app.MetaPath(dir, "acme", ""), filepath.Join(dir, "acme.loop.json"); got != want {
		t.Fatalf("MetaPath = %q, want %q", got, want)
	}
}

// `-f` is a regular-file test. A directory named acme.pem is not a key, and
// diverting onto it would report a path no key can be read from.
func TestPEMPathIgnoresADirectoryAtTheLegacyName(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "acme.pem"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got := app.PEMPath(dir, "acme", "loop")
	if want := filepath.Join(dir, "acme.loop.pem"); got != want {
		t.Fatalf("PEMPath = %q, want %q", got, want)
	}
}

func TestMetaPathPrefersTheRoledName(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "acme.loop.json"))
	touch(t, filepath.Join(dir, "acme.json"))

	got := app.MetaPath(dir, "acme", "loop")
	if want := filepath.Join(dir, "acme.loop.json"); got != want {
		t.Fatalf("MetaPath = %q, want %q", got, want)
	}
}

func TestMetaPathFallsBackToTheLegacyName(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "acme.json"))

	got := app.MetaPath(dir, "acme", "loop")
	if want := filepath.Join(dir, "acme.json"); got != want {
		t.Fatalf("MetaPath = %q, want %q", got, want)
	}
}

func TestMetaPathDoesNotFallBackForTheRefresher(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "acme.json"))

	got := app.MetaPath(dir, "acme", "refresher")
	if want := filepath.Join(dir, "acme.refresher.json"); got != want {
		t.Fatalf("MetaPath = %q, want %q", got, want)
	}
}

// The pair a caller holds: the two files of one App, in one directory, named
// for the same owner and role.
func TestPathsAreBuiltFromTheDirectoryTheyAreGiven(t *testing.T) {
	got := app.PEMPath("/x/crossrev/apps", "ShoreLogic", "refresher")
	if want := "/x/crossrev/apps/ShoreLogic.refresher.pem"; got != want {
		t.Fatalf("PEMPath = %q, want %q", got, want)
	}
	got = app.MetaPath("/x/crossrev/apps", "ShoreLogic", "refresher")
	if want := "/x/crossrev/apps/ShoreLogic.refresher.json"; want != got {
		t.Fatalf("MetaPath = %q, want %q", got, want)
	}
}

// OSEnvironment is what production wiring passes. The read is os.Getenv, which
// internal/archtest deliberately leaves unconfined (environment_test.go:32-42);
// the confined call is the bulk read, os.Environ, and nothing here makes one.
func TestOSEnvironmentReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/from-the-process")
	if got, want := app.Dir(app.OSEnvironment{}), "/from-the-process/crossrev/apps"; got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}
