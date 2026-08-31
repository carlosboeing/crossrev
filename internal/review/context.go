package review

import (
	"context"
	"encoding/json"
	"fmt"
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
		// lib/run.sh:259-262
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

	author := req.Author
	if author == "" {
		author, err = l.Forge.ViewerLogin(ctx)
		if err != nil || author == "" {
			return loaded, "", &ui.FatalError{
				Reason: fmt.Sprintf("could not resolve whose markers to trust on %s#%d", repo, req.PR),
				Action: "Pass numbering, revision detection and the daily cap all read from the trusted author. Run: gh auth login",
			}
		}
	}
	loaded.Author = author
	loaded.Markers = markersFromComments(l.Forge.IssueComments(ctx, repo, req.PR), author)

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
