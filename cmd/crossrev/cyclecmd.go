package main

import (
	"context"
	"time"

	"github.com/carlosboeing/crossrev/internal/app"
	"github.com/carlosboeing/crossrev/internal/cli"
	"github.com/carlosboeing/crossrev/internal/cycle"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// status is cmd_status (lib/run.sh:3034-3103): one read of the pull request,
// every decision derived from it, then the page.
//
// The liveness probe answers whether the run behind an unfinished claim is
// still working. Its git-dir function is `git rev-parse --git-common-dir` put
// through `pwd -P` (lib/run.sh:3315-3316), which is what the lock keys on so
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
	}
	report, err := reader.Load(ctx, repo, req.PR)
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	cycle.Render(out, report)
	return cli.ExitOK, nil
}

// watchdog is cmd_watchdog (lib/run.sh:3666-3735).
//
// The list of pull requests waiting on a leg is read here rather than inside
// the sweep, because internal/cycle takes it as an argument: the sweep's whole
// subject is what it decides about a pull request, and handing the list in is
// what makes that answerable offline. The read is one forge call
// (lib/run.sh:3691-3693).
//
// The author is resolved once, before the loop. Bash resolves it per pull
// request with `state_trusted_author automated` (lib/run.sh:3709), and the
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
		})
	}

	author, err := automatedAuthor(osEnv{})
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}

	w := &cycle.Watchdog{
		Forge:   client,
		Now:     time.Now,
		Out:     out,
		Timeout: req.Timeout,
		Author:  author,
	}
	if _, err := w.Run(ctx, repo, waiting); err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	return cli.ExitOK, nil
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
	if slug := env.Getenv("CROSSREV_APP_SLUG"); slug != "" {
		return slug + "[bot]", nil
	}
	meta, err := app.ReadMetadata(app.MetaPath(app.Dir(env), env.Getenv("CROSSREV_OWNER"), app.RoleLoop))
	if err == nil && meta.Slug != "" {
		return meta.Slug + "[bot]", nil
	}
	return "", &ui.FatalError{
		Reason: "cannot determine which App's markers to trust",
		Action: "Automated mode reads markers only from the App that writes them. In a workflow, set CROSSREV_APP_SLUG from the token step's app-slug output. Locally, run: crossrev auth status",
	}
}
