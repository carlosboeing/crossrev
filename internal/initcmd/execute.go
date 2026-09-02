// execute.go — everything `init` changes, once the plan has been agreed to
// (_init_execute, lib/init.sh:436-651).
//
// Nothing here runs before the gate. Resolve reads and Print reads; this file
// is the only one in the package that declares a label, writes a secret or puts
// a file in a repository, and `--dry-run` returns before any of it.

package initcmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Execution is every port `init` needs to change something.
//
// It is separate from Request for the reason the plan gate exists: a Request is
// what a run may read, and an Execution is what it may write. A command that
// only prints a plan never builds one, so there is no wiring through which a
// dry run could reach a write.
//
// A nil port is refused by the section that needs it rather than filled in with
// a default, which is the rule Resolve already follows: every default here
// would be a lie an operator then acts on.
type Execution struct {
	// Labels declares the loop's labels and the filed issues' labels.
	Labels LabelWriter

	// Secrets writes a repository or organisation secret through `gh`. It
	// is a struct rather than an interface because the argv is the parity
	// surface: the offline suite matches routes on the whole argument
	// string, so the flags belong to this package and only the Runner under
	// them is wired in.
	Secrets *SecretStore

	// Keys is the App private key on disk.
	Keys Keys

	// Register registers the refresher App, and is reached only after
	// somebody at a terminal has agreed to it.
	Register Registrar

	// Tokens starts the one-year clock on a captured token.
	Tokens TokenRecorder

	// Seeds runs a harness's own credential seed command. Nil is not a
	// failure: it is a run with no way to open a browser, which is what
	// `command -v` answering nothing means at lib/init.sh:751.
	Seeds Seeder

	// Files is the working tree of the repository being set up.
	Files FileWriter
}

// FileWriter is the working tree of the repository being set up, writable.
//
// Two methods, because `_init_execute` makes two kinds of change to it: one
// `mkdir -p` and one write per generated file (lib/init.sh:566-587). Nothing
// here deletes: a workflow the pairing no longer needs is reported rather than
// removed, because a file somebody edited is not this command's to throw away.
type FileWriter interface {
	// MkdirAll is `mkdir -p`, which creates 0777 against the umask.
	MkdirAll(path string) error
	// WriteFile is a `>` redirection: it truncates an existing file and
	// keeps its mode, and creates a new one 0666 against the umask.
	WriteFile(path string, body []byte) error
}

// Dir is one directory as both halves of the working tree.
//
// Read and write are separate interfaces because the plan gate needs them
// separate, but they are the same directory in every real run, and a test that
// wrote through one and read through the other would be reading a different
// tree from the one the code changed.
type Dir string

// Exists is `[[ -e "$path" ]]`.
func (d Dir) Exists(path string) bool {
	_, err := os.Stat(d.join(path))
	return err == nil
}

// MkdirAll creates the directory and its parents.
func (d Dir) MkdirAll(path string) error { return os.MkdirAll(d.join(path), 0o777) }

// WriteFile writes one file.
func (d Dir) WriteFile(path string, body []byte) error {
	return os.WriteFile(d.join(path), body, 0o666)
}

func (d Dir) join(path string) string { return filepath.Join(string(d), filepath.FromSlash(path)) }

// Run is `cmd_init` once the flags are parsed (lib/init.sh:47-64).
//
// The yq requirement check the Bash makes first is the caller's: preflight is a
// tier-3 package like this one, so `crossrev init` runs it before it gets here.
//
// The gate is not politeness. It is the difference between a tool people trust
// with a second repository and one they run once — so the plan is printed
// first, `--dry-run` returns before anything is wired to a write, and a no
// closes the run with nothing changed.
func Run(ctx context.Context, req Request, ex Execution) error {
	plan, err := Resolve(ctx, req)
	if err != nil {
		return err
	}
	plan.Print(ctx, req)

	if req.DryRun {
		req.io().End("Nothing was changed — --dry-run prints the plan and stops.")
		return nil
	}

	// --yes sets CROSSREV_ASSUME_YES for the whole run rather than
	// answering this one question, and that is observable further in: the
	// refresher registration reads the same flag and declines to open a
	// browser under it (lib/init.sh:57-60 against :524).
	if req.Yes && req.Out != nil {
		req.Out.AssumeYes = true
	}
	agreed, err := req.io().Confirm("Proceed?")
	if err != nil {
		return err
	}
	if !agreed {
		req.io().End("Nothing was changed.")
		return nil
	}

	return plan.Execute(ctx, req, ex)
}

// Execute is `_init_execute` (lib/init.sh:436-651): labels, then secrets, then
// files, then what is still needed.
//
// The order is what an operator sees when something goes wrong. A label that
// will not create stops the run before a secret is written; a secret nobody can
// set is reported after the workflows are already installed, because they are,
// and saying otherwise would be the lie the plan gate exists to prevent.
func (p Plan) Execute(ctx context.Context, req Request, ex Execution) error {
	if err := p.EnsureLabels(ctx, req, ex); err != nil {
		return err
	}
	unfinished, err := p.EnsureSecrets(ctx, req, ex)
	if err != nil {
		return err
	}
	if err := p.WriteFiles(ctx, req, ex); err != nil {
		return err
	}
	p.ReportUnfinished(req, unfinished)
	return nil
}

