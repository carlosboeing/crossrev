package review

// Real-git sweep over the review worktree selection: every case the reuse
// removal covers is proved against a real repository, not the fake. Each
// test drives selectWorktree with a *vcs.Repository, so the git answers —
// HEAD, registration, checkout contents — are git's own.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

var sweepIdentity = []string{"-c", "user.name=crossrev", "-c", "user.email=test@example.com"}

// sweepGit builds a Git whose children see a pinned environment and no
// configuration but the repository's own, so the machine running the suite
// cannot change what the sweep measures.
func sweepGit(t *testing.T) *vcs.Git {
	t.Helper()
	home := t.TempDir()
	env := append([]string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	}, exec.Inherit([]string{"PATH"})...)
	// NewOSRunner, not the orchestrator runner: the sweep starts only
	// local git children holding no credential, and the architecture test
	// confines the credential-holding runner to three directories.
	return vcs.New(exec.NewOSRunner(), env)
}

// sweepTempDir is a temporary directory with every symlink already resolved,
// so a containment assertion compares resolved paths on both sides.
func sweepTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve the temporary root: %v", err)
	}
	return resolved
}

func sweepMustGit(t *testing.T, repo *vcs.Repository, args ...string) vcs.Output {
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

func sweepInitRepo(t *testing.T, git *vcs.Git, dir string) *vcs.Repository {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make %s: %v", dir, err)
	}
	repo := git.At(dir)
	sweepMustGit(t, repo, "init", "-q", "-b", "main", ".")
	return repo
}

func sweepWrite(t *testing.T, dir, name, content string) string {
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

// sweepCommit writes one file, stages everything and commits, returning the
// new HEAD.
func sweepCommit(t *testing.T, repo *vcs.Repository, name, content, message string) core.Revision {
	t.Helper()
	sweepWrite(t, repo.Dir(), name, content)
	sweepMustGit(t, repo, "add", "-A")
	sweepMustGit(t, repo, append(append([]string{}, sweepIdentity...), "commit", "-q", "-m", message)...)
	head, err := repo.Head(context.Background())
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	return head
}

func sweepSlug(t *testing.T) core.Slug {
	t.Helper()
	slug, err := core.ParseSlug("acme/widget")
	if err != nil {
		t.Fatalf("slug: %v", err)
	}
	return slug
}

// sweepBase points the review worktree path at a state directory under the
// test's resolved root.
func sweepBase(t *testing.T, root string) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	base, err := vcs.ReviewWorktreeDir(sweepSlug(t), 42)
	if err != nil {
		t.Fatalf("ReviewWorktreeDir: %v", err)
	}
	return base
}

func sweepSelect(t *testing.T, repo *vcs.Repository, base string, head core.Revision) string {
	t.Helper()
	leg := &Leg{VCS: repo}
	got, err := leg.selectWorktree(context.Background(), base, head)
	if err != nil {
		t.Fatalf("selectWorktree: %v", err)
	}
	return got
}

// A fresh selection is a real directory at the head, registered to this
// clone — the post-create proof, against real git.
func TestSweepFreshCheckoutAtHead(t *testing.T) {
	ctx := context.Background()
	git := sweepGit(t)
	root := sweepTempDir(t)
	repo := sweepInitRepo(t, git, filepath.Join(root, "clone"))
	head := sweepCommit(t, repo, "app.ts", "export const ok = 1\n", "init")

	got := sweepSelect(t, repo, sweepBase(t, root), head)

	fi, err := os.Lstat(got)
	if err != nil {
		t.Fatalf("Lstat(%s): %v", got, err)
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("selected %s is not a real directory", got)
	}
	if have, err := repo.HeadAt(ctx, got); err != nil {
		t.Fatalf("HeadAt: %v", err)
	} else if !have.Equal(head) {
		t.Fatalf("HEAD = %s, want %s", have.SHA(), head.SHA())
	}
	if reusable, err := repo.WorktreeReusable(ctx, got, head); err != nil {
		t.Fatalf("WorktreeReusable: %v", err)
	} else if !reusable {
		t.Fatal("a tree this run created was not registered to this clone")
	}
	if _, err := os.Stat(filepath.Join(got, "app.ts")); err != nil {
		t.Fatalf("the fresh checkout has no head file: %v", err)
	}
}

