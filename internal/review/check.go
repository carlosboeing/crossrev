package review

import (
	"context"
	"errors"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cred"
	"github.com/carlosboeing/crossrev/internal/diff"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prompt"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// The check_reason tokens: why the check degraded, was unavailable, or
// ran on the reviewer's own model. review_isolation_unverified and
// same_model are the vocabulary the marker contract names; the rest
// name the failure the run log carries in full.
const (
	checkReasonIsolationUnverified = "review_isolation_unverified"
	checkReasonUnknownHarness      = "unknown_harness"
	checkReasonNotInstalled        = "harness_not_installed"
	checkReasonVersionRefused      = "version_refused"
	checkReasonUnavailable         = "harness_unavailable"
	checkReasonQuota               = "quota"
	checkReasonTransient           = "transient"
	checkReasonHarnessFailed       = "harness_failed"
	checkReasonAnswerRejected      = "answer_rejected"
	checkReasonCandidateExceeds    = "candidate_exceeds_limit"
)

// outsideDiffRadius is the committed-lines window around an outside-diff
// anchor: ±20 at head, because some harnesses have no read tool.
const outsideDiffRadius = 20

// checkCandidate is one numbered finding the check judges: the finding
// as the reviewer raised it, the neutral changed-lines fact, and the
// excerpt it is about.
//
// Positions number the path-ordered candidates, not the marker entries:
// index addresses the finding's entry in the stored record, so applying
// decisions never mistakes one order for the other.
type checkCandidate struct {
	position     int
	index        int
	finding      Finding
	changed      bool
	excerptLabel string
	excerpt      []byte
}

// checkCandidates runs the cross-model check between merged candidates
// and the first finding comment: it numbers the candidates in path
// order, reuses a matching recorded check, or checks and records.
//
// parsed is the base-to-head diff the excerpts slice from. It answers the
// marker with the check state
// applied: corrected findings, the record, and the checked-out entries
// when the check ran, or the check state alone when it did not.
func (l *Leg) checkCandidates(ctx context.Context, req Request, loaded Context, settings legSettings, claimID int64, marker prstate.Marker, parsed *diff.Diff) (prstate.Marker, []ui.Line, error) {
	clean := stripCheckApplication(marker.Findings)
	record, hasRecord := marker.DecodeCheckRecord()
	findings := parseFindings(clean)
	if hasRecord {
		clean = revertCheckConcerns(clean, record.Decisions, orderCandidates(findings))
		findings = parseFindings(clean)
	}
	clearCheck := func() {
		marker.CheckRecord = nil
		marker.CheckedOut = nil
		marker.CheckReason = prstate.Opt[string]{}
	}

	if settings.checkMode == config.ReviewCheckOff {
		marker.Findings = clean
		marker.Check = prstate.Some(prstate.CheckOff)
		clearCheck()
		if l.Log != nil {
			l.Log.Event("check", "result=off")
		}
		return marker, nil, nil
	}
	if len(findings) == 0 {
		marker.Findings = clean
		marker.Check = prstate.Some(prstate.CheckNoCandidates)
		clearCheck()
		if l.Log != nil {
			l.Log.Event("check", "result=no_candidates")
		}
		return marker, nil, nil
	}
	ordered := orderCandidates(findings)
	candidates := l.buildCheckCandidates(ctx, loaded, ordered, parsed)
	digest := checkDigest(candidates)
	if hasRecord && checkRecordMatches(record, digest, loaded, settings.checkMode) {
		return l.reuseCheck(record, clean, ordered, marker)
	}
	if hasRecord {
		// A stale record goes before the re-check, with the reason in
		// the run log: the digest, the revision or the check setting
		// moved under it.
		if l.Log != nil {
			l.Log.Event("check", "discarded the recorded check: "+checkStaleReason(record, digest, loaded, settings.checkMode))
		}
	}
	return l.runCheck(ctx, req, loaded, claimID, marker, clean, candidates, digest)
}

// checkRecordMatches reports whether a recorded check still describes
// these candidates: the same digest, the same revision pair, and the
// same check mode.
func checkRecordMatches(record prstate.CheckRecord, digest string, loaded Context, mode string) bool {
	return record.Digest == digest &&
		record.Base == loaded.PR.BaseRefOid.SHA() &&
		record.Head == loaded.PR.HeadRefOid.SHA() &&
		record.Check == mode
}

// checkStaleReason names why a recorded check no longer applies.
func checkStaleReason(record prstate.CheckRecord, digest string, loaded Context, mode string) string {
	switch {
	case record.Digest != digest:
		return "the candidate set changed"
	case record.Base != loaded.PR.BaseRefOid.SHA() || record.Head != loaded.PR.HeadRefOid.SHA():
		return "the revision moved"
	default:
		return "the check setting changed"
	}
}

// reuseCheck applies a matching recorded check without a model call:
// the decisions go back onto the stripped findings, and the
// checked-out entries rebuild from the decisions.
func (l *Leg) reuseCheck(record prstate.CheckRecord, clean json.RawMessage, ordered []checkCandidate, marker prstate.Marker) (prstate.Marker, []ui.Line, error) {
	decisions := make([]validate.CheckDecision, 0, len(record.Decisions))
	for _, stored := range record.Decisions {
		decisions = append(decisions, validate.CheckDecision{
			Position:    stored.Position,
			Decision:    stored.Decision,
			DuplicateOf: stored.DuplicateOf,
			Reason:      stored.Reason,
			Severity:    stored.Severity,
			HasSeverity: stored.Severity != "",
		})
		if stored.PreExisting != nil {
			decisions[len(decisions)-1].PreExisting = *stored.PreExisting
			decisions[len(decisions)-1].HasPre = true
		}
	}
	applied, _, checkedOut, err := applyCheckDecisions(clean, ordered, decisions)
	if err != nil {
		return marker, nil, err
	}
	marker.Findings = applied
	if len(checkedOut) > 0 {
		if raw, err := json.Marshal(checkedOut); err == nil {
			marker.CheckedOut = raw
		}
	} else {
		marker.CheckedOut = nil
	}
	marker.Check = prstate.Some(prstate.CheckRan)
	if l.Log != nil {
		l.Log.Event("check", fmt.Sprintf("result=reused decisions=%d", len(decisions)))
	}
	return marker, []ui.Line{ui.Say(fmt.Sprintf("Reusing the recorded cross-model check (%d decisions).", len(decisions)))}, nil
}

// checkerSettings reads the checker's harness, model, effort and
// endpoint off the resolver configuration: the model judging a finding
// is not the model that raised it.
func checkerSettings(cfg *config.Config) legSettings {
	if cfg == nil {
		return legSettings{}
	}
	return legSettings{
		harness:  cfg.Get(".resolver.harness"),
		model:    cfg.Get(".resolver.model"),
		effort:   cfg.Get(".resolver.effort"),
		endpoint: cfg.Get(".resolver.endpoint"),
	}
}

// runCheck judges every candidate on the checker's harness and model,
// records the decisions, and writes the checked state to the claim
// before the first finding comment. A failure the check degrades on
// posts every candidate unchecked; any other failure propagates.
func (l *Leg) runCheck(ctx context.Context, req Request, loaded Context, claimID int64, marker prstate.Marker, clean json.RawMessage, candidates []checkCandidate, digest string) (prstate.Marker, []ui.Line, error) {
	checker := checkerSettings(loaded.Config)
	if checker.harness == "" {
		checker.harness = string(core.HarnessClaude)
	}
	var msgs []ui.Line
	degraded := func(state, reason, detail string) (prstate.Marker, []ui.Line, error) {
		marker.Findings = clean
		marker.Check = prstate.Some(state)
		marker.CheckReason = prstate.Some(reason)
		marker.CheckRecord = nil
		marker.CheckedOut = nil
		// The failed calls still read: fold their notes into the pass
		// envelope the way the ran path does below.
		l.attachReads(&marker)
		if l.Log != nil {
			l.Log.Event("check", fmt.Sprintf("result=%s reason=%s candidates=%d harness=%s %s", state, reason, len(candidates), checker.harness, detail))
		}
		// Earlier calls' lines travel with the degrade warning rather
		// than dropped: a retry note from an accepted call stays news
		// after a later call degrades.
		out := append(msgs, ui.Warn(
			fmt.Sprintf("the cross-model check %s (%s), so every candidate posts unchecked", state, reason),
			"The run log carries the detail. Nothing was judged by a second model on this pass."))
		return marker, out, nil
	}
	if _, known := harness.For(l.Harness, checker.harness); !known {
		return degraded(prstate.CheckUnavailable, checkReasonUnknownHarness, "the harness has no adapter")
	}
	// The isolation gate runs before the install probe: a harness whose
	// review isolation is unverified cannot check whether or not its CLI
	// is on PATH, and the record names the isolation gap either way.
	if refusal := harness.ReviewIsolationRefusal(l.Harness, checker.harness); refusal != nil {
		return degraded(prstate.CheckUnavailable, checkReasonIsolationUnverified, strings.TrimSpace(refusal.Reason))
	}
	if !l.binaryInstalled(checker.harness) {
		return degraded(prstate.CheckUnavailable, checkReasonNotInstalled, "the harness CLI is not installed")
	}
	budget, ok := l.Harness.InputBudget(checker.harness, checker.model)
	if !ok {
		return degraded(prstate.CheckUnavailable, checkReasonUnavailable, "the harness has no input budget")
	}
	meta := reviewMeta(loaded, req, marker.Pass)
	entry, _ := l.Harness.For(checker.harness)
	effective, _ := EffectiveReadMode(entry.ReadMode())
	reads := prompt.ReadsBlock(string(effective))
	limit := budget.PackBytes
	overhead := len(prompt.Check{Meta: meta, Reads: reads}.Render())
	if overhead > limit {
		limit = budget.HardBytes
	}
	calls := packCheckCalls(meta, reads, candidates, overhead, limit)
	// The hard-limit preflight runs before any call does: a packed
	// call is measured in rendered bytes, and one past the checker's
	// hard input limit degrades the check rather than invoking the
	// harness with a prompt past its window.
	for _, call := range calls {
		if rendered := len(renderCheckCall(meta, reads, call, len(candidates))); rendered > budget.HardBytes {
			return degraded(prstate.CheckDegraded, checkReasonCandidateExceeds,
				fmt.Sprintf("candidate %s renders %d bytes past the %d-byte hard input limit",
					checkPositionsList(call), rendered, budget.HardBytes))
		}
	}
	if l.Log != nil {
		l.Log.Event("check", fmt.Sprintf("harness=%s model=%s candidates=%d calls=%d", checker.harness, checker.model, len(candidates), len(calls)))
	}
	var merged []validate.CheckDecision
	var envelopes []harness.Envelope
	base := l.callsMade + 1
	for i, call := range calls {
		decisions, envelope, callMsgs, err := l.invokeCheckedCall(ctx, req, loaded, checker, call, len(candidates), meta, reads, base+i)
		if err != nil {
			if state, reason, ok := checkDegrade(err); ok {
				return degraded(state, reason, strings.TrimSpace(ui.Reason(err)))
			}
			return marker, callMsgs, err
		}
		msgs = append(msgs, callMsgs...)
		merged = append(merged, decisions...)
		envelopes = append(envelopes, envelope)
	}
	l.callsMade = base + len(calls) - 1
	if err := validate.CheckChains(merged); err != nil {
		// One merged retry: each call's answer was valid on its
		// own, so the calls behind the broken chains are asked
		// once more and their fresh decisions replace the
		// contradicted ones. A second contradiction degrades.
		members := validate.FailingChainPositions(merged)
		msgs = append(msgs, ui.Warn(
			fmt.Sprintf("the cross-model check's calls contradicted each other — %s", strings.TrimSpace(err.Error())),
			"Each call's answer was valid on its own, so the calls behind the broken chains are being asked once more; a second contradiction degrades to posting unchecked."))
		if len(members) > 0 {
			fresh, callMsgs, rerr := l.recheckFailedChains(ctx, req, loaded, checker, calls, members, len(candidates), meta, reads, &envelopes, &msgs)
			if rerr != nil {
				if state, reason, ok := checkDegrade(rerr); ok {
					return degraded(state, reason, strings.TrimSpace(ui.Reason(rerr)))
				}
				return marker, callMsgs, rerr
			}
			merged = spliceCheckDecisions(merged, calls, fresh)
		}
		if err := validate.CheckChains(merged); err != nil {
			return degraded(prstate.CheckDegraded, checkReasonAnswerRejected, strings.TrimSpace(err.Error()))
		}
	}
	applied, records, checkedOut, err := applyCheckDecisions(clean, candidates, merged)
	if err != nil {
		return marker, nil, err
	}
	checkerReported := firstReportedModel(envelopes)
	var usageRaw json.RawMessage
	if usage := priceCheckUsage(envelopes, l.Harness, checker, envValueSet(l.Env, "ANTHROPIC_API_KEY")); usage != nil {
		if raw, err := json.Marshal(usage); err == nil {
			usageRaw = raw
		}
	}
	stored := prstate.CheckRecord{
		Digest:        digest,
		Base:          loaded.PR.BaseRefOid.SHA(),
		Head:          loaded.PR.HeadRefOid.SHA(),
		Check:         config.ReviewCheckResolver,
		Harness:       checker.harness,
		ModelReported: checkerReported,
		Usage:         usageRaw,
		Decisions:     records,
	}
	recordRaw, err := json.Marshal(stored)
	if err != nil {
		return marker, nil, err
	}
	marker.Findings = applied
	marker.Check = prstate.Some(prstate.CheckRan)
	marker.CheckReason = prstate.Opt[string]{}
	marker.CheckRecord = recordRaw
	if len(checkedOut) > 0 {
		if raw, err := json.Marshal(checkedOut); err == nil {
			marker.CheckedOut = raw
		}
	} else {
		marker.CheckedOut = nil
	}
	confirmed, rejected, duplicates := countDecisions(merged)
	if l.Log != nil {
		l.Log.Event("check", fmt.Sprintf("result=ran candidates=%d confirmed=%d rejected=%d duplicate=%d harness=%s model=%s",
			len(candidates), confirmed, rejected, duplicates, checker.harness, nonEmpty(checkerReported)))
		for _, decision := range merged {
			l.Log.Event("check", checkDecisionLine(decision, candidates))
		}
	}
	if rejected+duplicates > 0 {
		msgs = append(msgs, ui.Say(checkFilteredLine(len(candidates), confirmed, rejected, duplicates)))
	}
	if reviewerReported := marker.ModelReported.Value(); reviewerReported != "" {
		if matched := checkSameModels(envelopes, reviewerReported); len(matched) > 0 {
			marker.CheckReason = prstate.Some(prstate.CheckReasonSameModel)
			msgs = append(msgs, ui.Warn(
				"the cross-model check ran on the reviewer's own model, so this pass had no second lineage",
				"The decisions stand, and the marker records same_model rather than a cross-model check. Point the resolver at another harness to get the second lineage back."))
			if l.Log != nil {
				l.Log.Event("check", "same_model reviewer="+reviewerReported+" checker="+strings.Join(matched, ","))
			}
		}
	}
	l.attachReads(&marker)
	cap := atoi(loaded.Config.Get(".policy.max_passes_per_cycle"))
	body := fmt.Sprintf("**crossrev — reviewing, %s**\n\nFindings recorded and checked; posting them now.", PassLabel(marker.Pass, cap))
	written, err := l.editClaim(ctx, loaded.Repo, claimID, body, marker, coverageOverflow(loaded))
	if err != nil {
		return marker, msgs, err
	}
	return written, msgs, nil
}

// checkDegrade maps a check-call failure onto the check's failure
// classes: the check state and reason token when the pass posts
// unchecked, or false when the failure propagates as the review leg's
// own would.
func checkDegrade(err error) (string, string, bool) {
	switch {
	case errors.Is(err, harness.ErrIsolationUnverified):
		return prstate.CheckUnavailable, checkReasonIsolationUnverified, true
	case errors.Is(err, harness.ErrHarnessFailed):
		reason := ui.Reason(err)
		switch {
		case harness.IsQuotaError(reason):
			return prstate.CheckDegraded, checkReasonQuota, true
		case harness.IsTransientHarnessError(reason):
			return prstate.CheckDegraded, checkReasonTransient, true
		default:
			return prstate.CheckDegraded, checkReasonHarnessFailed, true
		}
	case errors.Is(err, harness.ErrAnswerRejected):
		return prstate.CheckDegraded, checkReasonAnswerRejected, true
	}
	var refusal *harness.Refusal
	if errors.As(err, &refusal) {
		switch {
		case errors.Is(err, harness.ErrEndpointUnsupported), errors.Is(err, harness.ErrEndpointToken):
			return "", "", false
		case errors.Is(err, harness.ErrNotInstalled):
			return prstate.CheckUnavailable, checkReasonNotInstalled, true
		case errors.Is(err, harness.ErrVersionUnsupported):
			return prstate.CheckUnavailable, checkReasonVersionRefused, true
		default:
			return prstate.CheckUnavailable, checkReasonUnavailable, true
		}
	}
	return "", "", false
}

// firstReportedModel answers the first model the check's calls reported,
// the way the batch loop keeps its first answering model.
func firstReportedModel(envelopes []harness.Envelope) string {
	for _, envelope := range envelopes {
		if envelope.ModelReported != nil && *envelope.ModelReported != "" {
			return *envelope.ModelReported
		}
	}
	return ""
}

// priceCheckUsage prices each accepted check call's usage with its own
// answering model and aggregates: buckets summed, costs summed where
// every priced call carries one. A call no rate prices clears the
// total rather than understating it, and a cost mixing harness and
// table sources reads mixed. Identity stays on the first priced call,
// the way the batch outcome keeps its first envelope's. Nil when no
// call reported usage.
func priceCheckUsage(envelopes []harness.Envelope, doc harness.Document, checker legSettings, anthropic bool) *harness.Usage {
	var priced []harness.Usage
	for _, envelope := range envelopes {
		if envelope.Usage == nil {
			continue
		}
		reported := ""
		if envelope.ModelReported != nil {
			reported = *envelope.ModelReported
		}
		priced = append(priced, priceUsage(*envelope.Usage, doc, checker, reported, anthropic))
	}
	if len(priced) == 0 {
		return nil
	}
	sum := priced[0]
	for i := 1; i < len(priced); i++ {
		addUsageBuckets(&sum, &priced[i])
	}
	var cost float64
	for _, p := range priced {
		if p.CostUSD == nil {
			sum.CostUSD, sum.CostSource, sum.PriceTable = nil, nil, nil
			total := sum.WithTotal()
			return &total
		}
		cost += *p.CostUSD
	}
	sum.CostUSD = &cost
	if !samePricedSource(priced) {
		mixed := "mixed"
		sum.CostSource = &mixed
		sum.PriceTable = nil
	}
	total := sum.WithTotal()
	return &total
}

// samePricedSource reports whether every priced call's cost triple
// agrees with the first's: one source for the summed cost, else mixed.
func samePricedSource(priced []harness.Usage) bool {
	for _, p := range priced[1:] {
		if !sameOptionalString(priced[0].CostSource, p.CostSource) ||
			!sameOptionalString(priced[0].PriceTable, p.PriceTable) {
			return false
		}
	}
	return true
}

func sameOptionalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// checkSameModels answers the distinct check-call models that read as
// the reviewer's own: one same-model call means the pass had no fully
// independent second lineage, however the other calls read.
func checkSameModels(envelopes []harness.Envelope, reviewer string) []string {
	var matched []string
	seen := map[string]bool{}
	for _, envelope := range envelopes {
		reported := ""
		if envelope.ModelReported != nil {
			reported = *envelope.ModelReported
		}
		if reported == "" || seen[reported] || !SameModel(reviewer, reported) {
			continue
		}
		seen[reported] = true
		matched = append(matched, reported)
	}
	return matched
}

// priceUsage prices one usage record with the checker's harness,
// endpoint and model: the answering model where the harness names one,
// else the configured one.
func priceUsage(u harness.Usage, doc harness.Document, checker legSettings, reported string, anthropic bool) harness.Usage {
	prices, err := harness.PriceTable()
	if err != nil {
		return u
	}
	model := checker.model
	if reported != "" {
		model = reported
	}
	return prices.Attach(u, doc, checker.harness, checker.endpoint, model, anthropic)
}

// countDecisions counts merged decisions by kind.
func countDecisions(decisions []validate.CheckDecision) (confirmed, rejected, duplicates int) {
	for _, decision := range decisions {
		switch decision.Decision {
		case "confirmed":
			confirmed++
		case "rejected":
			rejected++
		case "duplicate":
			duplicates++
		}
	}
	return confirmed, rejected, duplicates
}

// checkDecisionLine renders one decision for the run log: the full
// record the summary only counts.
func checkDecisionLine(decision validate.CheckDecision, ordered []checkCandidate) string {
	line := fmt.Sprintf("decision position=%d decision=%s reason=%s", decision.Position, decision.Decision, decision.Reason)
	if decision.Decision == "duplicate" {
		line += fmt.Sprintf(" duplicate_of=%d", decision.DuplicateOf)
	}
	if decision.HasSeverity {
		line += " severity=" + decision.Severity
	}
	if decision.HasPre {
		line += fmt.Sprintf(" pre_existing=%v", decision.PreExisting)
	}
	if decision.Position-1 >= 0 && decision.Position-1 < len(ordered) {
		line += " id=" + ordered[decision.Position-1].finding.ID
	}
	return line
}

// checkFilteredLine is the terminal line for a check that kept findings
// off the pull request.
func checkFilteredLine(total, confirmed, rejected, duplicates int) string {
	parts := []string{}
	if rejected > 0 {
		parts = append(parts, fmt.Sprintf("rejected %d", rejected))
	}
	if duplicates > 0 {
		parts = append(parts, fmt.Sprintf("folded %d duplicates", duplicates))
	}
	return fmt.Sprintf("The cross-model check %s of %d candidates; posting the %d confirmed.", strings.Join(parts, " and "), total, confirmed)
}

// nonEmpty reads a possibly empty model the way the call line does.
func nonEmpty(model string) string {
	if model == "" {
		return "-"
	}
	return model
}

// checkPositionsList names a packed call's candidates for the run log:
// their positions, with the finding id beside a singleton.
func checkPositionsList(call []checkCandidate) string {
	positions := make([]string, 0, len(call))
	for _, candidate := range call {
		positions = append(positions, strconv.Itoa(candidate.position))
	}
	if len(call) == 1 {
		return positions[0] + " " + call[0].finding.ID
	}
	return strings.Join(positions, ",")
}

// orderCandidates numbers the findings in path order: positions count
// the sorted candidates while each keeps its index into the stored
// record, so decisions apply onto the entries they judged whatever
// order the marker lists them in. The sort is stable with no id
// tiebreak: findings on one anchor keep payload order, so a human
// reading the reviewer's answer can count along.
func orderCandidates(findings []Finding) []checkCandidate {
	anchors := make([]prstate.CandidateAnchor, len(findings))
	for i, finding := range findings {
		anchors[i] = prstate.CandidateAnchor{ID: finding.ID, Path: finding.Path, Line: finding.Line, Side: finding.Side}
	}
	out := make([]checkCandidate, 0, len(findings))
	for i, index := range prstate.OrderCandidateIndexes(anchors) {
		out = append(out, checkCandidate{position: i + 1, index: index, finding: findings[index]})
	}
	return out
}

// buildCheckCandidates reads each ordered candidate's changed-lines
// fact and excerpt: the supplied hunk for an in-scope candidate, the
// committed lines around the anchor at head for one outside the diff.
func (l *Leg) buildCheckCandidates(ctx context.Context, loaded Context, ordered []checkCandidate, parsed *diff.Diff) []checkCandidate {
	out := make([]checkCandidate, 0, len(ordered))
	for _, entry := range ordered {
		finding := entry.finding
		side := core.SideRight
		if parsedSide, err := core.ParseSide(finding.Side); err == nil {
			side = parsedSide
		}
		candidate := checkCandidate{position: entry.position, index: entry.index, finding: finding}
		if parsed != nil {
			candidate.changed = parsed.ChangedLine(finding.Path, side, finding.Line)
		}
		if finding.AnchorKind == string(AnchorOutsideDiff) {
			candidate.excerptLabel, candidate.excerpt = l.committedExcerpt(ctx, loaded, finding)
		} else if parsed != nil {
			if hunk, ok := parsed.Hunk(finding.Path, side, finding.Line); ok {
				candidate.excerptLabel = fmt.Sprintf("The hunk at %s:%d", finding.Path, finding.Line)
				candidate.excerpt = hunk
			} else {
				candidate.excerptLabel, candidate.excerpt = l.committedExcerpt(ctx, loaded, finding)
			}
		} else {
			candidate.excerptLabel = fmt.Sprintf("The hunk at %s:%d", finding.Path, finding.Line)
		}
		out = append(out, candidate)
	}
	return out
}

// committedExcerpt reads the committed lines around an anchor at head,
// ±20 clamped to the file: the excerpt for an outside-diff candidate,
// and the fallback where the diff holds no hunk. Without a git reader
// the label stands alone and the prompt marks it unavailable.
func (l *Leg) committedExcerpt(ctx context.Context, loaded Context, finding Finding) (string, []byte) {
	label := fmt.Sprintf("%s around line %d at head", finding.Path, finding.Line)
	if l.VCS == nil {
		return label, nil
	}
	body, _, err := l.VCS.Show(ctx, loaded.PR.HeadRefOid, finding.Path)
	if err != nil || len(body) == 0 {
		return label, nil
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	start := finding.Line - outsideDiffRadius
	if start < 1 {
		start = 1
	}
	end := finding.Line + outsideDiffRadius
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return label, nil
	}
	var b strings.Builder
	for n := start; n <= end; n++ {
		fmt.Fprintf(&b, "%d: %s\n", n, lines[n-1])
	}
	return fmt.Sprintf("%s lines %d-%d at head", finding.Path, start, end), []byte(b.String())
}

// checkDigest digests the candidate set the decisions were recorded
// against: every candidate's identity, words, values, excerpt and
// changed-lines fact, in position order.
func checkDigest(candidates []checkCandidate) string {
	type digestCandidate struct {
		Position    int      `json:"position"`
		ID          string   `json:"id"`
		Path        string   `json:"path"`
		Line        int      `json:"line"`
		Side        string   `json:"side"`
		Severity    string   `json:"severity"`
		Category    string   `json:"category"`
		PreExisting bool     `json:"pre_existing"`
		Concerns    []string `json:"concerns"`
		Title       string   `json:"title"`
		Why         string   `json:"why"`
		Changed     bool     `json:"changed"`
		Excerpt     []byte   `json:"excerpt"`
	}
	projected := make([]digestCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		finding := candidate.finding
		projected = append(projected, digestCandidate{
			Position:    candidate.position,
			ID:          finding.ID,
			Path:        finding.Path,
			Line:        finding.Line,
			Side:        finding.Side,
			Severity:    finding.Severity,
			Category:    finding.Category,
			PreExisting: finding.PreExisting,
			Concerns:    finding.Concerns,
			Title:       finding.Title,
			Why:         finding.Why,
			Changed:     candidate.changed,
			Excerpt:     candidate.excerpt,
		})
	}
	raw, err := json.Marshal(projected)
	if err != nil {
		return ""
	}
	return core.BodyDigestHex(raw)
}

// packCheckCalls groups candidates into bounded calls in path order.
// Candidates sharing one excerpt stay in one call and render it once;
// each call's rendered bytes fit the limit where any packing can fit
// them. A group past the limit alone splits into singleton groups,
// each rendering the shared excerpt in its own call; a singleton
// past the limit still goes, in a call of its own, for the hard-limit
// preflight to judge.
func packCheckCalls(meta prompt.Meta, reads string, candidates []checkCandidate, overhead, limit int) [][]checkCandidate {
	groups := groupSharedExcerpts(candidates)
	measure := func(group []checkCandidate) int {
		return len(renderCheckCall(meta, reads, group, len(candidates))) - overhead
	}
	var split [][]checkCandidate
	for _, group := range groups {
		if len(group) > 1 && measure(group) > limit-overhead {
			for _, candidate := range group {
				split = append(split, []checkCandidate{candidate})
			}
			continue
		}
		split = append(split, group)
	}
	measured := make([]int, len(split))
	for i, group := range split {
		measured[i] = measure(group)
	}
	var calls [][]checkCandidate
	var current []checkCandidate
	currentBytes := 0
	flush := func() {
		if len(current) > 0 {
			calls = append(calls, current)
			current = nil
			currentBytes = 0
		}
	}
	for i, group := range split {
		if len(current) > 0 && currentBytes+measured[i] > limit-overhead {
			flush()
		}
		current = append(current, group...)
		currentBytes += measured[i]
	}
	flush()
	return calls
}

// groupSharedExcerpts gathers candidates rendering one identical excerpt,
// in first-position order. The excerpt renders once, under its lowest
// position.
func groupSharedExcerpts(candidates []checkCandidate) [][]checkCandidate {
	byKey := map[string][]checkCandidate{}
	var keys []string
	for _, candidate := range candidates {
		key := candidate.finding.Path + "\x00" + candidate.finding.Side + "\x00" + string(candidate.excerpt)
		if _, ok := byKey[key]; !ok {
			keys = append(keys, key)
		}
		byKey[key] = append(byKey[key], candidate)
	}
	groups := make([][]checkCandidate, 0, len(keys))
	for _, key := range keys {
		groups = append(groups, byKey[key])
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0].position < groups[j][0].position })
	return groups
}

