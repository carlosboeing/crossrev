package review_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/validate"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// pinnedReq is the production posture: no workdir override, so the leg pins
// a clean detached worktree at the pull request head itself.
func pinnedReq(t *testing.T, e *env) review.Request {
	t.Helper()
	req := e.request(t)
	req.Workdir = ""
	return req
}

// TestPinnedWorktreeRunsHarnessAtHead pins the F6 contract: with no workdir
// override the harness runs in a worktree the leg created, that worktree's
// HEAD is the pull request head, the operator checkout is untouched, and a
// clean finish removes the worktree.
func TestPinnedWorktreeRunsHarnessAtHead(t *testing.T) {
	e := newEnv(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var dirs []string
	var heads []string
	e.runner.onSpec = func(spec exec.Spec) {
		dirs = append(dirs, spec.Dir)
		have, err := e.vcs.HeadAt(context.Background(), spec.Dir)
		if err != nil {
			t.Errorf("HeadAt(%s): %v", spec.Dir, err)
			return
		}
		heads = append(heads, have.SHA())
	}
	leg := e.leg(t)
	got := leg.Run(context.Background(), pinnedReq(t, e))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(dirs) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(dirs))
	}
	if dirs[0] == e.dir {
		t.Errorf("the harness ran in the operator checkout %s, want the pinned worktree", e.dir)
	}
	if len(heads) != 1 || heads[0] != headSHA {
		t.Errorf("worktree HEAD = %v, want the pull request head %s", heads, headSHA)
	}
	if _, err := os.Stat(dirs[0]); !os.IsNotExist(err) {
		t.Errorf("a clean finish left the worktree at %s", dirs[0])
	}
	if got.KeptWorktree != "" {
		t.Errorf("a clean finish kept %q for debugging, want none", got.KeptWorktree)
	}
	if len(e.vcs.fetchCalls) != 0 {
		t.Errorf("fetched a head the clone holds: %v", e.vcs.fetchCalls)
	}
}

// TestFailedLegKeepsPinnedWorktree proves the failure half of the leg-end
// hook: a leg that dies keeps its worktree for debugging.
func TestFailedLegKeepsPinnedWorktree(t *testing.T) {
	e := newEnv(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var dirs []string
	e.runner.onSpec = func(spec exec.Spec) { dirs = append(dirs, spec.Dir) }
	e.validate = func([]byte, validate.ReviewExpectations) error {
		return &validate.ShapeError{Problem: "no verdict key"}
	}
	leg := e.leg(t)
	got := leg.Run(context.Background(), pinnedReq(t, e))
	if got.Err == nil {
		t.Fatal("wanted a shape failure")
	}
	if len(dirs) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(dirs))
	}
	if _, err := os.Stat(dirs[0]); err != nil {
		t.Errorf("a failed leg removed its worktree at %s: %v", dirs[0], err)
	}
	if got.KeptWorktree != dirs[0] {
		t.Errorf("kept worktree = %q, want the failed leg's own %q", got.KeptWorktree, dirs[0])
	}
}

