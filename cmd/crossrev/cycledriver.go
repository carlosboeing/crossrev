package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/carlosboeing/crossrev/internal/cli"
	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cycle"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/resolve"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/runlog"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// cycleCommand is cmd_cycle (lib/run.sh:2895-3016).
//
// The driver reloads the context after every leg, because state lives on the
// pull request rather than in memory. Everything it needs to do that arrives as
// a field, and this is where those fields are filled.
func cycleCommand(ctx context.Context, out *ui.IO, doc harness.Document, req cli.CycleRequest) (int, error) {
	d := open(out, doc)
	client := d.forgeClient()

	repo, cfg, err := legContext(ctx, d, client, req.Repo, req.PR)
	if err != nil {
		return cli.ExitFailure, err
	}
	d.log = openLog(repo, req.PR, cfg.Get(".logs.retention_days"),
		req.KeepTranscripts || runlog.KeepTranscripts(cfg.Get(".logs.keep_transcripts")), "review")
	client = d.forgeClient()

	author, err := trustedAuthor(ctx, client, cfg.Get(".mode"), out)
	if err != nil {
		return cli.ExitFailure, err
	}

	driver := &cycle.Driver{
		Review:  reviewAdapter{leg: reviewLeg(d, client), out: out, workdir: d.repo.Dir()},
		Resolve: resolveAdapter{leg: resolveLeg(d, client), out: out, author: author},
		Loader:  &contextLoader{forge: client, show: d.show(), author: author},
		Out:     out.Out,
		Nudge:   func() { upgradeNudge(out, cfg) },
		Pairing: cycle.Pairing(doc, cfg),
	}

	result := driver.Run(ctx, cycle.Request{
		PR:              req.PR,
		Repo:            req.Repo,
		Trigger:         cycle.Trigger(req.Trigger),
		HarnessOverride: req.HarnessOverride,
		KeepTranscripts: req.KeepTranscripts,
		NoTips:          req.NoTips,
	})
	if result.Err != nil {
		return cli.ExitFailure, result.Err
	}
	return result.ExitCode, nil
}

// reviewAdapter is cycle.ReviewLeg over internal/review.
//
// internal/cycle declares its own narrow leg interfaces because the tier rule
// forbids it importing a peer, so the two shapes are joined here and nowhere
// else. The messages a leg answers are printed as they arrive, which is what
// the shell does: the leg prints as it goes and the driver prints around it.
type reviewAdapter struct {
	leg     *review.Leg
	out     *ui.IO
	workdir string
}

func (a reviewAdapter) Run(ctx context.Context, req cycle.LegRequest) cycle.LegResult {
	result := a.leg.Run(ctx, review.Request{
		PR:              req.PR,
		Repo:            req.Repo,
		Trigger:         review.Trigger(req.Trigger),
		Continuation:    req.Continuation,
		HarnessOverride: req.HarnessOverride,
		Workdir:         a.workdir,
		RunID:           runlog.RunID(),
	})
	a.out.PrintAll(result.Messages)
	if result.Err != nil {
		a.out.Say(ui.Reason(result.Err))
		return cycle.LegResult{Failed: true}
	}
	return cycle.LegResult{Failed: result.Outcome == review.OutcomeError}
}

// resolveAdapter is cycle.ResolveLeg over internal/resolve.
type resolveAdapter struct {
	leg    *resolve.Leg
	out    *ui.IO
	author string
}

func (a resolveAdapter) Run(ctx context.Context, req cycle.LegRequest) cycle.LegResult {
	result := a.leg.Run(ctx, resolve.Request{
		PR:              req.PR,
		Repo:            req.Repo,
		Trigger:         resolve.Trigger(req.Trigger),
		Harness:         req.HarnessOverride,
		Author:          a.author,
		KeepTranscripts: req.KeepTranscripts,
	})
	a.out.PrintAll(result.Messages)
	if result.Message != "" {
		a.out.Say(result.Message)
	}
	if result.Err != nil {
		a.out.Say(ui.Reason(result.Err))
		return cycle.LegResult{Failed: true}
	}
	return cycle.LegResult{Failed: false}
}

// contextLoader is ctx_load, reduced to what the driver reads back after a leg
// (lib/run.sh:232-300).
//
// It prints nothing. The shell's ctx_load prints two lines for a draft pull
// request and returns 2 (lib/run.sh:259-262); here the draft is a field on
// State and the driver has its own line for it, so a loader that also printed
// would print it twice.
type contextLoader struct {
	forge  forge.Forge
	show   config.ShowFile
	author string
}

