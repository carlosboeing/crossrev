package resolve

import (
	"context"
	"encoding/json"
	"github.com/carlosboeing/crossrev/internal/ui"
	"strconv"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

func (l *Leg) publish(ctx context.Context, s *session, got Result, workdir string) Result {
	marker := got.Marker
	commentID := marker.CommentID()
	keep := true
	settled := false
	defer func() {
		if settled {
			_ = l.Git.RemoveWorktree(ctx, workdir)
			if l.Log != nil {
				// lib/run.sh:2451, then :2458.
				l.Log.Event("worktree", "removed "+workdir)
				l.Log.ClearTranscripts("")
			}
		}
	}()
	fail := func(err error) Result {
		r := wrapErr(err)
		r.Pass = s.pass
		r.Marker = marker
		r.Resolutions = got.Resolutions
		r.Messages = got.Messages
		if marker.State == core.PassStarted {
			l.reportFatal(ctx, s, marker, r.Err.Error(), workdir, keep)
		}
		return r
	}

	if resolutionCount(marker) == 0 {
		marker = l.attachPayload(marker, got)
		body, err := l.encodeClaim(marker, passHeading(s), "Resolutions recorded; committing and replying now.")
		if err != nil {
			return fail(err)
		}
		if err := l.Forge.CommentEdit(ctx, s.repo, commentID, body); err != nil {
			return fail(err)
		}
	} else {
		if summary, ok := marker.Summary.Get(); !ok || summary == "" {
			if wrap := wrapUpFromRaw(marker); wrap != "" {
				marker.Summary = prstate.Some(wrap)
			}
		}
	}
	got.Marker = marker
	got.Resolutions = marker.Resolutions

	if err := l.assertDiverged(s, marker); err != nil {
		return fail(err)
	}

	remote, err := l.pushRemote(ctx, s.pr.HeadRefName)
	if err != nil {
		return fail(err)
	}

	recs := unmarshalResolutions(marker.Resolutions)
	findings := s.findings
	sha, _ := marker.HeadSHA.Get()
	filed, matched, wrote, deferredLines, recs, persistMessages := l.persistDeferred(ctx, s, workdir, recs, findings, sha)
	got.Messages = append(got.Messages, persistMessages...)
	marker.Resolutions = marshalResolutions(recs)
	got.Resolutions = marker.Resolutions

	commitSHA, msgs, emptyRemote, err := l.commitAndPush(ctx, s, workdir, recs, findings, marker, wrote, remote)
	got.Messages = append(got.Messages, msgs...)
	if emptyRemote {
		// ui_warn, the pair kept apart (lib/run.sh:2385-2386).
		got.Messages = append(got.Messages, ui.Warn(
			"could not read "+s.pr.HeadRefName+" on "+remote+", so the check for a concurrent push did not run",
			"If someone pushed to that branch while this leg was working, this push may not include their commit. Confirm the branch looks right before merging."))
	}
	if err != nil {
		return fail(err)
	}
	if commitSHA != "" {
		marker.CommitSHA = prstate.Some(commitSHA)
		short := commitSHA
		if len(short) > 7 {
			short = short[:7]
		}
		body, encErr := l.encodeClaim(marker, passHeading(s), "Pushed `"+short+"`; replying to each thread now.")
		if encErr != nil {
			return fail(encErr)
		}
		if err := l.Forge.CommentEdit(ctx, s.repo, commentID, body); err != nil {
			return fail(err)
		}
	}

	already := l.postedFindingIDs(ctx, s)
	if s.redriving {
		already = excludeCurrentFindings(already, findings)
	}
	unthreadedIDs := l.unthreadedFindingIDs(ctx, s)
	unthreaded := len(unthreadedIDs)

	threads := l.Forge.ReviewThreads(ctx, s.repo, s.req.PR)
	resolvedN, escalated, unthreaded, findingsRaw, replyMessages := l.replyAndResolve(ctx, s, recs, findings, threads, commitSHA, already, unthreaded)
	got.Messages = append(got.Messages, replyMessages...)

	// The three counts the pass reports, in the shell's order and with the
	// shell's helper at each (lib/run.sh:2375-2377). A filing and a resolved
	// thread are verified successes; a match is a statement of fact, so it is
	// ui_say and not ui_ok.
	if filed > 0 {
		got.Messages = append(got.Messages, ui.OK("filed "+strconv.Itoa(filed)+" issue(s) for deferred work"))
	}
	if matched > 0 {
		got.Messages = append(got.Messages, ui.Say(strconv.Itoa(matched)+" deferred finding(s) already had an issue, so nothing was filed for them."))
	}
	if resolvedN > 0 {
		got.Messages = append(got.Messages, ui.OK("resolved "+strconv.Itoa(resolvedN)+" thread(s)"))
	}
	if unthreaded > 0 {
		noun := "replies"
		if unthreaded == 1 {
			noun = "reply"
		}
		got.Messages = append(got.Messages, ui.Warn(
			strconv.Itoa(unthreaded)+" "+noun+" could not be threaded and landed as top-level comments",
			"Each one names the finding it answers, so nothing is lost, but a reader following the diff will not see it beside the code.",
		))
	}

	reviewBody := reviewSummaryBody(findingsRaw, s.review, s.repo, s.minFix, s.maxPasses)
	updated := s.review
	updated.Findings = findingsRaw
	encodedReview, err := updated.Encode()
	if err != nil {
		return fail(err)
	}
	if err := l.Forge.CommentEdit(ctx, s.repo, s.review.CommentID(), reviewBody+encodedReview); err != nil {
		return fail(err)
	}

	now := l.now().Unix()
	marker.DoneTS = prstate.Some(now)
	marker.Unthreaded = prstate.Some(unthreaded)
	marker.Resolutions = marshalResolutions(recs)
	summary := ResolveSummaryBody(marker.Resolutions, findingsRaw, deferredLines, marker, s.repo.String(), s.req.PR, s.maxPasses)
	encoded, err := marker.Encode()
	if err != nil {
		return fail(err)
	}
	if err := l.Forge.CommentEdit(ctx, s.repo, commentID, summary+encoded); err != nil {
		return fail(err)
	}
	// ui_ok (lib/run.sh:2408).
	got.Messages = append(got.Messages, ui.OK("posted a summary comment"))

	marker.State = core.PassComplete
	encoded, err = marker.Encode()
	if err != nil {
		return fail(err)
	}
	if err := l.Forge.CommentEdit(ctx, s.repo, commentID, summary+encoded); err != nil {
		return fail(err)
	}
	settled = true

	other := otherEscalated(s.markers, s.pass)
	next := policy.ResolvePassLabel(asPolicyResolve(marker), other)
	if err := l.applyPassLabels(ctx, s, s.pass, next); err != nil {
		// ui_warn: applyPassLabels answers the pair already joined by addLabel.
		got.Messages = append(got.Messages, ui.Say(err.Error()))
	}
	if escalated > 0 {
		_ = l.addLabel(ctx, s, policy.LabelStop)
		// ui_say (lib/run.sh:2428).
		got.Messages = append(got.Messages, ui.Say(strconv.Itoa(escalated)+" finding(s) need a human decision, so crossrev/stop is applied and the loop halts."))
	}

	got.Messages = append(got.Messages, closingReport(marker, next, s.pass, s.req.PR)...)

	got.Outcome = OutcomeComplete
	got.Marker = marker
	got.Resolutions = marker.Resolutions
	got.Pass = s.pass
	keep = false
	return got
}

// closingReport is the block the leg ends on (lib/run.sh:2431-2446).
//
// The first line is a bare printf carrying an arrow and a trailing blank line,
// and each of the three endings below it is a ui_say followed by its own blank.
// Only one ending can apply, because they are branches of the same `if`.
func closingReport(marker prstate.Marker, next policy.PassLabelState, pass, pr int) []ui.Line {
	blocked, _ := marker.Blocked.Get()
	if blocked {
		reason, _ := marker.BlockedReason.Get()
		return []ui.Line{ui.Say("→ blocked: " + reason), ui.Blank()}
	}
	out := []ui.Line{ui.Say("→ resolved pass " + strconv.Itoa(pass)), ui.Blank()}
	switch {
	case next == policy.PassAwaitingReview:
		out = append(out,
			ui.Say("To look again with the reviewer:"),
			ui.Say("  crossrev review --pr "+strconv.Itoa(pr)),
			ui.Blank())
	case next == policy.PassConverged:
		out = append(out,
			ui.Say("Every finding settled without a change to the code, so the loop is done."),
			ui.Blank())
	case next == policy.PassHalted && policy.ResolveUnpushedFix(asPolicyResolve(marker)):
		out = append(out,
			ui.Say("A claimed fix reached no commit, so its thread stays open and the pass halts."),
			ui.Blank())
	}
	return out
}

func (l *Leg) attachPayload(marker prstate.Marker, got Result) prstate.Marker {
	marker.Resolutions = got.Resolutions
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(got.Envelope.Payload, &doc)
	if s := jsonString(doc["summary"]); s != "" {
		marker.Summary = prstate.Some(s)
	}
	if b, ok := asBool(doc["blocked"]); ok {
		marker.Blocked = prstate.Some(b)
	}
	if br := jsonString(doc["blocked_reason"]); br != "" && br != "null" {
		marker.BlockedReason = prstate.Some(br)
	} else {
		marker.BlockedReason = prstate.Null[string]()
	}
	if cs := jsonString(doc["commit_subject"]); cs != "" && cs != "null" {
		marker.CommitSubject = prstate.Some(cs)
	} else if raw, ok := doc["commit_subject"]; ok && string(raw) != "null" && len(raw) > 0 {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if s == "" {
				marker.CommitSubject = prstate.Some("")
			}
		}
	}
	if got.Envelope.ModelReported != nil && *got.Envelope.ModelReported != "" && *got.Envelope.ModelReported != "null" {
		marker.ModelReported = prstate.Some(*got.Envelope.ModelReported)
	}
	// Bash assigns the key unconditionally (lib/run.sh:2111), so the marker
	// always holds effort_reported, null when the harness reported none.
	// omitzero drops an unset Opt entirely, which is different bytes.
	marker.EffortReported = prstate.Null[string]()
	if got.Envelope.EffortReported != nil && *got.Envelope.EffortReported != "" && *got.Envelope.EffortReported != "null" {
		marker.EffortReported = prstate.Some(*got.Envelope.EffortReported)
	}
	if got.Envelope.Tokens != nil {
		b, _ := json.Marshal(*got.Envelope.Tokens)
		marker.Tokens = b
	}
	marker.Billing = prstate.Null[string]()
	// document() rather than l.Harness: a Leg built without a descriptor falls
	// back to the embedded one, and invoke already takes that path. Reading
	// l.Harness directly hands BillingFor a zero-value document, which answers
	// "" for every harness, so the marker went back to billing:null — the
	// defect this line exists to close.
	descriptor, descErr := l.document()
	if descErr != nil {
		descriptor = harness.Document{}
	}
	billing := harness.BillingFor(descriptor, marker.Harness.Value(), marker.Endpoint.Value(), lookupEnv(l.Env, "ANTHROPIC_API_KEY") != "")
	if billing != "" {
		marker.Billing = prstate.Some(billing)
	}
	if got.Envelope.Usage != nil {
		usage := *got.Envelope.Usage
		if table, err := harness.PriceTable(); err == nil {
			usage = table.Attach(usage, harness.Document{}, got.Envelope.Harness, "", "", false)
		}
		b, _ := json.Marshal(usage)
		marker.Usage = b
	}
	return marker
}

