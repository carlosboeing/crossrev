package vcs_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// recorder is a Runner that keeps the spec and answers without starting
// anything, so a test can assert what a git call would have been.
type recorder struct {
	specs []exec.Spec
}

func (r *recorder) Run(_ context.Context, spec exec.Spec) exec.Result {
	r.specs = append(r.specs, spec)
	return exec.Result{}
}

// Every git spec is orchestrator-facing, and that decision is load-bearing
// rather than cosmetic. exec.Spec.Audience defaults to model-facing, which
// refuses a child whose environment names a forge credential — and
// lib/github.sh pushes with a plain `git push` over whatever credential helper
// the environment configures, which on a GitHub-hosted runner is the ambient
// token. A model-facing spec here would refuse that push.
func TestGitSpecsAreOrchestratorFacing(t *testing.T) {
	runner := &recorder{}
	git := vcs.New(runner, []string{"PATH=/usr/bin", "GH_TOKEN=not-a-real-token"})

	if _, err := git.At("/somewhere").Run(context.Background(), "status"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(runner.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(runner.specs))
	}
	spec := runner.specs[0]
	if spec.Audience != exec.AudienceOrchestrator {
		t.Errorf("audience = %v, want AudienceOrchestrator", spec.Audience)
	}
	if spec.Path != "git" {
		t.Errorf("path = %q, want %q", spec.Path, "git")
	}
	if spec.Dir != "/somewhere" {
		t.Errorf("dir = %q, want %q", spec.Dir, "/somewhere")
	}
	if strings.Join(spec.Args, " ") != "status" {
		t.Errorf("args = %v", spec.Args)
	}

	// And the real runner would have started it rather than refusing it.
	if result := exec.NewOSRunner().Run(context.Background(), exec.Spec{
		Path: "git", Args: []string{"--version"}, Env: spec.Env, Audience: spec.Audience,
	}); errors.Is(result.Err, exec.ErrForgeCredential) {
		t.Errorf("a git call carrying a forge credential was refused: %v", result.Err)
	}
}

// ExtraEnv is appended rather than replacing the base, so GIT_INDEX_FILE can
// reach one call without the package holding a mutable environment.
func TestExtraEnvIsAppended(t *testing.T) {
	runner := &recorder{}
	git := vcs.New(runner, []string{"PATH=/usr/bin"})

	if _, err := git.At("").Run(context.Background(), "status"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := git.At("").RunWithEnv(context.Background(), []string{"GIT_INDEX_FILE=/tmp/x"}, "add", "-A"); err != nil {
		t.Fatalf("RunWithEnv: %v", err)
	}

	if got := strings.Join(runner.specs[0].Env, " "); got != "PATH=/usr/bin" {
		t.Errorf("the plain call's env = %q", got)
	}
	if got := strings.Join(runner.specs[1].Env, " "); got != "PATH=/usr/bin GIT_INDEX_FILE=/tmp/x" {
		t.Errorf("the extra-env call's env = %q", got)
	}
	// The base must not have grown a member of the second call's making.
	if got := strings.Join(runner.specs[0].Env, " "); got != "PATH=/usr/bin" {
		t.Errorf("the first call's env was mutated: %q", got)
	}
}

// The lock and the worktree-ownership test both key on this, so every working
// tree of one clone has to answer with one absolute, symlink-free path
// (lib/run.sh:65-78, :184-187).
func TestCommonDirIsSharedAndPhysical(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	head := commitFile(t, repo, "app.ts", "x\n", "init")

	worktree := filepath.Join(root, "wt")
	if err := repo.AddWorktree(ctx, worktree, head); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	fromClone, err := repo.CommonDir(ctx)
	if err != nil {
		t.Fatalf("CommonDir from the clone: %v", err)
	}
	fromWorktree, err := git.At(worktree).CommonDir(ctx)
	if err != nil {
		t.Fatalf("CommonDir from the worktree: %v", err)
	}
	if fromClone != fromWorktree {
		t.Errorf("the clone answers %q and its worktree answers %q", fromClone, fromWorktree)
	}
	if !filepath.IsAbs(fromClone) {
		t.Errorf("common dir = %q, want an absolute path", fromClone)
	}
	if resolved, err := filepath.EvalSymlinks(fromClone); err != nil || resolved != fromClone {
		t.Errorf("common dir = %q, want it symlink-free (%q, %v)", fromClone, resolved, err)
	}

	// A relative answer is resolved against the directory git ran in, which is
	// what `cd "$dir" && cd "$common"` does. Running from a subdirectory is how
	// git comes to answer "../.git" at all.
	sub := filepath.Join(repo.Dir(), "src", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("make a subdirectory: %v", err)
	}
	fromSub, err := git.At(sub).CommonDir(ctx)
	if err != nil {
		t.Fatalf("CommonDir from a subdirectory: %v", err)
	}
	if fromSub != fromClone {
		t.Errorf("a subdirectory answers %q, the root answers %q", fromSub, fromClone)
	}
}

func TestCommonDirOutsideARepository(t *testing.T) {
	git := testGit(t)
	_, err := git.At(realTempDir(t)).CommonDir(context.Background())
	if !errors.Is(err, vcs.ErrNotARepository) {
		t.Errorf("err = %v, want ErrNotARepository", err)
	}
}

// An unborn HEAD is the empty string in the shell (`|| true` at
// lib/run.sh:1902), and the zero revision here.
func TestHeadOnAnUnbornBranch(t *testing.T) {
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))

	head, err := repo.Head(context.Background())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if !head.IsZero() {
		t.Errorf("head = %q, want the zero revision", head)
	}
}

