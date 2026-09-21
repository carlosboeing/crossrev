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
		// Two ui_say lines (lib/run.sh:272-273). One message serves both legs,
		// so it names neither: a refused resolve leg was being told that
		// nothing would "review" the pull request, which is the wrong
		// instruction for the leg that was refused.
		out.Messages = ui.SayLines(
			fmt.Sprintf("%s#%d is a draft pull request, so an automatic invocation does not run on it.", loaded.Repo, req.PR),
			"Mark it ready for review, or run the leg yourself.",
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
		// Two ui_say lines (lib/run.sh:971-972).
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
		// Two ui_say lines (lib/run.sh:1011-1012).
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
		// ui_say (lib/run.sh:1036).
		out.Messages = []ui.Line{ui.Say(fmt.Sprintf("not reviewing %s#%d — %s", loaded.Repo, req.PR, ad.decline))}
		if err := l.postDeclined(ctx, req, loaded, ad); err != nil {
			out.Outcome = OutcomeError
			out.Err = err
		}
		return out
	}
	if ad.stale != "" {
		// ui_warn, the pair kept apart (lib/run.sh:982-983).
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
	// before the claim is posted (lib/run.sh:1072-1073):
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

	// lib/run.sh:1092, which the shell prints BELOW the header because the
	// redrive branch sits inside the claim block that follows it.
	if ad.redrive {
		msg := fmt.Sprintf("Pass %d's review ended blocked — driving pass %d again.", ad.pass, ad.pass)
		// ui_say (lib/run.sh:1092).
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

	// The coverage loop, after the claim exists and before any finding is
	// published. Scope is built from the authoritative git history at the
	// current base and head; the initial generation records every uncovered
	// unit as outstanding, so a crash before the first accepted batch leaves
	// the started claim and the same scope to rebuild from. A leg without a
	// git reader keeps the frozen single-prompt path below; the ledger store
	// always answers, falling back to the marker when refs are refused.
	// A git failure enumerating the changed files fails closed instead: the
	// frozen path carries no coverage obligation, so falling through to it
	// would review and converge with no required set at all.
	scope, scopeErr := l.buildScope(ctx, loaded.PR.BaseRefOid, loaded.PR.HeadRefOid, scopeExclusions(loaded.Backlog.Path))
	if scopeErr != nil && !errors.Is(scopeErr, errNoScopeReader{}) {
		out.Outcome = OutcomeError
		out.Err = scopeErr
		return out
	}
	// A successful enumeration binds the pass to its coverage obligation,
	// whatever it counted. loaded.Scope being set is what arms both gates in
	// publication (publish.go:119, :181), and policy.Converged refuses a
	// required count of zero (internal/policy/convergence.go:50) — so from
	// here on a green verdict on an empty set is downgraded rather than
	// believed, and no model answer can put crossrev/converged on a pull
	// request nothing read. Only errNoScopeReader, which no production leg
	// reaches because cmd/crossrev/legs.go:124 always supplies a reader,
	// leaves the obligation unbound.
	if scopeErr == nil {
		loaded.Scope = &scope
	}

	// The pull request that changes no files is settled here without a model
	// at all: there is nothing to send, and the two sources agree on why.
	// When they disagree the leg still runs, because GitHub's count can lag a
	// force-push or a revert and the diff may yet be readable. Such a pass
	// can still report findings; what the binding above denies it is green.
	if scopeErr == nil && len(scope.Required) == 0 && loaded.PR.ChangedFiles == 0 {
		result, state := l.finishNoChangesRun(ctx, req, loaded, ad, cap, claimID, out.Marker, &out)
		settled = state.settled
		return result
	}
	if scopeErr == nil && len(scope.Required) > 0 {
		store, selection, ledgerErr := l.ledgerFor(loaded)
		if ledgerErr != nil {
			out.Outcome = OutcomeError
			out.Err = ledgerErr
			return out
		}
		if covErr := l.runCoverage(ctx, req, loaded, settings, ad.pass, claimID, scope, store, selection, &out); covErr != nil {
			out.Outcome = OutcomeError
			out.Err = covErr
			return out
		}
		if out.Outcome == OutcomeHalted {
			return out
		}
		if covered, ok := out.Covered.(coveredPass); ok {
			out.Covered = nil
			result, state := l.finishCoveredRun(ctx, req, loaded, settings, ad, cap, claimID, out.Marker, covered, &out)
			settled = state.settled
			return result
		}
	}
	if ad.recovering && !ad.redrive {
		// ui_say (lib/run.sh:1098).
		out.Messages = append(out.Messages, ui.Say(resumeMessage(ad.pass, marker.Findings)))
	}

	if hasRecordedFindings(marker) {
		// ui_say (lib/run.sh:1124).
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
		// ui_say, one per finding the anchor moved (lib/run.sh:1179).
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

	marker, pubMsgs, published, err := l.publish(ctx, req, loaded, settings, ad.pass, claimID, marker)
	settled = published.settled
	out.Nudge = published.nudge
	out.Messages = append(out.Messages, pubMsgs...)
	out.Marker = marker
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return out
	}
	// log_transcripts_clear, at the end of leg_review and nowhere earlier
	// (lib/run.sh:1332). A failed leg keeps them: they are the reason the files
	// exist.
	if l.Log != nil {
		l.Log.ClearTranscripts("")
	}
	out.Outcome = OutcomeInvoked
	return out
}

// finishCoveredRun folds a fully covered batch pass into the frozen
// enrich-and-publish path: the batch findings are enriched, anchored and
// published exactly as a single-prompt pass's findings are. The
// confirmation pair is already on the marker, where the publish path's
// convergence check reads it.
func (l *Leg) finishCoveredRun(ctx context.Context, req Request, loaded Context, settings legSettings, ad admission, cap int, claimID int64, marker prstate.Marker, covered coveredPass, out *Result) (result Result, state publishState) {
	out.Marker = marker
	if covered.envelope != nil {
		out.Envelope = covered.envelope
	}
	out.Payload = covered.payload
	marker.Verdict = prstate.Some(covered.verdict)
	marker.BlockedReason = prstate.Null[string]()
	if covered.envelope != nil {
		if covered.envelope.ModelReported != nil && *covered.envelope.ModelReported != "" {
			marker.ModelReported = prstate.Some(*covered.envelope.ModelReported)
		} else {
			marker.ModelReported = prstate.Null[string]()
		}
		if covered.envelope.EffortReported != nil && *covered.envelope.EffortReported != "" {
			marker.EffortReported = prstate.Some(*covered.envelope.EffortReported)
		} else {
			marker.EffortReported = prstate.Null[string]()
		}
		l.attachUsage(&marker, *covered.envelope, settings)
	}
	workdir := req.Workdir
	diffBytes, _ := l.reviewDiff(ctx, loaded)
	enriched, snaps, err := enrichFindingsInScope(covered.payload, diffBytes, workdir, requiredPaths(loaded))
	if err == nil {
		marker.Findings = enriched
	}
	out.Messages = append(out.Messages, ui.SayLines(snaps...)...)
	out.Marker = marker
	published, pubMsgs, state, err := l.publish(ctx, req, loaded, settings, ad.pass, claimID, marker)
	out.Messages = append(out.Messages, pubMsgs...)
	out.Marker = published
	out.Nudge = state.nudge
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return *out, state
	}
	// log_transcripts_clear, at the end of leg_review and nowhere earlier
	// (lib/run.sh:1332). A failed leg keeps them: they are the reason the
	// files exist.
	if l.Log != nil {
		l.Log.ClearTranscripts("")
	}
	out.Outcome = OutcomeInvoked
	return *out, state
}

// requiredPaths reads the current required paths off the loaded scope for
// anchor decisions: a finding on one of these paths with no valid hunk line
// is file-level; anywhere else is outside the diff. Nil scope means the
// frozen path, where the diff alone decides.
func requiredPaths(loaded Context) map[string]bool {
	if loaded.Scope == nil {
		return nil
	}
	out := make(map[string]bool, len(loaded.Scope.Required))
	for _, unit := range loaded.Scope.Required {
		out[unit.Path] = true
	}
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
