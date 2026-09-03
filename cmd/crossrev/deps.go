package main

import (
	"context"
	"os"

	"github.com/carlosboeing/crossrev/internal/app"
	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/forge/ghexec"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/runlog"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// deps is what the composition root opens once and every command reads from.
//
// The two runners are the whole of ADR 0001 as a pair of fields. The
// orchestrator runner starts git and `gh`, both of which legitimately hold a
// forge credential; the model-facing runner starts a harness, and refuses one.
// Neither has a default here: exec.NewOSRunner is the safe default inside the
// packages that take a Runner, and it is the wrong one for `gh`, so every wiring
// below says which it means.
type deps struct {
	// out is the voice. One IO per command rather than one per process,
	// because --yes writes through it: see newIO.
	out *ui.IO

	// orchestrator starts git and gh. model starts a harness.
	orchestrator exec.Runner
	model        exec.Runner

	// harnessDoc is lib/harnesses.json, compiled in and parsed once.
	harnessDoc harness.Document

	// git is the checkout the command was invoked from.
	git  *vcs.Git
	repo *vcs.Repository

	// log is the per-run record. A nil *Log writes nothing and every method
	// tolerates it, which is the port of the empty CROSSREV_RUN_DIR the shell
	// library carries until log_init runs (lib/log.sh:26-30).
	log *runlog.Log
}

// ghEnvironment is what `gh` inherits, and it is ghexec's own allowlist rather
// than a second one here: internal/forge/ghexec/client.go states the reasoning
// for every name on it, so this wiring takes the default by passing no option.

// gitEnvironment is what a git child inherits.
//
// git is the one tool here that may hold a forge credential, because a push
// over https uses whatever credential helper the environment configures
// (internal/vcs's package comment). The names are the ones git itself
// documents and the ones the offline suite sets: a fixture points HOME and the
// XDG variables at a temporary directory, and a git that inherited neither
// would read the developer's own configuration.
var gitEnvironment = []string{
	"PATH",
	"HOME",
	"XDG_CONFIG_HOME",
	"GIT_CONFIG_GLOBAL",
	"GIT_CONFIG_SYSTEM",
	"GIT_CONFIG_NOSYSTEM",
	"GIT_TERMINAL_PROMPT",
	"GIT_SSL_CAINFO",
	"GIT_SSL_CAPATH",
	"GIT_ASKPASS",
	"SSH_AUTH_SOCK",
	"GH_TOKEN",
	"GITHUB_TOKEN",
	"GH_ENTERPRISE_TOKEN",
	"GITHUB_ENTERPRISE_TOKEN",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"no_proxy",
}

// open builds the dependencies every command shares.
//
// It opens no run log and reads no configuration: both belong to a command
// that has a repository and a pull request, and `crossrev doctor` has neither.
func open(out *ui.IO, doc harness.Document) *deps {
	orchestrator := exec.NewOrchestratorRunner()
	git := vcs.New(orchestrator, exec.Inherit(gitEnvironment))
	return &deps{
		out:          out,
		orchestrator: orchestrator,
		model:        exec.NewOSRunner(),
		harnessDoc:   doc,
		git:          git,
		repo:         git.At(""),
	}
}

// publisher is the filter every published body passes through
// (log_redact_publish, lib/log.sh:143-157, and log_redact_line at :116-118).
//
// forge.Publisher is two halves that are not interchangeable. Filter may fail,
// and a body it could not process must not be published; Mask cannot fail and
// is what an issue title gets, because the publish notice is a paragraph and a
// title has no room for one.
//
// The Log is nil for a command that opened none, and runlog tolerates that: the
// filtering is the same, and only the event line about it is lost.
type publisher struct{ log *runlog.Log }

func (p publisher) Filter(body string) (string, error) { return p.log.Publish(body) }
func (p publisher) Mask(text string) string            { return runlog.Redact(text) }

var _ forge.Publisher = publisher{}

// forgeClient is the `gh` boundary, with the run log's filter under it.
//
// It is built per command rather than once, because the filter has to carry
// whichever run log that command opened, and a client built before the log
// would carry a nil one for the whole run.
func (d *deps) forgeClient() *ghexec.Client {
	return ghexec.New(d.orchestrator, publisher{log: d.log},
		ghexec.WithWarn(func(summary, detail string) { d.out.Warn(summary, detail) }))
}

// show reads a path at a revision, which is how policy is read from the pull
// request's base revision and never its head (ADR 0003).
func (d *deps) show() config.ShowFile {
	return func(ctx context.Context, revision core.Revision, path string) ([]byte, config.FileStatus, error) {
		body, status, err := d.repo.Show(ctx, revision, path)
		return body, config.FileStatus(status), err
	}
}

// loadConfig reads the configuration at a revision.
//
// A zero revision is the working tree, which is `cfg_load ""` — what `config`,
// `doctor` and `init` do (lib/config.sh:134-136).
func (d *deps) loadConfig(ctx context.Context, base core.Revision) (*config.Config, error) {
	return config.Load(ctx, base, d.show())
}

// newIO builds the voice for one command.
//
// One per command rather than one per process, because --yes is a field on it:
// ui.IO.AssumeYes answers every confirmation, and the flag that sets it is
// parsed per command. The environment is the other half of the same switch —
// the shell reads CROSSREV_ASSUME_YES at lib/ui.sh:145 — so an inherited 1
// answers even where no flag was passed.
//
// Input is the terminal resolved the way _ui_input_source does: the controlling
// terminal first, then stdin when stdin is itself a terminal, and nothing at
// all otherwise, which is the arm that dies with a message rather than hanging
// (lib/ui.sh:129-134).
func newIO(assumeYes bool) *ui.IO {
	return &ui.IO{
		Out:       os.Stdout,
		Err:       os.Stderr,
		Palette:   ui.PaletteFor(ui.IsTerminal(os.Stdout), os.Getenv("NO_COLOR")),
		Input:     ui.Terminal{Stdin: os.Stdin},
		AssumeYes: assumeYes || ui.AssumeYes(os.Getenv("CROSSREV_ASSUME_YES")),
	}
}

// processEnv is the environment read, as internal/app spells it. The whole
// interface is one method, and it is here rather than reached for directly so
// the reads a command makes are visible in one file.
type processEnv interface{ Getenv(name string) string }

// osEnv is the real process environment.
//
// It is the only environment reader in this package, and internal/app's
// Environment is satisfied by it: every path an App's key, metadata and token
// ledger live under is resolved through this one value.
type osEnv struct{}

func (osEnv) Getenv(name string) string { return os.Getenv(name) }

var _ app.Environment = osEnv{}