func TestHasCommit(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	head := commitFile(t, repo, "app.ts", "x\n", "init")

	present, err := repo.HasCommit(ctx, head)
	if err != nil {
		t.Fatalf("HasCommit: %v", err)
	}
	if !present {
		t.Error("the head revision was reported absent")
	}

	other := initRepo(t, git, filepath.Join(realTempDir(t), "other"))
	present, err = other.HasCommit(ctx, head)
	if err != nil {
		t.Fatalf("HasCommit in another repository: %v", err)
	}
	if present {
		t.Error("a revision this repository does not hold was reported present")
	}
}

// A key set more than once carries every value, which is the whole reason
// legs_resolve_push_repo reads the config keys rather than `git remote get-url
// --push` (lib/legs.sh:367-372).
func TestConfigGetAll(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	mustGit(t, repo, "config", "--add", "remote.origin.pushurl", "https://github.com/o/r.git")
	mustGit(t, repo, "config", "--add", "remote.origin.pushurl", "git@github.com:o/r.git")

	values, err := repo.ConfigGetAll(ctx, "remote.origin.pushurl")
	if err != nil {
		t.Fatalf("ConfigGetAll: %v", err)
	}
	want := []string{"git@github.com:o/r.git", "https://github.com/o/r.git"}
	got := append([]string{}, values...)
	sort.Strings(got)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("values = %v, want both entries", values)
	}

	missing, err := repo.ConfigGetAll(ctx, "remote.nowhere.pushurl")
	if err != nil {
		t.Fatalf("ConfigGetAll for a missing key: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("values = %v, want none", missing)
	}

	single, err := repo.ConfigGet(ctx, "remote.nowhere.url")
	if err != nil {
		t.Fatalf("ConfigGet: %v", err)
	}
	if single != "" {
		t.Errorf("value = %q, want the empty string", single)
	}
}

func TestOutputReading(t *testing.T) {
	output := vcs.Output{Stdout: "one\n\ntwo\n"}
	if got := output.Text(); got != "one\n\ntwo" {
		t.Errorf("Text = %q", got)
	}
	if got := strings.Join(output.Lines(), "|"); got != "one|two" {
		t.Errorf("Lines = %q", got)
	}
	if !(vcs.Output{}).OK() {
		t.Error("a zero Output is not OK")
	}
	if (vcs.Output{ExitCode: 1}).OK() {
		t.Error("a non-zero Output is OK")
	}
}

// Nothing these tests do may write into the checkout they run from. Every
// operation is given a path under a temporary root, and this proves that the
// package directory is untouched by a full cycle of them.
func TestNothingIsWrittenIntoTheCheckout(t *testing.T) {
	ctx := context.Background()
	before := listing(t, ".")

	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	head := commitFile(t, repo, "app.ts", "x\n", "init")

	worktree := filepath.Join(root, "wt")
	if err := repo.AddWorktree(ctx, worktree, head); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	lock, err := repo.AcquireRunLock(ctx, 42, "local")
	if err != nil {
		t.Fatalf("AcquireRunLock: %v", err)
	}
	index := filepath.Join(root, "snapshot.index")
	tree, err := repo.CaptureTree(ctx, index)
	if err != nil {
		t.Fatalf("CaptureTree: %v", err)
	}
	if err := repo.RestoreTree(ctx, index, tree); err != nil {
		t.Fatalf("RestoreTree: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := repo.RemoveWorktree(ctx, worktree); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	if after := listing(t, "."); after != before {
		t.Errorf("the package directory changed\n before: %s\n  after: %s", before, after)
	}
}

// listing is every regular file in dir with a hash of its contents.
//
// Names alone catch a file that was added and miss one that was overwritten in
// place, which is the more likely damage: every path here is built from a
// temporary root, and a bug that dropped the root would land on a file that is
// already there.
func listing(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			names = append(names, entry.Name()+"/")
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		names = append(names, fmt.Sprintf("%s@%x", entry.Name(), sha256.Sum256(content)))
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}

// The guard above is only as good as what listing compares. Names alone pass
// while a file is overwritten in place, and an overwrite is the damage a path
// that lost its temporary root would do.
func TestListingDetectsAnInPlaceOverwrite(t *testing.T) {
	dir := realTempDir(t)
	write(t, dir, "app.ts", "export const ok = 1\n")
	before := listing(t, dir)

	write(t, dir, "app.ts", "export const ok = 2\n")
	if after := listing(t, dir); after == before {
		t.Errorf("an in-place overwrite left the listing unchanged: %s", after)
	}
}
