package ghexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
)

// prViewFields is the field list one `gh pr view` call asks for
// (lib/github.sh:39-41).
//
// One read, in one order. Every leg needs the same set, and asking for it in
// two calls is what would let a push between them anchor a finding to one
// revision and post it against another.
const prViewFields = "number,title,body,url,headRefName,headRefOid,baseRefName,baseRefOid," +
	"changedFiles,labels,isCrossRepository,isDraft,headRepositoryOwner,headRepository," +
	"maintainerCanModify,state"

// stdoutExcerptChars bounds how much of gh's own output may appear in an error
// a person reads. It is the only route from that output into operator-visible
// text, and gh answers with whatever the API sent it.
const stdoutExcerptChars = 80

// maxSlugChars is the longest answer that can be a repository slug: GitHub caps
// an owner at 39 characters and a repository name at 100, with one separator
// between them.
//
// It is a bound on the ANSWER and not only on the message. core.ParseSlug
// refuses a separator and whitespace in either half and nothing else, by
// design — it is not a copy of GitHub's naming rules — so a page of HTML
// carrying one slash parses: `<html>AAA…` becomes the owner and `html>` the
// name, and PathKey then builds a directory from it. Whatever gh printed, an
// answer longer than a slug can be is not the answer to this question.
const maxSlugChars = 39 + 1 + 100

// sinceLayout is the timestamp GitHub's `since` filter takes, which is what
// `date -u '+%Y-%m-%dT%H:%M:%SZ'` prints at lib/github.sh:70-72.
const sinceLayout = "2006-01-02T15:04:05Z"

// RepoSlug is which repository the working directory belongs to.
func (c *Client) RepoSlug(ctx context.Context) (core.Slug, error) {
	const summary = "could not work out which repository this is"

	res := c.run(ctx, "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if !answered(res) {
		return core.Slug{}, failure(summary, res)
	}
	answer := strings.TrimSpace(string(res.Stdout))
	if len([]rune(answer)) > maxSlugChars {
		return core.Slug{}, fmt.Errorf("%s: %w: gh answered %d characters, and a slug is at most %d: %q",
			summary, core.ErrSlug, len([]rune(answer)), maxSlugChars, excerpt(answer, stdoutExcerptChars))
	}

	slug, err := core.ParseSlug(answer)
	if err != nil {
		// The shell cannot reach this: it prints whatever gh printed. Go
		// refuses instead, because a slug reaches PathKey and PathKey builds
		// a directory under the state home (internal/core/repository.go:14-19).
		//
		// core.ErrSlug renders the whole answer with %q, so the message is
		// built here instead: an unbounded page of API output — a proxy's
		// error page, a terminal escape sequence — would otherwise go straight
		// to a terminal and into the run log.
		return core.Slug{}, fmt.Errorf("%s: %w: %q", summary, core.ErrSlug, excerpt(answer, stdoutExcerptChars))
	}
	return slug, nil
}

// DefaultBranch answers `main` when GitHub does not answer at all.
func (c *Client) DefaultBranch(ctx context.Context, repo core.Slug) string {
	res := c.run(ctx, "repo", "view", repo.String(), "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name")
	name := strings.TrimSpace(string(res.Stdout))
	if !answered(res) || name == "" {
		return "main"
	}
	return name
}

