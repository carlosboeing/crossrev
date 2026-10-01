package review

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/verify"
)

// publishState is what the caller has to know beyond the marker and the lines.
//
// settled is run_leg_settled (lib/run.sh:160-163): from the instant the
// complete edit lands, nothing may rewrite the claim as blocked, because the
// record on the pull request is accurate.
//
// nudge is the upgrade tip's half of the `if` at lib/run.sh:1325-1330. The leg
// holds no terminal, so the decision travels and the composition root prints.
type publishState struct {
	settled bool
	nudge   bool
}

// The state is whether the complete edit landed, which is run_leg_settled
// (lib/run.sh:160-163): from that instant nothing may rewrite the claim as
// blocked, because the record on the pull request is accurate.
func (l *Leg) publish(ctx context.Context, req Request, loaded Context, settings legSettings, pass int, claimID int64, marker prstate.Marker) (prstate.Marker, []ui.Line, publishState, error) {
	var msgs []ui.Line
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
		// Three ui_say lines (lib/run.sh:1234-1236).
		msgs = append(msgs, ui.SayLines(
			fmt.Sprintf("Found %d issue(s) — %d high, %d medium, %d low, of which %d pre-existing.", len(findings), high, medium, low, pre),
			fmt.Sprintf("%d at or above min_fix_severity (%s); the rest are reported and left alone.", actionable, minFix),
			"Posting them as inline comments on the lines they affect.",
		)...)
	}

	// Passes after the first hold below-threshold findings: they are
	// recorded on the marker with posted:false, skipped by the posting loop
	// below, and counted in the summary. The stamp lands before anything
	// posts, so the marker, the summary and the next pass's priors all read
	// the same record; convergence still counts every finding, held or not.
	marker.Findings = stampNotPosted(marker.Findings, heldEntries(findings, minFix, pass))
	findings = parseFindings(marker.Findings)

	heldEarlier := heldEarlierIDs(loaded.Markers)
	postedThisPass := map[string]bool{}
	posted, skipped := 0, 0
	for _, f := range findings {
		// Within-pass retries post once: a second finding under an id
		// this pass already posted is the same point twice.
		if f.ID != "" && postedThisPass[f.ID] {
			skipped++
			continue
		}
		// An upgraded held finding posts again. It was recorded without
		// posting on an earlier pass and now ranks at or above the bar,
		// so the earlier comment's lower severity must not suppress it;
		// anything never held back stays duplicate-suppressed.
		if already[f.ID] && !(heldEarlier[f.ID] && f.IsPosted()) {
			skipped++
			continue
		}
		if !f.IsPosted() {
			continue
		}
		side := core.SideRight
		if s, err := core.ParseSide(f.Side); err == nil {
			side = s
		}
		body := CommentBody(f, pass, settings.harness, settings.model, minFix)
		placement, err := l.postFinding(ctx, loaded, req, f, side, body)
		if err != nil {
			return marker, msgs, publishState{}, err
		}
		posted++
		if f.ID != "" {
			postedThisPass[f.ID] = true
		}
		if placement == forge.PlacementFallback {
			unanchored++
			msgs = append(msgs, ui.Warn(
				fmt.Sprintf("GitHub would not anchor a comment to %s:%d (%s) on %s#%d", f.Path, f.Line, side, loaded.Repo, req.PR),
				"The finding is posted as a top-level comment naming that location instead, so it is not lost. A finding on a deleted line needs side LEFT.",
			))
		}
	}
	if posted > 0 {
		// ui_ok, not ui_say: the comments are on the pull request (lib/run.sh:1261).
		msgs = append(msgs, ui.OK(fmt.Sprintf("posted %d finding comment(s)", posted)))
	}
	if skipped > 0 {
		msgs = append(msgs, ui.Say(fmt.Sprintf("%d finding(s) were already on the pull request from an earlier attempt, so they were not posted twice.", skipped)))
	}
	if unanchored > 0 {
		noun := "findings"
		if unanchored == 1 {
			noun = "finding"
		}
		msgs = append(msgs, ui.Warn(
			fmt.Sprintf("%d %s could not be anchored to a line and landed as top-level comments", unanchored, noun),
			"Each one names the location it faults, so nothing is lost, but it sits at the top of the pull request rather than beside the code — and the resolve leg has no thread to reply into either."))
	}

	marker.Findings = attachThreads(marker.Findings, l.Forge.ReviewThreads(ctx, loaded.Repo, req.PR))
	marker.DoneTS = prstate.Some(l.now().Unix())
	marker.Unanchored = prstate.Some(unanchored)

	verdict := core.Verdict(marker.Verdict.Value())
	escalated := escalatedCount(loaded.Markers)
	// Both convergence reads below judge the same settled marker, so they
	// share one producer built from the model the pass stored.
	producer := producerOf(settings, marker.ModelReported.Value())
	conv, ev, obliged := l.buildConvergenceEvidence(ctx, loaded, marker, actionable, producer, settings)
	if obliged && ev.Configured() {
		// The record of what the pass judged, written with the claim
		// below. Absent without a configured gate, so an unconfigured
		// marker reads exactly as it always has.
		marker.Verification = prstate.Some(ev.MarkerRecord())
	}
	if obliged && policy.ConvergedExceptVerification(conv) && (ev.State == verify.Pending || ev.State == verify.Missing) {
		// The route would converge if the checks reported: wait for them,
		// then judge what the wait ended with.
		ev = l.waitForVerification(ctx, req, loaded, settings, loaded.Scope.Head, ev)
		conv.Verification = ev.State
		marker.Verification = prstate.Some(ev.MarkerRecord())
	}
	if obliged && !policy.Converged(conv) {
		// The coverage obligation is unmet: a green verdict cannot stand,
		// and a quiet one cannot pass as finished. With actionable findings
		// the resolve leg is still owed, so the verdict stays issues-remain;
		// with nothing actionable the review could not complete, so it
		// records blocked with the specific debt named.
		if actionable > 0 {
			if verdict == core.VerdictConverged {
				msgs = append(msgs, ui.Warn(
					"the reviewer returned verdict 'converged' with the coverage obligation unmet",
					"The verdict is recorded as issues-remain instead: a green verdict needs every required file covered, no outstanding or unexamined record, a reported scope and any confirmed repair. Nothing here judges the code."))
			}
			verdict = core.VerdictIssuesRemain
			marker.Verdict = prstate.Some(string(verdict))
		} else {
			verdict = core.VerdictBlocked
			marker.Verdict = prstate.Some(string(verdict))
			debt := coverageDebt(conv, ev)
			marker.BlockedReason = prstate.Some(debt)
			if policy.ConvergedExceptVerification(conv) {
				// Only the gate blocks: name the checks and the next step.
				msgs = append(msgs, verificationHaltLines(req.PR, debt)...)
			} else {
				msgs = append(msgs, ui.Warn(
					"the review leaves its coverage obligation unmet with nothing actionable",
					"The pass records blocked instead of finished: "+debt+". Nothing here judges the code."))
			}
		}
	}

	renderCtx := RenderContext{
		Repo:    loaded.Repo.String(),
		PR:      req.PR,
		MinFix:  minFix,
		MaxPass: cap,
	}
	if obliged {
		// The footnote's counts are the pass's own convergence, counted
		// above, so the sentence reads the same on either store. Reused
		// rather than rebuilt: only the verdict moved since, which
		// convergence never reads. The frozen path supplies none and the
		// footnote stays silent. The denominator counts every changed file,
		// exclusions included, and the skips and policy exclusions render
		// beside it.
		renderCtx.Coverage = &CoverageCounts{Covered: conv.Covered, Required: conv.Required, Excluded: len(loaded.Scope.Excluded)}
		renderCtx.Skipped = skipRenderDetails(*loaded.Scope)
		renderCtx.Excluded = policyExclusionPaths(*loaded.Scope)
	}
	summary := SummaryBody(parseFindings(marker.Findings), marker, renderCtx)
	written, err := l.editClaim(ctx, loaded.Repo, claimID, summary, marker, coverageOverflow(loaded))
	if err != nil {
		return marker, msgs, publishState{}, err
	}
	marker = written
	// ui_ok: the comment is on the pull request (lib/run.sh:1299).
	msgs = append(msgs, ui.OK("posted a summary comment"))

	marker.State = core.PassComplete
	written, err = l.editClaim(ctx, loaded.Repo, claimID, summary, marker, coverageOverflow(loaded))
	if err != nil {
		return marker, msgs, publishState{}, err
	}
	marker = written
	// run_leg_settled, immediately after the complete edit lands and not one
	// line later (lib/run.sh:160-163). Everything below can still fail, and
	// none of it may rewrite this record.
	state := publishState{settled: true}

	// The coverage obligation was judged against the scope's base and head,
	// and a push landing during the review retires both. Re-read the pair
	// from the forge before the loop-state label moves — the same freshness
	// check PublishGeneration runs before committing the manifest — and
	// refuse a stale convergence: the label, not the ledger, is what drives
	// the loop.
	// A scope that required nothing published no generation, so there is no
	// convergence to go stale and nothing to re-read for. Binding the scope
	// on every successful enumeration is what arms the coverage gates above;
	// it must not also buy a second pull request read on a pass that has no
	// coverage to protect (forge.PullRequest is one read on purpose).
	if loaded.Scope != nil && len(loaded.Scope.Required) > 0 {
		current, err := l.Forge.PullRequest(ctx, loaded.Repo, req.PR)
		if err != nil {
			return marker, msgs, state, err
		}
		if current.BaseRefOid.SHA() != loaded.Scope.Base.SHA() || current.HeadRefOid.SHA() != loaded.Scope.Head.SHA() {
			return marker, msgs, state, fmt.Errorf("the base or head moved during the review; not applying a loop label for the old revision %s", loaded.Scope.Head.Short())
		}
	}

	next := policy.PassLabel(verdict, actionable, escalated)
	if conv, ok := l.buildCoverageConvergence(ctx, loaded, marker, actionable, producer); ok {
		// The pass's one evidence, carried forward: the label judges the
		// same gate the marker records.
		conv.Verification = ev.State
		next = policy.PassLabelWithCoverage(verdict, actionable, escalated, conv)
	}
	if verdict == core.VerdictConverged && next != policy.PassConverged {
		noun := "findings"
		if actionable == 1 {
			noun = "finding"
		}
		msgs = append(msgs, ui.Warn(
			fmt.Sprintf("the reviewer returned verdict '%s' alongside %d actionable %s", verdict, actionable, noun),
			fmt.Sprintf("The actionable count outranks the verdict, so the pass is labelled '%s' to run the resolve leg.", next)))
	}
	warns, err := l.applyPassLabels(ctx, req, loaded, pass, next)
	msgs = append(msgs, warns...)
	if err != nil {
		return marker, msgs, state, err
	}

	// Two bare printfs, each with a trailing blank line
	// (lib/run.sh:1321-1323, :1319-1322).
	if blocked, ok := marker.BlockedReason.Get(); ok && blocked != "" && verdict == core.VerdictBlocked {
		msgs = append(msgs, ui.Say("→ verdict: blocked — "+blocked), ui.Blank())
	} else {
		msgs = append(msgs, ui.Say("→ verdict: "+string(verdict)), ui.Blank())
	}
	if next == policy.PassAwaitingResolution {
		msgs = append(msgs, ui.Say("Nothing was changed in your working tree. To act on these:"),
			ui.Say(fmt.Sprintf("  crossrev resolve --pr %d", req.PR)),
			ui.Blank())
	} else {
		// The other arm of the same `if`, so a pass gets the handover or the
		// tip and never both (lib/run.sh:1325-1330).
		state.nudge = true
	}
	return marker, msgs, state, nil
}

