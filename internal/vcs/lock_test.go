package vcs_test

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/vcs"
)

// lockPath is where run_lock_acquire writes, given the clone's shared git
// directory (lib/run.sh:194-195).
func lockPath(t *testing.T, repo *vcs.Repository, pr int) string {
	t.Helper()
	common, err := repo.CommonDir(context.Background())
	if err != nil {
		t.Fatalf("CommonDir: %v", err)
	}
	return filepath.Join(common, "crossrev", fmt.Sprintf("pr-%d.lock", pr))
}

func TestRunLockHolderLine(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")

	lock, err := repo.AcquireRunLock(ctx, 42, "local")
	if err != nil {
		t.Fatalf("AcquireRunLock: %v", err)
	}
	if !lock.Held() {
		t.Fatal("the first acquisition took no lock")
	}
	if lock.Warning != nil {
		t.Errorf("warning = %v, want none", lock.Warning)
	}
	if want := lockPath(t, repo, 42); lock.Path != want {
		t.Errorf("lock path = %q, want %q", lock.Path, want)
	}

	raw, err := os.ReadFile(lock.Path)
	if err != nil {
		t.Fatalf("read the lock: %v", err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Errorf("the holder line does not end in a newline: %q", string(raw))
	}
	holder := vcs.ParseHolder(strings.TrimRight(string(raw), "\n"))
	if holder.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", holder.PID, os.Getpid())
	}
	if holder.Host == "" {
		t.Error("the holder names no host")
	}
	if len(holder.Since) != len("2026-01-01T00:00:00Z") || !strings.HasSuffix(holder.Since, "Z") {
		t.Errorf("since = %q, want an ISO 8601 instant in UTC", holder.Since)
	}

	// The lock lives inside the operator's own git directory, and a mode
	// narrower than the one git itself uses would stop a second run by the
	// same user under a different umask from reading the holder it must name.
	info, err := os.Stat(lock.Path)
	if err != nil {
		t.Fatalf("stat the lock: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("lock file mode = %04o, want 0644", got)
	}
	dir, err := os.Stat(filepath.Dir(lock.Path))
	if err != nil {
		t.Fatalf("stat the lock directory: %v", err)
	}
	if got := dir.Mode().Perm(); got != 0o755 {
		t.Errorf("lock directory mode = %04o, want 0755", got)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(lock.Path); !os.IsNotExist(err) {
		t.Errorf("the lock survived release: %v", err)
	}
}

// The lock keys on the clone's shared git directory, so every working tree of
// one clone finds the lock whichever tree took it (lib/run.sh:181-187).
func TestRunLockIsSharedAcrossWorktrees(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	head := commitFile(t, repo, "app.ts", "x\n", "init")

	worktree := filepath.Join(root, "wt", "pr-42")
	if err := repo.AddWorktree(ctx, worktree, head); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	first, err := repo.AcquireRunLock(ctx, 42, "local")
	if err != nil {
		t.Fatalf("AcquireRunLock from the clone: %v", err)
	}
	t.Cleanup(func() { _ = first.Release() })
	if !first.Held() {
		t.Fatal("the first acquisition took no lock")
	}

	// A lock whose recorded pid is this process is kept rather than re-taken.
	// One process drives both legs, and releasing between them would open a
	// window for a second terminal to start a pass halfway through this one
	// (lib/run.sh:199-205).
	second, err := git.At(worktree).AcquireRunLock(ctx, 42, "local")
	if err != nil {
		t.Fatalf("AcquireRunLock from the worktree: %v", err)
	}
	if second.Held() {
		t.Error("the second leg re-took a lock this process already holds")
	}
	if err := second.Release(); err != nil {
		t.Fatalf("Release of a kept lock: %v", err)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("releasing a kept lock removed the real one: %v", err)
	}

	// And the path both trees resolved is one file under the shared directory.
	if want := lockPath(t, git.At(worktree), 42); want != first.Path {
		t.Errorf("the worktree resolves %q, the clone resolves %q", want, first.Path)
	}
}

func TestRunLockRefusesALiveHolder(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")

	// A real process that is not this one. `kill -0` is what the shell asks,
	// and it can only be answered about a process that exists.
	sleeper := osexec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatalf("start a live holder: %v", err)
	}
	t.Cleanup(func() {
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()
	})

	holder := fmt.Sprintf("%d on elsewhere since 2026-01-01T00:00:00Z", sleeper.Process.Pid)
	seedLock(t, repo, 42, holder)

	_, err := repo.AcquireRunLock(ctx, 42, "local")
	refusal, ok := err.(*vcs.Refusal)
	if !ok {
		t.Fatalf("AcquireRunLock = %v, want a refusal", err)
	}
	want := "another CrossRev run already holds pull request 42 — " + holder
	if refusal.Message != want {
		t.Errorf("message = %q, want %q", refusal.Message, want)
	}
	if !strings.Contains(refusal.Hint, "interleave comments and replies") {
		t.Errorf("hint = %q", refusal.Hint)
	}
}

func TestRunLockTakesOverADeadHolder(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")

	dead := osexec.Command("sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatalf("run a process to death: %v", err)
	}
	holder := fmt.Sprintf("%d on elsewhere since 2026-01-01T00:00:00Z", dead.Process.Pid)
	seedLock(t, repo, 42, holder)

	lock, err := repo.AcquireRunLock(ctx, 42, "local")
	if err != nil {
		t.Fatalf("AcquireRunLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	if !lock.Held() {
		t.Fatal("the dead holder's lock was not taken over")
	}
	if lock.Warning == nil {
		t.Fatal("taking over a dead holder's lock said nothing")
	}
	want := "a previous run left a lock on pull request 42 held by " + holder + ", which is no longer running"
	if lock.Warning.Message != want {
		t.Errorf("message = %q, want %q", lock.Warning.Message, want)
	}

	raw, err := os.ReadFile(lock.Path)
	if err != nil {
		t.Fatalf("read the lock: %v", err)
	}
	if got := vcs.ParseHolder(strings.TrimRight(string(raw), "\n")).PID; got != os.Getpid() {
		t.Errorf("the lock still names pid %d, not %d", got, os.Getpid())
	}
}

// A zombie holder is dead even though `kill -0` still answers for it: the
// process has terminated, its parent has not reaped it, and on macOS and
// Linux the process entry stays signalable until it is reaped. The lock it
// held must be taken over, not refused as a live run.
func TestRunLockTakesOverAZombieHolder(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")

	// A child of this test that has been killed but not waited for is a
	// zombie: terminated, unreaped, and still answering `kill -0`.
	zombie := osexec.Command("sleep", "60")
	if err := zombie.Start(); err != nil {
		t.Fatalf("start a holder to zombify: %v", err)
	}
	t.Cleanup(func() { _ = zombie.Wait() })
	if err := zombie.Process.Kill(); err != nil {
		t.Fatalf("kill the holder: %v", err)
	}
	// Give the kernel a moment to move the child to zombie state; the
	// probe, not this sleep, is what must tell the two states apart.
	time.Sleep(200 * time.Millisecond)

	holder := fmt.Sprintf("%d on elsewhere since 2026-01-01T00:00:00Z", zombie.Process.Pid)
	seedLock(t, repo, 42, holder)

	lock, err := repo.AcquireRunLock(ctx, 42, "local")
	if err != nil {
		t.Fatalf("AcquireRunLock over a zombie holder: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	if !lock.Held() {
		t.Fatal("the zombie holder's lock was not taken over")
	}
	if lock.Warning == nil {
		t.Fatal("taking over a zombie holder's lock said nothing")
	}
}

func TestStopRunLockWithNoLock(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")

	result, err := repo.StopRunLock(ctx, 42)
	if err != nil {
		t.Fatalf("StopRunLock: %v", err)
	}
	if result.State != vcs.StopNone {
		t.Errorf("state = %q, want %q", result.State, vcs.StopNone)
	}
}

func TestStopRunLockClearsADeadHolder(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")

	dead := osexec.Command("sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatalf("run a process to death: %v", err)
	}
	holder := fmt.Sprintf("%d on %s since 2026-01-01T00:00:00Z", dead.Process.Pid, hostForLock(t))
	seedLock(t, repo, 42, holder)

	result, err := repo.StopRunLock(ctx, 42)
	if err != nil {
		t.Fatalf("StopRunLock: %v", err)
	}
	if result.State != vcs.StopCleared {
		t.Errorf("state = %q, want %q", result.State, vcs.StopCleared)
	}
	if _, err := os.Stat(lockPath(t, repo, 42)); !os.IsNotExist(err) {
		t.Errorf("the stale lock survived a stop: %v", err)
	}

	lock, err := repo.AcquireRunLock(ctx, 42, "local")
	if err != nil {
		t.Fatalf("AcquireRunLock after clearing: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	if !lock.Held() {
		t.Error("no lock was taken after the stale one was cleared")
	}
}

func TestStopRunLockClearsAZombieHolderWithoutSignalling(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")

	zombie := osexec.Command("sleep", "60")
	if err := zombie.Start(); err != nil {
		t.Fatalf("start a holder to zombify: %v", err)
	}
	t.Cleanup(func() { _ = zombie.Wait() })
	if err := zombie.Process.Kill(); err != nil {
		t.Fatalf("kill the holder: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	holder := fmt.Sprintf("%d on %s since 2026-01-01T00:00:00Z", zombie.Process.Pid, hostForLock(t))
	seedLock(t, repo, 42, holder)

	result, err := repo.StopRunLock(ctx, 42)
	if err != nil {
		t.Fatalf("StopRunLock: %v", err)
	}
	if result.State != vcs.StopCleared {
		t.Errorf("state = %q, want %q (a zombie is already dead, so there is nothing to signal)", result.State, vcs.StopCleared)
	}
	if _, err := os.Stat(lockPath(t, repo, 42)); !os.IsNotExist(err) {
		t.Errorf("the zombie's lock survived a stop: %v", err)
	}
}

func TestStopRunLockStopsALiveHolder(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")

	sleeper := osexec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatalf("start a live holder: %v", err)
	}
	t.Cleanup(func() {
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()
	})
	holder := fmt.Sprintf("%d on %s since 2026-01-01T00:00:00Z", sleeper.Process.Pid, hostForLock(t))
	seedLock(t, repo, 42, holder)

	result, err := repo.StopRunLock(ctx, 42)
	if err != nil {
		t.Fatalf("StopRunLock: %v", err)
	}
	if result.State != vcs.StopStopped {
		t.Errorf("state = %q, want %q", result.State, vcs.StopStopped)
	}
	if vcs.PIDAlive(sleeper.Process.Pid) {
		t.Error("the holder still reads alive after a stop")
	}
	_ = sleeper.Wait()
	if _, err := os.Stat(lockPath(t, repo, 42)); !os.IsNotExist(err) {
		t.Errorf("the stopped run's lock survived: %v", err)
	}

	lock, err := repo.AcquireRunLock(ctx, 42, "local")
	if err != nil {
		t.Fatalf("AcquireRunLock after stopping: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	if !lock.Held() {
		t.Error("no lock was taken after the live run was stopped")
	}
}

func TestStopRunLockKillsAHolderThatIgnoresTerm(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")

	// A holder that traps TERM away can only be stopped by the kill the
	// term grace expires into. The grace is the production five seconds,
	// so this test takes that long; shortening it per test would leave
	// the escalation itself unexercised.
	holderProc := osexec.Command("sh", "-c", `trap "" TERM; while :; do sleep 1; done`)
	if err := holderProc.Start(); err != nil {
		t.Fatalf("start a term-ignoring holder: %v", err)
	}
	t.Cleanup(func() {
		_ = holderProc.Process.Kill()
		_ = holderProc.Wait()
	})
	holder := fmt.Sprintf("%d on %s since 2026-01-01T00:00:00Z", holderProc.Process.Pid, hostForLock(t))
	seedLock(t, repo, 42, holder)

	result, err := repo.StopRunLock(ctx, 42)
	if err != nil {
		t.Fatalf("StopRunLock: %v", err)
	}
	if result.State != vcs.StopStopped {
		t.Errorf("state = %q, want %q", result.State, vcs.StopStopped)
	}
	_ = holderProc.Wait()
	if vcs.PIDAlive(holderProc.Process.Pid) {
		t.Error("the term-ignoring holder still reads alive after a stop")
	}
	if _, err := os.Stat(lockPath(t, repo, 42)); !os.IsNotExist(err) {
		t.Errorf("the killed holder's lock survived: %v", err)
	}
}

func TestStopRunLockRefusesAHolderOnAnotherHost(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")

	sleeper := osexec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatalf("start a live holder: %v", err)
	}
	t.Cleanup(func() {
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()
	})
	holder := fmt.Sprintf("%d on elsewhere.example since 2026-01-01T00:00:00Z", sleeper.Process.Pid)
	seedLock(t, repo, 42, holder)

	_, err := repo.StopRunLock(ctx, 42)
	refusal, ok := err.(*vcs.Refusal)
	if !ok {
		t.Fatalf("StopRunLock = %v, want a refusal", err)
	}
	if !strings.Contains(refusal.Message, "elsewhere.example") {
		t.Errorf("message = %q, want it to name the other host", refusal.Message)
	}
	if !vcs.PIDAlive(sleeper.Process.Pid) {
		t.Error("a stop refused for another host signalled the local process carrying that pid")
	}
	if _, err := os.Stat(lockPath(t, repo, 42)); err != nil {
		t.Errorf("a refused stop removed the lock: %v", err)
	}
}

// hostForLock is this machine's own name as the lock records it, which a
// stop compares the holder's host against before it signals anything.
func hostForLock(t *testing.T) string {
	t.Helper()
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Skip("this machine will not name itself")
	}
	return host
}

func TestPIDAliveDistinguishesLiveDeadAndZombie(t *testing.T) {
	if !vcs.PIDAlive(os.Getpid()) {
		t.Error("this test's own process reads as not alive")
	}
	if vcs.PIDAlive(0) || vcs.PIDAlive(-1) {
		t.Error("a pid that cannot name a process reads as alive")
	}

	live := osexec.Command("sleep", "60")
	if err := live.Start(); err != nil {
		t.Fatalf("start a live process: %v", err)
	}
	t.Cleanup(func() {
		_ = live.Process.Kill()
		_ = live.Wait()
	})
	if !vcs.PIDAlive(live.Process.Pid) {
		t.Errorf("live pid %d reads as not alive", live.Process.Pid)
	}

	dead := osexec.Command("sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatalf("run a process to death: %v", err)
	}
	if vcs.PIDAlive(dead.Process.Pid) {
		t.Errorf("reaped pid %d reads as alive", dead.Process.Pid)
	}

	zombie := osexec.Command("sleep", "60")
	if err := zombie.Start(); err != nil {
		t.Fatalf("start a holder to zombify: %v", err)
	}
	t.Cleanup(func() { _ = zombie.Wait() })
	if err := zombie.Process.Kill(); err != nil {
		t.Fatalf("kill the holder: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if vcs.PIDAlive(zombie.Process.Pid) {
		t.Errorf("zombie pid %d reads as alive; kill -0 alone would say the same, which is the defect", zombie.Process.Pid)
	}
}

// Automated mode uses one concurrency group per pull request, so it takes no
// lock at all (lib/run.sh:191).
func TestRunLockAutomatedModeTakesNothing(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")

	lock, err := repo.AcquireRunLock(ctx, 42, "automated")
	if err != nil {
		t.Fatalf("AcquireRunLock: %v", err)
	}
	if lock.Held() {
		t.Error("automated mode took a lock")
	}
	if _, err := os.Stat(lockPath(t, repo, 42)); !os.IsNotExist(err) {
		t.Errorf("automated mode wrote a lock file: %v", err)
	}
}

// Outside a repository there is nothing to key the lock on, and the shell
// returns 0 rather than failing (lib/run.sh:192-193).
func TestRunLockOutsideARepository(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	lock, err := git.At(realTempDir(t)).AcquireRunLock(ctx, 42, "local")
	if err != nil {
		t.Fatalf("AcquireRunLock: %v", err)
	}
	if lock.Held() {
		t.Error("a lock was taken outside a repository")
	}
}

func TestParseHolder(t *testing.T) {
	tests := []struct {
		line string
		pid  int
		host string
		when string
	}{
		{line: "1234 on mac.local since 2026-01-01T00:00:00Z", pid: 1234, host: "mac.local", when: "2026-01-01T00:00:00Z"},
		{line: "1234 on local since 2026-01-01T00:00:00Z", pid: 1234, host: "local", when: "2026-01-01T00:00:00Z"},
		{line: "not-a-pid on host since when", pid: 0, host: "host", when: "when"},
		{line: "", pid: 0, host: "", when: ""},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			holder := vcs.ParseHolder(tt.line)
			if holder.PID != tt.pid || holder.Host != tt.host || holder.Since != tt.when {
				t.Errorf("ParseHolder(%q) = %+v, want pid %d host %q since %q", tt.line, holder, tt.pid, tt.host, tt.when)
			}
			if holder.Raw != tt.line {
				t.Errorf("Raw = %q, want %q", holder.Raw, tt.line)
			}
		})
	}
}

// seedLock writes a holder line as though another run had taken the lock.
func seedLock(t *testing.T, repo *vcs.Repository, pr int, holder string) {
	t.Helper()
	path := lockPath(t, repo, pr)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("make the lock directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(holder+"\n"), 0o644); err != nil {
		t.Fatalf("seed the lock: %v", err)
	}
}
