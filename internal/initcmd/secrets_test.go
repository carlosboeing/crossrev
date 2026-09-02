package initcmd_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/initcmd"
)

// runRecorder is a Runner that records the argv it was asked for and answers
// from a fixed table.
//
// It never records or prints Spec.Stdin. A secret's value travels there, and a
// recorder that kept it would put a credential in the failure output of every
// test that asserts on the call list.
type runRecorder struct {
	fail   map[string]bool
	stdout map[string]string

	calls []string
	// stdin is what the last call carried, for the one test that proves the
	// value goes there rather than onto the command line. It is compared,
	// never printed.
	stdin map[string][]byte
}

func (r *runRecorder) Run(_ context.Context, spec exec.Spec) exec.Result {
	line := strings.Join(append([]string{spec.Path}, spec.Args...), " ")
	r.calls = append(r.calls, line)
	if r.stdin == nil {
		r.stdin = map[string][]byte{}
	}
	r.stdin[line] = spec.Stdin
	if r.fail[line] {
		return exec.Result{ExitCode: 1}
	}
	return exec.Result{ExitCode: 0, Stdout: []byte(r.stdout[line])}
}

// refusingRunner fails the test if anything asks it to start a process.
type refusingRunner struct{ t *testing.T }

func (r refusingRunner) Run(_ context.Context, spec exec.Spec) exec.Result {
	r.t.Errorf("a process was started: %s %s", spec.Path, strings.Join(spec.Args, " "))
	return exec.Result{ExitCode: -1}
}

// fakeKeys is the App private key on disk, keyed "<owner>/<role>".
type fakeKeys struct {
	pem  map[string]string
	path string
}

func (k fakeKeys) PrivateKey(owner, role string) (string, []byte, bool) {
	path := k.path + "/" + owner + "." + role + ".pem"
	body, found := k.pem[owner+"/"+role]
	if !found {
		return path, nil, false
	}
	return path, []byte(body), true
}

// fakeRegistrar records a refresher registration and, when told to, makes the
// App appear the way auth_login does.
type fakeRegistrar struct {
	apps  fakeApps
	app   initcmd.App
	err   error
	calls []string
}

func (r *fakeRegistrar) Login(_ context.Context, owner, role string) error {
	r.calls = append(r.calls, owner+" "+role)
	if r.err != nil {
		return r.err
	}
	r.apps[owner+"/"+role] = r.app
	return nil
}

// fakeTokens records the expiry clock init starts.
type fakeTokens struct {
	calls []string
	err   error
}

func (f *fakeTokens) Record(repo core.Slug, secret string, days int) error {
	f.calls = append(f.calls, repo.String()+" "+secret+" "+itoa(days))
	return f.err
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// answers is a terminal that gives the same answer to every question.
//
// Re-openable, because that is what a controlling terminal is: the shell tests
// for one with `( : </dev/tty )` and then opens it again for the read, so an
// Input that could only be opened once would report no terminal at the first
// question.
type answers struct{ line string }

func (a answers) Open() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(a.line + "\n")), nil
}

// hostedPairing is tests/test-runner.sh's config_for, with the backlog off so
// the secrets section is what a test is looking at.
func hostedPairing(reviewer, resolver string) string {
	return `version: 1
mode: automated
policy:
  min_fix_severity: medium
  max_passes_per_cycle: 1
  max_files_changed_per_pr: 200
  max_prs_per_day: 25
runner: github-hosted
reviewer:
  harness: ` + reviewer + `
  model: reviewer-model
resolver:
  harness: ` + resolver + `
  model: resolver-model
backlog:
  destination: none
`
}