// coverageDebt names the specific unmet obligation for a blocked pass: the
// coverage counts, the missing scope report, the unconfirmed repair, or the
// blocking required checks. Coverage reads first: a pass that could not
// cover its files re-drives however the checks report.
func coverageDebt(conv policy.Convergence, ev verify.Evidence) string {
	switch {
	case !conv.LedgerCurrent:
		return "no current coverage generation at this revision"
	case conv.Outstanding > 0:
		return fmt.Sprintf("%d required file(s) left outstanding", conv.Outstanding)
	case conv.CouldNotReview > 0:
		return fmt.Sprintf("%d required file(s) could not be examined", conv.CouldNotReview)
	case conv.Covered != conv.Required:
		return fmt.Sprintf("%d of %d required files covered", conv.Covered, conv.Required)
	case !conv.ScopeReported:
		return "the reviewer reported no examined scope"
	case conv.ConfirmationRequired && !conv.ConfirmationComplete:
		return "the repair delta has no accepted confirmation"
	case ev.State == verify.Pending || ev.State == verify.Missing || ev.State == verify.Failed || ev.State == verify.Unreadable:
		return verificationDebt(ev)
	default:
		return "the coverage obligation is unmet"
	}
}

// postFinding posts one finding where its anchor kind says: a hunk line goes
// inline, a required file with no valid hunk line lands a file-level review
// comment, and an outside-diff path posts top-level with its finding id. The
// finding id travels in every shape, so resolution identity never needs a
// thread.
func (l *Leg) postFinding(ctx context.Context, loaded Context, req Request, f Finding, side core.Side, body string) (forge.Placement, error) {
	switch AnchorKind(f.AnchorKind) {
	case AnchorFile:
		return l.Forge.ReviewFileComment(ctx, forge.ReviewComment{
			Repo:   loaded.Repo,
			Number: req.PR,
			Commit: loaded.PR.HeadRefOid,
			Path:   f.Path,
			Line:   f.Line,
			Side:   side,
			Body:   body,
		})
	case AnchorOutsideDiff:
		if _, err := l.Forge.CommentCreate(ctx, loaded.Repo, req.PR, outsideDiffBody(f, body)); err != nil {
			return "", err
		}
		return forge.PlacementFallback, nil
	default:
		return l.Forge.ReviewCommentCreate(ctx, forge.ReviewComment{
			Repo:   loaded.Repo,
			Number: req.PR,
			Commit: loaded.PR.HeadRefOid,
			Path:   f.Path,
			Line:   f.Line,
			Side:   side,
			Body:   body,
		})
	}
}