func (l *contextLoader) Load(ctx context.Context, req cycle.LoadRequest) (cycle.State, error) {
	var state cycle.State

	repo := req.Repo
	if repo.Incomplete() {
		slug, err := l.forge.RepoSlug(ctx)
		if err != nil {
			return state, &ui.FatalError{
				Reason: "could not work out which repository this is",
				Action: "Run crossrev from a checkout with a GitHub remote, or pass --repo owner/name.",
			}
		}
		repo = slug
	}
	pull, err := l.forge.PullRequest(ctx, repo, req.PR)
	if err != nil {
		return state, &ui.FatalError{
			Reason: fmt.Sprintf("could not read %s#%d", repo, req.PR),
			Action: "Check the number, and that `gh auth status` passes for that repository.",
		}
	}
	if req.Trigger == cycle.TriggerAutomatic && pull.IsCrossRepository {
		return state, &ui.FatalError{
			Reason: fmt.Sprintf("%s#%d comes from a fork", repo, req.PR),
			Action: "crossrev does not run on fork pull requests: GitHub withholds secrets from them. Review it locally or by hand.",
		}
	}
	if pull.State != "OPEN" {
		return state, &ui.FatalError{
			Reason: fmt.Sprintf("%s#%d is not open", repo, req.PR),
			Action: "crossrev only runs on open pull requests. Reopen it, or pick another number.",
		}
	}

	cfg, err := config.Load(ctx, pull.BaseRefOid, l.show)
	if err != nil {
		return state, err
	}

	state.Repo = repo
	state.PR = req.PR
	state.Draft = req.Trigger == cycle.TriggerAutomatic && pull.IsDraft
	state.Markers = markersFor(l.forge.IssueComments(ctx, repo, req.PR), l.author)
	for _, label := range pull.Labels {
		state.Labels = append(state.Labels, label.Name)
	}
	state.MaxPassesPerCycle = atoi(cfg.Get(".policy.max_passes_per_cycle"))
	state.MinFixSeverity = core.Severity(cfg.Get(".policy.min_fix_severity"))
	return state, nil
}

// markersFor is state_markers (lib/state.sh:56-63): every comment on the pull
// request authored by the trusted author, oldest first, decoded.
func markersFor(comments []forge.IssueComment, author string) []prstate.Marker {
	var lines []string
	for _, c := range comments {
		if c.AuthorLogin != author {
			continue
		}
		raw, err := json.Marshal(prstate.Comment{ID: c.ID, Body: c.Body, CreatedAt: c.CreatedAt})
		if err != nil {
			continue
		}
		lines = append(lines, string(raw))
	}
	if len(lines) == 0 {
		return []prstate.Marker{}
	}
	return prstate.Markers([]byte(strings.Join(lines, "\n")))
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// upgradeNudge is run_upgrade_nudge (lib/run.sh:3777-3789).
//
// Three conditions, in the shell's order: the flag or the environment silences
// it, the config key silences it, and a repository with no .github/workflows
// directory has no Actions to upgrade. A repository that already carries a
// crossrev workflow is set up, so the tip would be wrong rather than merely
// unwanted.
//
// The --no-tips flag is not read here. internal/cycle decides which endings
// call Nudge at all, and the flag suppresses the cycle's single tip by not
// calling it — which is the shell's own split at lib/run.sh:2904.
func upgradeNudge(out *ui.IO, cfg *config.Config) {
	if os.Getenv("CROSSREV_NO_TIPS") == "1" {
		return
	}
	if cfg.Get(".enable_automation_hint") == "false" {
		return
	}
	if info, err := os.Stat(filepath.Join(".github", "workflows")); err != nil || !info.IsDir() {
		return
	}
	if matches, _ := filepath.Glob(filepath.Join(".github", "workflows", "crossrev-*.yml")); len(matches) > 0 {
		return
	}
	fmt.Fprint(out.Out, upgradeTip)
}

// upgradeTip is the heredoc at lib/run.sh:3783-3788, byte for byte. It is a
// `cat <<'EOF'` rather than a ui_ helper, so it carries no gutter and ends with
// a blank line.
const upgradeTip = "  Tip: this repo already runs GitHub Actions. `crossrev init` would run this\n" +
	"  loop automatically on every pull request — review, fixes, re-review — and\n" +
	"  takes about a minute to set up. Silence this with `--no-tips`.\n\n"
