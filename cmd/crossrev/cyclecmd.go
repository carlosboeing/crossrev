package main

import (
	"context"
	"strconv"
	"time"

	"github.com/carlosboeing/crossrev/internal/app"
	"github.com/carlosboeing/crossrev/internal/cli"
	"github.com/carlosboeing/crossrev/internal/cycle"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// status is cmd_status (lib/run.sh:3040-3110): one read of the pull request,
// every decision derived from it, then the page.
//
// The liveness probe answers whether the run behind an unfinished claim is
// still working. Its git-dir function is `git rev-parse --git-common-dir` put
// through `pwd -P` (lib/run.sh:3322-3323), which is what the lock keys on so
// that every working tree of a clone finds the same one.
func status(ctx context.Context, out *ui.IO, doc harness.Document, req cli.StatusRequest) (int, error) {
	d := open(out, doc)
	client := d.forgeClient()

	repo := req.Repo
	if repo.Incomplete() {
		slug, err := client.RepoSlug(ctx)
		if err != nil {
			return cli.ExitFailure, reportFatal(out, &ui.FatalError{
				Reason: "could not work out which repository this is",
				Action: "Run crossrev from a checkout with a GitHub remote, or pass --repo owner/name.",
			})
		}
		repo = slug
	}

	probe := &cycle.LivenessProbe{
		Repo:   repo,
		PR:     req.PR,
		Forge:  client,
		GitDir: d.repo.CommonDir,
	}
	reader := &cycle.Status{
		Forge:    client,
		Liveness: probe,
		Now:      time.Now,
		Show:     d.show(),
		AppSlug:  appSlug(osEnv{}),
	}
	report, err := reader.Load(ctx, repo, req.PR)
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	cycle.Render(out, report)
	return cli.ExitOK, nil
}

// watchdog is cmd_watchdog (lib/run.sh:3681-3763).
//
// The list of pull requests waiting on a leg is read here rather than inside
// the sweep, because internal/cycle takes it as an argument: the sweep's whole
// subject is what it decides about a pull request, and handing the list in is
// what makes that answerable offline. The read is one forge call
// (lib/run.sh:3707-3709).
//
// The author is resolved once, before the loop. Bash resolves it per pull
// request with `state_trusted_author automated` (lib/run.sh:3737), and the
// answer is the same every time: the watchdog only ever runs on a schedule, so
// the mode is automated by construction and the trusted author is the App.
func watchdog(ctx context.Context, out *ui.IO, doc harness.Document, req cli.WatchdogRequest) (int, error) {
	d := open(out, doc)
	client := d.forgeClient()

	repo := req.Repo
	if repo.Incomplete() {
		slug, err := client.RepoSlug(ctx)
		if err != nil || slug.Incomplete() {
			return cli.ExitFailure, reportFatal(out, &ui.FatalError{
				Reason: "could not work out which repository to watch",
				Action: "Run the watchdog from a checkout with a GitHub remote, or pass --repo owner/name.",
			})
		}
		repo = slug
	}

	waiting := make([]cycle.Waiting, 0)
	for _, pr := range client.AwaitingPullRequests(ctx, repo) {
		waiting = append(waiting, cycle.Waiting{
			PR:      pr.Number,
			Labels:  pr.Labels,
			HeadSHA: pr.HeadSHA,
			Draft:   pr.Draft,
		})
	}

	author, err := automatedAuthor(osEnv{})
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}

	timeout, timeoutRefusal := watchdogTimeout(req.Timeout)

	w := &cycle.Watchdog{
		Forge:          client,
		Now:            time.Now,
		Out:            out,
		Timeout:        timeout,
		TimeoutRefusal: timeoutRefusal,
		Author:         author,
	}
	if _, err := w.Run(ctx, repo, waiting); err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	return cli.ExitOK, nil
}

// watchdogTimeout converts the `--timeout` string the parser kept, and answers
// the refusal rather than raising it.
//
// This is where bash's arithmetic reads the variable. The shell stores the flag
// as written (lib/run.sh:3686) and only evaluates it at `(( age < timeout ))`
// (lib/run.sh:3747), where a non-number ends the process with bash's own
// `abc: unbound variable`. Measured against bin/crossrev with `--timeout abc`:
// exit 0 and the closing summary on a repository with nothing waiting, exit 1
// on one with a pull request waiting. Converting at the flag refused both.
//
// The refusal is built and not printed, because internal/cycle raises it at the
// comparison and the caller's reportFatal prints it once. The words are this
// tool's rather than bash's, by the same ruling that covers the `${2:?…}`
// frames: a library path and a line number say nothing to an operator.
func watchdogTimeout(raw string) (time.Duration, error) {
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return 0, &ui.FatalError{
			Reason: "--timeout must be a number of seconds, and it was: " + raw,
			Action: "Pass the timeout in seconds, for example: --timeout 1800",
		}
	}
	return time.Duration(seconds) * time.Second, nil
}

// automatedAuthor is `state_trusted_author automated` (lib/state.sh:27-40).
//
// The App, and nothing else: a forged marker in automated mode makes an agent
// act. On a runner there is no operator config directory, so the slug comes
// from CROSSREV_APP_SLUG, which actions/create-github-app-token emits as
// `app-slug` and the generated workflows pass through. The metadata file is the
// fallback for an automated run started from a machine.
//
// It is `<slug>[bot]` and never `gh api user`. That read is the OTHER arm of
// the same function — the invoking user, for a local run — and using it here
// would have the watchdog trust whichever account the runner authenticated as.
func automatedAuthor(env processEnv) (string, error) {
	slug := appSlug(env)
	if slug == "" {
		return "", &ui.FatalError{
			Reason: "cannot determine which App's markers to trust",
			Action: "Automated mode reads markers only from the App that writes them. In a workflow, set CROSSREV_APP_SLUG from the token step's app-slug output. Locally, run: crossrev auth status",
		}
	}
	return slug + "[bot]", nil
}

// appSlug is the two halves of the slug lib/state.sh:35-36 reads, in its order:
// the variable a workflow passes from the token step's app-slug output, then
// the App metadata file an automated run started from a machine has.
//
// It lives here because internal/app is a tier-3 package, so neither leg nor
// internal/cycle may read the file. The composition root resolves it once and
// hands the answer into whichever of them needs it.
func appSlug(env processEnv) string {
	if slug := env.Getenv("CROSSREV_APP_SLUG"); slug != "" {
		return slug
	}
	meta, err := app.ReadMetadata(app.MetaPath(app.Dir(env), env.Getenv("CROSSREV_OWNER"), app.RoleLoop))
	if err == nil {
		return meta.Slug
	}
	return ""
}
