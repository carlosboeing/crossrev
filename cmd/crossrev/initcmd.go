package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/carlosboeing/crossrev/internal/app"
	"github.com/carlosboeing/crossrev/internal/cli"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/initcmd"
	"github.com/carlosboeing/crossrev/internal/preflight"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// initCommand is cmd_init (lib/init.sh:34-64).
//
// The yq check runs first and the plan gate follows, in the shell's own order:
// `preflight_require_yq` at lib/init.sh:49, then resolve, then the printed plan,
// then `--dry-run` returning before anything is wired to a write, then the
// confirmation.
//
// --yes writes through the IO rather than through a field on the request. The
// shell sets CROSSREV_ASSUME_YES=1 and lets ui_confirm read it (lib/init.sh:59,
// lib/ui.sh:145), and ui.IO.AssumeYes is that variable — so the IO is built per
// command with the flag folded in, and initcmd.Request.Yes is the same fact
// stated where the package can see it.
func initCommand(ctx context.Context, doc harness.Document, req cli.InitRequest) (int, error) {
	out := newIO(req.Yes)
	d := open(out, doc)

	if err := requireYq(ctx, out, doc); err != nil {
		return cli.ExitFailure, err
	}

	client := d.forgeClient()
	env := osEnv{}
	ghApp := app.NewGH(d.orchestrator)

	request := initcmd.Request{
		Owner:   req.Owner,
		Repo:    req.Repo,
		DryRun:  req.DryRun,
		Upgrade: req.Upgrade,
		Yes:     req.Yes,
		Show:    d.show(),
		Harness: doc,
		GitHub:  initGitHub{forge: client, gh: ghApp, secrets: &secretLister{runner: d.orchestrator, env: exec.Inherit(ghSecretEnvironment)}},
		Apps:    initApps{env: env},
		Pairing: initPairing{doc: doc},
		Source:  initSource{repo: d.git.At(crossrevCheckout())},
		Files:   initFiles{},
		Out:     out,
	}

	execution := initcmd.Execution{
		Labels:   client,
		Secrets:  &initcmd.SecretStore{Runner: d.orchestrator, Env: exec.Inherit(ghSecretEnvironment)},
		Keys:     initKeys{env: env},
		Register: initRegistrar{commands: authCommands(out, doc)},
		Tokens:   initTokens{env: env},
		Seeds:    initcmd.SeedCommands{Runner: d.model, Env: exec.Inherit(harnessEnvironment)},
		Files:    initFiles{},
	}

	if err := initcmd.Run(ctx, request, execution); err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	return cli.ExitOK, nil
}

// ghSecretEnvironment is what a `gh secret` child inherits. It is the same
// allowlist internal/forge/ghexec builds its client with, and it is spelled
// again here because SecretStore takes the environment rather than reading one.
var ghSecretEnvironment = []string{
	"PATH", "HOME", "XDG_CONFIG_HOME", "GH_CONFIG_DIR", "GH_HOST",
	"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
}

// initGitHub is initcmd.GitHub over the three places its reads already live:
// the forge client for the repository facts, internal/app for the owner's
// account type, and a small `gh` caller for the two reads neither covers.
type initGitHub struct {
	forge   forge.Forge
	gh      *app.GH
	secrets *secretLister
}

func (g initGitHub) RepoSlug(ctx context.Context) (core.Slug, error) {
	return g.forge.RepoSlug(ctx)
}

// OwnerType is `gh api users/<owner> --jq .type` lowercased, and anything that
// is not `organization` is `user` (lib/init.sh:79-80).
func (g initGitHub) OwnerType(ctx context.Context, owner string) string {
	account, err := g.gh.AccountInfo(ctx, owner)
	if err != nil {
		return ""
	}
	return strings.ToLower(account.Type)
}

func (g initGitHub) DefaultBranch(ctx context.Context, repo core.Slug) string {
	return g.forge.DefaultBranch(ctx, repo)
}

