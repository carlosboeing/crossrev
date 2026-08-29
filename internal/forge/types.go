package forge

import (
	"github.com/carlosboeing/crossrev/internal/core"
)

// PullRequest is everything about a pull request the legs need, in the shape
// one `gh pr view --json` call returns (lib/github.sh:37-45).
//
// It is one read on purpose. The line numbers a finding anchors to and the
// revision a comment is posted against have to come from the same read, or a
// push between two of them validates lines from one revision and posts them
// against another.
type PullRequest struct {
	Number              int
	Title               string
	Body                string
	URL                 string
	HeadRefName         string
	HeadRefOid          core.Revision
	BaseRefName         string
	BaseRefOid          core.Revision
	ChangedFiles        int
	Labels              []Label
	IsCrossRepository   bool
	IsDraft             bool
	HeadRepositoryOwner string
	HeadRepository      string
	MaintainerCanModify bool
	State               string
}

// Label is a repository label: the name the loop drives on, the colour it is
// declared with, and the description shown beside it.
type Label struct {
	Name string
	// Colour is six hexadecimal digits with no leading hash, the way GitHub's
	// API spells it. Reads lowercase it, because GitHub accepts either case on
	// write and answers in one of them (lib/github.sh:258-260).
	Colour      string
	Description string
}

// LabelState says what ensuring a label actually did, so a caller reporting an
// inventory can tell the truth about it (lib/github.sh:276-279).
type LabelState string

const (
	// LabelCreated means the label was not there and now is.
	LabelCreated LabelState = "created"
	// LabelRecoloured means it was there in another colour.
	LabelRecoloured LabelState = "recoloured"
	// LabelExists means it was already declared as asked, or a failed
	// recolour left it as it was.
	LabelExists LabelState = "exists"
)

// ReviewThread is one inline conversation on a pull request, with the CrossRev
// finding ids its comments carry.
//
// Thread identity comes from GraphQL because the REST review-comment API does
// not expose the node id the resolve mutation needs (lib/github.sh:115-118).
type ReviewThread struct {
	// ID is the GraphQL node id, which is what ThreadResolve addresses.
	ID         string
	IsResolved bool
	IsOutdated bool
	Path       string
	// Line is the line the thread is anchored to. Zero means GitHub returned
	// null, which is what an outdated thread reads as; GitHub numbers lines
	// from one, so zero cannot collide with a real anchor.
	Line int
	// RootCommentID is the first comment in the thread, which is the id a
	// threaded reply is addressed to. Zero means the thread had no comments.
	RootCommentID int64
	FindingIDs    []core.FindingID
	Comments      []ThreadComment
}

// ThreadComment is one comment inside a review thread, reduced to what a leg
// reads: who wrote it and what it says.
type ThreadComment struct {
	Author string
	Body   string
}

// IssueComment is one comment as GitHub's issue-comment API returns it.
//
// Pull-request conversation comments are issue comments in that API, and the
// issue_url is what identifies the pull request a repository-wide read found
// the comment on (lib/github.sh:67-69).
type IssueComment struct {
	ID          int64
	Body        string
	AuthorLogin string
	IssueURL    string
	CreatedAt   string
}

// IssueCandidate is one issue the fuzzy dedupe offers a model to judge, with
// its body cut to the first 500 characters the way lib/github.sh:351 cuts it.
type IssueCandidate struct {
	Number int
	Title  string
	State  string
	Body   string
}

// ReviewComment is one inline comment to post, anchored to a line and a side
// of the diff.
type ReviewComment struct {
	Repo core.Slug
	// Number is the pull request.
	Number int
	// Commit is the revision the comment is anchored against, which is the
	// head the leg already read rather than whatever head is now.
	Commit core.Revision
	Path   string
	Line   int
	Side   core.Side
	Body   string
}

// Placement says where an inline comment actually landed.
type Placement string

const (
	// PlacementInline means GitHub anchored it to the line asked for.
	PlacementInline Placement = "inline"
	// PlacementFallback means GitHub refused the anchor and the finding was
	// posted as a top-level comment naming the location instead, so it is not
	// lost. The commonest cause is a finding on a deleted line sent as RIGHT
	// (lib/github.sh:205-209).
	PlacementFallback Placement = "fallback"
)

// RunStatus is a workflow run's status: `queued`, `in_progress`, `completed`,
// or one of the newer waiting states.
//
// The empty value means unknown, and every caller has to treat it as unknown
// rather than as finished. A run in another repository, a token without
// `actions: read` and no network all answer this way, and a status that said a
// leg died because the API was unreachable is the failure this read exists to
// remove (lib/github.sh:50-60).
type RunStatus string

// Known reports that GitHub answered with a status at all.
func (s RunStatus) Known() bool { return s != "" }

// String renders the status as GitHub spells it.
func (s RunStatus) String() string { return string(s) }

// Publisher is the filter every published body passes through on its way out.
//
// It is a dependency rather than a decision made here: what text is safe to
// publish is not a question a provider adapter may answer. lib/github.sh calls
// log_redact_publish at each of its six writes and never decides the rule
// itself.
//
// Filter fails closed, and an implementation of Forge must not depend on it
// doing so. A non-nil error means the body could not be processed and must not
// be published; the string returned alongside that error is not used, because a
// provider that sent it would publish whatever a broken filter handed back. The
// substitute text is the provider's, and so is the choice between publishing it
// and refusing the write — the split lib/github.sh:166-186 draws between a body
// carrying a marker and one that does not.
//
// Mask is the other half of the same filter, and the two are not
// interchangeable. It masks a one-line string and cannot fail, which is what an
// issue title needs: the publish notice is a paragraph, so on a filed issue the
// note rides on the body where a reader can act on it while the title is only
// masked (lib/github.sh:358-361, lib/log.sh:116-118).
type Publisher interface {
	Filter(body string) (string, error)
	Mask(text string) string
}
