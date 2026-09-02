package main

import (
	"context"
	"strconv"
	"time"

	"github.com/carlosboeing/crossrev/internal/cli"
	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/resolve"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/runlog"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// harnessEnvironment is what a harness child may inherit.
//
// This is the model-facing side of ADR 0001, and the reason it is an allowlist
// rather than the process environment with four names removed: a strip-list
// holds only against names somebody wrote down, and every credential a vendor
// adds later passes through it by default. exec.NewOSRunner refuses the four
// forge credentials at Run whatever this list says, so the two guards are
// independent.
//
// The names are the ones the adapters and the credential staging read: the
// harness's own home and configuration, the vendor credentials the descriptor
// declares, and the terminal and locale a CLI renders with.
var harnessEnvironment = []string{
	"PATH",
	"HOME",
	"SHELL",
	"USER",
	"LOGNAME",
	"TMPDIR",
	"LANG",
	"LC_ALL",
	"TERM",
	"COLORTERM",
	"NO_COLOR",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_STATE_HOME",
	"XDG_CACHE_HOME",

	// The vendor credentials and homes lib/harnesses.json declares, plus the
	// endpoint pair lib/adapters/claude.sh:82 and :91 read and set.
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_BASE_URL",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CROSSREV_CODEX_AUTH",
	"CODEX_HOME",
	"CROSSREV_GROK_AUTH",
	"GROK_HOME",
	"CROSSREV_OPENCODE_AUTH",
	"OPENCODE_CONFIG",
	"OPENCODE_CONFIG_DIR",

	// The proxy names, because a harness CLI reaching a vendor through a
	// corporate proxy has no other route to one.
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"no_proxy",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
}

// openLog is log_init (lib/log.sh:64), with the retention window and the
// keep-transcripts decision the leg has already made.
//
// The flag outranks the config key, which is the shell's own order at
// lib/run.sh:956: keeping everything is the debugging posture, and it is asked
// for per run. A directory that cannot be created answers a nil *Log, which is
// a working Log that writes nothing — the port of the empty CROSSREV_RUN_DIR
// the shell library falls back to.
func openLog(repo core.Slug, pr int, retention string, keep bool, leg string) *runlog.Log {
	log, _ := runlog.Open(runlog.Options{
		Dir:             runlog.RunDir(repo.String(), strconv.Itoa(pr)),
		SweepBase:       runlog.RunsBase(),
		RetentionDays:   runlog.RetentionDays(retention),
		Repo:            repo.String(),
		PR:              strconv.Itoa(pr),
		KeepTranscripts: keep,
		Leg:             leg,
	})
	return log
}

// reviewLeg builds the review orchestrator (leg_review, lib/run.sh:913).
//
// Runner is the model-facing one, which refuses a Spec whose environment names
// a forge credential. Env is the allowlist above rather than this process's
// environment, so a credential nobody named never reaches the child at all.
func reviewLeg(d *deps, client forge.Forge) *review.Leg {
	return &review.Leg{
		Forge:   client,
		VCS:     d.repo,
		Harness: d.harnessDoc,
		Log:     d.log,
		Now:     time.Now,
		Runner:  d.model,
		Env:     exec.Inherit(harnessEnvironment),
	}
}

// resolveLeg builds the resolve orchestrator (leg_resolve, lib/run.sh:1730).
func resolveLeg(d *deps, client forge.Forge) *resolve.Leg {
	return &resolve.Leg{
		Forge:   client,
		Git:     resolve.GitFrom(d.repo),
		Runner:  d.model,
		Log:     d.log,
		Clock:   time.Now,
		Env:     exec.Inherit(harnessEnvironment),
		Harness: d.harnessDoc,
	}
}

// reviewCommand is `crossrev review` once the flags are parsed.
func reviewCommand(ctx context.Context, out *ui.IO, doc harness.Document, req cli.ReviewRequest) (int, error) {
	d := open(out, doc)
	client := d.forgeClient()

	repo, cfg, err := legContext(ctx, d, client, req.Repo, req.PR)
	if err != nil {
		return cli.ExitFailure, err
	}
	d.log = openLog(repo, req.PR, cfg.Get(".logs.retention_days"),
		req.KeepTranscripts || runlog.KeepTranscripts(cfg.Get(".logs.keep_transcripts")), "review")
	client = d.forgeClient()

	leg := reviewLeg(d, client)
	leg.Config = cfg
	result := leg.Run(ctx, review.Request{
		PR:              req.PR,
		Repo:            repo,
		Trigger:         review.Trigger(req.Trigger),
		Continuation:    req.Continuation,
		HarnessOverride: req.HarnessOverride,
		Workdir:         d.repo.Dir(),
		RunID:           runlog.RunID(),
	})
	return reportLeg(out, result.Messages, result.Err)
}

// resolveCommand is `crossrev resolve` once the flags are parsed.
func resolveCommand(ctx context.Context, out *ui.IO, doc harness.Document, req cli.ResolveRequest) (int, error) {
	d := open(out, doc)
	client := d.forgeClient()

	repo, cfg, err := legContext(ctx, d, client, req.Repo, req.PR)
	if err != nil {
		return cli.ExitFailure, err
	}
	d.log = openLog(repo, req.PR, cfg.Get(".logs.retention_days"),
		req.KeepTranscripts || runlog.KeepTranscripts(cfg.Get(".logs.keep_transcripts")), "resolve")
	client = d.forgeClient()

	author, err := trustedAuthor(ctx, client, cfg.Get(".mode"), out)
	if err != nil {
		return cli.ExitFailure, err
	}

	leg := resolveLeg(d, client)
	result := leg.Run(ctx, resolve.Request{
		PR:              req.PR,
		Repo:            repo,
		Trigger:         resolve.Trigger(req.Trigger),
		Harness:         req.HarnessOverride,
		Author:          author,
		KeepTranscripts: req.KeepTranscripts,
	})
	messages := result.Messages
	if result.Message != "" {
		messages = append(messages, result.Message)
	}
	return reportLeg(out, messages, result.Err)
}

// legContext resolves the repository and loads the configuration from the pull
// request's BASE revision, which is where policy is read from and never the
// head (ADR 0003, lib/config.sh:50-55).
//
// The leg loads it again for itself when Config is nil. It is loaded here as
// well because two decisions are made before the leg is built and both are
// configuration: the run log's retention window and whether transcripts are
// kept (lib/run.sh:956-960).
func legContext(ctx context.Context, d *deps, client forge.Forge, want core.Slug, pr int) (core.Slug, *config.Config, error) {
	repo := want
	if repo.Incomplete() {
		slug, err := client.RepoSlug(ctx)
		if err != nil {
			return repo, nil, d.out.Die("could not work out which repository this is",
				"Run crossrev from a checkout with a GitHub remote, or pass --repo owner/name.")
		}
		repo = slug
	}
	pull, err := client.PullRequest(ctx, repo, pr)
	if err != nil {
		return repo, nil, d.out.Die("could not read "+repo.String()+"#"+strconv.Itoa(pr),
			"Check the number, and that `gh auth status` passes for that repository.")
	}
	cfg, err := d.loadConfig(ctx, pull.BaseRefOid)
	if err != nil {
		return repo, nil, err
	}
	return repo, cfg, nil
}

// trustedAuthor is state_trusted_author (lib/state.sh:24-47), whose answer
// depends on the mode: the App in automated mode, the invoking user otherwise.
func trustedAuthor(ctx context.Context, client forge.Forge, mode string, out *ui.IO) (string, error) {
	if mode == "automated" {
		return automatedAuthor(osEnv{}, out)
	}
	login, err := client.ViewerLogin(ctx)
	if err != nil {
		return "", out.Die("could not resolve your GitHub identity", "Run: gh auth login")
	}
	return login, nil
}

// reportLeg prints what a leg reported and answers the status.
//
// The kind of each line is lost on the way here: internal/review and
// internal/resolve answer a []string, and the shell prints some of those with
// ui_say and some with ui_ok. Every line is printed with ui_say, which is the
// commonest of the two, and the difference is the one place this wiring cannot
// be byte-exact against the shell.
func reportLeg(out *ui.IO, messages []string, err error) (int, error) {
	for _, message := range messages {
		out.Say(message)
	}
	if err != nil {
		return cli.ExitFailure, err
	}
	return cli.ExitOK, nil
}
