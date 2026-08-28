package vcs_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/vcs"
)

// The resolve harness edits files before it returns its answer, and a rejected
// answer is thrown away while its edits are not. So each attempt starts from
// the state captured before the first one (lib/run.sh:612-631).
func TestCaptureAndRestoreTree(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	// A working tree with all three kinds of state a capture has to hold:
	// committed, modified-but-unstaged, and untracked.
	write(t, repo.Dir(), "app.ts", "export const ok = 1\n// operator edit\n")
	write(t, repo.Dir(), "notes.txt", "operator scratch\n")

	index := filepath.Join(root, "snapshot.index")
	tree, err := repo.CaptureTree(ctx, index)
	if err != nil {
		t.Fatalf("CaptureTree: %v", err)
	}
	if len(tree) != 40 {
		t.Fatalf("tree = %q, want a 40-character object name", tree)
	}

	// The attempt: it rewrites one file, adds another, and removes a third.
	write(t, repo.Dir(), "app.ts", "export const ok = 2\n")
	write(t, repo.Dir(), "attempt.txt", "written by the discarded attempt\n")
	if err := os.Remove(filepath.Join(repo.Dir(), "notes.txt")); err != nil {
		t.Fatalf("remove notes.txt: %v", err)
	}

	if err := repo.RestoreTree(ctx, index, tree); err != nil {
		t.Fatalf("RestoreTree: %v", err)
	}

	if got := read(t, repo.Dir(), "app.ts"); got != "export const ok = 1\n// operator edit\n" {
		t.Errorf("app.ts = %q, want the operator's unstaged edit back", got)
	}
	if got := read(t, repo.Dir(), "notes.txt"); got != "operator scratch\n" {
		t.Errorf("notes.txt = %q, want the untracked file back", got)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir(), "attempt.txt")); !os.IsNotExist(err) {
		t.Errorf("the discarded attempt's file survived: %v", err)
	}
}

// Reading into the capture's own index is what keeps someone's staging area out
// of this. The capture holds everything that was there, staged and unstaged
// alike, so resetting the real index to it would stage every unstaged change
// the run happened to find (lib/run.sh:643-651).
func TestRestoreTreeLeavesTheRealIndexAlone(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	// The operator has one file staged and one merely edited.
	write(t, repo.Dir(), "staged.txt", "staged by the operator\n")
	mustGit(t, repo, "add", "staged.txt")
	write(t, repo.Dir(), "app.ts", "export const ok = 1\n// unstaged\n")

	before := mustGit(t, repo, "diff", "--cached", "--name-only").Text()

	index := filepath.Join(root, "snapshot.index")
	tree, err := repo.CaptureTree(ctx, index)
	if err != nil {
		t.Fatalf("CaptureTree: %v", err)
	}
	write(t, repo.Dir(), "app.ts", "export const ok = 2\n")
	if err := repo.RestoreTree(ctx, index, tree); err != nil {
		t.Fatalf("RestoreTree: %v", err)
	}

	after := mustGit(t, repo, "diff", "--cached", "--name-only").Text()
	if after != before {
		t.Errorf("the staging area changed\n before: %q\n  after: %q", before, after)
	}
}

// Ignored files are neither captured nor removed, which is right — they are not
// committed either (lib/run.sh:650-651).
func TestRestoreTreeKeepsIgnoredFiles(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	write(t, repo.Dir(), ".gitignore", "build/\n")
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")
	write(t, repo.Dir(), "build/out.js", "generated\n")

	index := filepath.Join(root, "snapshot.index")
	tree, err := repo.CaptureTree(ctx, index)
	if err != nil {
		t.Fatalf("CaptureTree: %v", err)
	}
	if err := repo.RestoreTree(ctx, index, tree); err != nil {
		t.Fatalf("RestoreTree: %v", err)
	}
	if got := read(t, repo.Dir(), "build/out.js"); got != "generated\n" {
		t.Errorf("an ignored file was removed: %q", got)
	}
}

// A capture that was never taken, or a restore that will not apply, has to fail
// rather than degrade quietly: asking again on top of a discarded attempt's
// edits records the accepted answer against changes it never made
// (lib/run.sh:665-666, :675-677).
func TestRestoreTreeRefusesWithoutACapture(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	index := filepath.Join(root, "never-written.index")
	if err := repo.RestoreTree(ctx, index, ""); err == nil {
		t.Error("restoring an empty tree succeeded")
	}
	if err := repo.RestoreTree(ctx, index, strings.Repeat("0", 40)); err == nil {
		t.Error("restoring from an index that was never written succeeded")
	}
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// Nothing a capture removes may sit outside the checkout. git never emits such
// a path from `ls-files --others`, so this changes no behaviour — it is the
// guard that keeps `rm -rf` on a path built from a subprocess's stdout from
// being a matter of trust.
func TestRestoreTreeRemovesNothingOutsideTheCheckout(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	outside := write(t, root, "outside.txt", "not the checkout's\n")

	index := filepath.Join(root, "snapshot.index")
	tree, err := repo.CaptureTree(ctx, index)
	if err != nil {
		t.Fatalf("CaptureTree: %v", err)
	}
	if err := repo.RestoreTree(ctx, index, tree); err != nil {
		t.Fatalf("RestoreTree: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("a file outside the checkout was removed: %v", err)
	}
	if got := vcs.RemovableUnder(root, "../escape"); got {
		t.Error("a path climbing out of the root was reported removable")
	}
	if got := vcs.RemovableUnder(root, "inside/file.txt"); !got {
		t.Error("a path inside the root was not reported removable")
	}
}