// TestSecretsSetsTheAppCredentialsAtRepositoryScope is the user-owned case: the
// App's id and its private key, each set on the repository, and nothing left
// outstanding for them (lib/init.sh:478-493).
func TestSecretsSetsTheAppCredentialsAtRepositoryScope(t *testing.T) {
	runner := &runRecorder{}
	plan, req, _, buffer := planned(t, hostedPairing("claude", "claude"), func(r *initcmd.Request) {
		r.Pairing = livePairing{doc: descriptor(t)}
		r.Apps = fakeApps{"acme/loop": {Name: "crossrev-acme", ID: "12345"}}
	})
	ex := initcmd.Execution{
		Secrets: &initcmd.SecretStore{Runner: runner},
		Keys:    fakeKeys{path: "/apps", pem: map[string]string{"acme/loop": "PEMBODY\n"}},
	}
	unfinished, err := plan.EnsureSecrets(context.Background(), req, ex)
	if err != nil {
		t.Fatalf("Secrets: %v", err)
	}

	want := "\n◇  Secrets\n" +
		"│  ✓ APP_ID — set on acme/widget\n" +
		"│  ✓ APP_PRIVATE_KEY — set on acme/widget\n" +
		"│  ✗ CLAUDE_CODE_OAUTH_TOKEN — not set, and CrossRev does not have the value to set it\n"
	if got := buffer.String(); got != want {
		t.Errorf("the Secrets block is\n%q\nwant\n%q", got, want)
	}
	if got := strings.Join(unfinished, " "); got != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("unfinished = %q, want CLAUDE_CODE_OAUTH_TOKEN", got)
	}

	wantCalls := []string{
		"gh secret set APP_ID --repo acme/widget",
		"gh secret set APP_PRIVATE_KEY --repo acme/widget",
	}
	if got := strings.Join(runner.calls, "\n"); got != strings.Join(wantCalls, "\n") {
		t.Errorf("argv is\n%s\nwant\n%s", got, strings.Join(wantCalls, "\n"))
	}
}

// TestSecretsSendsTheValueOnStdinRatherThanTheCommandLine: the shell pipes it
// (`printf '%s' "$value" | gh secret set …`, lib/init.sh:828), and a value on
// argv is readable from the process table by every user on the machine.
//
// The trailing newlines go, because the shell reads the key file through
// `$(cat "$pem")` and a command substitution strips them.
func TestSecretsSendsTheValueOnStdinRatherThanTheCommandLine(t *testing.T) {
	runner := &runRecorder{}
	plan, req, _, _ := planned(t, hostedPairing("claude", "claude"), func(r *initcmd.Request) {
		r.Pairing = livePairing{doc: descriptor(t)}
		r.Apps = fakeApps{"acme/loop": {Name: "crossrev-acme", ID: "12345"}}
	})
	ex := initcmd.Execution{
		Secrets: &initcmd.SecretStore{Runner: runner},
		Keys:    fakeKeys{path: "/apps", pem: map[string]string{"acme/loop": "KEYMATERIAL\n\n"}},
	}
	if _, err := plan.EnsureSecrets(context.Background(), req, ex); err != nil {
		t.Fatalf("Secrets: %v", err)
	}

	const line = "gh secret set APP_PRIVATE_KEY --repo acme/widget"
	if !bytes.Equal(runner.stdin[line], []byte("KEYMATERIAL")) {
		t.Error("the key did not reach the child's stdin with its trailing newlines stripped")
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "KEYMATERIAL") {
			t.Error("a credential reached the command line")
		}
	}
}