// TestFailedLegReportsTheSelectedWorktree proves the kept path is the
// directory selectWorktree actually selected: with the canonical review
// path preserved as a dirty occupant, the failed leg works in the suffixed
// directory beside it, and that is the path the caller is told to report.
func TestFailedLegReportsTheSelectedWorktree(t *testing.T) {
	e := newEnv(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	canonical, err := vcs.ReviewWorktreeDir(mustSlug(t), 42)
	if err != nil {
		t.Fatalf("ReviewWorktreeDir: %v", err)
	}
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatalf("lay the occupant: %v", err)
	}
	sentinel := filepath.Join(canonical, "leftover.txt")
	if err := os.WriteFile(sentinel, []byte("a failed attempt left this\n"), 0o644); err != nil {
		t.Fatalf("plant the leftover: %v", err)
	}
	e.vcs.reusable = map[string]bool{canonical: true}
	e.vcs.clean = map[string]bool{canonical: false}
	if e.vcs.heads == nil {
		e.vcs.heads = map[string]string{}
	}
	e.vcs.heads[canonical] = headSHA
	e.validate = func([]byte, validate.ReviewExpectations) error {
		return &validate.ShapeError{Problem: "no verdict key"}
	}
	leg := e.leg(t)
	got := leg.Run(context.Background(), pinnedReq(t, e))
	if got.Err == nil {
		t.Fatal("wanted a shape failure")
	}
	specs := e.runner.Specs()
	if len(specs) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(specs))
	}
	if specs[0].Dir == canonical {
		t.Fatalf("the harness ran in the preserved occupant %s", canonical)
	}
	if got.KeptWorktree != specs[0].Dir {
		t.Errorf("kept worktree = %q, want the selected %q", got.KeptWorktree, specs[0].Dir)
	}
	if _, err := os.Stat(specs[0].Dir); err != nil {
		t.Errorf("a failed leg removed its own worktree at %s: %v", specs[0].Dir, err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("the preserved occupant was not preserved: %v", err)
	}
}

// TestQuarantineResolvesUnderPinnedWorktree proves quarantine moves files in
// the pinned worktree, never in the operator checkout: the harness's blind
// write lands in the worktree and is discarded, and the checkout gains no
// quarantine file of its own.
func TestQuarantineResolvesUnderPinnedWorktree(t *testing.T) {
	e := newEnv(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	e.vcs.onAddWorktree = func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("quarantine me\n"), 0o644)
	}
	e.runner.onSpec = func(spec exec.Spec) {
		if spec.Dir == e.dir {
			t.Errorf("the harness ran in the operator checkout %s", e.dir)
		}
		if err := os.WriteFile(filepath.Join(spec.Dir, "CLAUDE.md"), []byte("blind\n"), 0o644); err != nil {
			t.Errorf("plant the blind write: %v", err)
		}
	}
	leg := e.leg(t)
	got := leg.Run(context.Background(), pinnedReq(t, e))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	want := "the harness wrote to quarantined path(s): CLAUDE.md"
	if !strings.Contains(ui.Joined(got.Messages), want) {
		t.Errorf("messages = %q, want warning %q", got.Messages, want)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("quarantine touched the operator checkout")
	}
}

// TestMissingHeadFetchedThroughOrderedFallbacks proves a head the clone does
// not hold is fetched the resolve way: the head SHA first, then the pull
// request ref, and nothing past the fallback that lands it.
func TestWorktreeMissingHeadFetchedThroughOrderedFallbacks(t *testing.T) {
	e := newEnv(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	e.vcs.hasCommit = map[string]bool{}
	e.vcs.onFetch = func(remote, refspec string) {
		if refspec == "refs/pull/42/head" {
			e.vcs.hasCommit[headSHA] = true
		}
	}
	leg := e.leg(t)
	got := leg.Run(context.Background(), pinnedReq(t, e))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	want := []string{"origin " + headSHA, "origin refs/pull/42/head"}
	if len(e.vcs.fetchCalls) != len(want) {
		t.Fatalf("fetch calls = %v, want %v", e.vcs.fetchCalls, want)
	}
	for i := range want {
		if e.vcs.fetchCalls[i] != want[i] {
			t.Fatalf("fetch calls = %v, want %v", e.vcs.fetchCalls, want)
		}
	}
}

// TestExplicitWorkdirAtWrongCommitRefused proves an explicit workdir whose
// HEAD differs from the pull request head is refused with a named error
// before any model call — and before any claim — instead of mis-anchoring
// findings against the wrong checkout.
func TestWorktreeExplicitWorkdirAtWrongCommitRefused(t *testing.T) {
	e := newEnv(t)
	const other = "3333333333333333333333333333333333333333"
	if e.vcs.heads == nil {
		e.vcs.heads = map[string]string{}
	}
	e.vcs.heads[e.dir] = other
	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("wanted a worktree-head refusal for the wrong checkout")
	}
	var mismatch *review.WorktreeHeadMismatch
	if !errors.As(got.Err, &mismatch) {
		t.Fatalf("err = %v (%T), want a *review.WorktreeHeadMismatch", got.Err, got.Err)
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatalf("harness started on a refused workdir: %d specs", len(e.runner.Specs()))
	}
	if len(e.forge.created) != 0 {
		t.Errorf("the refusal posted a claim first: %v", e.forge.created)
	}
}

