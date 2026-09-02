// secrets.go — the secret half of `_init_execute` (lib/init.sh:476-562), and
// the three helpers it reaches for (lib/init.sh:749-849).
//
// Every value here is a credential, so two rules hold throughout. A value goes
// to the child on stdin and never onto a command line, where the process table
// would publish it to every user on the machine. And no value reaches a printed
// line, a returned error or a test's output: `init` reports the name of a
// secret and what it did with it, never what it was.

package initcmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// Keys is the App private key on disk (_auth_pem, lib/auth.sh:31-37).
//
// The path comes back with the bytes because a key that is not there is
// reported by its path, which is the only thing that tells an operator where to
// put one (lib/init.sh:486).
type Keys interface {
	PrivateKey(owner, role string) (path string, pem []byte, found bool)
}

// Registrar registers a GitHub App for an owner in a role (`auth_login --owner
// … --role refresher`, lib/init.sh:528).
type Registrar interface {
	Login(ctx context.Context, owner, role string) error
}

// TokenRecorder starts the clock on a token that cannot be read back
// (auth_token_record, lib/init.sh:795).
//
// The one-year clock starts at capture and this is the only moment the date
// exists: the token cannot be read back, so nothing later can work out when it
// was issued, and without it the first sign of expiry is a CI failure.
type TokenRecorder interface {
	Record(repo core.Slug, secret string, days int) error
}

// Seeder runs a harness's own credential seed command
// (_init_set_archetype_a_token, lib/init.sh:749-798).
type Seeder interface {
	// Available is `command -v <harness>` (lib/init.sh:751).
	Available(harness string) bool
	// Run starts the command and answers everything it wrote on both
	// streams, which is what `$cmd 2>&1 | tee` captures.
	Run(ctx context.Context, command string) string
}

// SecretStore writes a repository or organisation secret through `gh`.
//
// The argv is the shell's, verbatim, because that is the observable surface: an
// extra flag is a different request, and the offline suite matches routes on
// the whole argument string.
type SecretStore struct {
	// Runner starts `gh`. It carries the forge credential, so it is the
	// orchestrator runner rather than the model-facing one; the composition
	// root wires it, the way internal/forge/ghexec is wired.
	Runner exec.Runner
	// Env is the environment `gh` receives, as NAME=VALUE entries.
	Env []string
}

// SetRepo is `gh secret set <name> --repo <repo>` (lib/init.sh:828 and :843).
func (s *SecretStore) SetRepo(ctx context.Context, repo core.Slug, name, value string) bool {
	return s.set(ctx, []string{"secret", "set", name, "--repo", repo.String()}, value)
}

// SetOrg is `gh secret set <name> --org <owner> --visibility all`
// (lib/init.sh:836).
func (s *SecretStore) SetOrg(ctx context.Context, owner, name, value string) bool {
	return s.set(ctx, []string{"secret", "set", name, "--org", owner, "--visibility", "all"}, value)
}

// set sends the value on stdin, which is `printf '%s' "$value" | gh …`. The
// value never appears in Args, and nothing here puts it in a return value.
func (s *SecretStore) set(ctx context.Context, args []string, value string) bool {
	if s == nil || s.Runner == nil {
		return false
	}
	return s.Runner.Run(ctx, exec.Spec{
		Path:  "gh",
		Args:  args,
		Env:   s.Env,
		Stdin: []byte(value),
	}).OK()
}

// SeedCommands runs a harness's seed command through a Runner.
//
// It is the model-facing side of execution: `claude setup-token` opens a
// browser for the operator's own subscription, so the runner wired here is the
// one that refuses a forge credential.
type SeedCommands struct {
	Runner exec.Runner
	Env    []string
	// LookPath is `command -v` (lib/init.sh:751). Nil searches PATH.
	LookPath func(string) (string, error)
}

// Available reports whether the harness CLI is on PATH.
func (s SeedCommands) Available(name string) bool {
	look := s.LookPath
	if look == nil {
		look = lookPath
	}
	_, err := look(name)
	return err == nil
}

// Run starts the seed command and answers everything it wrote.
//
// The command is split on whitespace because the shell runs it as a bare `$cmd`
// (lib/init.sh:780), where the word split is what turns `claude setup-token`
// into a program and an argument. Both streams arrive as one, which is the
// `2>&1` in front of the tee.
//
// A failure answers with whatever was captured rather than an error, because
// the `|| true` at lib/init.sh:782 is load-bearing: without it a cancelled
// authorisation would abort `init` halfway through, having already written
// labels and secrets. The token check below is what decides whether it worked.
func (s SeedCommands) Run(ctx context.Context, command string) string {
	words := strings.Fields(command)
	if len(words) == 0 || s.Runner == nil {
		return ""
	}
	result := s.Runner.Run(ctx, exec.Spec{
		Path:    words[0],
		Args:    words[1:],
		Env:     s.Env,
		Streams: exec.StreamsCombined,
	})
	return string(result.Stdout)
}

