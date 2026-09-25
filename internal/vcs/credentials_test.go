package vcs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// recordingRunner keeps the specs it was handed and starts nothing, so a test
// can read the invocation a git child would have received.
type recordingRunner struct{ specs []exec.Spec }

func (r *recordingRunner) Run(_ context.Context, spec exec.Spec) exec.Result {
	r.specs = append(r.specs, spec)
	return exec.Result{ExitCode: 0}
}

func specArgs(t *testing.T, r *recordingRunner) []string {
	t.Helper()
	if len(r.specs) != 1 {
		t.Fatalf("ran %d children, want exactly 1", len(r.specs))
	}
	return r.specs[0].Args
}

// runnerGit is a test git on a runner: the ActionsRunner field is set
// directly, the way the composition root sets it, so no test inherits the
// machine it runs on. A test that read the process environment instead would
// prove opposite things on a laptop and under CI.
func runnerGit(t *testing.T) *vcs.Git {
	t.Helper()
	git := testGit(t)
	git.ActionsRunner = true
	return git
}

// persistV5 writes the actions/checkout v5 layout: the token in the
// checkout's own git config, beside configuration that must survive.
func persistV5(t *testing.T, repo *vcs.Repository) {
	t.Helper()
	mustGit(t, repo, "config", "--local", "http.https://github.com/.extraheader", "AUTHORIZATION: basic dGVzdA==")
	mustGit(t, repo, "config", "--local", "http.https://ghe.example.com/.extraheader", "AUTHORIZATION: basic Z2hl")
	mustGit(t, repo, "config", "--local", "user.name", "kept")
	mustGit(t, repo, "config", "--local", "http.sslVerify", "false")
}

// persistV6 writes the actions/checkout v6 layout: the token in a separate
// credential file the local config pulls in through includeIf, the form
// git-auth-helper.ts writes.
func persistV6(t *testing.T, repo *vcs.Repository, repoDir, creds string) {
	t.Helper()
	mustGit(t, repo, "config", "--local", "user.name", "kept")
	mustGit(t, repo, "config", "--local", "includeIf.gitdir:"+repoDir+"/.git.path", creds)
}

const v6Creds = "[http \"https://github.com/\"]\n\textraheader = AUTHORIZATION: basic dGVzdA==\n# a comment that survives\n"

// extraheaders lists persisted entries across every scope, deliberately
// without --local: the independent oracle the scrub is asserted against.
func extraheaders(t *testing.T, repo *vcs.Repository) vcs.Output {
	t.Helper()
	output, err := repo.Run(context.Background(), "config", "--show-origin", "--get-regexp", `^http\..*\.extraheader$`)
	if err != nil {
		t.Fatalf("list extraheaders: %v", err)
	}
	return output
}

func TestRemovePersistedCredentialsRemovesTheV5Entries(t *testing.T) {
	repoDir := filepath.Join(realTempDir(t), "repo")
	repo := initRepo(t, runnerGit(t), repoDir)
	persistV5(t, repo)

	removed, err := repo.RemovePersistedCredentials(context.Background())
	if err != nil {
		t.Fatalf("RemovePersistedCredentials: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed %d entries, want 2 (github.com and the enterprise host)", len(removed))
	}
	for _, r := range removed {
		if r.File != ".git/config" {
			t.Errorf("removed %s from %q, want the local config", r.Key, r.File)
		}
		if !strings.HasSuffix(r.Key, ".extraheader") {
			t.Errorf("removed key %q, want an extraheader key", r.Key)
		}
	}
	if output := extraheaders(t, repo); output.OK() {
		t.Errorf("extraheaders remain after the scrub: %q", output.Stdout)
	} else if output.ExitCode != 1 {
		t.Errorf("listing after the scrub exited %d, want 1 for no match", output.ExitCode)
	}
	if got, err := repo.ConfigGet(context.Background(), "user.name"); err != nil || got != "kept" {
		t.Errorf("user.name = %q, %v; the scrub must keep unrelated configuration", got, err)
	}
	if got, err := repo.ConfigGet(context.Background(), "http.sslVerify"); err != nil || got != "false" {
		t.Errorf("http.sslVerify = %q, %v; the scrub must keep http keys that are not extraheaders", got, err)
	}
}

