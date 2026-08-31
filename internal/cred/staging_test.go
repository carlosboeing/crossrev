package cred_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/cred"
)

// fakeEnv is a process environment a test owns outright.
//
// The alternative is t.Setenv, and it is the wrong tool here: Prepare exports a
// variable and Discard restores it, so a test driving both against the real
// environment would be asserting on state every other test in the binary
// shares.
type fakeEnv struct{ vars map[string]string }

func newEnv(pairs map[string]string) *fakeEnv {
	vars := map[string]string{}
	for name, value := range pairs {
		vars[name] = value
	}
	return &fakeEnv{vars: vars}
}

func (e *fakeEnv) Lookup(name string) (string, bool) {
	value, set := e.vars[name]
	return value, set
}
func (e *fakeEnv) Set(name, value string) error { e.vars[name] = value; return nil }
func (e *fakeEnv) Unset(name string) error      { delete(e.vars, name); return nil }

func fixedNow() time.Time { return epoch }

// options stage into a directory this test owns, against a real file system so
// the mode assertions below measure real modes.
func options(t *testing.T, env *fakeEnv) cred.Options {
	t.Helper()
	return cred.Options{Env: env, Now: fixedNow, ScratchRoot: t.TempDir()}
}

func opencode(t *testing.T) cred.Descriptor {
	t.Helper()
	d := descriptors(t).For("opencode")
	if d.Credential.Staging.Path != "opencode/auth.json" {
		t.Fatalf("the opencode staging path is now %q, so it no longer covers the nested case",
			d.Credential.Staging.Path)
	}
	return d
}

func mode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// A restored credential lands in a scratch home, not in the harness's own
// store, and the harness is pointed at it
// (tests/test-credentials.sh:112-129).
func TestPrepareStagesIntoAScratchHomeNobodyReadsAgain(t *testing.T) {
	raw := credential(t, 86400)
	env := newEnv(map[string]string{"CROSSREV_CODEX_AUTH": string(raw)})

	staged, err := cred.Prepare(codex(t), "", options(t, env))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if staged.Dir == "" {
		t.Fatal("Prepare staged nothing")
	}
	if got, _ := env.Lookup("CODEX_HOME"); got != staged.Dir {
		t.Errorf("CODEX_HOME = %q, want the scratch home %q", got, staged.Dir)
	}
	if want := filepath.Join(staged.Dir, "auth.json"); staged.File != want {
		t.Errorf("staged file = %q, want %q", staged.File, want)
	}

	written, err := os.ReadFile(staged.File)
	if err != nil {
		t.Fatalf("reading the staged credential: %v", err)
	}
	if string(written) != string(raw) {
		t.Error("the staged credential is not what the secret held")
	}
	// Readable by nobody else (tests/test-credentials.sh:128-129).
	if got := mode(t, staged.File); got != 0o600 {
		t.Errorf("the staged credential is mode %o, want 600", got)
	}
	// `mktemp -d` makes the scratch home 0700 and so does os.MkdirTemp.
	if got := mode(t, staged.Dir); got != 0o700 {
		t.Errorf("the scratch home is mode %o, want 700", got)
	}
}

// Two legs running at once must not share a copy. They each borrow their own,
// and neither is the one the secret holds — which is what makes several holders
// safe as long as none of them writes
// (tests/test-credentials.sh:133-140).
func TestASecondLegGetsItsOwnCopy(t *testing.T) {
	env := newEnv(map[string]string{"CROSSREV_CODEX_AUTH": string(credential(t, 86400))})
	o := options(t, env)

	first, err := cred.Prepare(codex(t), "", o)
	if err != nil {
		t.Fatalf("first Prepare: %v", err)
	}
	second, err := cred.Prepare(codex(t), "", o)
	if err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	if first.Dir == second.Dir {
		t.Errorf("both legs staged into %q", first.Dir)
	}

	// The Bash pair keeps CRED_SCRATCH in a global, so a second prepare with no
	// discard between them overwrites the only record of the first directory
	// and leaks it (lib/credentials.sh:20, :144). A handle cannot be
	// overwritten by somebody else's call, so both are still removable.
	if err := cred.Discard(first); err != nil {
		t.Errorf("discarding the first: %v", err)
	}
	if err := cred.Discard(second); err != nil {
		t.Errorf("discarding the second: %v", err)
	}
	for _, dir := range []string{first.Dir, second.Dir} {
		if dir != "" {
			t.Errorf("a discarded handle still names %q", dir)
		}
	}
}