// outsideDiffBody renders an outside-diff finding as a top-level comment
// that keeps its finding id: the resolve leg matches the id, not a thread.
func outsideDiffBody(f Finding, body string) string {
	return fmt.Sprintf("**%s** — outside the changed files (`%s`).\n\n%s", f.ID, f.Path, body)
}

// coverageOverflow answers the retention policy in force for marker writes:
// degrade unless the operator configured halt. A missing config degrades,
// the way an absent value reads as the default everywhere else.
func coverageOverflow(loaded Context) string {
	if loaded.Config == nil {
		return prstate.OverflowDegrade
	}
	return loaded.Config.Coverage().OnOverflow
}

// editClaim writes body with the marker attached, applying the retention
// ladder when the rendered comment does not fit: the predecessor goes
// first, then the current generation compacts under degrade, and only a
// comment that cannot hold even one compact generation refuses with
// LedgerExhausted. It answers the marker as written, so the caller records
// what shedding gave up; on any failure it answers the marker as it was,
// because nothing was written and the last write still stands.
func (l *Leg) editClaim(ctx context.Context, repo core.Slug, claimID int64, body string, marker prstate.Marker, onOverflow string) (prstate.Marker, error) {
	original := marker
	render := func(m prstate.Marker) (string, error) {
		encoded, err := m.Encode()
		if err != nil {
			return "", err
		}
		return body + encoded, nil
	}
	shed, err := prstate.ShedToFit(&marker, render, onOverflow)
	if err != nil {
		return original, err
	}
	if shed.Degraded {
		marker.CoverageDegraded = prstate.Some(true)
	}
	final, err := render(marker)
	if err != nil {
		return original, err
	}
	if err := prstate.FitMarkerComment(final); err != nil {
		return original, err
	}
	if err := l.Forge.CommentEdit(ctx, repo, claimID, final); err != nil {
		return original, &ui.FatalError{
			Reason: fmt.Sprintf("could not update comment %d on %s", claimID, repo),
			Action: "The pass marker lives in that comment, so leaving it stale would misreport what happened. Retry, or check the token's permissions.",
		}
	}
	return marker, nil
}

