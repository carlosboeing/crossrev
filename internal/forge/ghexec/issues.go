package ghexec

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
)

// candidateBodyChars is the cut lib/github.sh:351 makes with `(.body // "")
// [0:500]`. jq slices a string by codepoint, so this counts characters and not
// bytes.
const candidateBodyChars = 500

// IssueByFinding is the exact dedupe, against CrossRev's own issues.
//
// Every filed issue carries the finding's hidden marker, the same mechanism
// every other outward write uses. Deterministic, no model, no false positives —
// this is what stops three pull requests touching one legacy bug filing it
// three times (lib/github.sh:320-338).
//
// The label reaches a query string unescaped, exactly as lib/github.sh:332
// interpolates it. A label carrying `&` or `#` therefore changes or truncates
// the query. It is left as the shell has it and declared rather than fixed: the
// request is a read, the label is configuration from the base revision
// (ADR 0003) so setting one needs write access to the default branch, and the
// worst answer is a dedupe that finds nothing and files an issue twice. The
// three places the same name reaches a URL PATH are escaped, because two of
// those are writes and one of them leaves the repository.
func (c *Client) IssueByFinding(ctx context.Context, repo core.Slug, label string, id core.FindingID) (int, bool) {
	res := c.run(ctx, "api", "--paginate",
		"repos/"+repo.String()+"/issues?state=all&labels="+label+"&per_page=100")
	if !answered(res) {
		return 0, false
	}

	for _, issue := range decodePages[struct {
		Number      int             `json:"number"`
		Body        string          `json:"body"`
		PullRequest json.RawMessage `json:"pull_request"`
	}](res.Stdout) {
		// A pull request is an issue in this API and carries a pull_request
		// key; filing deferred work against one would be nonsense.
		if len(issue.PullRequest) > 0 && string(issue.PullRequest) != "null" {
			continue
		}
		for _, found := range findingIDsIn(issue.Body) {
			if found == id.String() {
				return issue.Number, true
			}
		}
	}
	return 0, false
}

// IssueCandidates is the fuzzy dedupe, against every issue in the repository.
//
// No shared key exists between a finding and a human-written issue about the
// same bug, so the orchestrator retrieves and the model judges. Open and
// recently-closed both, because closing an issue is a decision and re-filing
// something explicitly closed is the most irritating duplicate available
// (lib/github.sh:340-354).
//
// The file name and the terms go into the search query as written, so a file
// named `is:pr` narrows the search to pull requests and a term with a colon in
// it means whatever GitHub's search grammar says it means. The shell builds the
// same string at lib/github.sh:348-349. Declared rather than fixed for the same
// reason: it is a read, and the worst answer is a candidate list that misses
// the issue and a finding filed twice.
func (c *Client) IssueCandidates(ctx context.Context, repo core.Slug, filePath, terms string) []forge.IssueCandidate {
	query := "repo:" + repo.String() + " is:issue " + path.Base(filePath)
	if terms != "" {
		query += " " + terms
	}

	res := c.run(ctx, "api", "-X", "GET", "search/issues",
		"--raw-field", "q="+query, "--raw-field", "per_page=10")
	if !answered(res) {
		return nil
	}

	var body struct {
		Items []struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			State  string `json:"state"`
			Body   string `json:"body"`
		} `json:"items"`
	}
	if err := json.Unmarshal(res.Stdout, &body); err != nil {
		return nil
	}

	candidates := make([]forge.IssueCandidate, 0, len(body.Items))
	for _, item := range body.Items {
		candidates = append(candidates, forge.IssueCandidate{
			Number: item.Number,
			Title:  item.Title,
			State:  item.State,
			Body:   cutChars(item.Body, candidateBodyChars),
		})
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

// cutChars keeps the first n characters, counting the way jq counts.
func cutChars(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// IssueCreate files an issue for deferred work and returns its number.
//
// The failure is deliberately not fatal to the leg and deliberately loud: the
// caller must leave the thread open rather than resolve it against a write that
// did not land, which is exactly how deferred work disappears
// (lib/github.sh:363-377).
func (c *Client) IssueCreate(ctx context.Context, repo core.Slug, title, body string, labels []string) (int, error) {
	summary := fmt.Sprintf("could not file an issue on %s for a deferred finding", repo)

	if c.filter == nil {
		return 0, errNoFilter
	}
	// The title is masked rather than filtered: an issue title is one line and
	// the publish notice is a paragraph, so the note rides on the body where a
	// reader can act on it (lib/github.sh:358-361).
	filteredBody, lost, err := c.publish(body)
	if err != nil {
		return 0, err
	}
	if lost {
		c.warn(unfilteredSummary, unfilteredWarning)
	}

	args := []string{"api", "--method", "POST", "repos/" + repo.String() + "/issues",
		"-f", "title=" + c.filter.Mask(title), "-f", "body=" + filteredBody}
	// GitHub's issue API takes labels as an array, so each one is its own
	// repeated field (lib/github.sh:355-356).
	for _, label := range labels {
		if label != "" {
			args = append(args, "-f", "labels[]="+label)
		}
	}
	args = append(args, "--jq", ".number")

	res := c.run(ctx, args...)
	if !answered(res) {
		return 0, failure(summary, res)
	}
	number, convErr := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if convErr != nil {
		// The shell reads an unusable answer as an empty number and warns on
		// exactly that (`|| true`, then `[[ -z "$n" ]]`), so this is the same
		// outcome rather than a stricter one.
		return 0, fmt.Errorf("%s: gh named no issue number", summary)
	}
	return number, nil
}

// IssueCommentCreate comments on an issue. lib/github.sh:380-385 discards the
// outcome, so there is nothing here to report.
//
// # The one write that returns without a word
//
// publish has exactly one error, errNoFilter, and this is the only caller that
// drops it. Every other write reports it, and the difference is that this one
// has nothing to report it with: no error, no returned value, and — by the
// rule at WithWarn — no warning either, because a Client is built with its
// filter once rather than per call, so the fact is a construction bug and not
// something that happened to this comment.
//
// Silent is the right shape but not a free one, so it is written down: the
// guard is what stops a nil filter publishing an empty body, and a test in
// this package asserts that nothing reaches gh. Removing it does not fail to
// post — it posts `-f body=` with the caller's text gone.
func (c *Client) IssueCommentCreate(ctx context.Context, repo core.Slug, issue int, body string) {
	filtered, lost, err := c.publish(body)
	if err != nil {
		return
	}
	if lost {
		c.warn(unfilteredSummary, unfilteredWarning)
	}
	c.run(ctx, "api", "--method", "POST", issuePath(repo, issue)+"/comments", "-f", "body="+filtered)
}
