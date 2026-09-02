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
func (l *Leg) Run(ctx context.Context, req Request) (out Result) {
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
		// Two ui_say lines (lib/run.sh:260-261).
		out.Messages = ui.SayLines(
			fmt.Sprintf("%s#%d is a draft pull request, so an automatic invocation does not review it.", loaded.Repo, req.PR),
			"Mark it ready for review, or ask for a review explicitly.",
		)
		return out
	}

	if l.Log != nil {
		l.Log.SetLeg("review")
		l.Log.Event("leg", fmt.Sprintf("review trigger=%s mode=%s head=%s", req.Trigger, loaded.Config.Get(".mode"), loaded.PR.HeadRefOid.Short()))
	}

	if hasStop(loaded.PR) {
		out.Outcome = OutcomeSkipped
		out.Reason = "crossrev/stop"
		// Two ui_say lines (lib/run.sh:965-966).
		out.Messages = ui.SayLines(
			fmt.Sprintf("crossrev/stop is on %s#%d, so this run stops without reviewing.", loaded.Repo, req.PR),
			"Remove the label to let the loop continue.",
		)
		return out
	}

	ad, err := l.admit(ctx, req, loaded)
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return out
	}
	out.Pass = ad.pass
	if ad.warning.Text != "" {
		out.Messages = append(out.Messages, ad.warning)
	}
	if ad.already {
		out.Outcome = OutcomeSkipped
		out.Reason = "already reviewed"
		// Two ui_say lines (lib/run.sh:1005-1006).
		out.Messages = ui.SayLines(
			fmt.Sprintf("%s#%d is already reviewed at %s — pass %d, and nothing has changed since.",
				loaded.Repo, req.PR, loaded.PR.HeadRefOid.Short(), ad.pass),
			fmt.Sprintf("Push a revision, or run: crossrev resolve --pr %d", req.PR),
		)
		return out
	}
	if ad.decline != "" {
		out.Outcome = OutcomeDeclined
		out.Reason = ad.decline
		// ui_say (lib/run.sh:1030).
		out.Messages = []ui.Line{ui.Say(fmt.Sprintf("not reviewing %s#%d — %s", loaded.Repo, req.PR, ad.decline))}
		if err := l.postDeclined(ctx, req, loaded, ad); err != nil {
			out.Outcome = OutcomeError
			out.Err = err
		}
		return out
	}
	if ad.stale != "" {
		// ui_warn, the pair kept apart (lib/run.sh:976-977).
		out.Reason = "abandoning the unfinished pass-" + fmt.Sprint(ad.pass) + " review — " + ad.stale
		out.Messages = append(out.Messages, ui.Warn(out.Reason,
			"Resuming it would reconcile against findings that no longer describe this code. Starting the pass again instead."))
	}
	settings, warn, err := l.settings(req, loaded)
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return out
	}
	if warn.Text != "" {
		out.Messages = append(out.Messages, warn)
	}

	// The run header, two bare printfs after the settings are chosen and
	// before the claim is posted (lib/run.sh:1066-1067):
	//
	//	printf '\n  Reviewing %s#%s — %s\n' …
	//	printf '  Reviewer: %s%s%s\n' "$harness" "${model:+, $model}" "${effort:+, $effort effort}"
	//
	// The leading newline is its own line, and the two text lines carry the
	// same two-space prefix ui_say prints, so they are Say lines.
	cap := atoi(loaded.Config.Get(".policy.max_passes_per_cycle"))
	out.Messages = append(out.Messages,
		ui.Blank(),
		ui.Say(fmt.Sprintf("Reviewing %s#%d — %s", loaded.Repo, req.PR, PassLabel(ad.pass, cap))),
		ui.Say("Reviewer: "+settings.describe()),
	)

	// lib/run.sh:1086, which the shell prints BELOW the header because the
	// redrive branch sits inside the claim block that follows it.
	if ad.redrive {
		msg := fmt.Sprintf("Pass %d's review ended blocked — driving pass %d again.", ad.pass, ad.pass)
		// ui_say (lib/run.sh:1086).
		out.Reason = msg
		out.Messages = append(out.Messages, ui.Say(msg))
	}

	marker, claimID, err := l.postClaim(ctx, req, loaded, ad, settings)
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return out
	}
	out.ClaimID = claimID
	out.Marker = marker

	// The EXIT trap, from here on. run_checkpoint snapshots the open leg at
	// every settled point and run_leg_settled clears it, so the report fires on
	// every way out of the leg between the claim landing and the complete edit
	// (lib/run.sh:87-89, :167-179). `settled` is that snapshot.
	//
	// 130 is not a failure: run_checkpoint has already explained the interrupt
	// and the claim it leaves is deliberately resumable, so naming it here would
	// turn every Ctrl-C into a halted pull request (lib/run.sh:141-143). This
	// package cannot see internal/cli's ErrInterrupted — a tier-3 peer — so the
	// cancellation is read off the context, which is where it came from.
	settled := false
	defer func() {
		if settled || out.Err == nil {
			return
		}
		if ctx.Err() != nil || errors.Is(out.Err, context.Canceled) {
			return
		}
		l.reportFatal(ctx, req, loaded, out.Marker, claimID, out.Err)
	}()
	if ad.recovering && !ad.redrive {
		// ui_say (lib/run.sh:1092).
		out.Messages = append(out.Messages, ui.Say(resumeMessage(ad.pass, marker.Findings)))
	}

	if hasRecordedFindings(marker) {
		// ui_say (lib/run.sh:1118).
		out.Messages = append(out.Messages, ui.Say("The previous attempt already recorded its findings, so the review is not run again."))
	} else {
		envelope, payload, invokeMsgs, err := l.invoke(ctx, req, loaded, settings, ad.pass)
		out.Messages = append(out.Messages, invokeMsgs...)
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
		// ui_say, one per finding the anchor moved (lib/run.sh:1173).
		out.Messages = append(out.Messages, ui.SayLines(snaps...)...)
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

	marker, pubMsgs, complete, err := l.publish(ctx, req, loaded, settings, ad.pass, claimID, marker)
	settled = complete
	out.Messages = append(out.Messages, pubMsgs...)
	out.Marker = marker
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return out
	}
	// log_transcripts_clear, at the end of leg_review and nowhere earlier
	// (lib/run.sh:1326). A failed leg keeps them: they are the reason the files
	// exist.
	if l.Log != nil {
		l.Log.ClearTranscripts("")
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