// TestSecretsSetsAnOrganisationsSecretsAtOrganisationScope, and falls back to
// the repository when the login has no admin access — with the warning saying
// what that costs (lib/init.sh:835-848).
func TestSecretsSetsAnOrganisationsSecretsAtOrganisationScope(t *testing.T) {
	for _, row := range []struct {
		name     string
		fail     map[string]bool
		printed  string
		wantArgv []string
	}{
		{
			name:    "admin access",
			printed: "│  ✓ APP_ID — set at the acme organisation level\n",
			wantArgv: []string{
				"gh secret set APP_ID --org acme --visibility all",
				"gh secret set APP_PRIVATE_KEY --org acme --visibility all",
			},
		},
		{
			name: "no admin access",
			fail: map[string]bool{
				"gh secret set APP_ID --org acme --visibility all":          true,
				"gh secret set APP_PRIVATE_KEY --org acme --visibility all": true,
			},
			printed: "\n⚠  could not set APP_ID at the acme organisation level\n" +
				"   That needs admin access to the organisation. Falling back to a repository secret, which works but has to be repeated for every repository.\n\n" +
				"│  ✓ APP_ID — set on acme/widget\n",
			wantArgv: []string{
				"gh secret set APP_ID --org acme --visibility all",
				"gh secret set APP_ID --repo acme/widget",
				"gh secret set APP_PRIVATE_KEY --org acme --visibility all",
				"gh secret set APP_PRIVATE_KEY --repo acme/widget",
			},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			runner := &runRecorder{fail: row.fail}
			plan, req, _, buffer := planned(t, hostedPairing("claude", "claude"), func(r *initcmd.Request) {
				r.Pairing = livePairing{doc: descriptor(t)}
				r.Apps = fakeApps{"acme/loop": {Name: "crossrev-acme", ID: "12345"}}
				r.GitHub.(*fakeGitHub).ownerType = "Organization"
			})
			ex := initcmd.Execution{
				Secrets: &initcmd.SecretStore{Runner: runner},
				Keys:    fakeKeys{path: "/apps", pem: map[string]string{"acme/loop": "PEM"}},
			}
			if _, err := plan.EnsureSecrets(context.Background(), req, ex); err != nil {
				t.Fatalf("Secrets: %v", err)
			}
			if !strings.Contains(buffer.String(), row.printed) {
				t.Errorf("the block is\n%q\nand does not carry\n%q", buffer.String(), row.printed)
			}
			for _, call := range row.wantArgv {
				if !containsCall(runner.calls, call) {
					t.Errorf("argv %q was never sent; calls were %q", call, runner.calls)
				}
			}
		})
	}
}

// TestSecretsReportsAKeyFileThatIsNotThereByItsPath (lib/init.sh:486).
func TestSecretsReportsAKeyFileThatIsNotThereByItsPath(t *testing.T) {
	runner := &runRecorder{}
	plan, req, _, buffer := planned(t, hostedPairing("claude", "claude"), func(r *initcmd.Request) {
		r.Pairing = livePairing{doc: descriptor(t)}
		r.Apps = fakeApps{"acme/loop": {Name: "crossrev-acme", ID: "12345"}}
	})
	ex := initcmd.Execution{
		Secrets: &initcmd.SecretStore{Runner: runner},
		Keys:    fakeKeys{path: "/apps"},
	}
	unfinished, err := plan.EnsureSecrets(context.Background(), req, ex)
	if err != nil {
		t.Fatalf("Secrets: %v", err)
	}
	want := "│  ✗ APP_PRIVATE_KEY — the key file is missing at /apps/acme.loop.pem\n"
	if !strings.Contains(buffer.String(), want) {
		t.Errorf("the block is\n%q\nand does not carry\n%q", buffer.String(), want)
	}
	if !containsCall(unfinished, "APP_PRIVATE_KEY") {
		t.Errorf("unfinished = %q, want APP_PRIVATE_KEY in it", unfinished)
	}
	if containsCall(runner.calls, "gh secret set APP_PRIVATE_KEY --repo acme/widget") {
		t.Error("a key that is not there was set anyway")
	}
}

// TestSecretsNamesTheLoginWhenNoAppIsRegistered (lib/init.sh:490-492).
func TestSecretsNamesTheLoginWhenNoAppIsRegistered(t *testing.T) {
	runner := &runRecorder{}
	plan, req, _, buffer := planned(t, hostedPairing("claude", "claude"), func(r *initcmd.Request) {
		r.Pairing = livePairing{doc: descriptor(t)}
	})
	ex := initcmd.Execution{Secrets: &initcmd.SecretStore{Runner: runner}, Keys: fakeKeys{path: "/apps"}}
	unfinished, err := plan.EnsureSecrets(context.Background(), req, ex)
	if err != nil {
		t.Fatalf("Secrets: %v", err)
	}
	want := "\n◇  Secrets\n" +
		"│  ✗ no App is registered for acme, so APP_ID and APP_PRIVATE_KEY were not set\n" +
		"│  → crossrev auth login --owner acme\n"
	if !strings.HasPrefix(buffer.String(), want) {
		t.Errorf("the block is\n%q\nwant it to start\n%q", buffer.String(), want)
	}
	if got := strings.Join(unfinished, " "); got != "APP_ID APP_PRIVATE_KEY CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("unfinished = %q", got)
	}
	if len(runner.calls) != 0 {
		t.Errorf("a secret was set with no App to set it from: %q", runner.calls)
	}
}