// renderCheckCall renders one check call's prompt: its candidates in
// position order, a shared excerpt once under its lowest position.
func renderCheckCall(meta prompt.Meta, reads string, call []checkCandidate, total int) []byte {
	ordered := append([]checkCandidate(nil), call...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].position < ordered[j].position })
	shared := map[string][]int{}
	for _, candidate := range ordered {
		key := candidate.finding.Path + "\x00" + candidate.finding.Side + "\x00" + string(candidate.excerpt)
		shared[key] = append(shared[key], candidate.position)
	}
	rendered := make([]prompt.CheckCandidate, 0, len(ordered))
	for _, candidate := range ordered {
		finding := candidate.finding
		out := prompt.CheckCandidate{
			Position:     candidate.position,
			Total:        total,
			ID:           finding.ID,
			Path:         finding.Path,
			Line:         finding.Line,
			Side:         finding.Side,
			Severity:     finding.Severity,
			Category:     finding.Category,
			PreExisting:  finding.PreExisting,
			Concerns:     finding.Concerns,
			Title:        finding.Title,
			Why:          finding.Why,
			Changed:      candidate.changed,
			ExcerptLabel: candidate.excerptLabel,
		}
		key := finding.Path + "\x00" + finding.Side + "\x00" + string(candidate.excerpt)
		members := shared[key]
		if len(members) > 1 && len(candidate.excerpt) > 0 {
			if candidate.position == members[0] {
				out.Excerpt = candidate.excerpt
				out.SharedWith = members[1:]
			} else {
				out.SharedFirst = members[0]
			}
		} else {
			out.Excerpt = candidate.excerpt
		}
		rendered = append(rendered, out)
	}
	return prompt.Check{Meta: meta, Candidates: rendered, Reads: reads}.Render()
}