func TestRemovePersistedCredentialsRemovesTheV6Entry(t *testing.T) {
	root := realTempDir(t)
	repoDir := filepath.Join(root, "repo")
	repo := initRepo(t, runnerGit(t), repoDir)
	creds := write(t, root, "creds", v6Creds)
	persistV6(t, repo, repoDir, creds)

	if output := extraheaders(t, repo); !output.OK() {
		t.Fatal("the fixture's includeIf did not match, so the scrub has nothing to find")
	}
	removed, err := repo.RemovePersistedCredentials(context.Background())
	if err != nil {
		t.Fatalf("RemovePersistedCredentials: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed %d entries, want 1", len(removed))
	}
	if removed[0].File != creds {
		t.Errorf("removed from %q, want the credential file %q", removed[0].File, creds)
	}
	if output := extraheaders(t, repo); output.OK() {
		t.Errorf("extraheaders remain after the scrub: %q", output.Stdout)
	}
	rest, err := os.ReadFile(creds)
	if err != nil {
		t.Fatalf("read the credential file: %v", err)
	}
	if strings.Contains(string(rest), "extraheader") {
		t.Errorf("the credential file still holds the entry: %q", rest)
	}
	if !strings.Contains(string(rest), "a comment that survives") {
		t.Errorf("the scrub rewrote the credential file instead of unsetting the entry: %q", rest)
	}
	if got, err := repo.ConfigGet(context.Background(), "user.name"); err != nil || got != "kept" {
		t.Errorf("user.name = %q, %v; the scrub must keep unrelated configuration", got, err)
	}
}

// A non-ASCII credential path arrives C-quoted without --null, and the
// quoted text matches no file. The scrub must remove through it.
func TestRemovePersistedCredentialsRemovesThroughANonASCIIPath(t *testing.T) {
	root := realTempDir(t)
	repoDir := filepath.Join(root, "repo")
	repo := initRepo(t, runnerGit(t), repoDir)
	creds := write(t, root, "créds.config", v6Creds)
	persistV6(t, repo, repoDir, creds)

	if output := extraheaders(t, repo); !output.OK() {
		t.Fatal("the fixture's includeIf did not match, so the scrub has nothing to find")
	}
	removed, err := repo.RemovePersistedCredentials(context.Background())
	if err != nil {
		t.Fatalf("RemovePersistedCredentials: %v", err)
	}
	if len(removed) != 1 || removed[0].File != creds {
		t.Fatalf("removed %v, want the entry from %q", removed, creds)
	}
	if output := extraheaders(t, repo); output.OK() {
		t.Errorf("extraheaders remain after the scrub: %q", output.Stdout)
	}
}

// Git reports the local config relative to the top of the worktree while
// --file resolves against the working directory, so from a subdirectory the
// origin must not be passed back blindly.
func TestRemovePersistedCredentialsRunsFromASubdirectory(t *testing.T) {
	repoDir := filepath.Join(realTempDir(t), "repo")
	repo := initRepo(t, runnerGit(t), repoDir)
	persistV5(t, repo)
	sub := filepath.Join(repoDir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("make %s: %v", sub, err)
	}

	removed, err := runnerGit(t).At(sub).RemovePersistedCredentials(context.Background())
	if err != nil {
		t.Fatalf("RemovePersistedCredentials: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed %d entries from a subdirectory, want 2", len(removed))
	}
	if output := extraheaders(t, repo); output.OK() {
		t.Errorf("extraheaders remain after the scrub: %q", output.Stdout)
	}
}