// TestPinnedWorktreeNeverReusesPriorTree proves the sweep rule: a worktree
// already sitting at the head, clean and owned by this clone, is still not
// reused. Reuse is what let dirty, ignored, symlinked, nested and foreign
// occupants reach the harness, and what a path swap between the check and
// its use could divert; every pass works in a tree this run created, so an
// occupant is always preserved and the harness only ever sees a fresh
// checkout.
func TestPinnedWorktreeNeverReusesPriorTree(t *testing.T) {
	e := newEnv(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	wt, err := vcs.ReviewWorktreeDir(mustSlug(t), 42)
	if err != nil {
		t.Fatalf("WorktreeDir: %v", err)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("lay the prior worktree: %v", err)
	}
	sentinel := filepath.Join(wt, "prior.txt")
	if err := os.WriteFile(sentinel, []byte("the previous pass left this\n"), 0o644); err != nil {
		t.Fatalf("plant the leftover: %v", err)
	}
	e.vcs.reusable = map[string]bool{wt: true}
	e.vcs.clean = map[string]bool{wt: true}
	if e.vcs.heads == nil {
		e.vcs.heads = map[string]string{}
	}
	e.vcs.heads[wt] = headSHA
	leg := e.leg(t)
	got := leg.Run(context.Background(), pinnedReq(t, e))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.vcs.addCalls != 1 {
		t.Errorf("fresh worktree adds = %d, want 1: a clean prior tree must not be reused", e.vcs.addCalls)
	}
	specs := e.runner.Specs()
	if len(specs) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(specs))
	}
	if specs[0].Dir == wt {
		t.Fatalf("the harness ran in the prior worktree %s, want a fresh one", wt)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("the prior worktree was not preserved: %v", err)
	}
	if _, err := os.Stat(specs[0].Dir); !os.IsNotExist(err) {
		t.Errorf("a clean finish left the fresh worktree at %s", specs[0].Dir)
	}
}

// TestPinnedWorktreeSymlinkOccupantSkipped proves a symlink at the canonical
// path is never followed: it may point at a clean checkout at the head,
// which the old stat-and-reuse probe accepted, handing the harness the
// operator checkout. The link is preserved and the pass works beside it.
func TestPinnedWorktreeSymlinkOccupantSkipped(t *testing.T) {
	e := newEnv(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	wt, err := vcs.ReviewWorktreeDir(mustSlug(t), 42)
	if err != nil {
		t.Fatalf("WorktreeDir: %v", err)
	}
	target := t.TempDir()
	marker := filepath.Join(target, "checkout.txt")
	if err := os.WriteFile(marker, []byte("the operator checkout\n"), 0o644); err != nil {
		t.Fatalf("plant the target file: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatalf("lay the parent: %v", err)
	}
	if err := os.Symlink(target, wt); err != nil {
		t.Fatalf("lay the symlink: %v", err)
	}
	e.vcs.reusable = map[string]bool{wt: true}
	e.vcs.clean = map[string]bool{wt: true}
	if e.vcs.heads == nil {
		e.vcs.heads = map[string]string{}
	}
	e.vcs.heads[wt] = headSHA
	leg := e.leg(t)
	got := leg.Run(context.Background(), pinnedReq(t, e))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	specs := e.runner.Specs()
	if len(specs) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(specs))
	}
	if specs[0].Dir == wt {
		t.Fatalf("the harness ran through the symlink %s", wt)
	}
	if specs[0].Dir == target {
		t.Fatalf("the harness ran in the symlink target %s", target)
	}
	if fi, err := os.Lstat(wt); err != nil {
		t.Errorf("the symlink was not preserved: %v", err)
	} else if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the path %s is no longer a symlink", wt)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the symlink target was touched: %v", err)
	}
}