// The scratch home is gone when the leg finishes, and the staging variable is
// unset again (tests/test-credentials.sh:142-145).
func TestDiscardRemovesTheScratchHomeAndUnsetsTheVariable(t *testing.T) {
	env := newEnv(map[string]string{"CROSSREV_CODEX_AUTH": string(credential(t, 86400))})

	staged, err := cred.Prepare(codex(t), "", options(t, env))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	dir := staged.Dir

	if err := cred.Discard(staged); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the scratch home survived the discard: %v", err)
	}
	if _, set := env.Lookup("CODEX_HOME"); set {
		t.Error("CODEX_HOME is still set after the discard")
	}
}

// The discard clears whatever variable the descriptor named, not one written
// out by hand. XDG_DATA_HOME is the case that made the difference visible: it
// is not CrossRev's variable, so a stale value pointing at a deleted directory
// reaches everything XDG-aware the same process runs next
// (tests/test-credentials.sh:290-303).
func TestDiscardClearsTheVariableTheDescriptorNamed(t *testing.T) {
	env := newEnv(map[string]string{"CROSSREV_OPENCODE_AUTH": `{"opencode":{"type":"api","key":"stub"}}`})

	staged, err := cred.Prepare(opencode(t), "", options(t, env))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if staged.Env != "XDG_DATA_HOME" {
		t.Fatalf("opencode staged through %q, want XDG_DATA_HOME", staged.Env)
	}
	// Its staging path carries a directory, which the staging write has to
	// create rather than assume (tests/test-credentials.sh:277-278).
	if got, _ := os.ReadFile(staged.File); !strings.Contains(string(got), `"stub"`) {
		t.Errorf("the staged credential is %q", got)
	}
	if got := mode(t, staged.File); got != 0o600 {
		t.Errorf("the staged credential is mode %o, want 600", got)
	}
	// 0700 rather than the 0755 `mkdir -p` produces under the default umask.
	// Measured on the shell: the opencode staging directory came out 755.
	if got := mode(t, filepath.Dir(staged.File)); got != 0o700 {
		t.Errorf("the staging directory is mode %o, want 700", got)
	}

	dir := staged.Dir
	if err := cred.Discard(staged); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, set := env.Lookup("XDG_DATA_HOME"); set {
		t.Error("XDG_DATA_HOME is still set after the discard")
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the scratch home survived the discard: %v", err)
	}
}

// A variable the operator set before CrossRev ran is theirs: the discard puts
// it back rather than unsetting it (tests/test-credentials.sh:305-314).
func TestDiscardRestoresAnOperatorsOwnValue(t *testing.T) {
	env := newEnv(map[string]string{
		"CROSSREV_OPENCODE_AUTH": `{"opencode":{"type":"api","key":"stub"}}`,
		"XDG_DATA_HOME":          "/operator/data",
	})

	staged, err := cred.Prepare(opencode(t), "", options(t, env))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got, _ := env.Lookup("XDG_DATA_HOME"); got == "/operator/data" {
		t.Error("a staged run did not override the operator's value")
	}
	if err := cred.Discard(staged); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if got, _ := env.Lookup("XDG_DATA_HOME"); got != "/operator/data" {
		t.Errorf("XDG_DATA_HOME = %q, want the operator's own value back", got)
	}
}