// heldEntries marks, per finding entry, whether the pass holds it back:
// below min_fix_severity on a pass after the first. The decision is per
// entry, not per id: ids carry path, title and anchor but no severity, so
// two entries under one id at mixed severities hold only the
// below-threshold one and the actionable entry still posts. Findings
// without an id are never held, so a payload the enricher never minted
// cannot suppress its siblings.
func heldEntries(findings []Finding, minFix string, pass int) []bool {
	held := make([]bool, len(findings))
	for i, f := range findings {
		if f.ID == "" {
			continue
		}
		held[i] = holdBelowThreshold(f, minFix, pass)
	}
	return held
}

// heldEarlierIDs names the findings whose latest review-marker occurrence
// was recorded without posting: explicit posted:false. Finding ids carry no
// severity, so a finding posted low on pass 1, held on pass 2 and raised to
// medium on pass 3 keeps its id — and the pass-1 comment would suppress the
// upgrade as already posted. These ids are the exception: raised back at or
// above the bar, they post again. Only the latest occurrence counts, so
// once the upgrade posts, reporting it again is a duplicate like any other
// and suppression resumes. Within one marker a posted occurrence wins over
// a held one under the same id: the point reached the pull request on that
// pass, whatever else the pass recorded beside it.
func heldEarlierIDs(markers []prstate.Marker) map[string]bool {
	// Markers read oldest first, so the last write per id is its latest
	// occurrence. Within one marker a posted occurrence wins over a held
	// one under the same id.
	held := map[string]bool{}
	for _, m := range markers {
		if m.Leg != core.LegReview {
			continue
		}
		var findings []Finding
		if err := m.DecodeFindings(&findings); err != nil {
			continue
		}
		postedHere := map[string]bool{}
		heldHere := map[string]bool{}
		for _, f := range findings {
			if f.ID == "" {
				continue
			}
			if f.IsPosted() {
				postedHere[f.ID] = true
			} else {
				heldHere[f.ID] = true
			}
		}
		for id := range postedHere {
			held[id] = false
		}
		for id := range heldHere {
			if !postedHere[id] {
				held[id] = true
			}
		}
	}
	out := map[string]bool{}
	for id, h := range held {
		if h {
			out[id] = true
		}
	}
	return out
}