// TestSecretsRegistersTheRefresherAppOnlyWithSomeoneToApproveIt: registering an
// App means approving it in a browser, so a blanket --yes must not stand in for
// an approval nobody is present to give (lib/init.sh:515-531).
func TestSecretsRegistersTheRefresherAppOnlyWithSomeoneToApproveIt(t *testing.T) {
	explanation := "│\n" +
		"│  codex authenticates by subscription on an ephemeral runner, so one\n" +
		"│  scheduled job has to refresh its credential. That job needs an App\n" +
		"│  of its own carrying secrets:write — never the loop's App, which the\n" +
		"│  review jobs use on attacker-controlled text.\n"

	t.Run("--yes names the command instead of pretending to run it", func(t *testing.T) {
		runner := &runRecorder{}
		registrar := &fakeRegistrar{apps: fakeApps{}}
		plan, req, out, buffer := planned(t, hostedPairing("codex", "claude"), func(r *initcmd.Request) {
			r.Pairing = livePairing{doc: descriptor(t)}
			r.Apps = fakeApps{"acme/loop": {Name: "crossrev-acme", ID: "12345"}}
		})
		out.AssumeYes = true
		out.Input = answers{"y"}
		ex := initcmd.Execution{
			Secrets:  &initcmd.SecretStore{Runner: runner},
			Keys:     fakeKeys{path: "/apps", pem: map[string]string{"acme/loop": "PEM"}},
			Register: registrar,
		}
		unfinished, err := plan.EnsureSecrets(context.Background(), req, ex)
		if err != nil {
			t.Fatalf("Secrets: %v", err)
		}
		want := explanation +
			"│  ✗ no refresher App is registered for acme, and registering one needs a browser\n" +
			"│  → crossrev auth login --owner acme --role refresher\n"
		if !strings.Contains(buffer.String(), want) {
			t.Errorf("the block is\n%q\nand does not carry\n%q", buffer.String(), want)
		}
		if len(registrar.calls) != 0 {
			t.Errorf("--yes registered an App nobody approved: %q", registrar.calls)
		}
		if !containsCall(unfinished, "CROSSREV_REFRESH_APP_ID") ||
			!containsCall(unfinished, "CROSSREV_REFRESH_APP_PRIVATE_KEY") {
			t.Errorf("unfinished = %q, want both refresher secrets", unfinished)
		}
	})

	t.Run("a terminal is asked, and a yes registers", func(t *testing.T) {
		runner := &runRecorder{}
		registrar := &fakeRegistrar{apps: fakeApps{}, app: initcmd.App{Name: "crossrev-acme-refresh", ID: "999"}}
		plan, req, out, buffer := planned(t, hostedPairing("codex", "claude"), func(r *initcmd.Request) {
			r.Pairing = livePairing{doc: descriptor(t)}
		})
		registrar.apps["acme/loop"] = initcmd.App{Name: "crossrev-acme", ID: "12345"}
		req.Apps = registrar.apps
		out.Input = answers{"y"}
		ex := initcmd.Execution{
			Secrets: &initcmd.SecretStore{Runner: runner},
			Keys: fakeKeys{path: "/apps", pem: map[string]string{
				"acme/loop": "PEM", "acme/refresher": "RPEM",
			}},
			Register: registrar,
		}
		unfinished, err := plan.EnsureSecrets(context.Background(), req, ex)
		if err != nil {
			t.Fatalf("Secrets: %v", err)
		}
		if got := strings.Join(registrar.calls, " "); got != "acme refresher" {
			t.Errorf("registrar calls = %q, want the refresher role for acme", got)
		}
		if !strings.Contains(buffer.String(), "◆  Register the refresher App for acme?  [y/N] ") {
			t.Errorf("the question was not asked:\n%s", buffer.String())
		}
		want := "│  ✓ CROSSREV_REFRESH_APP_ID — set on acme/widget, repository-scoped on purpose\n" +
			"│  ✓ CROSSREV_REFRESH_APP_PRIVATE_KEY — set on acme/widget, repository-scoped on purpose\n"
		if !strings.Contains(buffer.String(), want) {
			t.Errorf("the block is\n%q\nand does not carry\n%q", buffer.String(), want)
		}
		if containsCall(unfinished, "CROSSREV_REFRESH_APP_ID") {
			t.Errorf("a refresher secret that was set is still outstanding: %q", unfinished)
		}
	})
}