// TestPinnedWorktreeAddFailureRetriesNextPath proves a creation race is
// ridden out rather than reported: when the first free path fails to create
// — another run won the race between the absence check and the add — the
// leg tries the next suffixed directory beside it.
func TestPinnedWorktreeAddFailureRetriesNextPath(t *testing.T) {
	e := newEnv(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	wt, err := vcs.ReviewWorktreeDir(mustSlug(t), 42)
	if err != nil {
		t.Fatalf("WorktreeDir: %v", err)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("lay the occupant: %v", err)
	}
	second := wt + "-2"
	e.vcs.addErrs = map[string]error{second: errors.New("already exists")}
	leg := e.leg(t)
	got := leg.Run(context.Background(), pinnedReq(t, e))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	specs := e.runner.Specs()
	if len(specs) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(specs))
	}
	if want := wt + "-3"; specs[0].Dir != want {
		t.Errorf("harness dir = %s, want %s past the lost race", specs[0].Dir, want)
	}
	if e.vcs.addCalls != 2 {
		t.Errorf("worktree adds = %d, want 2: the failed creation plus the retry", e.vcs.addCalls)
	}
}

// TestPinnedWorktreeUnregisteredTreeRefused proves the post-create ownership
// proof: when the fresh directory is not registered to this clone — a swap
// between creation and use, or a worktree of another clone at the predicted
// path — the leg stops before any model call instead of running there.
func TestPinnedWorktreeUnregisteredTreeRefused(t *testing.T) {
	e := newEnv(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	wt, err := vcs.ReviewWorktreeDir(mustSlug(t), 42)
	if err != nil {
		t.Fatalf("WorktreeDir: %v", err)
	}
	e.vcs.reusable = map[string]bool{wt: false}
	leg := e.leg(t)
	got := leg.Run(context.Background(), pinnedReq(t, e))
	if got.Err == nil {
		t.Fatal("wanted a refusal for a tree registered to another clone")
	}
	if !strings.Contains(got.Err.Error(), "registered") {
		t.Errorf("err = %v, want a refusal naming the failed registration", got.Err)
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatalf("harness started in an unowned tree: %d specs", len(e.runner.Specs()))
	}
	if len(e.forge.created) != 0 {
		t.Errorf("the refusal posted a claim first: %v", e.forge.created)
	}
}

// TestPinnedWorktreeDirtyAtHeadPreserved proves a worktree at the head with
// uncommitted changes is not reused: a failed leg keeps its worktree for
// debugging, and running the next pass on top of those leftovers would hand
// the reviewer files the pull request never carried — then delete them on a
// clean finish. The dirty tree is preserved and the pass works in a fresh
// one.
func TestPinnedWorktreeDirtyAtHeadPreserved(t *testing.T) {
	e := newEnv(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	wt, err := vcs.ReviewWorktreeDir(mustSlug(t), 42)
	if err != nil {
		t.Fatalf("WorktreeDir: %v", err)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("lay the dirty worktree: %v", err)
	}
	sentinel := filepath.Join(wt, "leftover.txt")
	if err := os.WriteFile(sentinel, []byte("a failed attempt left this\n"), 0o644); err != nil {
		t.Fatalf("plant the leftover: %v", err)
	}
	e.vcs.reusable = map[string]bool{wt: true}
	e.vcs.clean = map[string]bool{wt: false}
	if e.vcs.heads == nil {
		e.vcs.heads = map[string]string{}
	}
	e.vcs.heads[wt] = headSHA
	leg := e.leg(t)
	got := leg.Run(context.Background(), pinnedReq(t, e))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	specs := e.runner.Specs()
	if len(specs) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(specs))
	}
	if specs[0].Dir == wt {
		t.Fatalf("the harness ran in the dirty worktree %s, want a fresh one", wt)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("the dirty worktree was not preserved: %v", err)
	}
	if e.vcs.addCalls != 1 {
		t.Errorf("fresh worktree adds = %d, want 1", e.vcs.addCalls)
	}
	if _, err := os.Stat(specs[0].Dir); !os.IsNotExist(err) {
		t.Errorf("a clean finish left the fresh worktree at %s", specs[0].Dir)
	}
}

