package vcs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

func slug(t *testing.T, owner, name string) core.Slug {
	t.Helper()
	s, err := core.NewSlug(owner, name)
	if err != nil {
		t.Fatalf("NewSlug(%q, %q): %v", owner, name, err)
	}
	return s
}

// The path is built by concatenation rather than by joining, because the shell
// builds it that way and a double slash in XDG_STATE_HOME survives into the
// answer. Nothing depends on the double slash; what depends on it is that this
// and the shell agree byte for byte.
func TestWorktreeDir(t *testing.T) {
	tests := []struct {
		name  string
		state string
		home  string
		owner string
		repo  string
		pr    int
		want  string
	}{
		{
			name:  "xdg-state-set",
			state: "/state",
			home:  "/home/dev",
			owner: "carlosboeing", repo: "crossrev", pr: 42,
			want: "/state/crossrev/worktrees/carlosboeing-crossrev/pr-42",
		},
		{
			name:  "xdg-state-unset-falls-back-to-home",
			state: "",
			home:  "/home/dev",
			owner: "carlosboeing", repo: "crossrev", pr: 42,
			want: "/home/dev/.local/state/crossrev/worktrees/carlosboeing-crossrev/pr-42",
		},
		{
			name:  "trailing-slash-in-xdg-state",
			state: "/state/",
			home:  "/home/dev",
			owner: "o", repo: "r", pr: 1,
			want: "/state//crossrev/worktrees/o-r/pr-1",
		},
		{
			name:  "dots-and-dashes-survive",
			state: "/state",
			home:  "/home/dev",
			owner: "some-org", repo: "some.repo", pr: 7,
			want: "/state/crossrev/worktrees/some-org-some.repo/pr-7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", tt.state)
			t.Setenv("HOME", tt.home)
			got, err := vcs.WorktreeDir(slug(t, tt.owner, tt.repo), tt.pr)
			if err != nil {
				t.Fatalf("WorktreeDir: %v", err)
			}
			if got != tt.want {
				t.Errorf("dir = %q, want %q", got, tt.want)
			}
		})
	}
}

// A slug that never went through a constructor renders as `-` and would give
// every repository one worktree, so the zero value is refused rather than used.
func TestWorktreeDirRefusesTheZeroSlug(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if got, err := vcs.WorktreeDir(core.Slug{}, 1); err == nil {
		t.Errorf("WorktreeDir(zero) = %q, want a refusal", got)
	}
}

// commitFile writes a file, stages it and commits, returning the new revision.
func commitFile(t *testing.T, repo *vcs.Repository, name, content, message string) core.Revision {
	t.Helper()
	write(t, repo.Dir(), name, content)
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(append([]string{}, testIdentity...), "commit", "-q", "-m", message)...)
	head, err := repo.Head(context.Background())
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	return head
}

func TestWorktreeAddAndRemove(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	head := commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	dir := filepath.Join(root, "state", "crossrev", "worktrees", "o-r", "pr-42")
	if err := repo.AddWorktree(ctx, dir, head); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "app.ts")); err != nil {
		t.Fatalf("the worktree has no checkout: %v", err)
	}

	// The worktree belongs to this clone and sits at the right revision, so a
	// second leg reuses it rather than rebuilding it (lib/run.sh:1884-1892).
	reusable, err := repo.WorktreeReusable(ctx, dir, head)
	if err != nil {
		t.Fatalf("WorktreeReusable: %v", err)
	}
	if !reusable {
		t.Error("a worktree at the head revision of its own clone was not reusable")
	}

	other := commitFile(t, repo, "app.ts", "export const ok = 2\n", "second")
	reusable, err = repo.WorktreeReusable(ctx, dir, other)
	if err != nil {
		t.Fatalf("WorktreeReusable: %v", err)
	}
	if reusable {
		t.Error("a worktree at the wrong revision was reported reusable")
	}

	if err := repo.RemoveWorktree(ctx, dir); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the worktree directory survived removal: %v", err)
	}
	// One level up, and one only. The `-p` form of rmdir walks up removing
	// every parent that becomes empty, which on a machine where the state
	// directory holds nothing else deletes far more than CrossRev's own
	// (lib/run.sh:2458-2460).
	if _, err := os.Stat(filepath.Join(root, "state", "crossrev", "worktrees")); err != nil {
		t.Errorf("the grandparent directory was removed as well: %v", err)
	}
}

// A directory that is not a worktree of this clone must not be reused: the path
// is keyed on the slug and the pull request alone, so two checkouts of one
// repository collide on it, and the head revision cannot tell them apart —
// two checkouts at the same pull request hold the same commit by definition
// (lib/run.sh:64-70).
func TestWorktreeReusableRefusesAnotherClone(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)

	first := initRepo(t, git, filepath.Join(root, "first"))
	head := commitFile(t, first, "app.ts", "export const ok = 1\n", "init")

	// A second clone of the same content, at the same revision, in a directory
	// the first clone knows nothing about.
	second := initRepo(t, git, filepath.Join(root, "second"))
	mustGit(t, second, "fetch", filepath.Join(root, "first"), head.SHA())
	mustGit(t, second, "checkout", "-q", "--detach", head.SHA())

	reusable, err := first.WorktreeReusable(ctx, second.Dir(), head)
	if err != nil {
		t.Fatalf("WorktreeReusable: %v", err)
	}
	if reusable {
		t.Error("a checkout belonging to another clone was reported reusable")
	}
}

// Removal falls back to deleting the directory when git will not, because a
// worktree whose administrative record is gone is still a directory in the way
// (lib/run.sh:2456).
func TestRemoveWorktreeFallsBackToDeleting(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	stray := filepath.Join(root, "state", "worktrees", "pr-1")
	write(t, stray, "left-behind.txt", "x\n")

	if err := repo.RemoveWorktree(ctx, stray); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Errorf("the stray directory survived: %v", err)
	}
}

// One level of parent, and one only.
//
// The `-p` form of rmdir walks up removing every parent that becomes empty and
// stops at the first that is not, which deletes far more than CrossRev's own.
// Between two pull requests of one repository the parent is shared, so a
// sibling's worktree is what the single level protects.
func TestRemoveWorktreeSparesASiblingPullRequest(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	head := commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	parent := filepath.Join(root, "state", "crossrev", "worktrees", "o-r")
	first := filepath.Join(parent, "pr-42")
	second := filepath.Join(parent, "pr-43")
	for _, dir := range []string{first, second} {
		if err := repo.AddWorktree(ctx, dir, head); err != nil {
			t.Fatalf("AddWorktree(%s): %v", dir, err)
		}
	}

	if err := repo.RemoveWorktree(ctx, first); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Errorf("pr-42 survived removal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(second, "app.ts")); err != nil {
		t.Errorf("a sibling pull request's worktree was removed: %v", err)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Errorf("the shared parent was removed while it still held a worktree: %v", err)
	}
}

// The question a caller asks before a worktree exists at all.
//
// It is the function's primary entry condition and nothing asked it, which left
// the os.Stat error arm untested — and that arm is what stops a nil FileInfo
// reaching IsDir.
func TestWorktreeReusableOnSomethingThatIsNotAWorktree(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	head := commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	file := write(t, root, "not-a-directory", "x\n")

	for _, dir := range []string{
		filepath.Join(root, "never-created"),
		file,
	} {
		reusable, err := repo.WorktreeReusable(ctx, dir, head)
		if err != nil {
			t.Fatalf("WorktreeReusable(%s): %v", dir, err)
		}
		if reusable {
			t.Errorf("WorktreeReusable(%s) = true, want false", dir)
		}
	}
}
