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
// can read the environment a git child would have received.
type recordingRunner struct{ specs []exec.Spec }

func (r *recordingRunner) Run(_ context.Context, spec exec.Spec) exec.Result {
	r.specs = append(r.specs, spec)
	return exec.Result{ExitCode: 0}
}

func specEnv(t *testing.T, r *recordingRunner) []string {
	t.Helper()
	if len(r.specs) != 1 {
		t.Fatalf("ran %d children, want exactly 1", len(r.specs))
	}
	return r.specs[0].Env
}

func envValue(env []string, name string) (string, bool) {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, name+"="); ok {
			return value, true
		}
	}
	return "", false
}

// onRunner pins the process as a GitHub Actions runner, whatever the machine
// running the suite is. Tests are not that runner: without this the scrub
// and the helper would no-op on a laptop and run under CI, and the suite
// would prove opposite things in the two places.
func onRunner(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "true")
}

func localMachine(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "")
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
// credential file the local config pulls in through includeIf.
func persistV6(t *testing.T, repo *vcs.Repository, repoDir, creds string) {
	t.Helper()
	mustGit(t, repo, "config", "--local", "user.name", "kept")
	mustGit(t, repo, "config", "--local", "includeIf.gitdir:"+repoDir+"/.git.path", creds)
}

const v6Creds = "[http \"https://github.com/\"]\n\textraheader = AUTHORIZATION: basic dGVzdA==\n# a comment that survives\n"

func extraheaders(t *testing.T, repo *vcs.Repository) vcs.Output {
	t.Helper()
	output, err := repo.Run(context.Background(), "config", "--show-origin", "--get-regexp", `^http\..*\.extraheader$`)
	if err != nil {
		t.Fatalf("list extraheaders: %v", err)
	}
	return output
}

func TestRemovePersistedCredentialsRemovesTheV5Entries(t *testing.T) {
	onRunner(t)
	repoDir := filepath.Join(realTempDir(t), "repo")
	repo := initRepo(t, testGit(t), repoDir)
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
	onRunner(t)
	root := realTempDir(t)
	repoDir := filepath.Join(root, "repo")
	repo := initRepo(t, testGit(t), repoDir)
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
		t.Errorf("the scrub rewrote the credential file instead of unsetting the key: %q", rest)
	}
	if got, err := repo.ConfigGet(context.Background(), "user.name"); err != nil || got != "kept" {
		t.Errorf("user.name = %q, %v; the scrub must keep unrelated configuration", got, err)
	}
}

func TestRemovePersistedCredentialsIsCleanWhenNothingWasPersisted(t *testing.T) {
	onRunner(t)
	repo := initRepo(t, testGit(t), filepath.Join(realTempDir(t), "repo"))

	removed, err := repo.RemovePersistedCredentials(context.Background())
	if err != nil {
		t.Fatalf("RemovePersistedCredentials: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed %v from a checkout holding nothing", removed)
	}
}

func TestRemovePersistedCredentialsIsANoOpLocally(t *testing.T) {
	localMachine(t)
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
	onRunner(t)
	repo := testGit(t).At(filepath.Join(realTempDir(t), "missing"))

	_, err := repo.RemovePersistedCredentials(context.Background())
	if err == nil {
		t.Fatal("RemovePersistedCredentials answered nil where git could not run")
	}
	var refusal *vcs.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error is %T, want a *vcs.Refusal so the leg refuses loudly", err)
	}
}