// BranchProtected is `gh api repos/<repo>/branches/<branch>/protection`
// discarded (_init_branch_protected, lib/init.sh:427-430). A read that fails is
// false, which is what `>/dev/null 2>&1` makes of every failure alike.
func (g initGitHub) BranchProtected(ctx context.Context, repo core.Slug, branch string) bool {
	return g.secrets.ok(ctx, "api", "repos/"+repo.String()+"/branches/"+branch+"/protection")
}

func (g initGitHub) LabelColour(ctx context.Context, repo core.Slug, name string) string {
	return g.forge.LabelColour(ctx, repo, name)
}

// SecretsAtOrg is `gh secret list --org <owner>` with stderr discarded
// (lib/init.sh:413). The false answer covers a failed read and a login without
// admin:org alike, which the shell cannot tell apart either.
func (g initGitHub) SecretsAtOrg(ctx context.Context, owner string) (string, bool) {
	return g.secrets.list(ctx, false, "secret", "list", "--org", owner)
}

// SecretsAtRepo is `gh secret list --repo <repo>` with stderr folded into
// stdout (lib/init.sh:416). The string is what GitHub said whether or not the
// read worked, because the refusal quotes it.
func (g initGitHub) SecretsAtRepo(ctx context.Context, repo core.Slug) (string, error) {
	out, ok := g.secrets.list(ctx, true, "secret", "list", "--repo", repo.String())
	if !ok {
		return out, &ui.FatalError{Reason: "gh could not read the secrets", Action: out}
	}
	return out, nil
}

// secretLister runs `gh` for the three reads no other boundary covers.
//
// It is here rather than in internal/forge because none of the three is a
// forge operation the tool performs elsewhere: two list secrets, which only
// `init` does, and one asks about branch protection, which only the plan does.
type secretLister struct {
	runner exec.Runner
	env    []string
}

func (s *secretLister) list(ctx context.Context, combined bool, args ...string) (string, bool) {
	res := s.runner.Run(ctx, exec.Spec{Path: "gh", Args: args, Env: s.env})
	out := strings.TrimRight(string(res.Stdout), "\n")
	if combined {
		if stderr := strings.TrimRight(string(res.Stderr), "\n"); stderr != "" {
			if out == "" {
				out = stderr
			} else {
				out += "\n" + stderr
			}
		}
	}
	return out, res.Err == nil && res.ExitCode == 0
}

func (s *secretLister) ok(ctx context.Context, args ...string) bool {
	_, ok := s.list(ctx, false, args...)
	return ok
}

// initApps is initcmd.Apps over the metadata file `auth login` wrote
// (lib/init.sh:261, :294).
type initApps struct{ env app.Environment }

func (a initApps) App(owner, role string) (initcmd.App, bool) {
	meta, err := app.ReadMetadata(app.MetaPath(app.Dir(a.env), owner, role))
	if err != nil {
		return initcmd.App{}, false
	}
	return initcmd.App{Name: meta.Name, ID: strconv.FormatInt(meta.ID, 10)}, true
}

// initKeys is initcmd.Keys over the App private key on disk (_auth_pem,
// lib/auth.sh:31-37). The path comes back with the bytes because a key that is
// not there is reported by its path (lib/init.sh:486).
type initKeys struct{ env app.Environment }

func (k initKeys) PrivateKey(owner, role string) (string, []byte, bool) {
	path := app.PEMPath(app.Dir(k.env), owner, role)
	pem, err := os.ReadFile(path)
	if err != nil {
		return path, nil, false
	}
	return path, pem, true
}

// initRegistrar is initcmd.Registrar over `crossrev auth login --role refresher`
// (lib/init.sh:528).
type initRegistrar struct{ commands *app.Commands }

func (r initRegistrar) Login(ctx context.Context, owner, role string) error {
	return r.commands.Login(ctx, app.LoginRequest{Owner: owner, Role: role})
}

// initTokens is initcmd.TokenRecorder over the ledger (auth_token_record,
// lib/init.sh:815). The one-year clock starts at capture, because the token
// cannot be read back and nothing later can work out when it was issued.
type initTokens struct{ env app.Environment }

func (t initTokens) Record(repo core.Slug, secret string, days int) error {
	return app.TokenRecord(t.env, repo.String(), secret, days, time.Now())
}

