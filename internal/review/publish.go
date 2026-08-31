package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

func (l *Leg) publish(ctx context.Context, req Request, loaded Context, settings legSettings, pass int, claimID int64, marker prstate.Marker) (prstate.Marker, []string, error) {
	var msgs []string
	findings := parseFindings(marker.Findings)
	minFix := loaded.Config.Get(".policy.min_fix_severity")
	if minFix == "" {
		minFix = string(core.SeverityMedium)
	}
	cap := atoi(loaded.Config.Get(".policy.max_passes_per_cycle"))

	already := postedSet(PostedFindingIDs(
		l.Forge.ReviewComments(ctx, loaded.Repo, req.PR),
		l.Forge.IssueComments(ctx, loaded.Repo, req.PR),
		loaded.Author,
	))
	unanchored := len(UnthreadedFindingIDs(l.Forge.IssueComments(ctx, loaded.Repo, req.PR), loaded.Author, pass))

	high, medium, low, pre := 0, 0, 0, 0
	for _, f := range findings {
		switch f.Severity {
		case "high":
			high++
		case "medium":
			medium++
		case "low":
			low++
		}
		if f.PreExisting {
			pre++
		}
	}
	actionable := ActionableCount(findings, minFix)
	if len(findings) > 0 {
		msgs = append(msgs,
			fmt.Sprintf("Found %d issue(s) — %d high, %d medium, %d low, of which %d pre-existing.", len(findings), high, medium, low, pre),
			fmt.Sprintf("%d at or above min_fix_severity (%s); the rest are reported and left alone.", actionable, minFix),
			"Posting them as inline comments on the lines they affect.",
		)
	}

	posted, skipped := 0, 0
	for _, f := range findings {
		if already[f.ID] {
			skipped++
			continue
		}
		side := core.SideRight
		if s, err := core.ParseSide(f.Side); err == nil {
			side = s
		}
		body := CommentBody(f, pass, settings.harness, settings.model, minFix)
		placement, err := l.Forge.ReviewCommentCreate(ctx, forge.ReviewComment{
			Repo:   loaded.Repo,
			Number: req.PR,
			Commit: loaded.PR.HeadRefOid,
			Path:   f.Path,
			Line:   f.Line,
			Side:   side,
			Body:   body,
		})
		if err != nil {
			return marker, msgs, err
		}
		posted++
		if placement == forge.PlacementFallback {
			unanchored++
		}
	}
	if posted > 0 {
		msgs = append(msgs, fmt.Sprintf("posted %d finding comment(s)", posted))
	}
	if skipped > 0 {
		msgs = append(msgs, fmt.Sprintf("%d finding(s) were already on the pull request from an earlier attempt, so they were not posted twice.", skipped))
	}
	if unanchored > 0 {
		noun := "findings"
		if unanchored == 1 {
			noun = "finding"
		}
		msgs = append(msgs, fmt.Sprintf("%d %s could not be anchored to a line and landed as top-level comments", unanchored, noun))
	}

	marker.Findings = attachThreads(marker.Findings, l.Forge.ReviewThreads(ctx, loaded.Repo, req.PR))
	marker.DoneTS = prstate.Some(l.now().Unix())
	marker.Unanchored = prstate.Some(unanchored)

	summary := SummaryBody(parseFindings(marker.Findings), marker, RenderContext{
		Repo:    loaded.Repo.String(),
		PR:      req.PR,
		MinFix:  minFix,
		MaxPass: cap,
	})
	if err := l.editClaim(ctx, loaded.Repo, claimID, summary, marker); err != nil {
		return marker, msgs, err
	}
	msgs = append(msgs, "posted a summary comment")

	marker.State = core.PassComplete
	if err := l.editClaim(ctx, loaded.Repo, claimID, summary, marker); err != nil {
		return marker, msgs, err
	}

	verdict := core.Verdict(marker.Verdict.Value())
	escalated := escalatedCount(loaded.Markers)
	next := policy.PassLabel(verdict, actionable, escalated)
	if verdict == core.VerdictConverged && next != policy.PassConverged {
		noun := "findings"
		if actionable == 1 {
			noun = "finding"
		}
		msgs = append(msgs, fmt.Sprintf("the reviewer returned verdict '%s' alongside %d actionable %s", verdict, actionable, noun))
	}
	warns, err := l.applyPassLabels(ctx, req, loaded, pass, next)
	msgs = append(msgs, warns...)
	if err != nil {
		return marker, msgs, err
	}

	if blocked, ok := marker.BlockedReason.Get(); ok && blocked != "" && verdict == core.VerdictBlocked {
		msgs = append(msgs, "verdict: blocked — "+blocked)
	} else {
		msgs = append(msgs, "verdict: "+string(verdict))
	}
	if next == policy.PassAwaitingResolution {
		msgs = append(msgs,
			"Nothing was changed in your working tree. To act on these:",
			fmt.Sprintf("  crossrev resolve --pr %d", req.PR),
		)
	}
	return marker, msgs, nil
}

func (l *Leg) editClaim(ctx context.Context, repo core.Slug, claimID int64, body string, marker prstate.Marker) error {
	raw, err := marker.MarshalJSON()
	if err != nil {
		return err
	}
	encoded, err := prstate.EncodeMarker(raw)
	if err != nil {
		return err
	}
	if err := l.Forge.CommentEdit(ctx, repo, claimID, body+encoded); err != nil {
		return &ui.FatalError{
			Reason: fmt.Sprintf("could not update comment %d on %s", claimID, repo),
			Action: "The pass marker lives in that comment, so leaving it stale would misreport what happened. Retry, or check the token's permissions.",
		}
	}
	return nil
}

func attachThreads(raw json.RawMessage, threads []forge.ReviewThread) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return raw
	}
	var findings []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &findings); err != nil {
		return raw
	}
	for i, f := range findings {
		id, _ := jsonString(f["id"])
		for _, th := range threads {
			if !threadHas(th, id) {
				continue
			}
			if th.ID != "" {
				f["thread_id"] = marshalString(th.ID)
			}
			if th.RootCommentID != 0 {
				f["root_comment_id"] = json.RawMessage(strconv.FormatInt(th.RootCommentID, 10))
			}
			findings[i] = f
			break
		}
	}
	out, err := json.Marshal(findings)
	if err != nil {
		return raw
	}
	return out
}

func threadHas(th forge.ReviewThread, id string) bool {
	for _, fid := range th.FindingIDs {
		if fid.String() == id {
			return true
		}
	}
	return false
}

func escalatedCount(markers []prstate.Marker) int {
	n := 0
	for _, m := range markers {
		if m.Leg != core.LegResolve {
			continue
		}
		var res []struct {
			Resolution string `json:"resolution"`
		}
		if err := m.DecodeResolutions(&res); err != nil {
			continue
		}
		for _, r := range res {
			if r.Resolution == string(core.ResolutionEscalated) {
				n++
			}
		}
	}
	return n
}
