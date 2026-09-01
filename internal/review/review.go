package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// Run loads context, admits the pass, posts the claim, invokes the reviewer,
// then publishes findings and completes the original claim.
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
	if ad.warning != "" {
		out.Messages = append(out.Messages, ad.warning)
	}
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
		out.Messages = append(out.Messages, out.Reason+"\n   Resuming it would reconcile against findings that no longer describe this code. Starting the pass again instead.")
	}
	if ad.redrive {
		msg := fmt.Sprintf("Pass %d's review ended blocked — driving pass %d again.", ad.pass, ad.pass)
		out.Reason = msg
		out.Messages = append(out.Messages, msg)
	}

	settings, warn, err := l.settings(req, loaded)
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return out
	}
	if warn != "" {
		out.Messages = append(out.Messages, warn)
	}

	marker, claimID, err := l.postClaim(ctx, req, loaded, ad, settings)
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return out
	}
	out.ClaimID = claimID
	out.Marker = marker
	if ad.recovering && !ad.redrive {
		out.Messages = append(out.Messages, resumeMessage(ad.pass, marker.Findings))
	}

	if hasRecordedFindings(marker) {
		out.Messages = append(out.Messages, "The previous attempt already recorded its findings, so the review is not run again.")
	} else {
		envelope, payload, err := l.invoke(ctx, req, loaded, settings, ad.pass)
		if err != nil {
			var restoreErr *sandboxRestoreFailure
			if errors.As(err, &restoreErr) {
				out.Messages = append(out.Messages, restoreErr.Warning())
			}
			out.Outcome = OutcomeError
			out.Err = err
			return out
		}
		out.Envelope = &envelope
		out.Payload = payload

		workdir := req.Workdir
		diffBytes, _ := l.reviewDiff(ctx, loaded)
		findings, snaps, err := enrichFindings(payload, diffBytes, workdir)
		if err == nil {
			marker.Findings = findings
		}
		out.Messages = append(out.Messages, snaps...)
		var doc struct {
			Verdict       string  `json:"verdict"`
			BlockedReason *string `json:"blocked_reason"`
		}
		if json.Unmarshal(payload, &doc) == nil {
			if doc.Verdict != "" {
				marker.Verdict = prstate.Some(doc.Verdict)
			}
			if doc.BlockedReason != nil && *doc.BlockedReason != "" {
				marker.BlockedReason = prstate.Some(*doc.BlockedReason)
			} else {
				marker.BlockedReason = prstate.Null[string]()
			}
		}
		if envelope.ModelReported != nil && *envelope.ModelReported != "" {
			marker.ModelReported = prstate.Some(*envelope.ModelReported)
		}
		if envelope.EffortReported != nil && *envelope.EffortReported != "" {
			marker.EffortReported = prstate.Some(*envelope.EffortReported)
		} else {
			marker.EffortReported = prstate.Null[string]()
		}
		l.attachUsage(&marker, envelope, settings)
		out.Marker = marker
		cap := atoi(loaded.Config.Get(".policy.max_passes_per_cycle"))
		raw, err := marker.MarshalJSON()
		if err != nil {
			out.Outcome = OutcomeError
			out.Err = err
			return out
		}
		body := fmt.Sprintf("**crossrev — reviewing, %s**\n\nFindings recorded; posting them now.%s",
			PassLabel(ad.pass, cap), mustEncode(raw))
		if err := l.Forge.CommentEdit(ctx, loaded.Repo, claimID, body); err != nil {
			out.Outcome = OutcomeError
			out.Err = err
			return out
		}
	}

	marker, pubMsgs, err := l.publish(ctx, req, loaded, settings, ad.pass, claimID, marker)
	out.Messages = append(out.Messages, pubMsgs...)
	out.Marker = marker
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return out
	}
	out.Outcome = OutcomeInvoked
	return out
}

func (l *Leg) attachUsage(marker *prstate.Marker, envelope harness.Envelope, settings legSettings) {
	anthropic := envValueSet(l.Env, "ANTHROPIC_API_KEY")
	billing := harness.BillingFor(l.Harness, settings.harness, settings.endpoint, anthropic)
	if billing != "" {
		marker.Billing = prstate.Some(billing)
	} else {
		marker.Billing = prstate.Null[string]()
	}
	if envelope.Usage == nil {
		marker.Usage = json.RawMessage("null")
		if envelope.Tokens != nil {
			marker.Tokens = json.RawMessage(strconv.FormatInt(*envelope.Tokens, 10))
		} else {
			marker.Tokens = json.RawMessage("null")
		}
		return
	}
	u := *envelope.Usage
	if prices, err := harness.PriceTable(); err == nil {
		model := settings.model
		if envelope.ModelReported != nil && *envelope.ModelReported != "" {
			model = *envelope.ModelReported
		}
		u = prices.Attach(u, l.Harness, settings.harness, settings.endpoint, model, anthropic)
	}
	if raw, err := json.Marshal(u); err == nil {
		marker.Usage = raw
	}
	if u.Total != nil {
		marker.Tokens = json.RawMessage(strconv.FormatInt(*u.Total, 10))
	} else if envelope.Tokens != nil {
		marker.Tokens = json.RawMessage(strconv.FormatInt(*envelope.Tokens, 10))
	} else {
		marker.Tokens = json.RawMessage("null")
	}
}

func envValueSet(env []string, name string) bool {
	prefix := name + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) && len(e) > len(prefix) {
			return true
		}
	}
	return false
}
