package resolve

import (
	"context"
	"encoding/json"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
)

type threadRef struct {
	ID            string
	RootCommentID int64
}

func threadsByFinding(threads []forge.ReviewThread) map[string]threadRef {
	out := map[string]threadRef{}
	for _, th := range threads {
		for _, id := range th.FindingIDs {
			if _, ok := out[string(id)]; ok {
				continue
			}
			out[string(id)] = threadRef{ID: th.ID, RootCommentID: th.RootCommentID}
		}
	}
	return out
}

func (l *Leg) replyAndResolve(ctx context.Context, s *session, recs []harness.Node, findings []harness.Node, threads []forge.ReviewThread, commitSHA string, already map[string]bool, unthreaded int) (resolved, escalated, unthreadedOut int, findingsOut json.RawMessage) {
	by := threadsByFinding(threads)
	harnessName := s.settings.Harness
	model := s.settings.Model
	for _, d := range recs {
		id := d.Member("finding_id").StringVal()
		disp := d.Member("resolution").StringVal()
		tracked := d.Member("crossrev_tracked").StringVal()
		th := by[id]

		if !already[id] {
			body := ReplyBody(mustMarshal(d), tracked, s.pass, harnessName, model)
			if th.RootCommentID != 0 {
				if err := l.Forge.ReviewReply(ctx, s.repo, s.req.PR, th.RootCommentID, body); err != nil {
					unthreaded++
					_, _ = l.Forge.CommentCreate(ctx, s.repo, s.req.PR, body)
				}
			} else {
				unthreaded++
				_, _ = l.Forge.CommentCreate(ctx, s.repo, s.req.PR, body)
			}
			already[id] = true
		}

		should := false
		switch disp {
		case string(core.ResolutionFixed):
			should = commitSHA != ""
		case string(core.ResolutionSkipped), string(core.ResolutionDisputed):
			should = true
		case string(core.ResolutionDeferred):
			should = tracked != ""
		case string(core.ResolutionEscalated):
			escalated++
		}
		if should && th.ID != "" {
			if err := l.Forge.ThreadResolve(ctx, th.ID); err == nil {
				resolved++
			}
		}

		for j := range findings {
			if findings[j].Member("id").StringVal() == id {
				findings[j].Set("resolution", harness.FromString(disp))
				if tracked == "" {
					findings[j].Set("tracked_as", harness.FromNull())
				} else {
					findings[j].Set("tracked_as", harness.FromString(tracked))
				}
				break
			}
		}
	}
	findingsOut, _ = json.Marshal(findings)
	return resolved, escalated, unthreaded, findingsOut
}

func mustMarshal(d harness.Node) json.RawMessage {
	b, err := json.Marshal(d)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