// TestSecretsKeepsTheRotatingCredentialRepositoryScopedEvenOnAnOrganisation:
// concurrency groups do not span repositories, so an organisation copy is
// refreshed by every repository reading it and the first to refresh invalidates
// it for the rest (lib/init.sh:534-538 and :811-824).
func TestSecretsKeepsTheRotatingCredentialRepositoryScopedEvenOnAnOrganisation(t *testing.T) {
	runner := &runRecorder{}
	apps := fakeApps{
		"acme/loop":      {Name: "crossrev-acme", ID: "12345"},
		"acme/refresher": {Name: "crossrev-acme-refresh", ID: "999"},
	}
	plan, req, _, _ := planned(t, hostedPairing("codex", "claude"), func(r *initcmd.Request) {
		r.Pairing = livePairing{doc: descriptor(t)}
		r.Apps = apps
		r.GitHub.(*fakeGitHub).ownerType = "Organization"
	})
	ex := initcmd.Execution{
		Secrets: &initcmd.SecretStore{Runner: runner},
		Keys: fakeKeys{path: "/apps", pem: map[string]string{
			"acme/loop": "PEM", "acme/refresher": "RPEM",
		}},
		Register: &fakeRegistrar{apps: apps},
	}
	if _, err := plan.EnsureSecrets(context.Background(), req, ex); err != nil {
		t.Fatalf("Secrets: %v", err)
	}
	for _, name := range []string{"CROSSREV_REFRESH_APP_ID", "CROSSREV_REFRESH_APP_PRIVATE_KEY"} {
		if !containsCall(runner.calls, "gh secret set "+name+" --repo acme/widget") {
			t.Errorf("%s was not set repository-scoped; calls were %q", name, runner.calls)
		}
		if containsCall(runner.calls, "gh secret set "+name+" --org acme --visibility all") {
			t.Errorf("%s was set at organisation level", name)
		}
	}
}

// TestSecretsWarnsAboutARotatingCredentialSittingAtOrganisationLevel
// (lib/init.sh:508-512). The match is anchored and word-bounded, so a longer
// name that starts with the same text does not answer for it.
func TestSecretsWarnsAboutARotatingCredentialSittingAtOrganisationLevel(t *testing.T) {
	for _, row := range []struct {
		name string
		list string
		warn bool
	}{
		{name: "the secret itself", list: "CROSSREV_CODEX_AUTH\t2026-01-01T00:00:00Z", warn: true},
		{name: "a longer name that starts the same", list: "CROSSREV_CODEX_AUTH_OLD\t2026-01-01T00:00:00Z"},
		{name: "nothing at organisation level"},
	} {
		t.Run(row.name, func(t *testing.T) {
			apps := fakeApps{
				"acme/loop":      {Name: "crossrev-acme", ID: "12345"},
				"acme/refresher": {Name: "crossrev-acme-refresh", ID: "999"},
			}
			plan, req, _, buffer := planned(t, hostedPairing("codex", "claude"), func(r *initcmd.Request) {
				r.Pairing = livePairing{doc: descriptor(t)}
				r.Apps = apps
				forge := r.GitHub.(*fakeGitHub)
				forge.ownerType = "Organization"
				forge.orgList = row.list
				forge.orgOK = true
			})
			ex := initcmd.Execution{
				Secrets: &initcmd.SecretStore{Runner: &runRecorder{}},
				Keys: fakeKeys{path: "/apps", pem: map[string]string{
					"acme/loop": "PEM", "acme/refresher": "RPEM",
				}},
				Register: &fakeRegistrar{apps: apps},
			}
			if _, err := plan.EnsureSecrets(context.Background(), req, ex); err != nil {
				t.Fatalf("Secrets: %v", err)
			}
			want := "⚠  CROSSREV_CODEX_AUTH exists as an organisation secret on acme\n" +
				"   The refresher writes a repository secret, which takes precedence — so this repository will work and the organisation copy will go stale, breaking every other repository reading it. Each repository needs its own credential, seeded with its own `codex login`. Delete the organisation-level copy.\n"
			if got := strings.Contains(buffer.String(), want); got != row.warn {
				t.Errorf("warned = %v, want %v; block was\n%s", got, row.warn, buffer.String())
			}
		})
	}
}