// PullRequest is one read of everything a leg needs.
func (c *Client) PullRequest(ctx context.Context, repo core.Slug, number int) (forge.PullRequest, error) {
	summary := fmt.Sprintf("could not read %s#%d", repo, number)

	res := c.run(ctx, "pr", "view", strconv.Itoa(number), "--repo", repo.String(), "--json", prViewFields)
	if !answered(res) {
		return forge.PullRequest{}, failure(summary, res)
	}

	var view struct {
		Number       int    `json:"number"`
		Title        string `json:"title"`
		Body         string `json:"body"`
		URL          string `json:"url"`
		HeadRefName  string `json:"headRefName"`
		HeadRefOid   string `json:"headRefOid"`
		BaseRefName  string `json:"baseRefName"`
		BaseRefOid   string `json:"baseRefOid"`
		ChangedFiles int    `json:"changedFiles"`
		Labels       []struct {
			Name        string `json:"name"`
			Color       string `json:"color"`
			Description string `json:"description"`
		} `json:"labels"`
		// A pointer, because absence is a third answer. lib/run.sh:290
		// records a missing isCrossRepository as `unknown`, and every
		// reader then tests for an explicit `false`: the automatic-trigger
		// fork refusal (lib/run.sh:250), the head-repository branch
		// (lib/run.sh:291) and the maintainer-edit guard (lib/legs.sh:478).
		// A plain bool collapses `unknown` onto the one value that means
		// "this repository's own branch", which is the permissive one.
		IsCrossRepository   *bool `json:"isCrossRepository"`
		IsDraft             bool  `json:"isDraft"`
		HeadRepositoryOwner struct {
			Login string `json:"login"`
		} `json:"headRepositoryOwner"`
		HeadRepository struct {
			Name string `json:"name"`
		} `json:"headRepository"`
		MaintainerCanModify bool   `json:"maintainerCanModify"`
		State               string `json:"state"`
	}
	if err := json.Unmarshal(res.Stdout, &view); err != nil {
		return forge.PullRequest{}, fmt.Errorf("%s: %w", summary, err)
	}

	head, err := revisionOf(view.HeadRefOid)
	if err != nil {
		return forge.PullRequest{}, fmt.Errorf("%s: head: %w", summary, err)
	}
	base, err := revisionOf(view.BaseRefOid)
	if err != nil {
		return forge.PullRequest{}, fmt.Errorf("%s: base: %w", summary, err)
	}

	pr := forge.PullRequest{
		Number:       view.Number,
		Title:        view.Title,
		Body:         view.Body,
		URL:          view.URL,
		HeadRefName:  view.HeadRefName,
		HeadRefOid:   head,
		BaseRefName:  view.BaseRefName,
		BaseRefOid:   base,
		ChangedFiles: view.ChangedFiles,
		// Absent reads as a fork, which is the fail-closed half of the
		// shell's tri-state: `unknown` and `true` take the same branch
		// everywhere they are read.
		IsCrossRepository:   view.IsCrossRepository == nil || *view.IsCrossRepository,
		IsDraft:             view.IsDraft,
		HeadRepositoryOwner: view.HeadRepositoryOwner.Login,
		HeadRepository:      view.HeadRepository.Name,
		MaintainerCanModify: view.MaintainerCanModify,
		State:               view.State,
	}
	for _, l := range view.Labels {
		pr.Labels = append(pr.Labels, forge.Label{
			Name:        l.Name,
			Colour:      strings.ToLower(l.Color),
			Description: l.Description,
		})
	}
	return pr, nil
}

// revisionOf validates a SHA gh reported, treating an absent one as the zero
// revision rather than as an error. A closed pull request whose head branch is
// gone reports an empty oid.
func revisionOf(sha string) (core.Revision, error) {
	if sha == "" {
		return core.Revision{}, nil
	}
	return core.NewRevision(sha)
}

// PullRequestDiff is the three-dot comparison between two revisions.
//
// Pinned to the pair the leg already loaded rather than asked for by pull
// request number, because `repos/{repo}/pulls/{n}` returns whatever the diff is
// at the moment of the call (lib/github.sh:78-95).
//
// # It keeps the trailing newline the shell strips
//
// gh_pr_diff captures the diff in a command substitution and prints it with
// `printf '%s'` (lib/github.sh:103 and :109). A command substitution drops
// every trailing newline and printf does not put one back, so the shell hands
// on a diff one byte shorter than gh printed. Measured against a fixture ending
// `62 0a`: the shell returns `62`. These are the bytes gh printed.
//
// Harmless, and traced rather than assumed. The diff reaches two places — the
// prompt built at lib/run.sh:1150 and diff_anchor at lib/run.sh:1360 — and
// internal/diff already has a test proving both forms round-trip byte-exactly,
// so no anchor and no prompt reads differently for the extra byte.
func (c *Client) PullRequestDiff(ctx context.Context, repo core.Slug, base, head core.Revision) ([]byte, error) {
	res := c.run(ctx, "api", "-H", "Accept: application/vnd.github.diff",
		"repos/"+repo.String()+"/compare/"+base.SHA()+"..."+head.SHA())
	if !answered(res) {
		return nil, failure(fmt.Sprintf("could not fetch the diff for %s at %s\n   The review leg has nothing to reason about without it. Check network access and `gh auth status`.", repo, head.SHA()), res)
	}
	return res.Stdout, nil
}

// PullRequestLabels is the labels currently on the pull request.
func (c *Client) PullRequestLabels(ctx context.Context, repo core.Slug, number int) []string {
	res := c.run(ctx, "api", issuePath(repo, number)+"/labels", "--jq", ".[].name")
	if !answered(res) {
		return nil
	}
	var names []string
	for line := range strings.SplitSeq(string(res.Stdout), "\n") {
		if line != "" {
			names = append(names, line)
		}
	}
	return names
}

// IssueComments is every conversation comment on the pull request.
//
// The author comes back on each comment rather than being filtered here.
// lib/state.sh:56-63 filters with a piped jq, and which author to trust is a
// question about the mode the run is in — automated trusts the App, local
// trusts the invoking user — which is not a question this package can answer.
// So the trust input is supplied and the trust decision is the caller's.
func (c *Client) IssueComments(ctx context.Context, repo core.Slug, number int) []forge.IssueComment {
	return c.comments(ctx, issuePath(repo, number)+"/comments")
}