// An empty value the operator set is still a value they set. `${!v+set}` at
// lib/credentials.sh:152 tells it from an unset variable, and restoring it as
// unset would change what the rest of the process sees.
func TestDiscardRestoresAnEmptyValueRatherThanUnsettingIt(t *testing.T) {
	env := newEnv(map[string]string{
		"CROSSREV_OPENCODE_AUTH": `{"opencode":{"type":"api","key":"stub"}}`,
		"XDG_DATA_HOME":          "",
	})

	staged, err := cred.Prepare(opencode(t), "", options(t, env))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := cred.Discard(staged); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	value, set := env.Lookup("XDG_DATA_HOME")
	if !set || value != "" {
		t.Errorf("XDG_DATA_HOME = %q (set %t), want an empty value that is still set", value, set)
	}
}

// The repro at the boundary the run takes it: two legs in one process, staging
// two different harnesses (tests/test-credentials.sh:317-332).
func TestTheSecondLegDoesNotInheritTheFirstLegsStagingVariable(t *testing.T) {
	env := newEnv(map[string]string{
		"CROSSREV_OPENCODE_AUTH": `{"opencode":{"type":"api","key":"stub"}}`,
		"CROSSREV_CODEX_AUTH":    string(credential(t, 86400)),
	})
	o := options(t, env)

	first, err := cred.Prepare(opencode(t), "", o)
	if err != nil {
		t.Fatalf("first Prepare: %v", err)
	}
	firstDir := first.Dir
	if err := cred.Discard(first); err != nil {
		t.Fatalf("first Discard: %v", err)
	}

	second, err := cred.Prepare(codex(t), "", o)
	if err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	if _, set := env.Lookup("XDG_DATA_HOME"); set {
		t.Error("the second leg inherited the first leg's staging variable")
	}
	if _, err := os.Stat(firstDir); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the first leg's directory is still there rather than gone")
	}
	if err := cred.Discard(second); err != nil {
		t.Fatalf("second Discard: %v", err)
	}
	if _, set := env.Lookup("CODEX_HOME"); set {
		t.Error("the second leg's own variable did not clear in turn")
	}
}

// Discarding twice, or discarding a leg that staged nothing, does nothing at
// all. `[[ -n "$CRED_SCRATCH" ]] || return 0` at lib/credentials.sh:188.
func TestDiscardIsIdempotentAndNilSafe(t *testing.T) {
	if err := cred.Discard(nil); err != nil {
		t.Errorf("Discard(nil) = %v, want nil", err)
	}

	env := newEnv(map[string]string{"CROSSREV_CODEX_AUTH": string(credential(t, 86400))})
	staged, err := cred.Prepare(codex(t), "", options(t, env))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := cred.Discard(staged); err != nil {
		t.Fatalf("first Discard: %v", err)
	}
	env.vars["CODEX_HOME"] = "set-by-somebody-else"
	if err := cred.Discard(staged); err != nil {
		t.Errorf("second Discard = %v, want nil", err)
	}
	if got, _ := env.Lookup("CODEX_HOME"); got != "set-by-somebody-else" {
		t.Errorf("a second discard rewrote CODEX_HOME to %q", got)
	}
}

// With no restored credential, nothing is prepared, and claude needs no restore
// at all — setup-token is long-lived (tests/test-credentials.sh:147-159).
func TestPrepareStagesNothingWhenThereIsNothingToStage(t *testing.T) {
	doc := descriptors(t)
	for _, tc := range []struct {
		name    string
		harness string
		env     map[string]string
	}{
		{"a laptop with no secret", "codex", nil},
		{"a harness whose descriptor names no staging variable", "claude",
			map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "token"}},
		{"a harness the descriptor does not carry", "not-a-harness", nil},
		{"a secret that is set but empty", "codex", map[string]string{"CROSSREV_CODEX_AUTH": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(tc.env)
			staged, err := cred.Prepare(doc.For(tc.harness), "", options(t, env))
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if staged.Dir != "" || staged.Env != "" {
				t.Errorf("Prepare staged %+v, want nothing", staged)
			}
		})
	}
}

