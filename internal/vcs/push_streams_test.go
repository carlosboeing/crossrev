package vcs_test

import (
	"bytes"
	"context"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/vcs"
)

// prePushHook writes to both streams and refuses, which is what a repository's
// own policy check looks like when `git.hooks: run` is set (ADR 0017).
//
// git splits the two: the hook's stdout reaches git's stdout, and the hook's
// stderr plus git's own `error: failed to push some refs` reach stderr. That
// split is the whole reason the capture has to be one stream.
const prePushHook = `#!/bin/sh
cat >/dev/null
for i in 1 2 3 4; do
  echo "STDOUT line $i"
  echo "STDERR line $i" >&2
done
echo "STDERR FINAL: policy check failed" >&2
exit 1
`

// The refusal a failing push publishes must be the lines the shell keeps.
//
// The oracle is the shell run against the same repository: `git push … 2>&1`
// piped through the shipped _gh_git_tail. Anything reconstructed from two
// separate captures keeps a different set of lines, because the selection is
// `tail -5` over an interleaved stream.
func TestPushRefusalMatchesTheShell(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)

	origin := filepath.Join(root, "origin.git")
	mustGit(t, git.At(root), "init", "-q", "--bare", "-b", "main", origin)

	repo := initRepo(t, git, filepath.Join(root, "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")
	mustGit(t, repo, "remote", "add", "origin", origin)

	hook := write(t, repo.Dir(), ".git/hooks/pre-push", prePushHook)
	chmodExecutable(t, hook)

	// The shell's answer over the same repository, with hooks in play.
	want := shellPushTail(t, repo.Dir())
	if !strings.Contains(want, "STDOUT line 4") {
		t.Fatalf("the oracle did not produce an interleaved capture: %q", want)
	}

	err := repo.Push(ctx, "origin", "main", true)
	refusal, ok := err.(*vcs.Refusal)
	if !ok {
		t.Fatalf("Push = %v, want a refusal", err)
	}
	got := strings.TrimPrefix(refusal.Message, "could not push to main — ")
	if got == refusal.Message {
		t.Fatalf("message has no refusal prefix: %q", refusal.Message)
	}

	if got != want {
		t.Errorf("the published lines differ from the shell's\n shell: %q\n    go: %q", want, got)
	}
}

// A push with hooks skipped must still succeed, so the combined capture is not
// paid for by breaking the ordinary path.
func TestPushWithHooksSkippedIgnoresTheHook(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)

	origin := filepath.Join(root, "origin.git")
	mustGit(t, git.At(root), "init", "-q", "--bare", "-b", "main", origin)

	repo := initRepo(t, git, filepath.Join(root, "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")
	mustGit(t, repo, "remote", "add", "origin", origin)
	hook := write(t, repo.Dir(), ".git/hooks/pre-push", prePushHook)
	chmodExecutable(t, hook)

	if err := repo.Push(ctx, "origin", "main", false); err != nil {
		t.Fatalf("a skipped pre-push hook still refused the push: %v", err)
	}
}

// shellPushTail is `_gh_git_tail "$(git push … 2>&1)"` over a real repository:
// the shipped capture and the shipped selection, in one place.
func shellPushTail(t *testing.T, dir string) string {
	t.Helper()
	root := repoRoot(t)
	script := `set -uo pipefail
source "$1/lib/ui.sh"
source "$1/lib/github.sh"
cd "$2" || exit 2
out="$(git push origin HEAD:refs/heads/main 2>&1)" || true
_gh_git_tail "$out" || true`

	cmd := osexec.Command("bash", "-c", script, "_", root, dir)
	cmd.Env = shellOracleEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("bash push oracle: %v: %s", err, stderr.String())
	}
	return stdout.String()
}