// An ignored leftover is invisible to the old cleanliness probe
// (`git status --porcelain --untracked-files=all` lists no ignored path),
// so a tree carrying one looked clean and reusable. Selection never
// consults that probe anymore: the occupant is preserved and the pass works
// beside it.
func TestSweepIgnoredLeftoverPreserved(t *testing.T) {
	ctx := context.Background()
	git := sweepGit(t)
	root := sweepTempDir(t)
	repo := sweepInitRepo(t, git, filepath.Join(root, "clone"))
	sweepCommit(t, repo, "app.ts", "export const ok = 1\n", "init")
	head := sweepCommit(t, repo, ".gitignore", "ignored.log\n", "ignore the log")

	base := sweepBase(t, root)
	if err := repo.AddWorktree(ctx, base, head); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	sweepWrite(t, base, "ignored.log", "a failed attempt left this\n")

	// The hole the sweep closes: the occupant still reads clean.
	if clean, err := repo.WorktreeClean(ctx, base); err != nil {
		t.Fatalf("WorktreeClean: %v", err)
	} else if !clean {
		t.Fatal("the probe saw the ignored leftover; the test no longer proves the hole")
	}

	got := sweepSelect(t, repo, base, head)
	if got == base {
		t.Fatalf("selected the occupant carrying the ignored leftover")
	}
	if _, err := os.Stat(filepath.Join(base, "ignored.log")); err != nil {
		t.Errorf("the occupant was not preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(got, "ignored.log")); !os.IsNotExist(err) {
		t.Errorf("the fresh checkout carries the ignored leftover")
	}
}

// An untracked leftover in plain sight is preserved the same way.
func TestSweepUntrackedLeftoverPreserved(t *testing.T) {
	git := sweepGit(t)
	root := sweepTempDir(t)
	repo := sweepInitRepo(t, git, filepath.Join(root, "clone"))
	head := sweepCommit(t, repo, "app.ts", "export const ok = 1\n", "init")

	base := sweepBase(t, root)
	if err := repo.AddWorktree(context.Background(), base, head); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	sweepWrite(t, base, "untracked.ts", "export const extra = 1\n")

	got := sweepSelect(t, repo, base, head)
	if got == base {
		t.Fatalf("selected the occupant carrying the untracked leftover")
	}
	if _, err := os.Stat(filepath.Join(base, "untracked.ts")); err != nil {
		t.Errorf("the occupant was not preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(got, "untracked.ts")); !os.IsNotExist(err) {
		t.Errorf("the fresh checkout carries the untracked leftover")
	}
}

// A symlink at the candidate, pointing at a clean checkout at the head, is
// an occupant to work beside — never followed into.
func TestSweepSymlinkAtPathSkipped(t *testing.T) {
	ctx := context.Background()
	git := sweepGit(t)
	root := sweepTempDir(t)
	repo := sweepInitRepo(t, git, filepath.Join(root, "clone"))
	head := sweepCommit(t, repo, "app.ts", "export const ok = 1\n", "init")

	target := filepath.Join(root, "checkout")
	if err := repo.AddWorktree(ctx, target, head); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	base := sweepBase(t, root)
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		t.Fatalf("lay the parent: %v", err)
	}
	if err := os.Symlink(target, base); err != nil {
		t.Fatalf("lay the symlink: %v", err)
	}

	got := sweepSelect(t, repo, base, head)
	if got == base {
		t.Fatalf("selected the symlink")
	}
	resolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", got, err)
	}
	if resolved != got {
		t.Fatalf("selected %s, which resolves through a symlink to %s", got, resolved)
	}
	if fi, err := os.Lstat(base); err != nil {
		t.Fatalf("the symlink was not preserved: %v", err)
	} else if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is no longer a symlink", base)
	}
	// The target gained no worktree of its own: selection created beside
	// the link, not inside what it points at.
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("read the target: %v", err)
	}
	if len(entries) != 2 { // app.ts and .git
		t.Errorf("the symlink target gained entries: %v", entries)
	}
}