// invokeCheckedCall runs one packed check call through the checker and
// validates its answer against the call's positions and the pass's
// range: the per-call half the merged retry reuses.
func (l *Leg) invokeCheckedCall(ctx context.Context, req Request, loaded Context, checker legSettings, call []checkCandidate, total int, meta prompt.Meta, reads string, callNum int) ([]validate.CheckDecision, harness.Envelope, []ui.Line, error) {
	positions := make([]int, 0, len(call))
	for _, candidate := range call {
		positions = append(positions, candidate.position)
	}
	promptBytes := renderCheckCall(meta, reads, call, total)
	suppliedBytes := 0
	for _, candidate := range call {
		if len(candidate.excerpt) > 0 {
			suppliedBytes += len(candidate.excerpt)
		}
	}
	start := l.now()
	readsMark := len(l.readsNotes)
	payload, envelope, callMsgs, err := l.invokeCheckCall(ctx, req, loaded, checker, positions, promptBytes, callNum, total)
	ms := l.now().Sub(start).Milliseconds()
	if err != nil {
		return nil, harness.Envelope{}, callMsgs, err
	}
	l.logCheckCall(callNum, promptBytes, suppliedBytes, l.callReadsSince(readsMark), envelope, ms)
	decisions, err := validate.Check(payload, validate.CheckExpectations{Positions: positions, Total: total})
	if err != nil {
		// Unreachable: the call's own validation seam accepted
		// this payload before it was answered. Kept rather than
		// trusted, because a seam that stopped validating must
		// degrade rather than apply garbage.
		return nil, harness.Envelope{}, callMsgs, &ui.FatalError{
			Reason: strings.TrimSpace(err.Error()),
			Action: "The checker's answer passed its own validation and failed it on the re-read; the run log carries both.",
			Kind:   harness.ErrAnswerRejected,
		}
	}
	return decisions, envelope, callMsgs, nil
}