// ReviewComments is every inline comment on the pull request
// (lib/state.sh:187).
func (c *Client) ReviewComments(ctx context.Context, repo core.Slug, number int) []forge.IssueComment {
	return c.comments(ctx, "repos/"+repo.String()+"/pulls/"+strconv.Itoa(number)+"/comments")
}

// comments reads a paginated comment endpoint, oldest first.
func (c *Client) comments(ctx context.Context, path string) []forge.IssueComment {
	res := c.run(ctx, "api", "--paginate", path)
	if !answered(res) {
		// An unreadable list reads as no comments, which is how the shell
		// reads a pull request with no markers on it: pass 1. Its `2>/dev/null`
		// and empty jq stream reach the same answer.
		return nil
	}
	return decodeComments(res.Stdout)
}

// RepoIssueComments is one page of repository-wide issue comments updated since
// a timestamp.
//
// Pull-request conversation comments are issue comments in GitHub's REST API,
// and each response carries the issue_url that identifies the pull request
// (lib/github.sh:66-69).
func (c *Client) RepoIssueComments(ctx context.Context, repo core.Slug, since time.Time, page int) ([]forge.IssueComment, error) {
	res := c.run(ctx, "api", "--method", "GET", "repos/"+repo.String()+"/issues/comments",
		"-f", "since="+since.UTC().Format(sinceLayout),
		"-F", "per_page=100",
		"-F", "page="+strconv.Itoa(page))
	if !answered(res) {
		return nil, failure("could not read repository comments", res)
	}
	return decodeComments(res.Stdout), nil
}

// ViewerLogin is whose markers a local run trusts.
func (c *Client) ViewerLogin(ctx context.Context) (string, error) {
	const summary = "could not resolve your GitHub identity"

	res := c.run(ctx, "api", "user", "--jq", ".login")
	if !answered(res) {
		return "", failure(summary, res)
	}
	login := strings.TrimSpace(string(res.Stdout))
	if login == "" {
		// gh answering nothing is the same fact as gh refusing: there is no
		// author to trust, and lib/state.sh:43-46 stops either way.
		return "", fmt.Errorf("%s: gh named no login", summary)
	}
	return login, nil
}

// issuePath is the issues endpoint for one pull request. GitHub numbers issues
// and pull requests in one sequence, which is why the conversation comments of
// a pull request live under /issues/.
func issuePath(repo core.Slug, number int) string {
	return "repos/" + repo.String() + "/issues/" + strconv.Itoa(number)
}

// decodeComments reads a comment list, or the several concatenated lists
// `gh api --paginate` prints, in page order.
func decodeComments(b []byte) []forge.IssueComment {
	var out []forge.IssueComment
	for _, page := range decodePages[struct {
		ID        int64  `json:"id"`
		Body      string `json:"body"`
		IssueURL  string `json:"issue_url"`
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}](b) {
		out = append(out, forge.IssueComment{
			ID:          page.ID,
			Body:        page.Body,
			AuthorLogin: page.User.Login,
			IssueURL:    page.IssueURL,
			CreatedAt:   page.CreatedAt,
		})
	}
	return out
}

// decodePages flattens the JSON `gh api --paginate` prints.
//
// With --paginate gh concatenates one array per page rather than merging them,
// so the whole output is a stream of arrays and not one JSON value. The shell
// hands that to jq, which reads a stream by default. A decoder in a loop is the
// same rule: read values until EOF, in the order they were printed, which is
// page order and so oldest first.
//
// A page that will not parse ends the read and keeps what came before it, the
// way a broken value ends jq's stream.
func decodePages[T any](b []byte) []T {
	dec := json.NewDecoder(bytes.NewReader(b))
	var all []T
	for {
		var page []T
		if err := dec.Decode(&page); err != nil {
			// io.EOF is the end of the stream and anything else is a value
			// that will not parse. Both stop the read and keep what came
			// before, which is what jq's stream does with a broken value.
			break
		}
		all = append(all, page...)
	}
	return all
}

// excerpt bounds a piece of gh's output for a message a person reads.
//
// Cut by character rather than by byte, so a multi-byte rune is not halved, and
// with anything that is not graphic replaced. A terminal reads an escape
// sequence rather than showing it, and gh's output is the API's answer rather
// than anything CrossRev wrote.
//
// unicode.IsGraphic is the whole test. It keeps the ordinary space, which is
// category Zs and so graphic, and refuses a tab, a newline and an escape — so
// a `r == ' '` arm beside it would never decide anything.
//
// Both call sites render the result with %q, which would escape a control
// character rather than emit it, so this is the second of two. It is here
// because the first is a property of one verb at one call site: the excerpt is
// what is safe to print, and printing it with %s must not be the difference.
func excerpt(s string, limit int) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsGraphic(r) {
			return r
		}
		return '\uFFFD'
	}, s)

	runes := []rune(cleaned)
	if len(runes) <= limit {
		return cleaned
	}
	return string(runes[:limit]) + "…"
}
