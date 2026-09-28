package resolve

import (
	"context"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

func (l *Leg) postedFindingIDs(ctx context.Context, s *session) map[string]bool {
	var bodies []string
	for _, c := range l.Forge.ReviewComments(ctx, s.repo, s.req.PR) {
		if c.AuthorLogin == s.author {
			bodies = append(bodies, c.Body)
		}
	}
	for _, c := range l.Forge.IssueComments(ctx, s.repo, s.req.PR) {
		if c.AuthorLogin == s.author {
			bodies = append(bodies, c.Body)
		}
	}
	// Scoped to the current pass: a finding id's current thread is its
	// latest posted comment, so a reply marker from an earlier pass
	// answered a spent thread and must not suppress the upgrade's reply
	// to its new thread. A marker from this pass still suppresses a
	// same-pass retry, and a redrive re-answers its own findings through
	// excludeCurrentFindings below.
	out := map[string]bool{}
	for _, id := range prstate.FindingIDs(bodies, core.LegResolve, s.pass) {
		out[string(id)] = true
	}
	return out
}

func (l *Leg) unthreadedFindingIDs(ctx context.Context, s *session) []core.FindingID {
	var bodies []string
	for _, c := range l.Forge.IssueComments(ctx, s.repo, s.req.PR) {
		if c.AuthorLogin == s.author {
			bodies = append(bodies, c.Body)
		}
	}
	return prstate.FindingIDs(bodies, core.LegResolve, s.pass)
}

func excludeCurrentFindings(already map[string]bool, findings []harness.Node) map[string]bool {
	current := map[string]bool{}
	for _, f := range findings {
		current[f.Member("id").StringVal()] = true
	}
	out := map[string]bool{}
	for id, ok := range already {
		if ok && !current[id] {
			out[id] = true
		}
	}
	return out
}