// A secret that never arrived, on a runner that needed one. Two shapes, because
// a missing secret produces both: GitHub expands a reference to a secret that
// does not exist into the empty string rather than dropping the variable
// (tests/test-credentials.sh:180-199).
func TestAMissingSecretOnAHostedRunnerStopsTheLeg(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"empty", map[string]string{"RUNNER_ENVIRONMENT": "github-hosted", "CROSSREV_CODEX_AUTH": ""}},
		{"unset", map[string]string{"RUNNER_ENVIRONMENT": "github-hosted"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cred.Prepare(codex(t), "", options(t, newEnv(tc.env)))
			if !errors.Is(err, cred.ErrSecretMissing) {
				t.Fatalf("Prepare error = %v, want ErrSecretMissing", err)
			}
			var refusal *cred.Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("not a *cred.Refusal: %T", err)
			}
			printed := refusal.Reason + " " + refusal.Action
			for _, want := range []string{"CROSSREV_CODEX_AUTH", "secret", "github-hosted", "gh secret set"} {
				if !strings.Contains(printed, want) {
					t.Errorf("the refusal does not mention %q: %q", want, printed)
				}
			}
			// Rule 5 applied to the message: the failure a reader has to be
			// talked out of is assuming the harness is broken, because that is
			// what the 401 looks like (tests/test-credentials.sh:200-202).
			if strings.Contains(printed, "not installed") {
				t.Errorf("the refusal blames the harness: %q", printed)
			}
		})
	}
}

// The same hole, one harness wider: claude stages nothing, so nothing else in
// Prepare would have noticed (tests/test-credentials.sh:204-211).
func TestAMissingClaudeTokenOnAHostedRunnerStopsTheLegToo(t *testing.T) {
	env := newEnv(map[string]string{"RUNNER_ENVIRONMENT": "github-hosted"})
	_, err := cred.Prepare(descriptors(t).For("claude"), "", options(t, env))
	if !errors.Is(err, cred.ErrSecretMissing) {
		t.Fatalf("Prepare error = %v, want ErrSecretMissing", err)
	}
	if !strings.Contains(err.Error(), "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("the refusal does not name the token it wanted: %q", err.Error())
	}
}

// RUNNER_ENVIRONMENT is what separates the three environments, and
// GITHUB_ACTIONS cannot: it is true on a self-hosted runner too, where the
// harness is logged in on disk and no secret is expected
// (lib/preflight.sh:245-250, tests/test-credentials.sh:213-228).
//
// Both halves are asserted, because a build reading GITHUB_ACTIONS would pass
// the hosted case above and fail only here.
func TestOnlyAHostedRunnerDemandsASecret(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"a laptop", nil},
		{"a self-hosted runner", map[string]string{"RUNNER_ENVIRONMENT": "self-hosted"}},
		{"a self-hosted runner inside GitHub Actions", map[string]string{
			"RUNNER_ENVIRONMENT": "self-hosted",
			"GITHUB_ACTIONS":     "true",
		}},
		{"GITHUB_ACTIONS with no RUNNER_ENVIRONMENT at all", map[string]string{
			"GITHUB_ACTIONS": "true",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cred.Prepare(codex(t), "", options(t, newEnv(tc.env))); err != nil {
				t.Errorf("Prepare = %v, want nil: %s is none of crossrev's business", err, tc.name)
			}
		})
	}

	// And the hosted runner whose secret did arrive runs
	// (tests/test-credentials.sh:223-228).
	env := newEnv(map[string]string{
		"RUNNER_ENVIRONMENT":  "github-hosted",
		"CROSSREV_CODEX_AUTH": string(credential(t, 86400)),
	})
	staged, err := cred.Prepare(codex(t), "", options(t, env))
	if err != nil {
		t.Fatalf("a hosted runner whose secret did arrive: %v", err)
	}
	if staged.Dir == "" {
		t.Error("it staged nothing")
	}
}