// TestPinnedWorktreeIgnoresResolveWorktree proves the review leg never
// works in the resolve leg's directory: a resolve worktree sitting at the
// head, clean and reusable, is still not reused. The resolve leg reuses
// that path on HEAD and ownership alone with no cleanliness check, so
// sharing it lets a failed review's leftovers reach `git add -A`, lets a
// moved head delete the tree a failed review kept, and lets a clean review
// finish delete a resolve leftover sitting at the head.
func TestPinnedWorktreeIgnoresResolveWorktree(t *testing.T) {
	e := newEnv(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	resolveWT, err := vcs.WorktreeDir(mustSlug(t), 42)
	if err != nil {
		t.Fatalf("WorktreeDir: %v", err)
	}
	if err := os.MkdirAll(resolveWT, 0o755); err != nil {
		t.Fatalf("lay the resolve worktree: %v", err)
	}
	sentinel := filepath.Join(resolveWT, "resolve.txt")
	if err := os.WriteFile(sentinel, []byte("the resolve leg's own\n"), 0o644); err != nil {
		t.Fatalf("plant the resolve file: %v", err)
	}
	e.vcs.reusable = map[string]bool{resolveWT: true}
	e.vcs.clean = map[string]bool{resolveWT: true}
	if e.vcs.heads == nil {
		e.vcs.heads = map[string]string{}
	}
	e.vcs.heads[resolveWT] = headSHA
	leg := e.leg(t)
	got := leg.Run(context.Background(), pinnedReq(t, e))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	specs := e.runner.Specs()
	if len(specs) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(specs))
	}
	if specs[0].Dir == resolveWT {
		t.Fatalf("the harness ran in the resolve worktree %s, want the review leg's own directory", resolveWT)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("the resolve worktree was touched: %v", err)
	}
}

// TestPinnedWorktreeForeignOccupantPreserved proves a path occupied by a
// worktree this clone does not own — another checkout's, or a failed leg's
// at an older head — is never deleted to make room. The occupant is
// preserved with its uncommitted edits, and the pass works in a fresh
// directory beside it.
func TestPinnedWorktreeForeignOccupantPreserved(t *testing.T) {
	e := newEnv(t)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	wt, err := vcs.ReviewWorktreeDir(mustSlug(t), 42)
	if err != nil {
		t.Fatalf("WorktreeDir: %v", err)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("lay the occupant: %v", err)
	}
	sentinel := filepath.Join(wt, "uncommitted.txt")
	if err := os.WriteFile(sentinel, []byte("another checkout's edits\n"), 0o644); err != nil {
		t.Fatalf("plant the occupant's edits: %v", err)
	}
	e.vcs.reusable = map[string]bool{wt: false}
	if e.vcs.heads == nil {
		e.vcs.heads = map[string]string{}
	}
	e.vcs.heads[wt] = oldSHA
	leg := e.leg(t)
	got := leg.Run(context.Background(), pinnedReq(t, e))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	specs := e.runner.Specs()
	if len(specs) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(specs))
	}
	if specs[0].Dir == wt {
		t.Fatalf("the harness ran in the foreign worktree %s, want a fresh one", wt)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("the foreign occupant was deleted: %v", err)
	}
}