// WriteFiles renders the workflows and the policy file into the repository
// (lib/init.sh:564-589).
// The context is unused: every write here is a local file, and the shell makes
// them with no way to cancel either. It is taken so the four sections have one
// shape.
func (p Plan) WriteFiles(_ context.Context, req Request, ex Execution) error {
	if ex.Files == nil {
		return fmt.Errorf("initcmd: the execution is missing Files")
	}

	out := req.io()
	out.Section("Files")
	if err := ex.Files.MkdirAll(".github/workflows"); err != nil {
		return err
	}
	for _, workflow := range p.Workflows {
		name := ".github/workflows/crossrev-" + workflow + ".yml"
		if err := ex.Files.WriteFile(name, p.RenderWorkflow(req, workflowTemplate(workflow))); err != nil {
			return err
		}
		out.OK("wrote " + name)
	}

	// A pairing that stopped needing the refresher leaves its workflow
	// behind, still on a cron, still failing on a secret nobody sets any
	// more. Saying so beats a scheduled job that emails a failure every
	// twelve hours.
	if !p.NeedsRefresher && req.Files.Exists(".github/workflows/crossrev-token-refresh.yml") {
		out.Warn(
			"this configuration needs no refresher, but .github/workflows/crossrev-token-refresh.yml is still there",
			"It stays on its schedule and fails every run once the credential it reads is gone. Delete it, and remove the refresher App's secrets, if the pairing is not going back.",
		)
	}

	// --upgrade regenerates workflows from the installed version, so drift
	// across repositories is handled by regeneration rather than by
	// hand-editing every copy. It deliberately leaves the policy file alone.
	if req.Upgrade && req.Files.Exists(".github/crossrev.yml") {
		out.Say("left .github/crossrev.yml alone — --upgrade regenerates workflows, not policy")
		return nil
	}
	if err := ex.Files.WriteFile(".github/crossrev.yml", p.WriteConfig(PolicyTemplate())); err != nil {
		return err
	}
	out.OK("wrote .github/crossrev.yml, with deferred work resolved to " + p.BacklogResolved)
	return nil
}

// workflowTemplate is `$ROOT/templates/crossrev-$t.yml` (lib/init.sh:570).
//
// A name outside the four cannot arrive: Resolve builds the list from a fixed
// three plus the refresher. It panics rather than writing an empty workflow,
// for the reason assets.go's template() panics — a mistake in this file, fixed
// by a recompile, and not something a caller can cause.
func workflowTemplate(name string) []byte {
	switch name {
	case "review":
		return ReviewWorkflowTemplate()
	case "resolve":
		return ResolveWorkflowTemplate()
	case "watchdog":
		return WatchdogWorkflowTemplate()
	case "token-refresh":
		return TokenRefreshWorkflowTemplate()
	}
	panic("initcmd: no template for the workflow " + name)
}

// ReportUnfinished names every secret `init` could not set, and what each one
// costs (lib/init.sh:591-650).
//
// Refusing to finish quietly. A missing secret fails the run before any review
// happens, which is the good kind of failure — but only if someone knows.
func (p Plan) ReportUnfinished(req Request, unfinished []string) {
	out := req.io()
	out.Section("Still needed")
	if len(unfinished) == 0 {
		out.OK("nothing — open a pull request and the loop runs")
		out.End("Watch it with: crossrev status --pr <number>")
		return
	}

	for _, secret := range unfinished {
		switch secret {
		case "CROSSREV_REFRESH_APP_ID", "CROSSREV_REFRESH_APP_PRIVATE_KEY":
			name := refresherHarness(req.Harness)
			out.No(secret + " — without the refresher App, " + name + "'s credential expires and stays expired")
			out.Line("   crossrev auth login --owner " + p.Owner + " --role refresher")
			out.Line("   crossrev init --upgrade")
		case "APP_ID", "APP_PRIVATE_KEY":
			// Already explained in the Secrets section.
		default:
			p.reportHarnessSecret(req, secret)
		}
	}
	out.End("The workflows are installed but will fail at the first missing secret, before any review runs.")
}

// reportHarnessSecret is the arm for a secret a harness or an endpoint owns
// (lib/init.sh:610-646).
func (p Plan) reportHarnessSecret(req Request, secret string) {
	out := req.io()
	name := harnessForSecret(req.Harness, secret)
	if name == "" {
		out.No(secret + " — an endpoint in the config names it, and nothing sets it")
		out.Line("   gh secret set " + secret + " " + p.secretScopeFlag())
		return
	}

	entry, _ := req.Harness.For(name)
	out.No(secret + " — a leg runs on " + entry.ProductName + ", so it cannot authenticate")
	switch entry.Credential.Archetype {
	case "A":
		out.Line("   " + name + " setup-token")
		out.Line("   gh secret set " + secret + " " + p.secretScopeFlag())
		out.Line("")
		out.Line("   That token is valid for a year and the command will not show it")
		out.Line("   again, so put it in the secret in the same sitting. Re-run")
		out.Line("   `crossrev init` from a terminal and it does both, and records the")
		out.Line("   date so `crossrev auth status` can warn as the year closes.")
	case "B":
		out.Line("   " + name + " login          # on a machine with a browser")
		// --repo unconditionally, never the scope helper. On an
		// organisation-owned repository that helper prints --org, and
		// this is the one secret that must never be
		// organisation-scoped — the same misconfiguration init warns
		// about a few lines above. An instruction someone copies
		// verbatim is not the place to be inconsistent with your own
		// warning.
		out.Line("   gh secret set " + secret + " --repo " + p.Repo.String() + " < ~/." + name + "/auth.json")
		out.Line("")
		out.Line("   Repository-scoped, not organisation-scoped, even on an org.")
		out.Line("   Concurrency groups do not span repositories, so an org-level")
		out.Line("   copy is refreshed by every repository reading it and the first")
		out.Line("   one to refresh invalidates it for all the rest.")
		out.Line("")
		out.Line("   Seeded once. From then on the refresher workflow is the only")
		out.Line("   thing that writes it, because using a refresh token consumes it")
		out.Line("   and a second writer kills the chain for everyone.")
	}
}