// A leg using a named endpoint is exempt: its credential is a different
// variable altogether, named by the endpoint block, and the adapter resolves it
// and dies with its own message when it is unset (lib/credentials.sh:119-121).
//
// The literal string "null" counts as no endpoint, because that is what
// `cfg_get` prints for an absent YAML key — measured: `cred_prepare codex null`
// on a hosted runner with no secret still refused.
func TestANamedEndpointIsExemptAndTheStringNullIsNotOne(t *testing.T) {
	hosted := map[string]string{"RUNNER_ENVIRONMENT": "github-hosted"}

	if _, err := cred.Prepare(codex(t), "myendpoint", options(t, newEnv(hosted))); err != nil {
		t.Errorf("a leg with a named endpoint was refused: %v", err)
	}
	for _, endpoint := range []string{"", "null"} {
		if _, err := cred.Prepare(codex(t), endpoint, options(t, newEnv(hosted))); !errors.Is(err, cred.ErrSecretMissing) {
			t.Errorf("endpoint %q error = %v, want ErrSecretMissing", endpoint, err)
		}
	}
}

// A harness the descriptor gives no secret is exempt too, because there is no
// variable to be missing: preflight_harness_secret returns non-zero for one and
// cred_assert_present returns 0 on that (lib/preflight.sh:236-241).
func TestAHarnessWithNoSecretIsNotRefusedOnAHostedRunner(t *testing.T) {
	doc := descriptors(t)
	agy := doc.For("agy")
	if agy.Credential.Secret != "" {
		t.Fatalf("agy now carries the secret %q, so it no longer covers this case", agy.Credential.Secret)
	}
	env := newEnv(map[string]string{"RUNNER_ENVIRONMENT": "github-hosted"})
	if _, err := cred.Prepare(agy, "", options(t, env)); err != nil {
		t.Errorf("Prepare = %v, want nil", err)
	}
}

// Staging is where freshness is asked, on what was just written and before
// anything is exported (lib/credentials.sh:151).
//
// The scratch directory goes with the refusal. The shell's is a `ui_die`, which
// exits the process and leaves the directory behind; a Go caller carries on, so
// a credential left on disk would outlive the leg that refused to use it.
func TestPrepareRefusesAStaleCredentialAndLeavesNothingBehind(t *testing.T) {
	env := newEnv(map[string]string{"CROSSREV_CODEX_AUTH": string(credential(t, 60))})
	root := t.TempDir()

	staged, err := cred.Prepare(codex(t), "", cred.Options{Env: env, Now: fixedNow, ScratchRoot: root})
	if !errors.Is(err, cred.ErrStale) {
		t.Fatalf("Prepare error = %v, want ErrStale", err)
	}
	if staged.Dir != "" {
		t.Errorf("a refused Prepare handed back the scratch home %q", staged.Dir)
	}
	if _, set := env.Lookup("CODEX_HOME"); set {
		t.Error("a refused Prepare exported CODEX_HOME anyway")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading the scratch root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused Prepare left %d entries in the scratch root", len(entries))
	}
}

// An archetype-A credential stages without an expiry to read. opencode's
// auth.json holds {type, key} entries with no JWT inside, so there is no exp
// claim to reason about and the descriptor says so
// (tests/test-credentials.sh:272-288).
func TestAnArchetypeACredentialStagesWithoutAnExpiry(t *testing.T) {
	env := newEnv(map[string]string{"CROSSREV_OPENCODE_AUTH": `{"opencode":{"type":"api","key":"stub"}}`})
	staged, err := cred.Prepare(opencode(t), "", options(t, env))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if staged.Dir == "" {
		t.Error("Prepare staged nothing")
	}
}

// A staging failure is a refusal rather than a silent pass, and it leaves no
// scratch directory behind either.
type failingFS struct {
	cred.OSFileSystem
	failWrite bool
	failMkdir bool
}

func (f failingFS) MkdirAll(path string, perm fs.FileMode) error {
	if f.failMkdir {
		return fs.ErrPermission
	}
	return f.OSFileSystem.MkdirAll(path, perm)
}

func (f failingFS) WriteNew(name string, data []byte, perm fs.FileMode) error {
	if f.failWrite {
		return fs.ErrPermission
	}
	return f.OSFileSystem.WriteNew(name, data, perm)
}