// stampNotPosted records the held findings with posted:false, keeping every
// other byte of the marker as the enricher wrote it: the edit goes through
// the order-preserving node rather than a struct round-trip, so a posted
// finding encodes exactly as it always has and an older reader still reads
// the record. The decisions are index-aligned with the parsed findings the
// caller decided on; a length mismatch means the record moved underfoot,
// so it stays untouched rather than stamping the wrong entry.
func stampNotPosted(raw json.RawMessage, held []bool) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return raw
	}
	var findings []harness.Node
	if err := json.Unmarshal(raw, &findings); err != nil {
		return raw
	}
	if len(held) != len(findings) {
		return raw
	}
	any := false
	for _, h := range held {
		if h {
			any = true
			break
		}
	}
	if !any {
		return raw
	}
	for i := range findings {
		if held[i] {
			findings[i].Set("posted", harness.FromBool(false))
		}
	}
	out, err := json.Marshal(findings)
	if err != nil {
		return raw
	}
	return out
}

// A finding id's current thread is its latest posted comment: held
// entries carry no thread and no resolution, an upgrade posts a new
// comment that becomes the current thread, a re-held id may upgrade
// again, and each resolution lands on the posted occurrence and its
// current thread. Publish, the marker rewrite, the resolver's input and
// every summary follow this rule.
func attachThreads(raw json.RawMessage, threads []forge.ReviewThread) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return raw
	}
	// harness.Node rather than a map: encoding/json sorts a map's keys and jq
	// does not, and these bytes go into the marker. Set replaces a key in
	// place, so thread_id and root_comment_id keep the position enrichment
	// gave them rather than moving to where the alphabet would put them.
	var findings []harness.Node
	if err := json.Unmarshal(raw, &findings); err != nil {
		return raw
	}
	for i := range findings {
		// Held entries carry no thread: with no comment on the pull
		// request there is nothing to reply into, so an older thread
		// under the same id must not attach here.
		if posted := findings[i].Member("posted"); !posted.IsNull() && !posted.Truthy() {
			continue
		}
		id, _ := findings[i].Member("id").AsString()
		// An upgraded re-post shares its id with the spent thread, so
		// the latest posted comment wins: comment ids grow with
		// creation, and the current pass just posted the newest one.
		best := -1
		for j, th := range threads {
			if !threadHas(th, id) {
				continue
			}
			if best == -1 || th.RootCommentID > threads[best].RootCommentID {
				best = j
			}
		}
		if best == -1 {
			continue
		}
		th := threads[best]
		if th.ID != "" {
			findings[i].Set("thread_id", harness.FromString(th.ID))
		}
		if th.RootCommentID != 0 {
			findings[i].Set("root_comment_id", harness.FromInt(th.RootCommentID))
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