// recheckFailedChains asks once more the check calls holding the failed
// chains' positions, in call order with fresh call numbers, and answers
// each re-invoked call's fresh decisions by call index. The retry's
// envelopes join the pass's for usage and provenance: spent calls stay
// spent however their decisions read.
func (l *Leg) recheckFailedChains(ctx context.Context, req Request, loaded Context, checker legSettings, calls [][]checkCandidate, members []int, total int, meta prompt.Meta, reads string, envelopes *[]harness.Envelope, msgs *[]ui.Line) (map[int][]validate.CheckDecision, []ui.Line, error) {
	wanted := make(map[int]bool, len(members))
	for _, position := range members {
		wanted[position] = true
	}
	var indexes []int
	for i, call := range calls {
		for _, candidate := range call {
			if wanted[candidate.position] {
				indexes = append(indexes, i)
				break
			}
		}
	}
	fresh := make(map[int][]validate.CheckDecision, len(indexes))
	var retried []string
	for _, i := range indexes {
		l.callsMade++
		decisions, envelope, callMsgs, err := l.invokeCheckedCall(ctx, req, loaded, checker, calls[i], total, meta, reads, l.callsMade)
		if err != nil {
			return nil, callMsgs, err
		}
		*msgs = append(*msgs, callMsgs...)
		*envelopes = append(*envelopes, envelope)
		fresh[i] = decisions
		retried = append(retried, strconv.Itoa(l.callsMade))
	}
	if l.Log != nil && len(retried) > 0 {
		l.Log.Event("check", fmt.Sprintf("retrying calls %s after the merged contradiction", strings.Join(retried, ",")))
	}
	return fresh, nil, nil
}