func TestAStagingFailureRefusesRatherThanPassing(t *testing.T) {
	for _, tc := range []struct {
		name string
		fs   failingFS
	}{
		{"the staging directory cannot be created", failingFS{failMkdir: true}},
		{"the credential cannot be written", failingFS{failWrite: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(map[string]string{"CROSSREV_OPENCODE_AUTH": `{"opencode":{}}`})
			root := t.TempDir()

			_, err := cred.Prepare(opencode(t), "", cred.Options{
				Env: env, FS: tc.fs, Now: fixedNow, ScratchRoot: root,
			})
			if !errors.Is(err, cred.ErrStaging) {
				t.Fatalf("Prepare error = %v, want ErrStaging", err)
			}
			if !errors.Is(err, fs.ErrPermission) {
				t.Errorf("the refusal does not wrap the underlying failure: %v", err)
			}
			if _, set := env.Lookup("XDG_DATA_HOME"); set {
				t.Error("a failed Prepare exported XDG_DATA_HOME anyway")
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("reading the scratch root: %v", err)
			}
			if len(entries) != 0 {
				t.Errorf("a failed Prepare left %d entries in the scratch root", len(entries))
			}
		})
	}
}

// No refusal on the staging path quotes the credential.
func TestAStagingRefusalNeverQuotesTheCredential(t *testing.T) {
	secret := "s3cr3tSTAGEDVALUE"
	env := newEnv(map[string]string{"CROSSREV_OPENCODE_AUTH": `{"opencode":{"key":"` + secret + `"}}`})

	_, err := cred.Prepare(opencode(t), "", cred.Options{
		Env: env, FS: failingFS{failWrite: true}, Now: fixedNow, ScratchRoot: t.TempDir(),
	})
	if err == nil {
		t.Fatal("the staging failure was not refused")
	}
	var refusal *cred.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("not a *cred.Refusal: %T", err)
	}
	if strings.Contains(refusal.Reason+" "+refusal.Action, secret) {
		t.Errorf("the refusal quotes the credential: %q", err)
	}
}

// The default Options are the real ones, so a caller that supplies none still
// gets a working staging path.
func TestTheZeroOptionsUseTheRealFileSystem(t *testing.T) {
	env := newEnv(map[string]string{"CROSSREV_OPENCODE_AUTH": `{"opencode":{}}`})
	staged, err := cred.Prepare(opencode(t), "", cred.Options{Env: env})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = cred.Discard(staged) })

	if _, err := os.Stat(staged.File); err != nil {
		t.Errorf("the staged credential is not on disk: %v", err)
	}
	if !strings.HasPrefix(staged.Dir, os.TempDir()) {
		t.Errorf("the scratch home %q is not under the temporary directory %q", staged.Dir, os.TempDir())
	}
}