// TestSecretsReportsOneThatIsAlreadyThereRatherThanSettingItAgain
// (lib/init.sh:551-554).
func TestSecretsReportsOneThatIsAlreadyThereRatherThanSettingItAgain(t *testing.T) {
	runner := &runRecorder{}
	plan, req, _, buffer := planned(t, hostedPairing("claude", "claude"), func(r *initcmd.Request) {
		r.Pairing = livePairing{doc: descriptor(t)}
		r.GitHub.(*fakeGitHub).repoList = "CLAUDE_CODE_OAUTH_TOKEN\t2026-08-10T11:34:05Z"
	})
	unfinished, err := plan.EnsureSecrets(context.Background(), req, initcmd.Execution{
		Secrets: &initcmd.SecretStore{Runner: runner},
		Keys:    fakeKeys{path: "/apps"},
	})
	if err != nil {
		t.Fatalf("Secrets: %v", err)
	}
	if !strings.Contains(buffer.String(), "│  ✓ CLAUDE_CODE_OAUTH_TOKEN — already set\n") {
		t.Errorf("a secret already there was not reported so:\n%s", buffer.String())
	}
	if containsCall(unfinished, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("a secret already there is outstanding: %q", unfinished)
	}
}

// TestSecretsCapturesAnArchetypeATokenRatherThanAskingForAPaste closes the last
// place in the hosted setup where a credential would otherwise pass through a
// clipboard (lib/init.sh:749-798).
func TestSecretsCapturesAnArchetypeATokenRatherThanAskingForAPaste(t *testing.T) {
	seeds := &fakeSeeder{
		available: map[string]bool{"claude": true},
		output: map[string]string{
			"claude setup-token": "Visit https://claude.ai/oauth to authorise\n" +
				"sk-ant-oat01-AAAABBBBCCCCDDDDEEEEFFFF\n",
		},
	}
	runner := &runRecorder{}
	tokens := &fakeTokens{}
	plan, req, out, buffer := planned(t, hostedPairing("claude", "claude"), func(r *initcmd.Request) {
		r.Pairing = livePairing{doc: descriptor(t)}
	})
	out.Input = answers{"y"}
	unfinished, err := plan.EnsureSecrets(context.Background(), req, initcmd.Execution{
		Secrets: &initcmd.SecretStore{Runner: runner},
		Keys:    fakeKeys{path: "/apps"},
		Seeds:   seeds,
		Tokens:  tokens,
	})
	if err != nil {
		t.Fatalf("Secrets: %v", err)
	}

	explanation := "│\n" +
		"│  CLAUDE_CODE_OAUTH_TOKEN is missing, and both legs need it to authenticate.\n" +
		"│  `claude setup-token` opens a browser once and prints a token valid for a\n" +
		"│  year. CrossRev captures it straight into the secret — it is never printed\n" +
		"│  here, never written to a file, and never shown again by anything.\n" +
		"◆  Run `claude setup-token` now?  [y/N] "
	if !strings.Contains(buffer.String(), explanation) {
		t.Errorf("the block is\n%q\nand does not carry\n%q", buffer.String(), explanation)
	}
	if !strings.Contains(buffer.String(), "sk-ant-oat01-…[captured by crossrev, not shown]") {
		t.Errorf("the command's output was not redacted on the way past:\n%s", buffer.String())
	}
	if strings.Contains(buffer.String(), "AAAABBBB") {
		t.Error("the token reached the terminal")
	}
	if !strings.Contains(buffer.String(), "│     expires in 365 days — `crossrev auth status` warns as that closes\n") {
		t.Errorf("the one-year clock was not stated:\n%s", buffer.String())
	}
	if !containsCall(runner.calls, "gh secret set CLAUDE_CODE_OAUTH_TOKEN --repo acme/widget") {
		t.Errorf("the captured token was not put in the secret; calls were %q", runner.calls)
	}
	if got := strings.Join(tokens.calls, " "); got != "acme/widget CLAUDE_CODE_OAUTH_TOKEN 365" {
		t.Errorf("token record = %q", got)
	}
	if containsCall(unfinished, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("a secret that was captured and set is outstanding: %q", unfinished)
	}
}