// The two patterns the capture flow uses (lib/init.sh:781 and :784).
//
// The first redacts what reaches the terminal, keeping the first six characters
// after the prefix so a reader can see something happened. The second is the
// token itself, and the LAST match wins: the command prints the token after
// everything else it has to say.
var (
	redactedToken = regexp.MustCompile(`(sk-ant-[A-Za-z0-9_-]{6})[A-Za-z0-9_-]+`)
	capturedToken = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)
)

// EnsureSecrets provisions every secret this configuration needs and answers
// the ones it could not (lib/init.sh:476-562).
//
// The list it returns is `$unfinished`, in the order the shell appends to it.
// Refusing to finish quietly is the point: a missing secret fails the run
// before any review happens, which is the good kind of failure — but only if
// someone knows.
func (p Plan) EnsureSecrets(ctx context.Context, req Request, ex Execution) ([]string, error) {
	if ex.Secrets == nil || ex.Secrets.Runner == nil {
		return nil, fmt.Errorf("initcmd: the execution is missing Secrets")
	}
	if ex.Keys == nil {
		return nil, fmt.Errorf("initcmd: the execution is missing Keys")
	}

	out := req.io()
	var unfinished []string
	out.Section("Secrets")

	if app, found := req.Apps.App(p.Owner, RoleLoop); found {
		path, pem, keyFound := ex.Keys.PrivateKey(p.Owner, RoleLoop)
		if !p.setSecret(ctx, req, ex, "APP_ID", app.ID, false) {
			unfinished = append(unfinished, "APP_ID")
		}
		if keyFound {
			// `$(cat "$pem")` — a command substitution strips every
			// trailing newline, so the secret holds the key without one.
			if !p.setSecret(ctx, req, ex, "APP_PRIVATE_KEY", strings.TrimRight(string(pem), "\n"), false) {
				unfinished = append(unfinished, "APP_PRIVATE_KEY")
			}
		} else {
			out.No("APP_PRIVATE_KEY — the key file is missing at " + path)
			unfinished = append(unfinished, "APP_PRIVATE_KEY")
		}
	} else {
		out.No("no App is registered for " + p.Owner + ", so APP_ID and APP_PRIVATE_KEY were not set")
		out.Next("crossrev auth login --owner " + p.Owner)
		unfinished = append(unfinished, "APP_ID", "APP_PRIVATE_KEY")
	}

	if p.NeedsRefresher {
		refresher, err := p.refresherSecrets(ctx, req, ex)
		if err != nil {
			return nil, err
		}
		unfinished = append(unfinished, refresher...)
	}

	for _, secret := range p.Secrets {
		switch {
		case secret == "", secret == "APP_ID", secret == "APP_PRIVATE_KEY",
			strings.HasPrefix(secret, "CROSSREV_REFRESH_APP_"):
			continue
		}
		if secretSet(p.SecretInventory, secret) {
			out.OK(secret + " — already set")
			continue
		}
		if name := archetypeAHarnessFor(req.Harness, secret); name != "" {
			captured, err := p.seedArchetypeAToken(ctx, req, ex, name)
			if err != nil {
				return nil, err
			}
			if captured {
				continue
			}
		}
		out.No(secret + " — not set, and CrossRev does not have the value to set it")
		unfinished = append(unfinished, secret)
	}

	return unfinished, nil
}