// OSEnvironment reads and writes the real process environment. t.Setenv is safe
// here because nothing else in this file touches the real one.
func TestOSEnvironmentReadsAndWritesTheProcessEnvironment(t *testing.T) {
	var e cred.OSEnvironment
	t.Setenv("CROSSREV_CRED_PROBE", "before")

	if got, set := e.Lookup("CROSSREV_CRED_PROBE"); !set || got != "before" {
		t.Errorf("Lookup = %q (set %t), want before", got, set)
	}
	if err := e.Set("CROSSREV_CRED_PROBE", "after"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := os.Getenv("CROSSREV_CRED_PROBE"); got != "after" {
		t.Errorf("the process environment holds %q, want after", got)
	}
	if err := e.Unset("CROSSREV_CRED_PROBE"); err != nil {
		t.Fatalf("Unset: %v", err)
	}
	if _, set := os.LookupEnv("CROSSREV_CRED_PROBE"); set {
		t.Error("Unset left the variable set")
	}
}

// A staging path with a `..` segment writes the credential outside the scratch
// home, and Discard's RemoveAll of that home never reaches it — so a restored
// credential outlives the leg that borrowed it, on a disk nobody clears.
//
// Load refuses the descriptor. Prepare refuses the path again, because a
// Descriptor is an exported value any caller can build and this is the write.
func TestPrepareRefusesAStagingPathThatLeavesTheScratchHome(t *testing.T) {
	for _, path := range []string{"../escaped.json", "opencode/../../escaped.json", "", "/etc/escaped.json"} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			env := newEnv(map[string]string{"CROSSREV_ESCAPE_AUTH": `{"any":"thing"}`})
			d := cred.Descriptor{Harness: "probe", Credential: cred.Credential{
				Secret:  "CROSSREV_ESCAPE_AUTH",
				Staging: cred.Staging{Kind: "file", Env: "PROBE_HOME", Path: path},
			}}

			staged, err := cred.Prepare(d, "", cred.Options{Env: env, Now: fixedNow, ScratchRoot: root})
			if !errors.Is(err, cred.ErrStaging) {
				t.Fatalf("Prepare error = %v, want ErrStaging", err)
			}
			if !errors.Is(err, cred.ErrDescriptor) {
				t.Errorf("the refusal does not name the descriptor as the fault: %v", err)
			}
			if staged.Dir != "" || staged.File != "" {
				t.Errorf("a refused Prepare handed back %+v", staged)
			}
			if _, set := env.Lookup("PROBE_HOME"); set {
				t.Error("a refused Prepare exported the staging variable anyway")
			}
			// Nothing written anywhere: not inside the scratch root, and not
			// beside it either, which is where a `..` segment lands.
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("reading the scratch root: %v", err)
			}
			if len(entries) != 0 {
				t.Errorf("a refused Prepare left %d entries in the scratch root", len(entries))
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escaped.json")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the credential was written beside the scratch root: %v", err)
			}
		})
	}
}