// TestSecretsDoesNotOpenABrowserFlowWithNobodyThere pins the two guards at
// lib/init.sh:751-752, each on its own.
func TestSecretsDoesNotOpenABrowserFlowWithNobodyThere(t *testing.T) {
	for _, row := range []struct {
		name      string
		available bool
		input     bool
	}{
		{name: "the harness is not installed", input: true},
		{name: "there is no terminal", available: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			seeds := &fakeSeeder{available: map[string]bool{"claude": row.available}}
			plan, req, out, buffer := planned(t, hostedPairing("claude", "claude"), func(r *initcmd.Request) {
				r.Pairing = livePairing{doc: descriptor(t)}
			})
			if row.input {
				out.Input = answers{"y"}
			}
			unfinished, err := plan.EnsureSecrets(context.Background(), req, initcmd.Execution{
				Secrets: &initcmd.SecretStore{Runner: &runRecorder{}},
				Keys:    fakeKeys{path: "/apps"},
				Seeds:   seeds,
				Tokens:  &fakeTokens{},
			})
			if err != nil {
				t.Fatalf("Secrets: %v", err)
			}
			if len(seeds.calls) != 0 {
				t.Errorf("the seed command ran anyway: %q", seeds.calls)
			}
			if !containsCall(unfinished, "CLAUDE_CODE_OAUTH_TOKEN") {
				t.Errorf("unfinished = %q, want the token in it", unfinished)
			}
			if strings.Contains(buffer.String(), "opens a browser once") {
				t.Errorf("the explanation was printed with nobody to act on it:\n%s", buffer.String())
			}
		})
	}
}

// TestSecretsWarnsWhenTheSeedCommandPrintedNoTokenItRecognises
// (lib/init.sh:785-789). The secret is not set, so the warning has to say so.
func TestSecretsWarnsWhenTheSeedCommandPrintedNoTokenItRecognises(t *testing.T) {
	seeds := &fakeSeeder{
		available: map[string]bool{"claude": true},
		output:    map[string]string{"claude setup-token": "authorisation cancelled\n"},
	}
	runner := &runRecorder{}
	plan, req, out, buffer := planned(t, hostedPairing("claude", "claude"), func(r *initcmd.Request) {
		r.Pairing = livePairing{doc: descriptor(t)}
	})
	out.Input = answers{"y"}
	unfinished, err := plan.EnsureSecrets(context.Background(), req, initcmd.Execution{
		Secrets: &initcmd.SecretStore{Runner: runner},
		Keys:    fakeKeys{path: "/apps"},
		Seeds:   seeds,
		Tokens:  &fakeTokens{},
	})
	if err != nil {
		t.Fatalf("Secrets: %v", err)
	}
	want := "⚠  `claude setup-token` finished without printing a token CrossRev could recognise\n" +
		"   The secret is not set, so CI cannot authenticate yet. Run it by hand and set the secret: claude setup-token, then gh secret set CLAUDE_CODE_OAUTH_TOKEN --repo acme/widget\n"
	if !strings.Contains(buffer.String(), want) {
		t.Errorf("the block is\n%q\nand does not carry\n%q", buffer.String(), want)
	}
	if len(runner.calls) != 0 {
		t.Errorf("a secret was set from no token: %q", runner.calls)
	}
	if !containsCall(unfinished, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("unfinished = %q", unfinished)
	}
}

