package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

var instructionFiles = []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md"}

func (l *Leg) loadContext(ctx context.Context, req Request) (Context, string, error) {
	var loaded Context
	repo := req.Repo
	if repo.Incomplete() {
		slug, err := l.Forge.RepoSlug(ctx)
		if err != nil {
			return loaded, "", &ui.FatalError{
				Reason: "could not work out which repository this is",
				Action: "Run crossrev from a checkout with a GitHub remote, or pass --repo owner/name.",
			}
		}
		repo = slug
	}
	pr, err := l.Forge.PullRequest(ctx, repo, req.PR)
	if err != nil {
		return loaded, "", &ui.FatalError{
			Reason: fmt.Sprintf("could not read %s#%d", repo, req.PR),
			Action: "Check the number, and that `gh auth status` passes for that repository.",
		}
	}
	loaded.Repo = repo
	loaded.PR = pr
	if req.Trigger == TriggerAutomatic && pr.IsCrossRepository {
		return loaded, "", &ui.FatalError{
			Reason: fmt.Sprintf("%s#%d comes from a fork", repo, req.PR),
			Action: "crossrev does not run on fork pull requests: GitHub withholds secrets from them. Review it locally or by hand.",
		}
	}
	if pr.State != "OPEN" {
		return loaded, "", &ui.FatalError{
			Reason: fmt.Sprintf("%s#%d is not open", repo, req.PR),
			Action: "crossrev only runs on open pull requests. Reopen it, or pick another number.",
		}
	}
	if req.Trigger == TriggerAutomatic && pr.IsDraft {
		// lib/run.sh:265-268
		return loaded, "draft pull request", nil
	}

	loaded.DefaultBranch = l.Forge.DefaultBranch(ctx, repo)

	cfg := l.Config
	if cfg == nil {
		cfg, err = config.Load(ctx, pr.BaseRefOid, l.show())
		if err != nil {
			return loaded, "", err
		}
	}
	loaded.Config = cfg

	author, err := l.trustedAuthor(ctx, req, cfg.Get(".mode"))
	if err != nil {
		return loaded, "", err
	}
	loaded.Author = author
	loaded.Markers = markersFromComments(l.Forge.IssueComments(ctx, repo, req.PR), author)

	backlog, err := cfg.ResolveBacklog(ctx, pr.BaseRefOid, cfg.Get(".backlog.destination"))
	if err != nil {
		return loaded, "", err
	}
	loaded.Backlog = backlog

	loaded.ReviewMD = l.fileAtBase(ctx, pr.BaseRefOid, "REVIEW.md")
	loaded.GitMessage = firstLines(l.fileAtBase(ctx, pr.BaseRefOid, ".gitmessage"), 20)
	loaded.Instructions = map[string][]byte{}
	for _, path := range instructionFiles {
		if body := l.fileAtBase(ctx, pr.BaseRefOid, path); len(body) > 0 {
			loaded.Instructions[path] = body
		}
	}
	if tracker, found, err := config.ProjectMapTracker(ctx, pr.BaseRefOid, l.show()); err != nil {
		return loaded, "", err
	} else if found {
		loaded.ProjectMapTracker = tracker
	}
	return loaded, "", nil
}

// trustedAuthor is state_trusted_author (lib/state.sh:24-47), keyed on the
// MODE and never on who asked for the leg.
//
// lib/run.sh:315 passes CTX_MODE, which lib/run.sh:305 reads from the
// configuration at the base revision, and lib/state.sh:26 branches on
// `automated` alone. Keyed on the trigger instead, `crossrev review --pr 42
// --trigger automatic` against a `mode: local` repository refused with "cannot
// determine which App's markers to trust" where the shell trusts the invoking
// user, and the reverse — an automated repository reviewed by hand — read the
// operator's own markers rather than the App's.
func (l *Leg) trustedAuthor(ctx context.Context, req Request, mode string) (string, error) {
	if req.Author != "" {
		return req.Author, nil
	}
	if mode == "automated" {
		// lib/state.sh:35-40. Measured: CROSSREV_APP_SLUG=crossrev → crossrev[bot].
		slug := os.Getenv("CROSSREV_APP_SLUG")
		if slug == "" {
			return "", &ui.FatalError{
				Reason: "cannot determine which App's markers to trust",
				Action: "Automated mode reads markers only from the App that writes them. In a workflow, set CROSSREV_APP_SLUG from the token step's app-slug output. Locally, run: crossrev auth status",
			}
		}
		return slug + "[bot]", nil
	}
	author, err := l.Forge.ViewerLogin(ctx)
	if err != nil || author == "" {
		return "", &ui.FatalError{
			Reason: fmt.Sprintf("could not resolve whose markers to trust on %s#%d", req.Repo, req.PR),
			Action: "Pass numbering, revision detection and the daily cap all read from the trusted author. Run: gh auth login",
		}
	}
	return author, nil
}

func (l *Leg) fileAtBase(ctx context.Context, base core.Revision, path string) []byte {
	if l.VCS == nil {
		return nil
	}
	body, status, err := l.VCS.Show(ctx, base, path)
	if err != nil || status != vcs.IsFile {
		return nil
	}
	return body
}

func firstLines(body []byte, n int) []byte {
	if len(body) == 0 || n <= 0 {
		return nil
	}
	count := 0
	for i := 0; i < len(body); i++ {
		if body[i] == '\n' {
			count++
			if count == n {
				return body[:i+1]
			}
		}
	}
	return body
}

func markersFromComments(comments []forge.IssueComment, author string) []prstate.Marker {
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

func hasStop(pr forge.PullRequest) bool {
	for _, label := range pr.Labels {
		if label.Name == "crossrev/stop" {
			return true
		}
	}
	return false
}
