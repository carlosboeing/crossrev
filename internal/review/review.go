package review

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// Run loads context, admits the pass, posts the claim, then invokes the reviewer.
func (l *Leg) Run(ctx context.Context, req Request) Result {
	var out Result
	if req.PR == 0 {
		out.Outcome = OutcomeError
		out.Err = &ui.FatalError{
			Reason: "crossrev review needs a pull request number",
			Action: "Usage: crossrev review --pr 42",
		}
		return out
	}
	if req.Trigger == "" {
		req.Trigger = TriggerHuman
	}
	if req.Trigger != TriggerHuman && req.Trigger != TriggerAutomatic {
		out.Outcome = OutcomeError
		out.Err = &ui.FatalError{
			Reason: fmt.Sprintf("unknown review trigger: %s", req.Trigger),
			Action: "Use --trigger human or --trigger automatic.",
		}
		return out
	}

	loaded, skip, err := l.loadContext(ctx, req)
	out.Context = loaded
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return out
	}
	if skip != "" {
		out.Outcome = OutcomeSkipped
		out.Reason = skip
		out.Messages = []string{
			fmt.Sprintf("%s#%d is a draft pull request, so an automatic invocation does not review it.", loaded.Repo, req.PR),
			"Mark it ready for review, or ask for a review explicitly.",
		}
		return out
	}

	if l.Log != nil {
		l.Log.SetLeg("review")
		l.Log.Event("leg", fmt.Sprintf("review trigger=%s mode=%s head=%s", req.Trigger, loaded.Config.Get(".mode"), loaded.PR.HeadRefOid.Short()))
	}

	if hasStop(loaded.PR) {
		out.Outcome = OutcomeSkipped
		out.Reason = "crossrev/stop"
		out.Messages = []string{
			fmt.Sprintf("crossrev/stop is on %s#%d, so this run stops without reviewing.", loaded.Repo, req.PR),
			"Remove the label to let the loop continue.",
		}
		return out
	}

	ad, err := l.admit(ctx, req, loaded)
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return out
	}
	out.Pass = ad.pass
	if ad.already {
		out.Outcome = OutcomeSkipped
		out.Reason = "already reviewed"
		out.Messages = []string{
			fmt.Sprintf("%s#%d is already reviewed at %s — pass %d, and nothing has changed since.",
				loaded.Repo, req.PR, loaded.PR.HeadRefOid.Short(), ad.pass),
			fmt.Sprintf("Push a revision, or run: crossrev resolve --pr %d", req.PR),
		}
		return out
	}
	if ad.decline != "" {
		out.Outcome = OutcomeDeclined
		out.Reason = ad.decline
		out.Messages = []string{fmt.Sprintf("not reviewing %s#%d — %s", loaded.Repo, req.PR, ad.decline)}
		if err := l.postDeclined(ctx, req, loaded, ad); err != nil {
			out.Outcome = OutcomeError
			out.Err = err
		}
		return out
	}
	if ad.stale != "" {
		out.Reason = "abandoning the unfinished pass-" + fmt.Sprint(ad.pass) + " review — " + ad.stale
		out.Messages = append(out.Messages, out.Reason)
	}
	if ad.redrive {
		msg := fmt.Sprintf("Pass %d's review ended blocked — driving pass %d again.", ad.pass, ad.pass)
		out.Reason = msg
		out.Messages = append(out.Messages, msg)
	}

	settings, err := l.settings(req, loaded)
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return out
	}

	marker, claimID, err := l.postClaim(ctx, req, loaded, ad, settings)
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return out
	}
	out.ClaimID = claimID
	out.Marker = marker

	if hasRecordedFindings(marker) {
		out.Outcome = OutcomeInvoked
		out.Messages = append(out.Messages, "The previous attempt already recorded its findings, so the review is not run again.")
		return out
	}

	envelope, payload, err := l.invoke(ctx, req, loaded, settings, ad.pass)
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return out
	}
	out.Envelope = &envelope
	out.Payload = payload
	out.Outcome = OutcomeInvoked

	workdir := req.Workdir
	diffBytes, _ := l.Forge.PullRequestDiff(ctx, loaded.Repo, loaded.PR.BaseRefOid, loaded.PR.HeadRefOid)
	findings, err := enrichFindings(payload, diffBytes, workdir)
	if err == nil {
		marker.Findings = findings
	}
	var doc struct {
		Verdict string `json:"verdict"`
	}
	if json.Unmarshal(payload, &doc) == nil && doc.Verdict != "" {
		marker.Verdict = prstate.Some(doc.Verdict)
	}
	if envelope.ModelReported != nil {
		marker.ModelReported = prstate.Some(*envelope.ModelReported)
	}
	out.Marker = marker
	cap := atoi(loaded.Config.Get(".policy.max_passes_per_cycle"))
	raw, err := marker.MarshalJSON()
	if err == nil && claimID != 0 {
		body := fmt.Sprintf("**crossrev — reviewing, %s**\n\nFindings recorded; posting them now.%s",
			PassLabel(ad.pass, cap), mustEncode(raw))
		_ = l.Forge.CommentEdit(ctx, loaded.Repo, claimID, body)
	}
	return out
}

func hasRecordedFindings(m prstate.Marker) bool {
	if !m.Verdict.Present() || m.Verdict.IsNull() || m.Verdict.Value() == "" {
		return false
	}
	if len(m.Findings) == 0 || string(m.Findings) == "null" || string(m.Findings) == "[]" {
		return false
	}
	return true
}