// spliceCheckDecisions replaces the contradicted decisions with the
// retry's fresh ones: each re-invoked call's positions read fresh,
// every other position keeps its answer. Fresh calls splice in call
// order, so the merged order stays deterministic.
func spliceCheckDecisions(merged []validate.CheckDecision, calls [][]checkCandidate, fresh map[int][]validate.CheckDecision) []validate.CheckDecision {
	if len(fresh) == 0 {
		return merged
	}
	replaced := map[int]bool{}
	for i := range fresh {
		for _, candidate := range calls[i] {
			replaced[candidate.position] = true
		}
	}
	out := make([]validate.CheckDecision, 0, len(merged))
	for _, decision := range merged {
		if !replaced[decision.Position] {
			out = append(out, decision)
		}
	}
	var indexes []int
	for i := range fresh {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	for _, i := range indexes {
		out = append(out, fresh[i]...)
	}
	return out
}

// invokeCheckCall runs one rendered check prompt through the checker's
// harness with the review leg's isolation: quarantine, the version
// gates, served reads or supplied mode as the harness's review read
// mode declares, credential staging and the command tripwire.
func (l *Leg) invokeCheckCall(ctx context.Context, req Request, loaded Context, checker legSettings, positions []int, promptBytes []byte, call int, total int) (json.RawMessage, harness.Envelope, []ui.Line, error) {
	if err := harness.AssertEnvClean(l.Env); err != nil {
		return nil, harness.Envelope{}, nil, err
	}
	entry, _ := l.Harness.For(checker.harness)
	staged, err := cred.Prepare(l.Harness.Credentials().For(checker.harness), checker.endpoint, cred.Options{Now: l.Now})
	if err != nil {
		return nil, harness.Envelope{}, nil, err
	}
	defer func() { _ = cred.Discard(staged) }()
	adapter, known := harness.For(l.Harness, checker.harness)
	if !known {
		return nil, harness.Envelope{}, nil, noAdapterRefusal(l.Harness, checker.harness)
	}
	tmp, err := os.MkdirTemp("", "crossrev-check-")
	if err != nil {
		return nil, harness.Envelope{}, nil, err
	}
	defer os.RemoveAll(tmp)
	check := func(payload []byte) error {
		_, err := validate.Check(payload, validate.CheckExpectations{Positions: positions, Total: total})
		return err
	}
	envelope, payload, msgs, err := l.runPrompt(ctx, req, loaded, checker, adapter, entry, staged, tmp, promptBytes, nil, call, promptSpec{schema: validate.CheckSchema(), check: check})
	return payload, envelope, msgs, err
}

// logCheckCall writes one accepted check call's run-log line with
// kind=check, so the checker's usage reads back separately.
func (l *Leg) logCheckCall(call int, promptBytes []byte, suppliedBytes, reads int, envelope harness.Envelope, ms int64) {
	if call > l.callsMade {
		l.callsMade = call
	}
	if l.Log == nil {
		return
	}
	var fresh, cached, output int64
	if envelope.Usage != nil {
		fresh = envelope.Usage.InputFresh
		cached = envelope.Usage.Cached()
		output = envelope.Usage.Output
	}
	model := ""
	if envelope.ModelReported != nil {
		model = *envelope.ModelReported
	}
	l.Log.CheckCall(call, len(promptBytes), suppliedBytes, reads, fresh, cached, output, model, ms)
}