func TestGitRunCarriesTheCredentialHelperOnARunner(t *testing.T) {
	onRunner(t)
	recorder := &recordingRunner{}
	git := vcs.New(recorder, []string{"PATH=/usr/bin"})

	if _, err := git.Run(context.Background(), vcs.Call{Args: []string{"--version"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	env := specEnv(t, recorder)
	for _, want := range []string{
		"PATH=/usr/bin",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.helper",
		"GIT_CONFIG_VALUE_1=!gh auth git-credential",
	} {
		name, _, _ := strings.Cut(want, "=")
		got, ok := envValue(env, name)
		if !ok {
			t.Errorf("the child environment has no %s", name)
			continue
		}
		if entry := name + "=" + got; entry != want {
			t.Errorf("child environment carries %q, want %q", entry, want)
		}
	}
	if len(env) != 6 {
		t.Errorf("child environment holds %d entries %q, want exactly the six above", len(env), env)
	}
}

func TestGitRunAppendsAfterExistingConfigEntries(t *testing.T) {
	onRunner(t)
	recorder := &recordingRunner{}
	git := vcs.New(recorder, []string{
		"PATH=/usr/bin",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=user.name",
		"GIT_CONFIG_VALUE_0=kept",
	})

	if _, err := git.Run(context.Background(), vcs.Call{Args: []string{"--version"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	env := specEnv(t, recorder)
	for _, want := range []string{
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=user.name",
		"GIT_CONFIG_VALUE_0=kept",
		"GIT_CONFIG_KEY_1=credential.helper",
		"GIT_CONFIG_VALUE_1=",
		"GIT_CONFIG_KEY_2=credential.helper",
		"GIT_CONFIG_VALUE_2=!gh auth git-credential",
	} {
		name, _, _ := strings.Cut(want, "=")
		got, ok := envValue(env, name)
		if !ok {
			t.Errorf("the child environment has no %s", name)
			continue
		}
		if entry := name + "=" + got; entry != want {
			t.Errorf("child environment carries %q, want %q", entry, want)
		}
	}
}

func TestGitRunTreatsAMalformedCountAsZero(t *testing.T) {
	onRunner(t)
	recorder := &recordingRunner{}
	git := vcs.New(recorder, []string{"PATH=/usr/bin", "GIT_CONFIG_COUNT=abc"})

	if _, err := git.Run(context.Background(), vcs.Call{Args: []string{"--version"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	env := specEnv(t, recorder)
	got, ok := envValue(env, "GIT_CONFIG_COUNT")
	if !ok || got != "2" {
		t.Errorf("GIT_CONFIG_COUNT = %q, want 2 over the malformed entry", got)
	}
}

func TestGitRunLeavesTheEnvironmentAloneLocally(t *testing.T) {
	localMachine(t)
	recorder := &recordingRunner{}
	base := []string{"PATH=/usr/bin", "HOME=/tmp"}
	git := vcs.New(recorder, base)

	if _, err := git.Run(context.Background(), vcs.Call{Args: []string{"--version"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	env := specEnv(t, recorder)
	if len(env) != len(base) {
		t.Fatalf("child environment is %q, want it untouched: %q", env, base)
	}
	for i := range base {
		if env[i] != base[i] {
			t.Fatalf("child environment is %q, want it untouched: %q", env, base)
		}
	}
}

// The helper reaches a real git child, not just the recorded spec: with the
// runner signal set, git itself reports the helper the invocation carried.
func TestARealGitChildReportsTheHelperOnARunner(t *testing.T) {
	onRunner(t)
	repo := initRepo(t, testGit(t), filepath.Join(realTempDir(t), "repo"))

	output, err := repo.Run(context.Background(), "config", "--get-all", "credential.helper")
	if err != nil {
		t.Fatalf("git config: %v", err)
	}
	if !output.OK() || !strings.Contains(output.Stdout, "!gh auth git-credential") {
		t.Errorf("a real git child reports %q, want the gh helper among its helpers", output.Stdout)
	}
}

func TestARealGitChildCarriesNoGhHelperLocally(t *testing.T) {
	localMachine(t)
	repo := initRepo(t, testGit(t), filepath.Join(realTempDir(t), "repo"))

	output, err := repo.Run(context.Background(), "config", "--get-all", "credential.helper")
	if err != nil {
		t.Fatalf("git config: %v", err)
	}
	// Whatever the machine configures — Apple Git answers osxkeychain here
	// from a system file even the null-device scopes do not suppress — is
	// the operator's own helper answering as it always has. The property is
	// that ours is absent.
	if strings.Contains(output.Stdout, "!gh auth git-credential") {
		t.Errorf("a real git child reports %q locally, want no gh helper", output.Stdout)
	}
}