func asBool(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 {
		return false, false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b, true
	}
	return false, false
}

func wrapUpFromRaw(m prstate.Marker) string {
	raw := m.Raw()
	if len(raw) == 0 {
		return ""
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	return jsonString(fields["wrap_up"])
}

func (l *Leg) assertDiverged(s *session, marker prstate.Marker) error {
	reviewH, _ := s.review.Harness.Get()
	reviewEP, _ := s.review.Endpoint.Get()
	reviewM, _ := s.review.Model.Get()
	configured := policy.ConfiguredDifference(
		policy.LegSettings{Harness: core.HarnessName(reviewH), Endpoint: reviewEP, Model: reviewM},
		policy.LegSettings{Harness: core.HarnessName(s.settings.Harness), Endpoint: s.settings.Endpoint, Model: s.settings.Model},
	)
	reviewReported, _ := s.review.ModelReported.Get()
	resolveReported, _ := marker.ModelReported.Get()
	return policy.AssertModelsDiverged(configured, reviewReported, resolveReported)
}

func otherEscalated(markers []prstate.Marker, pass int) int {
	n := 0
	for _, m := range markers {
		if m.Leg != core.LegResolve || m.Pass == pass {
			continue
		}
		var recs []struct {
			Resolution string `json:"resolution"`
		}
		_ = m.DecodeResolutions(&recs)
		for _, r := range recs {
			if r.Resolution == string(core.ResolutionEscalated) {
				n++
			}
		}
	}
	return n
}

func (l *Leg) finishEmpty(ctx context.Context, s *session, got Result) Result {
	next := policy.PassConverged
	if got.Outcome == OutcomeHalted {
		next = policy.PassHalted
	}
	_ = l.applyPassLabels(ctx, s, s.pass, next)
	return got
}
