package forge

import (
	"context"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
)

// Forge is every GitHub read and write CrossRev makes.
//
// It is the operations the tool performs and not a GitHub client: there is no
// method here that no command calls. lib/github.sh states the reason the
// boundary is one surface at all — the agent process holds no forge credential,
// so every comment, reply, resolution, label and issue is made from here, and
// keeping the whole boundary in one place is what makes it stubbable.
//
// # Which failures are errors
//
// The methods that return an error are the ones the shell can tell has failed.
// The rest cannot: lib/github.sh sends the failure to /dev/null and answers
// with a default — an empty colour, an empty status, an empty thread list — and
// a caller that treated one of those as a failure would behave differently from
// the tool that ships. Each method below names the line it follows.
//
// # Ordering
//
// The batch reads answer in the order GitHub returned them, which is oldest
// first, and a paginated read concatenates its pages in page order. Pass
// numbering, revision detection and recovery all read chronologically.
type Forge interface {
	// RepoSlug is which repository the working directory belongs to
	// (lib/github.sh:30-34). The error is fatal to the caller: no repository
	// means no pull request to act on.
	RepoSlug(ctx context.Context) (core.Slug, error)

	// DefaultBranch answers `main` when GitHub does not answer at all
	// (lib/github.sh:46-48).
	DefaultBranch(ctx context.Context, repo core.Slug) string

	// PullRequest is one read of everything a leg needs
	// (lib/github.sh:36-45).
	PullRequest(ctx context.Context, repo core.Slug, number int) (PullRequest, error)

	// PullRequestDiff is the three-dot comparison between the two revisions
	// the leg already loaded, which is what the pull request view itself
	// shows (lib/github.sh:96-101). Excluding paths from it is the caller's
	// job: what the diff means is not a forge question.
	PullRequestDiff(ctx context.Context, repo core.Slug, base, head core.Revision) ([]byte, error)

	// PullRequestLabels is the labels currently on the pull request
	// (lib/state.sh:441-443). An unreadable list answers as no labels, the
	// way the shell's `2>/dev/null` does.
	PullRequestLabels(ctx context.Context, repo core.Slug, number int) []string

	// ReviewThreads is every inline conversation, with the finding ids its
	// comments carry (lib/github.sh:113-137). A failed read answers as no
	// threads, which is the shell's `|| printf '[]'`.
	ReviewThreads(ctx context.Context, repo core.Slug, number int) []ReviewThread

	// IssueComments is every conversation comment on the pull request, oldest
	// first (lib/state.sh:56-63). A failed read answers as none, which is how
	// the shell reads a pull request with no markers: pass 1.
	IssueComments(ctx context.Context, repo core.Slug, number int) []IssueComment

	// ReviewComments is every inline comment on the pull request, oldest
	// first (lib/state.sh:187). Filed under the same rule as IssueComments.
	ReviewComments(ctx context.Context, repo core.Slug, number int) []IssueComment

	// RepoIssueComments is one page of repository-wide issue comments updated
	// since a timestamp, a hundred to a page (lib/github.sh:66-76). This one
	// does report its failure, because the daily-count backstop above it
	// distinguishes an empty page from an unreadable one.
	RepoIssueComments(ctx context.Context, repo core.Slug, since time.Time, page int) ([]IssueComment, error)

	// ViewerLogin is whose markers a local run trusts (lib/state.sh:43-46).
	ViewerLogin(ctx context.Context) (string, error)

	// AwaitingPullRequests is every open pull request carrying a label that
	// starts `crossrev/awaiting-`, which is the list the watchdog sweeps
	// (lib/run.sh:3707-3709). A failed read answers as none, because the
	// shell's `|| stuck="[]"` cannot tell an empty repository from an
	// unreachable API either.
	AwaitingPullRequests(ctx context.Context, repo core.Slug) []AwaitingPullRequest

	// WorkflowRunStatus turns the run id a marker carries into an answer about
	// whether that leg is still going (lib/github.sh:61-64).
	WorkflowRunStatus(ctx context.Context, repo core.Slug, runID string) RunStatus

	// LabelColour is the hex a label currently carries, lowercased, or the
	// empty string if it does not exist (lib/github.sh:270-274). Absence is
	// the answer rather than an error: every fresh repository is that case.
	LabelColour(ctx context.Context, repo core.Slug, name string) string

	// IssueByFinding is the exact dedupe: the first CrossRev-filed issue
	// carrying that finding's marker (lib/github.sh:325-338). The bool is
	// false when there is none, and when the read failed — which the shell
	// cannot tell apart either.
	IssueByFinding(ctx context.Context, repo core.Slug, label string, id core.FindingID) (int, bool)

	// IssueCandidates is the fuzzy dedupe: issues the orchestrator retrieves
	// for a model to judge (lib/github.sh:340-354). A failed search answers
	// as no candidates.
	IssueCandidates(ctx context.Context, repo core.Slug, path, terms string) []IssueCandidate

	// CommentCreate posts an overall comment and returns its id, because
	// every later write to it is an edit of that id (lib/github.sh:187-195).
	CommentCreate(ctx context.Context, repo core.Slug, number int, body string) (int64, error)

	// CommentEdit rewrites a comment (lib/github.sh:197-202). Fatal on
	// failure for the same reason CommentCreate is: the pass marker lives in
	// that comment, so leaving it stale would misreport what happened.
	CommentEdit(ctx context.Context, repo core.Slug, commentID int64, body string) error

	// ReviewCommentCreate posts an inline comment, falling back to a
	// top-level comment naming the location when GitHub refuses the anchor
	// (lib/github.sh:204-227). The Placement says which happened.
	ReviewCommentCreate(ctx context.Context, comment ReviewComment) (Placement, error)

	// ReviewReply replies inside an existing thread, addressed by the
	// thread's first comment (lib/github.sh:229-238). Replying at top level
	// instead is what makes a pull request unreadable, so the caller counts
	// the failure rather than ignoring it.
	ReviewReply(ctx context.Context, repo core.Slug, number int, rootCommentID int64, body string) error

	// ThreadResolve marks a review thread resolved (lib/github.sh:240-249).
	ThreadResolve(ctx context.Context, threadID string) error

	// LabelEnsure declares a label at a colour and a description, recolouring
	// one that exists in another colour (lib/github.sh:281-318). The error is
	// a failed creation only; a failed recolour is cosmetic and answers
	// LabelExists, because a label with the wrong colour still drives the
	// chain.
	LabelEnsure(ctx context.Context, repo core.Slug, label Label) (LabelState, error)

	// IssueCreate files an issue for deferred work and returns its number
	// (lib/github.sh:356-378). The error is not fatal to the leg and is
	// deliberately loud: the caller must leave the thread open rather than
	// resolve it against a write that did not land.
	IssueCreate(ctx context.Context, repo core.Slug, title, body string, labels []string) (int, error)

	// IssueCommentCreate comments on an issue (lib/github.sh:380-385). The
	// shell discards the outcome, so there is nothing here to report.
	IssueCommentCreate(ctx context.Context, repo core.Slug, issue int, body string)

	// PullRequestLabelAdd applies a label (lib/state.sh:429-435). Fatal on
	// failure: the chain is label-driven, so a label that did not land leaves
	// the next workflow with no event to hear.
	PullRequestLabelAdd(ctx context.Context, repo core.Slug, number int, label string) error

	// PullRequestLabelRemove removes one (lib/state.sh:436-439). A label that
	// was not there is not a failure, and the shell discards the outcome.
	PullRequestLabelRemove(ctx context.Context, repo core.Slug, number int, label string)
}
