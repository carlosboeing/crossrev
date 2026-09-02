package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/carlosboeing/crossrev/internal/ui"
	"os"
	"strconv"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

func (l *Leg) claim(ctx context.Context, s *session) (prstate.Marker, ui.Line, error) {
	now := l.now().Unix()
	head := s.pr.HeadRefOid.SHA()
	runID := os.Getenv("GITHUB_RUN_ID")
	if runID == "" {
		runID = fmt.Sprintf("local-%d", os.Getpid())
	}

	if s.redriving {
		m := resetRedrive(s.redrive, now, head, runID, s.settings)
		body, err := claimBody(s, m, true)
		if err != nil {
			return prstate.Marker{}, ui.Line{}, err
		}
		id := s.redrive.CommentID()
		if err := l.Forge.CommentEdit(ctx, s.repo, id, body); err != nil {
			return prstate.Marker{}, ui.Line{}, &Refusal{
				Message: fmt.Sprintf("the claim comment did not post on %s#%d", s.repo, s.req.PR),
				Hint:    "The marker is what makes a retry safe, so CrossRev stops rather than resolving without one.",
			}
		}
		m = withCommentID(m, id)
		return m, ui.Line{}, nil
	}

	if open, ok := prstate.OpenClaim(s.markers, s.pass, core.LegResolve); ok {
		if reason, stale := prstate.ClaimIsStale(open, head, l.now(), prstate.DefaultClaimWindow); stale {
			open.TS = now
			open.HeadSHA = prstate.Some(head)
			open.Resolutions = json.RawMessage("[]")
			open.CommitSHA = prstate.Null[string]()
			open.CommitSubject = prstate.Null[string]()
			return open, ui.Warn(
				"abandoning the unfinished pass-"+strconv.Itoa(s.pass)+" resolve — "+reason,
				"Resuming it would reconcile replies against a revision that has moved. Starting the pass again instead.",
			), nil
		}
		return open, ui.Line{}, nil
	}

	m := newClaim(s, now, head, runID)
	body, err := claimBody(s, m, false)
	if err != nil {
		return prstate.Marker{}, ui.Line{}, err
	}
	id, err := l.Forge.CommentCreate(ctx, s.repo, s.req.PR, body)
	if err != nil || id == 0 {
		return prstate.Marker{}, ui.Line{}, &Refusal{
			Message: fmt.Sprintf("the claim comment did not post on %s#%d", s.repo, s.req.PR),
			Hint:    "The marker is what makes a retry safe, so CrossRev stops rather than resolving without one.",
		}
	}
	return withCommentID(m, id), ui.Line{}, nil
}

func newClaim(s *session, ts int64, head, runID string) prstate.Marker {
	return prstate.Marker{
		Version:       core.MarkerVersion,
		Leg:           core.LegResolve,
		Pass:          s.pass,
		State:         core.PassStarted,
		TS:            ts,
		DoneTS:        prstate.Null[int64](),
		RunID:         prstate.Some(runID),
		HeadSHA:       prstate.Some(head),
		Harness:       prstate.Some(s.settings.Harness),
		Model:         optString(s.settings.Model),
		Effort:        optString(s.settings.Effort),
		Endpoint:      optString(s.settings.Endpoint),
		ModelReported: prstate.Null[string](),
		Tokens:        json.RawMessage("null"),
		Usage:         json.RawMessage("null"),
		Billing:       prstate.Null[string](),
		Blocked:       prstate.Some(false),
		BlockedReason: prstate.Null[string](),
		CommitSHA:     prstate.Null[string](),
		CommitSubject: prstate.Null[string](),
		Summary:       prstate.Some(""),
		Resolutions:   json.RawMessage("[]"),
	}
}

func resetRedrive(done prstate.Marker, ts int64, head, runID string, set legSettings) prstate.Marker {
	done.State = core.PassStarted
	done.TS = ts
	done.DoneTS = prstate.Null[int64]()
	done.HeadSHA = prstate.Some(head)
	done.RunID = prstate.Some(runID)
	done.Harness = prstate.Some(set.Harness)
	done.Model = optString(set.Model)
	done.Effort = optString(set.Effort)
	done.Endpoint = optString(set.Endpoint)
	done.Blocked = prstate.Some(false)
	done.BlockedReason = prstate.Null[string]()
	done.CommitSHA = prstate.Null[string]()
	done.CommitSubject = prstate.Null[string]()
	done.ModelReported = prstate.Null[string]()
	done.Tokens = json.RawMessage("null")
	done.Usage = json.RawMessage("null")
	done.Billing = prstate.Null[string]()
	done.Summary = prstate.Some("")
	done.Resolutions = json.RawMessage("[]")
	done.Unthreaded = prstate.Opt[int]{}
	return done
}

func claimBody(s *session, m prstate.Marker, redrive bool) (string, error) {
	encoded, err := m.Encode()
	if err != nil {
		return "", err
	}
	label := passLabel(s.pass, s.maxPasses)
	lead := "Verifying each finding against the codebase. This comment becomes the pass summary when the resolve leg finishes."
	if redrive {
		lead = "Driving the pass again: the previous attempt ended without settling its findings. " + lead
	}
	return fmt.Sprintf("**crossrev — resolving %s**\n\n%s%s", label, lead, encoded), nil
}

func passLabel(pass, max int) string {
	if max > 0 && pass > max {
		return fmt.Sprintf("pass %d (past the cycle cap of %d)", pass, max)
	}
	return fmt.Sprintf("pass %d", pass)
}

func optString(s string) prstate.Opt[string] {
	if s == "" {
		return prstate.Null[string]()
	}
	return prstate.Some(s)
}

func withCommentID(m prstate.Marker, id int64) prstate.Marker {
	raw, err := json.Marshal(m)
	if err != nil {
		return m
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return m
	}
	fields["comment_id"] = json.RawMessage(strconv.FormatInt(id, 10))
	wrapped, err := json.Marshal(fields)
	if err != nil {
		return m
	}
	parsed, err := prstate.ParseMarker(wrapped)
	if err != nil {
		return m
	}
	return parsed
}

func resolutionCount(m prstate.Marker) int {
	var recs []json.RawMessage
	if err := m.DecodeResolutions(&recs); err != nil {
		return 0
	}
	return len(recs)
}