// --local is the scope: a global extraheader is another configuration's,
// and the scrub neither lists nor touches it. The fixture git nulls the
// global scope, so this case builds its own with a real global file.
func TestRemovePersistedCredentialsLeavesGlobalConfigAlone(t *testing.T) {
	root := realTempDir(t)
	global := filepath.Join(root, "gitconfig")
	before := "[http \"https://github.com/\"]\n\textraheader = AUTHORIZATION: basic R0xPQVA==\n"
	if err := os.WriteFile(global, []byte(before), 0o644); err != nil {
		t.Fatalf("write the global config: %v", err)
	}
	env := append([]string{
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_GLOBAL=" + global,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	}, exec.Inherit([]string{"PATH"})...)
	git := vcs.New(exec.NewOrchestratorRunner(), env)
	git.ActionsRunner = true
	repo := initRepo(t, git, filepath.Join(root, "repo"))
	mustGit(t, repo, "config", "--local", "http.https://github.com/.extraheader", "AUTHORIZATION: basic dGVzdA==")

	removed, err := repo.RemovePersistedCredentials(context.Background())
	if err != nil {
		t.Fatalf("RemovePersistedCredentials: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed %d entries, want the 1 local one", len(removed))
	}
	rest, err := os.ReadFile(global)
	if err != nil {
		t.Fatalf("read the global config: %v", err)
	}
	if string(rest) != before {
		t.Errorf("the global config changed under the scrub: %q", rest)
	}
}

// An extraheader the checkout did not write keeps its value: the unset names
// the checkout's form, so a second value on the same key survives.
func TestRemovePersistedCredentialsKeepsOtherValuesOnTheKey(t *testing.T) {
	repoDir := filepath.Join(realTempDir(t), "repo")
	repo := initRepo(t, runnerGit(t), repoDir)
	mustGit(t, repo, "config", "--local", "--add", "http.https://github.com/.extraheader", "X-Custom: keep")
	mustGit(t, repo, "config", "--local", "--add", "http.https://github.com/.extraheader", "AUTHORIZATION: basic dGVzdA==")

	removed, err := repo.RemovePersistedCredentials(context.Background())
	if err != nil {
		t.Fatalf("RemovePersistedCredentials: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed %d entries, want 1", len(removed))
	}
	values, err := repo.ConfigGetAll(context.Background(), "http.https://github.com/.extraheader")
	if err != nil {
		t.Fatalf("read the key: %v", err)
	}
	if len(values) != 1 || values[0] != "X-Custom: keep" {
		t.Errorf("remaining values = %q, want only the operator's header", values)
	}
}

// Two checkout values on one key: --unset refuses a key with multiple
// values, so the scrub unsets all of them at once and still keeps the rest.
func TestRemovePersistedCredentialsRemovesEveryCheckoutValueOnTheKey(t *testing.T) {
	repoDir := filepath.Join(realTempDir(t), "repo")
	repo := initRepo(t, runnerGit(t), repoDir)
	mustGit(t, repo, "config", "--local", "--add", "http.https://github.com/.extraheader", "AUTHORIZATION: basic Zmlyc3Q=")
	mustGit(t, repo, "config", "--local", "--add", "http.https://github.com/.extraheader", "AUTHORIZATION: basic c2Vjb25k")
	mustGit(t, repo, "config", "--local", "--add", "http.https://github.com/.extraheader", "X-Custom: keep")

	removed, err := repo.RemovePersistedCredentials(context.Background())
	if err != nil {
		t.Fatalf("RemovePersistedCredentials: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed %d entries, want 1", len(removed))
	}
	values, err := repo.ConfigGetAll(context.Background(), "http.https://github.com/.extraheader")
	if err != nil {
		t.Fatalf("read the key: %v", err)
	}
	if len(values) != 1 || values[0] != "X-Custom: keep" {
		t.Errorf("remaining values = %q, want only the operator's header", values)
	}
}

func TestRemovePersistedCredentialsIsCleanWhenNothingWasPersisted(t *testing.T) {
	repo := initRepo(t, runnerGit(t), filepath.Join(realTempDir(t), "repo"))

	removed, err := repo.RemovePersistedCredentials(context.Background())
	if err != nil {
		t.Fatalf("RemovePersistedCredentials: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed %v from a checkout holding nothing", removed)
	}
}

func TestRemovePersistedCredentialsIsANoOpLocally(t *testing.T) {
	recorder := &recordingRunner{}
	// A directory git could never run in: the no-op must not start it.
	repo := vcs.New(recorder, nil).At(filepath.Join(t.TempDir(), "missing"))

	removed, err := repo.RemovePersistedCredentials(context.Background())
	if err != nil {
		t.Fatalf("RemovePersistedCredentials: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed %v locally, want nothing", removed)
	}
	if len(recorder.specs) != 0 {
		t.Errorf("started %d git children locally, want none", len(recorder.specs))
	}
}

func TestRemovePersistedCredentialsRefusesWhenGitFails(t *testing.T) {
	repo := runnerGit(t).At(filepath.Join(realTempDir(t), "missing"))

	_, err := repo.RemovePersistedCredentials(context.Background())
	if err == nil {
		t.Fatal("RemovePersistedCredentials answered nil where git could not run")
	}
	var refusal *vcs.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error is %T, want a *vcs.Refusal so the leg refuses loudly", err)
	}
	if !strings.Contains(refusal.Message, "—") {
		t.Errorf("refusal %q names no cause", refusal.Message)
	}
}

func TestRemovePersistedCredentialsReturnsACancelledContext(t *testing.T) {
	repo := initRepo(t, runnerGit(t), filepath.Join(realTempDir(t), "repo"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := repo.RemovePersistedCredentials(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error is %v, want the cancelled context so the legs skip their fatal report", err)
	}
}

func TestGitRunCarriesTheCredentialPairOnARunner(t *testing.T) {
	recorder := &recordingRunner{}
	git := vcs.New(recorder, []string{"PATH=/usr/bin"})
	git.ActionsRunner = true

	if _, err := git.Run(context.Background(), vcs.Call{Args: []string{"--version"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := specArgs(t, recorder)
	want := []string{
		"-c", "credential.https://github.com.helper=",
		"-c", "credential.https://github.com.helper=!gh auth git-credential",
		"--version",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("args = %q, want the credential pair ahead of the subcommand", got)
	}
	if len(recorder.specs[0].Env) != 1 {
		t.Errorf("env = %q, want it untouched: the pair travels as arguments", recorder.specs[0].Env)
	}
}

func TestGitRunLeavesTheInvocationAloneLocally(t *testing.T) {
	recorder := &recordingRunner{}
	git := vcs.New(recorder, []string{"PATH=/usr/bin"})

	if _, err := git.Run(context.Background(), vcs.Call{Args: []string{"--version"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := specArgs(t, recorder); len(got) != 1 || got[0] != "--version" {
		t.Errorf("args = %q locally, want them untouched", got)
	}
}

// The pair reaches a real git child, not just the recorded spec: with the
// runner flag set, git itself reports the helper the invocation carried.
func TestARealGitChildReportsTheHelperOnARunner(t *testing.T) {
	repo := initRepo(t, runnerGit(t), filepath.Join(realTempDir(t), "repo"))

	output, err := repo.Run(context.Background(), "config", "--get-all", "credential.https://github.com.helper")
	if err != nil {
		t.Fatalf("git config: %v", err)
	}
	if !output.OK() || !strings.Contains(output.Stdout, "!gh auth git-credential") {
		t.Errorf("a real git child reports %q, want the gh helper among its helpers", output.Stdout)
	}
}

func TestARealGitChildCarriesNoGhHelperLocally(t *testing.T) {
	repo := initRepo(t, testGit(t), filepath.Join(realTempDir(t), "repo"))

	output, err := repo.Run(context.Background(), "config", "--get-all", "credential.https://github.com.helper")
	if err != nil {
		t.Fatalf("git config: %v", err)
	}
	if strings.Contains(output.Stdout, "!gh auth git-credential") {
		t.Errorf("a real git child reports %q locally, want no gh helper", output.Stdout)
	}
}

func TestRemovedCredentialLineNamesTheCountAndTheFiles(t *testing.T) {
	got := vcs.RemovedCredentialLine([]vcs.RemovedCredential{
		{Key: "http.https://github.com/.extraheader", File: ".git/config"},
		{Key: "http.https://ghe.example.com/.extraheader", File: ".git/config"},
		{Key: "http.https://github.com/.extraheader", File: "/tmp/runner/creds"},
	})
	want := "removed 3 persisted checkout credential entries from .git/config, /tmp/runner/creds"
	if got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}