// refresherSecrets provisions the second App, when and only when the pairing
// needs one (lib/init.sh:499-546).
//
// Registering it defensively would leave a private key carrying secrets:write
// sitting unused in an organisation, which is precisely the credential nobody
// remembers to rotate.
func (p Plan) refresherSecrets(ctx context.Context, req Request, ex Execution) ([]string, error) {
	out := req.io()
	name := refresherHarness(req.Harness)
	entry, _ := req.Harness.For(name)
	secret := entry.Credential.Secret
	command := entry.Credential.SeedCommand

	// One credential, one repository, one writer. Concurrency groups do not
	// span repositories, so an organisation-level copy of the rotating
	// credential means every repository that reads it also refreshes it —
	// and the first one to refresh invalidates it for all the others,
	// permanently.
	if p.OwnerType == "organization" {
		if list, ok := req.GitHub.SecretsAtOrg(ctx, p.Owner); ok && listedSecret(list, secret) {
			out.Warn(
				secret+" exists as an organisation secret on "+p.Owner,
				"The refresher writes a repository secret, which takes precedence — so this repository will work and the organisation copy will go stale, breaking every other repository reading it. Each repository needs its own credential, seeded with its own `"+command+"`. Delete the organisation-level copy.",
			)
		}
	}

	if _, found := req.Apps.App(p.Owner, RoleRefresher); !found {
		out.Gap()
		out.Line(name + " authenticates by subscription on an ephemeral runner, so one")
		out.Line("scheduled job has to refresh its credential. That job needs an App")
		out.Line("of its own carrying secrets:write — never the loop's App, which the")
		out.Line("review jobs use on attacker-controlled text.")
		// Registering an App means approving it in a browser, so --yes
		// cannot cover it: a blanket yes must not stand in for an
		// approval nobody is present to give. Scripted runs name the
		// command instead of pretending to run it.
		switch {
		case out.AssumeYes, !hasInput(out):
			out.No("no refresher App is registered for " + p.Owner + ", and registering one needs a browser")
			out.Next("crossrev auth login --owner " + p.Owner + " --role refresher")
		default:
			agreed, err := out.Confirm("Register the refresher App for " + p.Owner + "?")
			if err != nil {
				return nil, err
			}
			if agreed {
				if ex.Register == nil {
					return nil, fmt.Errorf("initcmd: the execution is missing Register")
				}
				if err := ex.Register.Login(ctx, p.Owner, RoleRefresher); err != nil {
					return nil, err
				}
			}
		}
	}

	app, found := req.Apps.App(p.Owner, RoleRefresher)
	if !found {
		return []string{"CROSSREV_REFRESH_APP_ID", "CROSSREV_REFRESH_APP_PRIVATE_KEY"}, nil
	}

	var unfinished []string
	if !p.setSecret(ctx, req, ex, "CROSSREV_REFRESH_APP_ID", app.ID, true) {
		unfinished = append(unfinished, "CROSSREV_REFRESH_APP_ID")
	}
	path, pem, keyFound := ex.Keys.PrivateKey(p.Owner, RoleRefresher)
	if keyFound {
		if !p.setSecret(ctx, req, ex, "CROSSREV_REFRESH_APP_PRIVATE_KEY", strings.TrimRight(string(pem), "\n"), true) {
			unfinished = append(unfinished, "CROSSREV_REFRESH_APP_PRIVATE_KEY")
		}
	} else {
		out.No("CROSSREV_REFRESH_APP_PRIVATE_KEY — the key file is missing at " + path)
		unfinished = append(unfinished, "CROSSREV_REFRESH_APP_PRIVATE_KEY")
	}
	return unfinished, nil
}

// setSecret writes one secret and says where it landed (_init_secret_set,
// lib/init.sh:825-849).
//
// Organisation level where the owner is an organisation, so later repositories
// in it need only config, labels and the App install. forceRepo is the
// exception, and two secrets take it.
//
// The refresher App's private key must never be an organisation secret with
// `--visibility all`. That key can rewrite repository secrets, and org-wide
// visibility hands it to every workflow in the organisation — including this
// design's own review job, which checks out a pull request branch and runs a
// model over a diff. The whole argument for a second App is that its permission
// is unreachable from untrusted text.
func (p Plan) setSecret(ctx context.Context, req Request, ex Execution, name, value string, forceRepo bool) bool {
	out := req.io()
	if forceRepo {
		if ex.Secrets.SetRepo(ctx, p.Repo, name, value) {
			out.OK(name + " — set on " + p.Repo.String() + ", repository-scoped on purpose")
			return true
		}
		out.No(name + " — could not be set on " + p.Repo.String())
		return false
	}
	if p.OwnerType == "organization" {
		if ex.Secrets.SetOrg(ctx, p.Owner, name, value) {
			out.OK(name + " — set at the " + p.Owner + " organisation level")
			return true
		}
		out.Warn(
			"could not set "+name+" at the "+p.Owner+" organisation level",
			"That needs admin access to the organisation. Falling back to a repository secret, which works but has to be repeated for every repository.",
		)
	}
	if ex.Secrets.SetRepo(ctx, p.Repo, name, value) {
		out.OK(name + " — set on " + p.Repo.String())
		return true
	}
	out.No(name + " — could not be set")
	return false
}