// initPairing is initcmd.Pairing over internal/preflight. Three answers about
// one subject from one implementation, which is why the interface has three
// methods rather than three function fields a root could half-wire.
type initPairing struct{ doc harness.Document }

func (p initPairing) Supported(runner, name, leg string) (string, bool) {
	return preflight.PairingSupported(p.doc, runner, name, leg)
}

func (p initPairing) Secret(name string) (string, bool) {
	return preflight.HarnessSecret(p.doc, name)
}

func (p initPairing) NeedsRefresher(runner, name, endpoint string) bool {
	return preflight.NeedsRefresher(p.doc, runner, name, endpoint)
}

// initSource is initcmd.Source: which commit of CrossRev the generated
// workflows pin (lib/init.sh:141-147).
//
// The shell runs `git -C "$ROOT" rev-parse HEAD` and `git -C "$ROOT" describe
// --tags` against its OWN checkout rather than the repository being set up, and
// ROOT is the directory `bin/crossrev` was invoked from with its symlinks
// resolved (bin/crossrev:16-27). This does the same, over os.Executable: the
// binary sits at <root>/<something>/crossrev, so <dir>/.. is the checkout, and
// os.Executable answers the real path rather than the symlink install.sh made.
//
// internal/buildinfo.Pin() is not used, and that is the difference between this
// phase and the next one. Pin reads the VCS revision `go build` stamped and
// refuses a build made from a modified tree, which is the right answer for a
// released binary and the wrong one here: the shell does not care whether its
// tree is dirty, and refusing would make `init` unusable from every build that
// is not from a clean tag. The release phase that ships a binary with no
// checkout beside it is where Pin belongs.
//
// The two answers fail differently, which is why the interface has two methods.
// A SHA that cannot be read stops the run — a workflow pinned to nothing would
// be pinned to whatever `main` is on the day it runs. A ref that cannot be read
// is the word `untagged`, which the plan prints as a comment beside the pin.
type initSource struct{ repo *vcs.Repository }

func (s initSource) SHA(ctx context.Context) (string, error) {
	if s.repo == nil {
		return "", nil
	}
	head, err := s.repo.Head(ctx)
	if err != nil {
		// `|| INIT_SOURCE_SHA=""` at lib/init.sh:141. The empty answer is what
		// initcmd refuses on, with the shell's own words.
		return "", nil
	}
	return head.SHA(), nil
}

func (s initSource) Ref(ctx context.Context) (string, error) {
	if s.repo == nil {
		return untaggedRef, nil
	}
	out, err := s.repo.Run(ctx, "describe", "--tags")
	if err != nil || !out.OK() {
		// `|| INIT_SOURCE_REF="untagged"` at lib/init.sh:142-143.
		return untaggedRef, nil
	}
	described := strings.TrimSpace(out.Text())
	if described == "" {
		return untaggedRef, nil
	}
	return described, nil
}

// untaggedRef is what `git describe --tags` answering nothing leaves behind
// (lib/init.sh:143).
const untaggedRef = "untagged"

// crossrevCheckout is $ROOT: the CrossRev checkout this binary was built in,
// found the way bin/crossrev finds it.
//
// os.Executable resolves the symlink install.sh puts on PATH, which is the
// whole of what `_resolve` at bin/crossrev:16-24 does by hand because BSD
// readlink has no -f. A binary that is not inside a checkout answers a
// directory with no git repository in it, and initSource then answers the empty
// SHA that initcmd refuses on.
func crossrevCheckout() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	return filepath.Dir(filepath.Dir(resolved))
}

// initFiles is the working tree of the repository being set up, on both sides
// of the plan gate: Exists is the read `_init_resolve` and the plan make, and
// the two writes are what `_init_execute` makes past the confirmation
// (lib/init.sh:155-157, :566-587).
type initFiles struct{}

func (initFiles) Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil || !os.IsNotExist(err)
}

// MkdirAll is `mkdir -p`, which creates 0777 against the umask.
func (initFiles) MkdirAll(path string) error { return os.MkdirAll(path, 0o777) }

// WriteFile is a `>` redirection: it truncates an existing file and keeps its
// mode, and creates a new one 0666 against the umask.
func (initFiles) WriteFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o666)
}
