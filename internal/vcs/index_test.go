package vcs_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
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
// committed either (lib/run.sh:661-662).
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
	for _, path := range []string{
		"../escape",
		// A sibling whose name begins with the root's. Without the separator
		// in the prefix test this is inside the root, and it is not.
		"../" + filepath.Base(root) + "x/secret",
		"/etc/passwd",
		".",
		"",
	} {
		if vcs.RemovableUnder(root, path) {
			t.Errorf("RemovableUnder(%q) = true, want false", path)
		}
	}
	for _, path := range []string{"inside/file.txt", "file.txt", "a/b/c"} {
		if !vcs.RemovableUnder(root, path) {
			t.Errorf("RemovableUnder(%q) = false, want true", path)
		}
	}
}

// RestoreTree over an ls-files that names a path outside the checkout.
//
// Every other test drives a real git, and git never emits such a line — which
// is why the guard has to be exercised against something that does. Without
// it, one line of a subprocess's stdout reaches os.RemoveAll on a sibling.
func TestRestoreTreeIgnoresAnEscapingLeftover(t *testing.T) {
	outer := realTempDir(t)
	top := filepath.Join(outer, "checkout")
	if err := os.MkdirAll(top, 0o755); err != nil {
		t.Fatalf("make the checkout: %v", err)
	}
	neighbour := write(t, outer, "escape", "the operator's own, outside the checkout\n")

	index := filepath.Join(outer, "snapshot.index")
	if err := os.WriteFile(index, []byte("not a real index, and never read by a fake git"), 0o600); err != nil {
		t.Fatalf("write the index: %v", err)
	}

	git := vcs.New(&scriptedRunner{answers: map[string]string{
		"rev-parse --show-toplevel":         top + "\n",
		"read-tree --reset -u " + fortyZero: "",
		// The hostile line, plus one legitimate one so the loop is proven to
		// still act on what it should.
		"ls-files --others --exclude-standard": "../escape\ninside.txt\n",
	}}, nil)
	write(t, top, "inside.txt", "written by the discarded attempt\n")

	if err := git.At(top).RestoreTree(context.Background(), index, fortyZero); err != nil {
		t.Fatalf("RestoreTree: %v", err)
	}
	if _, err := os.Stat(neighbour); err != nil {
		t.Errorf("a path outside the checkout was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(top, "inside.txt")); !os.IsNotExist(err) {
		t.Errorf("the leftover inside the checkout survived: %v", err)
	}
}

// ls-files runs at the top level, not in the caller's directory.
//
// git limits ls-files to the directory it runs in and prints paths relative to
// it, while the delete joins them onto the top level. Run from a subdirectory
// with the wrong Dir, a leftover at the root is never listed and a leftover in
// the subdirectory is deleted from the wrong place — so both survive, which is
// the exact failure the capture exists to prevent.
func TestRestoreTreeFromASubdirectory(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	sub := filepath.Join(repo.Dir(), "src", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("make a subdirectory: %v", err)
	}
	fromSub := git.At(sub)

	index := filepath.Join(root, "snapshot.index")
	tree, err := fromSub.CaptureTree(ctx, index)
	if err != nil {
		t.Fatalf("CaptureTree: %v", err)
	}

	write(t, repo.Dir(), "top-leftover.txt", "written by the discarded attempt\n")
	write(t, sub, "sub-leftover.txt", "written by the discarded attempt\n")

	if err := fromSub.RestoreTree(ctx, index, tree); err != nil {
		t.Fatalf("RestoreTree: %v", err)
	}
	for _, leftover := range []string{
		filepath.Join(repo.Dir(), "top-leftover.txt"),
		filepath.Join(sub, "sub-leftover.txt"),
	} {
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Errorf("%s survived the restore: %v", leftover, err)
		}
	}
}

// A repository that has never staged anything has no .git/index, so the seed
// copy fails and must stay swallowed: a cold stat cache is slower, not wrong.
func TestCaptureTreeWithNoIndexToSeedFrom(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	write(t, repo.Dir(), "app.ts", "export const ok = 1\n")

	gitDir, err := repo.GitDir(ctx)
	if err != nil {
		t.Fatalf("GitDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir(), gitDir, "index")); !os.IsNotExist(err) {
		t.Fatalf("the fixture repository already has an index: %v", err)
	}

	tree, err := repo.CaptureTree(ctx, filepath.Join(root, "snapshot.index"))
	if err != nil {
		t.Fatalf("CaptureTree: %v", err)
	}
	if len(tree) != 40 {
		t.Errorf("tree = %q, want a 40-character object name", tree)
	}
}

// git answers `--git-dir` relative to the directory it ran in — ".git" from the
// root — so the seed has to be resolved against the repository and not against
// whatever directory this process happens to be standing in. A decoy .git/index
// at the process's own working directory is what makes the difference show:
// seeded from that, the temporary index is not an index and git refuses it.
func TestCaptureTreeResolvesTheGitDirAgainstTheRepository(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	if answer, err := repo.GitDir(ctx); err != nil {
		t.Fatalf("GitDir: %v", err)
	} else if filepath.IsAbs(answer) {
		t.Skipf("this git answers --git-dir absolutely (%q), so there is nothing to resolve", answer)
	}

	decoy := filepath.Join(root, "decoy")
	write(t, decoy, ".git/index", "not an index, and not this repository's\n")
	t.Chdir(decoy)

	tree, err := repo.CaptureTree(ctx, filepath.Join(root, "snapshot.index"))
	if err != nil {
		t.Fatalf("CaptureTree seeded from the wrong index: %v", err)
	}
	if len(tree) != 40 {
		t.Errorf("tree = %q, want a 40-character object name", tree)
	}
}

// fortyZero is an object name shaped like a real one, for a fake git that never
// looks at it.
const fortyZero = "0000000000000000000000000000000000000000"

// scriptedRunner answers a git call from its arguments, so a test can put a
// line on stdout that a real git would never produce.
type scriptedRunner struct {
	answers map[string]string
}

func (s *scriptedRunner) Run(_ context.Context, spec exec.Spec) exec.Result {
	key := strings.Join(spec.Args, " ")
	answer, known := s.answers[key]
	if !known {
		return exec.Result{ExitCode: 1, Stderr: []byte("scriptedRunner has no answer for: " + key)}
	}
	return exec.Result{Stdout: []byte(answer)}
}