// seedArchetypeAToken captures the harness's own token rather than asking for a
// paste (_init_set_archetype_a_token, lib/init.sh:749-798).
//
// The command opens a browser for one authorisation and then prints the token
// to stdout, saying plainly that it will not show it again. Capturing it here
// closes the last place in the hosted setup where a credential would otherwise
// pass through a clipboard.
//
// # Where this is not the shell
//
// The Bash streams the command's output through `tee` and `sed` as it arrives,
// so the operator watches the authorisation happen. This runs the command to
// completion and then writes the redacted capture. The bytes and their order
// relative to everything else `init` prints are the same; the liveness is not.
func (p Plan) seedArchetypeAToken(ctx context.Context, req Request, ex Execution, name string) (bool, error) {
	out := req.io()
	if ex.Seeds == nil || !ex.Seeds.Available(name) {
		return false, nil
	}
	if !hasInput(out) {
		// No terminal, no browser flow.
		return false, nil
	}

	entry, _ := req.Harness.For(name)
	secret := entry.Credential.Secret
	command := entry.Credential.SeedCommand

	out.Gap()
	out.Line(secret + " is missing, and both legs need it to authenticate.")
	out.Line("`" + command + "` opens a browser once and prints a token valid for a")
	out.Line("year. CrossRev captures it straight into the secret — it is never printed")
	out.Line("here, never written to a file, and never shown again by anything.")
	agreed, err := out.Confirm("Run `" + command + "` now?")
	if err != nil {
		return false, err
	}
	if !agreed {
		return false, nil
	}

	raw := ex.Seeds.Run(ctx, command)
	// The terminal sees the command's own output with anything token-shaped
	// redacted on the way past: the URL and the prompts are what someone
	// needs to complete the flow, and the token is not. It goes out raw
	// rather than through a body line, because the shell's `tee` writes it
	// straight to the terminal.
	if out != nil && out.Out != nil {
		fmt.Fprint(out.Out, redactedToken.ReplaceAllString(raw, "$1…[captured by crossrev, not shown]"))
	}

	token := lastMatch(capturedToken, raw)
	if token == "" {
		out.Warn(
			"`"+command+"` finished without printing a token CrossRev could recognise",
			"The secret is not set, so CI cannot authenticate yet. Run it by hand and set the secret: "+command+", then gh secret set "+secret+" "+p.secretScopeFlag(),
		)
		return false, nil
	}

	if !p.setSecret(ctx, req, ex, secret, token, false) {
		return false, nil
	}
	if ex.Tokens == nil {
		return false, fmt.Errorf("initcmd: the execution is missing Tokens")
	}
	if err := ex.Tokens.Record(p.Repo, secret, 365); err != nil {
		return false, err
	}
	out.Line("   expires in 365 days — `crossrev auth status` warns as that closes")
	return true, nil
}

// secretScopeFlag is the scope an operator should set a secret at by hand
// (_init_secret_scope_flag, lib/init.sh:800-806).
func (p Plan) secretScopeFlag() string {
	if p.OwnerType == "organization" {
		return "--org " + p.Owner
	}
	return "--repo " + p.Repo.String()
}

// listedSecret is `grep -q "^$name\b"` over a `gh secret list` (lib/init.sh:509).
//
// A word boundary rather than the whitespace-or-end that _init_secret_exists
// uses. The two are different greps in the shell and answer differently for a
// name ending at a non-word character, so they are reproduced separately rather
// than folded together.
func listedSecret(list, name string) bool {
	return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\b`).MatchString(list)
}

// archetypeAHarnessFor is the harness whose archetype-A credential lives in
// this secret, and empty when none does (lib/init.sh:556).
//
// The first match rather than every match: the jq at that line emits one line
// per harness, and two harnesses sharing a secret name would leave the shell
// with a two-line "name" that `harness_get` answers nothing for. No shipped
// descriptor has one.
func archetypeAHarnessFor(doc harness.Document, secret string) string {
	for _, name := range doc.Names() {
		entry, found := doc.For(name)
		if found && entry.Credential.Secret == secret && entry.Credential.Archetype == "A" {
			return name
		}
	}
	return ""
}

// harnessForSecret is the harness whose credential lives in this secret,
// whatever its archetype, and empty when none does (lib/init.sh:611).
func harnessForSecret(doc harness.Document, secret string) string {
	for _, name := range doc.Names() {
		entry, found := doc.For(name)
		if found && entry.Credential.Secret == secret {
			return name
		}
	}
	return ""
}

// lastMatch is `grep -oE … | tail -1`.
func lastMatch(pattern *regexp.Regexp, text string) string {
	matches := pattern.FindAllString(text, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

// hasInput is `_ui_input_source >/dev/null 2>&1` (lib/init.sh:524 and :752):
// whether there is anywhere to read an answer from at all.
//
// It opens the source and closes it, which is what the shell's subshell test
// does, because the controlling terminal is not always there and the only way
// to find out is to try.
func hasInput(out *ui.IO) bool {
	if out == nil || out.Input == nil {
		return false
	}
	source, err := out.Input.Open()
	if err != nil {
		return false
	}
	source.Close()
	return true
}

// lookPath is `command -v` over PATH. os/exec is confined to internal/exec, so
// the search is written out here, the way internal/preflight writes it out.
func lookPath(name string) (string, error) {
	if name == "" {
		return "", os.ErrNotExist
	}
	if strings.ContainsRune(name, os.PathSeparator) {
		if _, err := os.Stat(name); err != nil {
			return "", err
		}
		return name, nil
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}