// 0600 from birth, which is the property this package claims. os.WriteFile
// applies its mode at creation only, so a file that was already there keeps the
// mode it had — measured: a 0644 file rewritten at 0600 stayed 0644, and a
// write to a symlink landed on the target at 0644 with no error.
//
// The scratch home is fresh from MkdirTemp, so neither should be reachable. The
// write refuses them anyway, because "should not be reachable" is the sentence
// that precedes every one of these.
func TestTheStagingWriteRefusesAFileThatIsAlreadyThere(t *testing.T) {
	var write cred.OSFileSystem
	dir := t.TempDir()

	existing := filepath.Join(dir, "existing.json")
	if err := os.WriteFile(existing, []byte("older"), 0o644); err != nil {
		t.Fatalf("building the fixture: %v", err)
	}
	if err := write.WriteNew(existing, []byte("s3cr3t"), 0o600); !errors.Is(err, fs.ErrExist) {
		t.Errorf("WriteNew over an existing file = %v, want ErrExist", err)
	}
	if got, _ := os.ReadFile(existing); string(got) != "older" {
		t.Errorf("the existing file was overwritten: %q", got)
	}
	if got := mode(t, existing); got != 0o644 {
		t.Errorf("the existing file is mode %o, want its own 644 untouched", got)
	}

	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatalf("building the fixture: %v", err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("building the fixture: %v", err)
	}
	if err := write.WriteNew(link, []byte("s3cr3t"), 0o600); !errors.Is(err, fs.ErrExist) {
		t.Errorf("WriteNew through a symlink = %v, want ErrExist", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "target" {
		t.Errorf("the credential was written through the symlink: %q", got)
	}

	fresh := filepath.Join(dir, "fresh.json")
	if err := write.WriteNew(fresh, []byte("s3cr3t"), 0o600); err != nil {
		t.Fatalf("WriteNew on a name that was free: %v", err)
	}
	if got := mode(t, fresh); got != 0o600 {
		t.Errorf("a newly written credential is mode %o, want 600", got)
	}
	if got, _ := os.ReadFile(fresh); string(got) != "s3cr3t" {
		t.Errorf("the credential is %q", got)
	}
}

// flakyRemoveFS fails the first RemoveAll and then behaves.
//
// A full disk, a busy mount and a permission fixed a moment later all look like
// this: one failure, then success.
type flakyRemoveFS struct {
	cred.OSFileSystem
	failures int
	calls    int
}

func (f *flakyRemoveFS) RemoveAll(path string) error {
	f.calls++
	if f.calls <= f.failures {
		return fs.ErrPermission
	}
	return f.OSFileSystem.RemoveAll(path)
}

// envRefusingRestore is a fakeEnv that can be made to refuse every write after
// Prepare has exported the staging variable, so only the restore fails.
type envRefusingRestore struct {
	*fakeEnv
	refuse bool
}

func (e *envRefusingRestore) Set(name, value string) error {
	if e.refuse {
		return fs.ErrPermission
	}
	return e.fakeEnv.Set(name, value)
}

func (e *envRefusingRestore) Unset(name string) error {
	if e.refuse {
		return fs.ErrPermission
	}
	return e.fakeEnv.Unset(name)
}

// A Discard whose removal failed leaves the handle usable, so a caller that
// retries gets a retry.
//
// The handle used to be cleared before the removal was attempted, on the
// reasoning that a later failure must not be retried "into a second removal of
// a path this handle no longer describes". A failed RemoveAll means the handle
// does still describe it: the credential bytes are on disk, and clearing turned
// the obvious repair — call Discard again — into a silent no-op.
func TestARetriedDiscardRemovesADirectoryTheFirstCallCouldNot(t *testing.T) {
	filesystem := &flakyRemoveFS{failures: 1}
	env := newEnv(map[string]string{"CROSSREV_CODEX_AUTH": string(credential(t, 86400))})

	staged, err := cred.Prepare(codex(t), "", cred.Options{
		Env: env, FS: filesystem, Now: fixedNow, ScratchRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	dir := staged.Dir
	if dir == "" {
		t.Fatal("Prepare staged nothing, so this test would prove nothing")
	}

	if err := cred.Discard(staged); err == nil {
		t.Fatal("the first Discard reported success while RemoveAll was failing")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the scratch home is gone after a failed removal: %v", err)
	}
	if staged.Dir != dir {
		t.Fatalf("a failed Discard cleared the handle: Dir = %q", staged.Dir)
	}

	if err := cred.Discard(staged); err != nil {
		t.Fatalf("the retried Discard: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the scratch home survived the retry: %v", err)
	}
	if staged.Dir != "" {
		t.Errorf("a Discard that succeeded left Dir = %q", staged.Dir)
	}
	// And a third call is the no-op it always was.
	if err := cred.Discard(staged); err != nil {
		t.Errorf("a third Discard: %v", err)
	}
	if filesystem.calls != 2 {
		t.Errorf("RemoveAll was called %d times, want 2", filesystem.calls)
	}
}

// When the removal and the environment restore both fail, both are reported.
//
// The environment failure used to be dropped: the removal error returned first,
// and a caller told the directory is still there was not told the staging
// variable still points at it.
func TestADiscardThatFailsTwiceReportsBothFailures(t *testing.T) {
	filesystem := &flakyRemoveFS{failures: 1}
	env := &envRefusingRestore{fakeEnv: newEnv(map[string]string{
		"CROSSREV_CODEX_AUTH": string(credential(t, 86400)),
	})}

	staged, err := cred.Prepare(codex(t), "", cred.Options{
		Env: env, FS: filesystem, Now: fixedNow, ScratchRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// The export has happened; from here every write refuses.
	env.refuse = true

	err = cred.Discard(staged)
	if err == nil {
		t.Fatal("a Discard that failed twice reported success")
	}
	message := err.Error()
	if !strings.Contains(message, "removing the scratch credential home") {
		t.Errorf("the error does not name the removal failure: %q", message)
	}
	if !strings.Contains(message, "restoring CODEX_HOME") {
		t.Errorf("the error does not name the restore failure: %q", message)
	}
}
