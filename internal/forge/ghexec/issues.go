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

// candidateBodyChars is the cut lib/github.sh:352 makes with `(.body // "")
// [0:500]`. jq slices a string by codepoint, so this counts characters and not
// bytes.
const candidateBodyChars = 500

// IssueByFinding is the exact dedupe, against CrossRev's own issues.
//
// Every filed issue carries the finding's hidden marker, the same mechanism
// every other outward write uses. Deterministic, no model, no false positives —
// this is what stops three pull requests touching one legacy bug filing it
// three times (lib/github.sh:320-338).
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
	// repeated field (lib/github.sh:356-357).
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
