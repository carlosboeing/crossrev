package vcs_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// The vectors were captured from the Bash functions once, and Go reads the same
// files at the same paths and never writes them. A test that could regenerate
// its own oracle is not an oracle.
const (
	slugFixture  = "tests/fixtures/parity/github_slug.json"
	pushFixture  = "tests/fixtures/parity/push_target.json"
	pathsFixture = "tests/fixtures/parity/paths.json"
)

// pinnedDates stop a commit object from depending on when the test ran.
// tests/capture-parity.sh:627 pins the same two for the same reason.
//
// The identity is deliberately NOT pinned here. GIT_AUTHOR_NAME and its three
// siblings outrank `git -c user.name=…`, so a test environment holding them
// would hide whatever identity the code under test chose. Every commit these
// tests make passes its own identity on the command line instead.
var pinnedDates = []string{
	"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
	"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
}

// testIdentity is what a helper commit signs itself as.
var testIdentity = []string{"-c", "user.name=crossrev", "-c", "user.email=test@example.com"}

// testGit builds a Git whose children see a pinned environment and no
// configuration but the repository's own.
//
// PATH is inherited because git starts helpers of its own. HOME points at a
// directory inside the test, and both config scopes are pointed at the null
// device, so a global `url.<base>.pushInsteadOf` on the machine running the
// suite cannot change what a push-target case resolves to.
func testGit(t *testing.T) *vcs.Git {
	t.Helper()
	home := t.TempDir()
	env := append([]string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	}, pinnedDates...)
	env = append(env, exec.Inherit([]string{"PATH"})...)
	return vcs.New(exec.NewOSRunner(), env)
}

// realTempDir is a temporary directory with every symlink already resolved.
//
// t.TempDir answers under /var on macOS, and /var is itself a symlink to
// /private/var. A containment test rooted at the unresolved path compares a
// resolved child against an unresolved root and reports an escape that is only
// the symlink; rooted at the resolved path, a real escape is the only thing
// that shows.
func realTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve the temporary root: %v", err)
	}
	return resolved
}

// initRepo makes an empty repository at dir and returns it.
func initRepo(t *testing.T, git *vcs.Git, dir string) *vcs.Repository {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make %s: %v", dir, err)
	}
	repo := git.At(dir)
	mustGit(t, repo, "init", "-q", "-b", "main", ".")
	return repo
}

// mustGit runs one git call and fails the test unless it exits zero.
func mustGit(t *testing.T, repo *vcs.Repository, args ...string) vcs.Output {
	t.Helper()
	output, err := repo.Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	if !output.OK() {
		t.Fatalf("git %s exited %d: %s", strings.Join(args, " "), output.ExitCode, output.Stderr)
	}
	return output
}

// write puts content at a path inside dir, making the parents it needs.
func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("make the parent of %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return full
}

// repoRoot walks up to the directory holding go.mod, so a fixture is found
// whatever directory the test binary runs in.
func repoRoot(t *testing.T) string {
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

// readFixture decodes one frozen vector file into target.
func readFixture(t *testing.T, name string, target any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

// chmodExecutable makes a hook runnable, which git requires before it runs one.
func chmodExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}