// TestSecretsRefusesTheSeedFlowRatherThanRunningItUnasked (lib/init.sh:763).
func TestSecretsRefusesTheSeedFlowRatherThanRunningItUnasked(t *testing.T) {
	seeds := &fakeSeeder{available: map[string]bool{"claude": true}}
	plan, req, out, _ := planned(t, hostedPairing("claude", "claude"), func(r *initcmd.Request) {
		r.Pairing = livePairing{doc: descriptor(t)}
	})
	out.Input = answers{"n"}
	if _, err := plan.EnsureSecrets(context.Background(), req, initcmd.Execution{
		Secrets: &initcmd.SecretStore{Runner: &runRecorder{}},
		Keys:    fakeKeys{path: "/apps"},
		Seeds:   seeds,
		Tokens:  &fakeTokens{},
	}); err != nil {
		t.Fatalf("Secrets: %v", err)
	}
	if len(seeds.calls) != 0 {
		t.Errorf("a no ran the command anyway: %q", seeds.calls)
	}
}

// TestSecretStoreArgvForTheSeedCommand: the seed command is split on
// whitespace the way `$cmd` is at lib/init.sh:780, so each word reaches the
// child as one argv entry.
func TestSecretStoreArgvForTheSeedCommand(t *testing.T) {
	runner := &runRecorder{stdout: map[string]string{"claude setup-token": "ok"}}
	seeds := initcmd.SeedCommands{
		Runner:   runner,
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
	}
	if !seeds.Available("claude") {
		t.Error("a harness on PATH was reported as absent")
	}
	if got := seeds.Run(context.Background(), "claude setup-token"); got != "ok" {
		t.Errorf("Run = %q, want the combined output", got)
	}
	if got := strings.Join(runner.calls, "|"); got != "claude setup-token" {
		t.Errorf("argv = %q, want the seed command split the way the shell splits it", got)
	}
}

// TestSecretsRefusesAnUnwiredPort: every default here would be a lie an
// operator then acts on.
func TestSecretsRefusesAnUnwiredPort(t *testing.T) {
	for _, row := range []struct {
		name string
		ex   initcmd.Execution
	}{
		{name: "Secrets", ex: initcmd.Execution{Keys: fakeKeys{}}},
		{name: "Keys", ex: initcmd.Execution{Secrets: &initcmd.SecretStore{Runner: refusingRunner{t}}}},
	} {
		t.Run(row.name, func(t *testing.T) {
			plan, req, _, _ := planned(t, hostedPairing("claude", "claude"), func(r *initcmd.Request) {
				r.Pairing = livePairing{doc: descriptor(t)}
			})
			_, err := plan.EnsureSecrets(context.Background(), req, row.ex)
			if err == nil || !strings.Contains(err.Error(), row.name) {
				t.Fatalf("err = %v, want a refusal naming %s", err, row.name)
			}
		})
	}
}

// TestSecretsCarriesAFailedRegistrationUp: auth_login runs under set -e, so a
// registration that fails ends the run rather than leaving init to report a
// secret it never had a chance to set.
func TestSecretsCarriesAFailedRegistrationUp(t *testing.T) {
	registrar := &fakeRegistrar{apps: fakeApps{}, err: errors.New("the browser never came back")}
	plan, req, out, _ := planned(t, hostedPairing("codex", "claude"), func(r *initcmd.Request) {
		r.Pairing = livePairing{doc: descriptor(t)}
	})
	registrar.apps["acme/loop"] = initcmd.App{Name: "crossrev-acme", ID: "12345"}
	req.Apps = registrar.apps
	out.Input = answers{"y"}
	_, err := plan.EnsureSecrets(context.Background(), req, initcmd.Execution{
		Secrets:  &initcmd.SecretStore{Runner: &runRecorder{}},
		Keys:     fakeKeys{path: "/apps", pem: map[string]string{"acme/loop": "PEM"}},
		Register: registrar,
	})
	if err == nil || !strings.Contains(err.Error(), "the browser never came back") {
		t.Fatalf("err = %v, want the registration's own failure", err)
	}
}

// fakeSeeder is the harness's own credential seed command.
type fakeSeeder struct {
	available map[string]bool
	output    map[string]string
	calls     []string
}

func (s *fakeSeeder) Available(name string) bool { return s.available[name] }

func (s *fakeSeeder) Run(_ context.Context, command string) string {
	s.calls = append(s.calls, command)
	return s.output[command]
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}
