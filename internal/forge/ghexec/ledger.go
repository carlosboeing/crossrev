package ghexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// CoverageComments is every conversation comment on the pull request, oldest
// first, with API, pagination and decode failures reported.
//
// Ordinary IssueComments collapses failure to absence, which is how the
// shell reads a pull request with no markers: pass 1. The ledger cannot
// read that way: an unreadable comment list must refuse rather than answer
// an empty ledger, because absent coverage is not covered code. So this
// reports what that call swallows, and the ledger store is built on this
// rather than on it.
func (c *Client) CoverageComments(ctx context.Context, repo core.Slug, number int) ([]prstate.CoverageComment, error) {
	const summary = "could not read the coverage comments"

	res := c.run(ctx, "api", "--paginate", issuePath(repo, number)+"/comments")
	if !answered(res) {
		return nil, failure(summary+" on "+repo.String(), res)
	}
	comments, err := decodeLedgerComments(res.Stdout)
	if err != nil {
		return nil, fmt.Errorf("%s on %s: %w", summary, repo, err)
	}
	return comments, nil
}

// CoverageComment reads one comment back by id. Publication verifies every
// new shard this way before the manifest names it: the digest proves the
// comment holds the bytes the manifest expects.
func (c *Client) CoverageComment(ctx context.Context, repo core.Slug, commentID int64) (prstate.CoverageComment, error) {
	summary := fmt.Sprintf("could not read coverage comment %d on %s", commentID, repo)

	res := c.run(ctx, "api", "repos/"+repo.String()+"/issues/comments/"+strconv.FormatInt(commentID, 10))
	if !answered(res) {
		return prstate.CoverageComment{}, failure(summary, res)
	}
	comment, err := decodeLedgerComment(res.Stdout)
	if err != nil {
		return prstate.CoverageComment{}, fmt.Errorf("%s: %w", summary, err)
	}
	return comment, nil
}

// CreateCoverageComment posts one coverage comment — a shard or a manifest —
// and returns its id. It never edits: a generation becomes authoritative
// when its manifest comment is created, so there is no shared cell for two
// writers to overwrite.
func (c *Client) CreateCoverageComment(ctx context.Context, repo core.Slug, number int, body string) (int64, error) {
	summary := fmt.Sprintf("could not post a coverage comment on %s#%d\n   Every pass records itself in a comment, so CrossRev stops rather than working without a record. Check the token has pull-requests write.", repo, number)

	filtered, lost, err := c.publish(body)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", summary, err)
	}
	if lost {
		return 0, fmt.Errorf("%s: %s: %s", summary, unfilteredSummary, unfilteredRefusal)
	}

	res := c.run(ctx, "api", "--method", "POST", issuePath(repo, number)+"/comments",
		"-f", "body="+filtered, "--jq", ".id")
	if !answered(res) {
		return 0, failure(summary, res)
	}
	id, convErr := strconv.ParseInt(strings.TrimSpace(string(res.Stdout)), 10, 64)
	if convErr != nil {
		return 0, fmt.Errorf("%s: gh named no coverage comment id", summary)
	}
	return id, nil
}

// ledgerComment is one comment as the ledger read decodes it. The author
// travels with the body because selection filters to the trusted author,
// and a comment nobody trusted wrote contributes nothing.
type ledgerComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

// decodeLedgerComments reads a comment list, or the several concatenated
// lists `gh api --paginate` prints, in page order. A page that will not
// parse ends the read with an error rather than keeping what came before
// it: decodePages keeps the prefix the way jq's stream does, and the ledger
// must not select a generation out of a truncated list.
func decodeLedgerComments(b []byte) ([]prstate.CoverageComment, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	var out []prstate.CoverageComment
	for {
		var page []ledgerComment
		if err := dec.Decode(&page); err != nil {
			if len(out) == 0 {
				return nil, fmt.Errorf("decoding the coverage comment list: %w", err)
			}
			return nil, fmt.Errorf("decoding a later coverage comment page: %w", err)
		}
		for _, entry := range page {
			out = append(out, prstate.CoverageComment{
				ID:     entry.ID,
				Author: entry.User.Login,
				Body:   entry.Body,
			})
		}
		if !dec.More() {
			break
		}
	}
	if out == nil {
		out = []prstate.CoverageComment{}
	}
	return out, nil
}

// decodeLedgerComment reads the single comment CoverageComment answers.
func decodeLedgerComment(b []byte) (prstate.CoverageComment, error) {
	var entry ledgerComment
	if err := json.Unmarshal(b, &entry); err != nil {
		return prstate.CoverageComment{}, fmt.Errorf("decoding the coverage comment: %w", err)
	}
	return prstate.CoverageComment{
		ID:     entry.ID,
		Author: entry.User.Login,
		Body:   entry.Body,
	}, nil
}

var _ prstate.LedgerStore = (*Client)(nil)
