package main

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	"github.com/carlosboeing/crossrev/internal/vcs"
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
		return cli.ExitFailure, reportFatal(out, err)
	}
	d.log = openLog(repo, req.PR, cfg.Get(".logs.retention_days"),
		keepTranscripts(req.KeepTranscripts, cfg), "review")
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
	status, err := reportLeg(out, result.Messages, result.Err)
	closeRun(out, d.log, status, result.Err, "")
	return status, err
}

// resolveCommand is `crossrev resolve` once the flags are parsed.
func resolveCommand(ctx context.Context, out *ui.IO, doc harness.Document, req cli.ResolveRequest) (int, error) {
	d := open(out, doc)
	client := d.forgeClient()

	repo, cfg, err := legContext(ctx, d, client, req.Repo, req.PR)
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	d.log = openLog(repo, req.PR, cfg.Get(".logs.retention_days"),
		keepTranscripts(req.KeepTranscripts, cfg), "resolve")
	client = d.forgeClient()

	author, err := trustedAuthor(ctx, client, cfg.Get(".mode"))
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
		messages = append(messages, ui.Say(result.Message))
	}
	status, err := reportLeg(out, messages, result.Err)
	// The worktree the resolve leg works in is named from the same two facts
	// the leg derives it from, because run_cleanup reads CROSSREV_WORKTREE and
	// this process holds no such variable (lib/run.sh:96-99).
	worktree, _ := vcs.WorktreeDir(repo, req.PR)
	closeRun(out, d.log, status, result.Err, worktree)
	return status, err
}

// closeRun is run_cleanup's closing half (lib/run.sh:92-108): the kept
// worktree, the run log's own last line, and the directory a failed run left
// its record in.
//
// The shell runs this from an EXIT trap, so it fires on every path out of a
// leg. Here it is called at the one return each leg command has, which is the
// same set of paths: internal/* answers a refusal as a value rather than
// exiting, so nothing below leaves by any other route.
//
// The reason is the first half of whatever refusal ended the run, which is what
// ui_die puts in CROSSREV_DIE_REASON.
func closeRun(out *ui.IO, log *runlog.Log, status int, err error, worktree string) {
	failed := status != cli.ExitOK
	if failed && worktree != "" && isDir(worktree) {
		fmt.Fprintf(out.Err, "  Worktree kept for debugging: %s\n", worktree)
		log.Event("worktree", "kept "+worktree)
	}
	reason := ""
	if err != nil {
		reason, _ = refusalText(err)
	}
	log.Event("exit", fmt.Sprintf("code=%d reason=%s", status, reason))
	if failed && log.Dir() != "" && isDir(log.Dir()) {
		fmt.Fprintf(out.Err, "  Run log and any kept transcripts: %s\n", log.Dir())
	}
}

// keepTranscripts is lib/run.sh:957 and :1775, which is the flag OR the config
// key spelled `true`.
//
// runlog.KeepTranscripts is the OTHER half of the same decision: it reads
// CROSSREV_KEEP_TRANSCRIPTS, which the shell sets to `1` once either of these
// is satisfied. Handing it the config key compared `true` against `1`, so
// `logs.keep_transcripts: true` deleted the transcripts it asked to keep.
func keepTranscripts(flag bool, cfg *config.Config) bool {
	return flag || cfg.Get(".logs.keep_transcripts") == "true"
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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
			return repo, nil, &ui.FatalError{
				Reason: "could not work out which repository this is",
				Action: "Run crossrev from a checkout with a GitHub remote, or pass --repo owner/name.",
			}
		}
		repo = slug
	}
	pull, err := client.PullRequest(ctx, repo, pr)
	if err != nil {
		return repo, nil, &ui.FatalError{
			Reason: "could not read " + repo.String() + "#" + strconv.Itoa(pr),
			Action: "Check the number, and that `gh auth status` passes for that repository.",
		}
	}
	cfg, err := d.loadConfig(ctx, pull.BaseRefOid)
	if err != nil {
		return repo, nil, err
	}
	return repo, cfg, nil
}

// trustedAuthor is state_trusted_author (lib/state.sh:24-47), whose answer
// depends on the mode: the App in automated mode, the invoking user otherwise.
func trustedAuthor(ctx context.Context, client forge.Forge, mode string) (string, error) {
	if mode == "automated" {
		return automatedAuthor(osEnv{})
	}
	login, err := client.ViewerLogin(ctx)
	if err != nil {
		return "", &ui.FatalError{
			Reason: "could not resolve your GitHub identity",
			Action: "Run: gh auth login",
		}
	}
	return login, nil
}

// reportLeg prints what a leg reported and answers the status.
//
// Each line carries the helper the shell used at that site, so a verified
// success prints as ui_ok and a warning as ui_warn on stderr rather than
// everything arriving as ui_say. ui.IO.PrintAll is the dispatch.
func reportLeg(out *ui.IO, messages []ui.Line, err error) (int, error) {
	out.PrintAll(messages)
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	return cli.ExitOK, nil
}

// reportFatal prints a command's refusal the way ui_die prints one.
//
// Every package under internal/ answers a refusal as a VALUE rather than
// printing it, because none of them holds a terminal: internal/config builds a
// config.Refusal, internal/resolve a resolve.Refusal, internal/harness a
// harness.Refusal, and internal/review a *ui.FatalError. All four are the same
// two strings ui_die takes (lib/ui.sh:113-119), and all four were reaching
// internal/cli, which folds an error into status 1 without a word.
//
// So `crossrev config show` over a config naming an invalid severity ended at
// status 1 with an empty terminal, where the shell prints the value it rejected
// and the values it accepts. Sixteen of tests/test-config.sh's assertions were
// that one silence.
//
// This is the single exit for every command in this package, so nothing here
// may call IO.Die and then return what it answered: that would print twice. A
// command builds a *ui.FatalError instead.
//
// The error is returned unchanged, so a caller still has it.
func reportFatal(out *ui.IO, err error) error {
	if err == nil {
		return nil
	}
	reason, action := refusalText(err)
	_ = out.Die(reason, action)
	return err
}

// refusalText is the two halves of whichever refusal this is.
//
// The four types are not one shared type because each package is a tier that
// imports no peer; the shape is identical on purpose, and this is the one place
// that has to know all four.
func refusalText(err error) (reason, action string) {
	var fatal *ui.FatalError
	if errors.As(err, &fatal) {
		return fatal.Reason, fatal.Action
	}
	var cfg *config.Refusal
	if errors.As(err, &cfg) {
		return cfg.Message, cfg.Hint
	}
	var res *resolve.Refusal
	if errors.As(err, &res) {
		return res.Message, res.Hint
	}
	var vcsRefusal *vcs.Refusal
	if errors.As(err, &vcsRefusal) {
		return vcsRefusal.Message, vcsRefusal.Hint
	}
	var harnessRefusal *harness.Refusal
	if errors.As(err, &harnessRefusal) {
		return harnessRefusal.Reason, harnessRefusal.Action
	}
	// A plain error has no second half. The action is the one a reader can
	// always take.
	return err.Error(), "Run `crossrev doctor`, then try again."
}
