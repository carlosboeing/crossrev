package resolve

import (
	"context"
	"encoding/json"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
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

func (l *Leg) replyAndResolve(ctx context.Context, s *session, recs []map[string]json.RawMessage, findings []map[string]json.RawMessage, threads []forge.ReviewThread, commitSHA string, already map[string]bool, unthreaded int) (resolved, escalated, unthreadedOut int, findingsOut json.RawMessage) {
	by := threadsByFinding(threads)
	harnessName := s.settings.Harness
	model := s.settings.Model
	for _, d := range recs {
		id := jsonString(d["finding_id"])
		disp := jsonString(d["resolution"])
		tracked := jsonString(d["crossrev_tracked"])
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

		f := findingByID(findings, id)
		if f != nil {
			raw, _ := json.Marshal(disp)
			f["resolution"] = raw
			if tracked == "" {
				f["tracked_as"] = json.RawMessage("null")
			} else {
				b, _ := json.Marshal(tracked)
				f["tracked_as"] = b
			}
			for j := range findings {
				if jsonString(findings[j]["id"]) == id {
					findings[j] = f
					break
				}
			}
		}
	}
	findingsOut, _ = json.Marshal(findings)
	return resolved, escalated, unthreaded, findingsOut
}

func mustMarshal(d map[string]json.RawMessage) json.RawMessage {
	b, err := json.Marshal(d)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