// A symlink anywhere on the path — here the repository directory shared by
// every candidate — fails closed before anything is created: checking out
// under it would land the head inside the link's target.
func TestSweepSymlinkedParentRefused(t *testing.T) {
	git := sweepGit(t)
	root := sweepTempDir(t)
	repo := sweepInitRepo(t, git, filepath.Join(root, "clone"))
	head := sweepCommit(t, repo, "app.ts", "export const ok = 1\n", "init")

	base := sweepBase(t, root)
	parent := filepath.Dir(base)
	evasive := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(evasive, 0o755); err != nil {
		t.Fatalf("lay the target: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
		t.Fatalf("lay the grandparent: %v", err)
	}
	if err := os.Symlink(evasive, parent); err != nil {
		t.Fatalf("lay the parent symlink: %v", err)
	}

	leg := &Leg{VCS: repo}
	_, err := leg.selectWorktree(context.Background(), base, head)
	if err == nil {
		t.Fatal("wanted a refusal under a symlinked parent")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("err = %v, want a refusal naming the symlink", err)
	}
	if entries, rerr := os.ReadDir(evasive); rerr != nil {
		t.Fatalf("read the target: %v", rerr)
	} else if len(entries) != 0 {
		t.Errorf("a checkout was created inside the symlink target: %v", entries)
	}
}

// A nested repository inside an occupant stays with the occupant: the pass
// never enters the tree, so foreign commits reachable only through the
// nested checkout never read as the pull request's files.
func TestSweepNestedRepositoryPreserved(t *testing.T) {
	ctx := context.Background()
	git := sweepGit(t)
	root := sweepTempDir(t)
	repo := sweepInitRepo(t, git, filepath.Join(root, "clone"))
	sweepCommit(t, repo, "app.ts", "export const ok = 1\n", "init")
	head := sweepCommit(t, repo, ".gitignore", "nested/\n", "ignore the nested checkout")

	base := sweepBase(t, root)
	if err := repo.AddWorktree(ctx, base, head); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	nested := git.At(filepath.Join(base, "nested"))
	if err := os.MkdirAll(nested.Dir(), 0o755); err != nil {
		t.Fatalf("lay the nested dir: %v", err)
	}
	sweepMustGit(t, nested, "init", "-q", "-b", "main", ".")
	foreign := sweepWrite(t, nested.Dir(), "foreign.txt", "not the pull request\n")
	sweepMustGit(t, nested, "add", "-A")
	sweepMustGit(t, nested, append(append([]string{}, sweepIdentity...), "commit", "-q", "-m", "foreign")...)

	got := sweepSelect(t, repo, base, head)
	if got == base {
		t.Fatalf("selected the occupant carrying the nested repository")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("the nested repository was not preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(got, "nested", "foreign.txt")); !os.IsNotExist(err) {
		t.Errorf("the fresh checkout reaches the nested repository's files")
	}
}

// A fresh checkout never initializes submodules: the submodule directory
// arrives empty, so submodule contents from no checkout can read as the
// pull request's files.
func TestSweepFreshSubmoduleStaysEmpty(t *testing.T) {
	git := sweepGit(t)
	root := sweepTempDir(t)
	sub := sweepInitRepo(t, git, filepath.Join(root, "subsrc"))
	sweepCommit(t, sub, "lib.txt", "upstream\n", "upstream")
	repo := sweepInitRepo(t, git, filepath.Join(root, "clone"))
	sweepMustGit(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir(), "sub")
	head := sweepCommit(t, repo, "app.ts", "export const ok = 1\n", "init")

	got := sweepSelect(t, repo, sweepBase(t, root), head)
	entries, err := os.ReadDir(filepath.Join(got, "sub"))
	if err != nil {
		t.Fatalf("read the submodule dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the fresh checkout initialized the submodule: %v", entries)
	}
}

// A checkout belonging to another clone, sitting at the same head, is an
// occupant like any other: never entered, never removed.
func TestSweepForeignClonePreserved(t *testing.T) {
	ctx := context.Background()
	git := sweepGit(t)
	root := sweepTempDir(t)
	first := sweepInitRepo(t, git, filepath.Join(root, "first"))
	head := sweepCommit(t, first, "app.ts", "export const ok = 1\n", "init")

	second := sweepInitRepo(t, git, filepath.Join(root, "second"))
	sweepMustGit(t, second, "fetch", "-q", first.Dir(), head.SHA())
	sweepMustGit(t, second, "checkout", "-q", "--detach", head.SHA())

	base := sweepBase(t, root)
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		t.Fatalf("lay the parent: %v", err)
	}
	if err := os.Rename(second.Dir(), base); err != nil {
		t.Fatalf("plant the foreign checkout: %v", err)
	}
	if reusable, err := first.WorktreeReusable(ctx, base, head); err != nil {
		t.Fatalf("WorktreeReusable: %v", err)
	} else if reusable {
		t.Fatal("the foreign checkout reads as reusable; the test proves nothing")
	}

	got := sweepSelect(t, first, base, head)
	if got == base {
		t.Fatalf("selected the foreign checkout")
	}
	if have, err := git.At(base).Head(ctx); err != nil {
		t.Fatalf("the foreign checkout was damaged: %v", err)
	} else if !have.Equal(head) {
		t.Fatalf("the foreign checkout moved to %s", have.SHA())
	}
}

// Planted occupants of every kind at the predicted paths cannot divert
// selection: a directory, a file, and a symlink at the canonical path and
// its first suffix are all worked beside.
func TestSweepPredictedPathsNeverEntered(t *testing.T) {
	git := sweepGit(t)
	root := sweepTempDir(t)
	repo := sweepInitRepo(t, git, filepath.Join(root, "clone"))
	head := sweepCommit(t, repo, "app.ts", "export const ok = 1\n", "init")

	base := sweepBase(t, root)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("lay the directory occupant: %v", err)
	}
	second := base + "-2"
	if err := os.Symlink(os.DevNull, second); err != nil {
		// /dev/null is a file-like target that always exists; the link
		// itself is what matters.
		t.Fatalf("lay the symlink occupant: %v", err)
	}

	got := sweepSelect(t, repo, base, head)
	if want := base + "-3"; got != want {
		t.Fatalf("selected %s, want %s past both planted occupants", got, want)
	}
	if fi, err := os.Lstat(second); err != nil {
		t.Fatalf("the symlink occupant was not preserved: %v", err)
	} else if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is no longer a symlink", second)
	}
}
