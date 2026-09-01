package ghexec

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
)

// The two reports lib/github.sh:174-186 prints when the publish filter could
// not process a body carrying a marker. The severity follows the write rather
// than the body: the writes that stop are the ones whose comment holds the pass
// marker, and the four that degrade are already handled by their callers.
const (
	unfilteredSummary = "could not filter a comment body for credential shapes"
	unfilteredRefusal = "That comment carries the pass marker, which is CrossRev's record of what ran, so it stopped rather than publishing a body without it."
	unfilteredWarning = "The text was withheld, so the marker it carried is not on the pull request. This pass still completes, and a later retry may repeat a comment it would otherwise have skipped."
)

// CommentCreate posts an overall comment and returns its id.
func (c *Client) CommentCreate(ctx context.Context, repo core.Slug, number int, body string) (int64, error) {
	summary := fmt.Sprintf("could not post a comment on %s#%d\n   Every pass records itself in a comment, so CrossRev stops rather than working without a record. Check the token has pull-requests write.", repo, number)

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
		// The shell carries an unreadable id as an empty string and edits
		// `issues/comments/` later. Refusing here is the fail-closed answer to
		// the same fact: the pass marker lives in this comment, and an id
		// nothing can address is a record CrossRev cannot update.
		return 0, fmt.Errorf("%s: gh named no comment id", summary)
	}
	return id, nil
}

// CommentEdit rewrites a comment.
func (c *Client) CommentEdit(ctx context.Context, repo core.Slug, commentID int64, body string) error {
	summary := fmt.Sprintf("could not update comment %d on %s", commentID, repo)

	filtered, lost, err := c.publish(body)
	if err != nil {
		return fmt.Errorf("%s: %w", summary, err)
	}
	if lost {
		return fmt.Errorf("%s: %s: %s", summary, unfilteredSummary, unfilteredRefusal)
	}

	res := c.run(ctx, "api", "--method", "PATCH",
		"repos/"+repo.String()+"/issues/comments/"+strconv.FormatInt(commentID, 10),
		"-f", "body="+filtered)
	if !answered(res) {
		return failure(summary, res)
	}
	return nil
}

// ReviewCommentCreate posts an inline comment anchored to a line and a side of
// the diff.
//
// GitHub rejects a comment whose line is not part of the diff — the commonest
// cause being a finding on a deleted line sent as RIGHT. Losing the finding
// would be worse than moving it, so this falls back to a top-level comment that
// names the location (lib/github.sh:204-227).
//
// The fallback body goes through the filter a second time, as it does in the
// shell, and the filter is documented as idempotent for exactly this path
// (lib/log.sh:138-141).
func (c *Client) ReviewCommentCreate(ctx context.Context, comment forge.ReviewComment) (forge.Placement, error) {
	filtered, lost, err := c.publish(comment.Body)
	if err != nil {
		return "", err
	}
	if lost {
		c.warn(unfilteredSummary, unfilteredWarning)
	}

	res := c.run(ctx, "api", "--method", "POST",
		"repos/"+comment.Repo.String()+"/pulls/"+strconv.Itoa(comment.Number)+"/comments",
		"-f", "body="+filtered,
		"-f", "commit_id="+comment.Commit.SHA(),
		"-f", "path="+comment.Path,
		"-F", "line="+strconv.Itoa(comment.Line),
		"-f", "side="+comment.Side.String())
	if answered(res) {
		return forge.PlacementInline, nil
	}

	located := fmt.Sprintf("**%s:%d** (%s)\n\n%s", comment.Path, comment.Line, comment.Side, filtered)
	if _, err := c.CommentCreate(ctx, comment.Repo, comment.Number, located); err != nil {
		return "", err
	}
	return forge.PlacementFallback, nil
}

// ReviewReply replies inside an existing review thread, addressed by the
// thread's first comment. Replying at top level instead is what makes a pull
// request unreadable (lib/github.sh:229-238).
func (c *Client) ReviewReply(ctx context.Context, repo core.Slug, number int, rootCommentID int64, body string) error {
	filtered, lost, err := c.publish(body)
	if err != nil {
		return err
	}
	if lost {
		c.warn(unfilteredSummary, unfilteredWarning)
	}

	res := c.run(ctx, "api", "--method", "POST",
		"repos/"+repo.String()+"/pulls/"+strconv.Itoa(number)+"/comments/"+
			strconv.FormatInt(rootCommentID, 10)+"/replies",
		"-f", "body="+filtered)
	if !answered(res) {
		return failure(fmt.Sprintf("could not reply in the thread rooted at comment %d on %s#%d",
			rootCommentID, repo, number), res)
	}
	return nil
}
