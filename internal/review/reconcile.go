package review

import (
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// PostedFindingIDs is state_posted_finding_ids (lib/state.sh:184-190): every
// finding id already written out by the review leg, read back from inline and
// top-level comments.
// The ids are read as written rather than parsed: _state_finding_ids validates
// no shape, and an id dropped here reads as never posted, so the leg posts a
// second inline comment over the one already on the pull request.
func PostedFindingIDs(reviewComments, issueComments []forge.IssueComment, author string) []string {
	bodies := authoredBodies(reviewComments, author)
	bodies = append(bodies, authoredBodies(issueComments, author)...)
	return prstate.FindingIDStrings(bodies, core.LegReview, 0)
}

// UnthreadedFindingIDs is state_unthreaded_finding_ids (lib/state.sh:206-211):
// review-leg findings whose marker landed as an issue comment, which is the
// record of a fallback post.
func UnthreadedFindingIDs(issueComments []forge.IssueComment, author string, pass int) []string {
	return prstate.FindingIDStrings(authoredBodies(issueComments, author), core.LegReview, pass)
}

func authoredBodies(comments []forge.IssueComment, author string) []string {
	out := make([]string, 0, len(comments))
	for _, c := range comments {
		if c.AuthorLogin == author {
			out = append(out, c.Body)
		}
	}
	return out
}

func postedSet(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}
