package forge_test

import (
	"context"
	"errors"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
)

// fakeForge answers every read from a script and records nothing else. It is
// here so a test of the counting rule can drive the interface without a process.
type fakeForge struct {
	// pages is what RepoIssueComments answers, indexed from page 1.
	pages [][]forge.IssueComment
	// failOn is the page number that reports an API failure, or zero.
	failOn int
	// asked records every page number RepoIssueComments was called with.
	asked []int
	// since records the cutoff each call carried.
	since []time.Time
}

var errPage = errors.New("gh could not read the repository comments")

func (f *fakeForge) RepoIssueComments(_ context.Context, _ core.Slug, since time.Time, page int) ([]forge.IssueComment, error) {
	f.asked = append(f.asked, page)
	f.since = append(f.since, since)
	if f.failOn == page {
		return nil, errPage
	}
	if page < 1 || page > len(f.pages) {
		return nil, nil
	}
	return f.pages[page-1], nil
}

func (f *fakeForge) RepoSlug(context.Context) (core.Slug, error)     { return core.Slug{}, nil }
func (f *fakeForge) DefaultBranch(context.Context, core.Slug) string { return "main" }
func (f *fakeForge) PullRequest(context.Context, core.Slug, int) (forge.PullRequest, error) {
	return forge.PullRequest{}, nil
}
func (f *fakeForge) PullRequestDiff(context.Context, core.Slug, core.Revision, core.Revision) ([]byte, error) {
	return nil, nil
}
func (f *fakeForge) PullRequestLabels(context.Context, core.Slug, int) []string           { return nil }
func (f *fakeForge) ReviewThreads(context.Context, core.Slug, int) []forge.ReviewThread   { return nil }
func (f *fakeForge) IssueComments(context.Context, core.Slug, int) []forge.IssueComment   { return nil }
func (f *fakeForge) ReviewComments(context.Context, core.Slug, int) []forge.IssueComment  { return nil }
func (f *fakeForge) ViewerLogin(context.Context) (string, error)                          { return "", nil }
func (f *fakeForge) WorkflowRunStatus(context.Context, core.Slug, string) forge.RunStatus { return "" }
func (f *fakeForge) LabelColour(context.Context, core.Slug, string) string                { return "" }
func (f *fakeForge) IssueByFinding(context.Context, core.Slug, string, core.FindingID) (int, bool) {
	return 0, false
}
func (f *fakeForge) IssueCandidates(context.Context, core.Slug, string, string) []forge.IssueCandidate {
	return nil
}
func (f *fakeForge) CommentCreate(context.Context, core.Slug, int, string) (int64, error) {
	return 0, nil
}
func (f *fakeForge) CommentEdit(context.Context, core.Slug, int64, string) error { return nil }
func (f *fakeForge) ReviewCommentCreate(context.Context, forge.ReviewComment) (forge.Placement, error) {
	return forge.PlacementInline, nil
}
func (f *fakeForge) ReviewReply(context.Context, core.Slug, int, int64, string) error { return nil }
func (f *fakeForge) ThreadResolve(context.Context, string) error                      { return nil }
func (f *fakeForge) LabelEnsure(context.Context, core.Slug, forge.Label) (forge.LabelState, error) {
	return forge.LabelExists, nil
}
func (f *fakeForge) IssueCreate(context.Context, core.Slug, string, string, []string) (int, error) {
	return 0, nil
}
func (f *fakeForge) IssueCommentCreate(context.Context, core.Slug, int, string)        {}
func (f *fakeForge) PullRequestLabelAdd(context.Context, core.Slug, int, string) error { return nil }
func (f *fakeForge) PullRequestLabelRemove(context.Context, core.Slug, int, string)    {}

var _ forge.Forge = (*fakeForge)(nil)
