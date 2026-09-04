package vcs_test

import (
	"context"
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

// The refusal a failing push publishes must be the lines the hook kept.
//
// The oracle used to be the shell run against the same repository: `git push …
// 2>&1` piped through the shipped _gh_git_tail. The shell is removed, so the
// interleaved lines it selected are frozen here. Anything reconstructed from
// two separate captures keeps a different set of lines, because the selection
// is `tail -5` over an interleaved stream.
func TestPushRefusalPublishesTheHookLines(t *testing.T) {
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

	err := repo.Push(ctx, "origin", "main", true)
	refusal, ok := err.(*vcs.Refusal)
	if !ok {
		t.Fatalf("Push = %v, want a refusal", err)
	}
	got := strings.TrimPrefix(refusal.Message, "could not push to main — ")
	if got == refusal.Message {
		t.Fatalf("message has no refusal prefix: %q", refusal.Message)
	}

	// The last five lines of the interleaved capture: the hook's own STDOUT
	// and STDERR lines plus git's summary, with the repository path standing
	// in for the temporary origin. The interleaving is what the frozen tail
	// vectors cannot show, because they start from text already captured.
	want := "STDERR line 3\nSTDOUT line 4\nSTDERR line 4\n" +
		"STDERR FINAL: policy check failed\n" +
		"error: failed to push some refs to '" + origin + "'"
	if got != want {
		t.Errorf("the published lines differ from the frozen capture\n want: %q\n  got: %q", want, got)
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
