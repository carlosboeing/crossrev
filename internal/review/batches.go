package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prompt"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// batchOutcome is what one batch run settled: the verdicts it accepted,
// the findings they name, the scope claims the manifest carries, and the
// per-file supplied measurements the accepted batches rendered.
type batchOutcome struct {
	verdicts map[core.UnitID]recordVerdict
	supplied map[core.UnitID]prstate.SuppliedInput
	findings     []Finding
	verdict      string
	envelope     *harness.Envelope
	payloads     []json.RawMessage
	examined     []string
	limits       []string
	batches      int
	retries      int
}

// runCoverage runs the batch loop for one pass and folds its outcome into
// the result: findings enriched and published through the existing path,
// the marker carrying the current coverage manifest id. A bounded halt sets
// the outcome to the halted record and returns true through the result;
// a nil error with an empty outcome means the caller continues on the
// frozen path (no git reader or ledger store in this run).
func (l *Leg) runCoverage(ctx context.Context, req Request, loaded Context, settings legSettings, pass int, claimID int64, scope intel.Scope, out *Result) error {
	outcome := batchOutcome{verdicts: map[core.UnitID]recordVerdict{}, supplied: map[core.UnitID]prstate.SuppliedInput{}}
	current, err := l.currentGeneration(ctx, loaded, scope.Base, scope.Head, scope.Engine)
	if err != nil {
		return err
	}
	gen := current.Gen
	accepted := acceptedFromGeneration(current, scope.Base, scope.Head, scope.Engine)
	acceptedIDs := make(map[core.UnitID]bool, len(accepted))
	for unitID, disp := range accepted {
		outcome.verdicts[unitID] = disp
		acceptedIDs[unitID] = true
	}
	// A resumed verdict keeps the measurement taken when it was judged: the
	// revision pair is unchanged, so the bytes handed over then are the bytes
	// that would be handed over now. A generation that predates the field
	// carries nothing, and its resumed records honestly read null.
	for _, record := range current.Records {
		if s, ok := record.Supplied.Get(); ok {
			outcome.supplied[core.UnitID(record.UnitID)] = s
		}
	}
	advisory := intel.AdvisoryFiles(ctx, scope, scopeSearcher{vcs: l.VCS})
	pair := repairConfirmation(loaded.Markers, scope.Head)
	confirmation, err := l.confirmationDelta(ctx, pair)
	if err != nil {
		pair = confirmationPair{}
		confirmation = nil
	}
	shared := l.discoverBatchContext(ctx, req, loaded, pass, scope, advisory, confirmation)
	render := func(files []intel.FileUnit) int {
		promptBytes, _ := shared.render(files, scope.Base, scope.Head)
		return len(promptBytes)
	}
	plan := intel.Batches(scope, acceptedIDs, render)
	marker := markerForPass(loaded.Markers, pass)
	// Findings a previous attempt recorded on the claim — after its accepted
	// batches, or in the blocked record the failure left — come back into the
	// outcome here, so a resumed pass republishes every accepted finding
	// rather than converging over the verdicts alone.
	if restored := marker.Findings; findingCount(restored) > 0 {
		outcome.payloads = append(outcome.payloads, findingsOnlyPayload(restored))
		outcome.findings = append(outcome.findings, parseFindings(restored)...)
	}
	claim := out.Marker
	if claim.Harness.Present() {
		marker.Harness = claim.Harness
	}
	if claim.Model.Present() {
		marker.Model = claim.Model
	}
	if claim.Effort.Present() {
		marker.Effort = claim.Effort
	}
	if claim.Endpoint.Present() {
		marker.Endpoint = claim.Endpoint
	}
	if claim.RunID.Present() {
		marker.RunID = claim.RunID
	}
	if claim.HeadSHA.Present() {
		marker.HeadSHA = claim.HeadSHA
	}
	marker.TS = claim.TS
	marker.Leg = core.LegReview
	marker.Pass = pass
	marker.Version = core.MarkerVersion
	initial, initialStop, err := l.publishInitialGeneration(ctx, req, loaded, scope, advisory, gen+1, outcome.verdicts, outcome.supplied)
	if err != nil {
		return err
	}
	marker.CoverageManifestID = prstate.Some(initial.CommentID())
	if initialStop.Limit != "" {
		return l.haltPass(ctx, req, loaded, pass, claimID, out, &batchBound{plan: planForStop(plan, initialStop), scope: scope, accepted: acceptedIDs})
	}
	_ = initial
	for _, batch := range plan.Batches {
		expected, _ := batchExpectations(batch.Files, scope.Base, scope.Head)
		if shared.diffErr != nil {
			return shared.diffErr
		}
		promptBytes, supplied := shared.render(batch.Files, scope.Base, scope.Head)
		payload, envelope, batchMsgs, err := l.invokePrompt(ctx, req, loaded, settings, expected, promptBytes)
		out.Messages = append(out.Messages, batchMsgs...)
		if err != nil {
			return err
		}
		verdicts, examined, limits, err := verdictsFromPayload(payload, batch.Files)
		if err != nil {
			return err
		}
		for id, disp := range verdicts {
			outcome.verdicts[id] = disp
			acceptedIDs[id] = true
		}
		// Only an accepted batch's measurement persists: a refused answer
		// judged nothing, so its bytes describe no record.
		for id, s := range supplied {
			outcome.supplied[id] = s
		}
		outcome.verdict = verdictFromPayload(payload)
		outcome.payloads = append(outcome.payloads, payload)
		if outcome.envelope == nil {
			outcome.envelope = &envelope
		}
		outcome.examined = append(outcome.examined, examined)
		outcome.limits = append(outcome.limits, limits...)
		for _, finding := range findingsFromPayload(payload) {
			outcome.findings = append(outcome.findings, finding)
		}
		outcome.batches++
		manifest, stop, err := l.publishBatchGeneration(ctx, req, loaded, scope, advisory, gen+outcome.batches+1, outcome.verdicts, outcome.supplied, outcome.examined, outcome.limits)
		if err != nil {
			if stop, ok := batchStop(err); ok {
				return l.haltPass(ctx, req, loaded, pass, claimID, out, &batchBound{plan: planForStop(plan, stop), scope: scope, accepted: acceptedIDs})
			}
			return err
		}
		if stop.Limit != "" {
			return l.haltPass(ctx, req, loaded, pass, claimID, out, &batchBound{plan: planForStop(plan, stop), scope: scope, accepted: acceptedIDs})
		}
		marker.CoverageManifestID = prstate.Some(manifest.CommentID())
		if raw := unionRawFindings(outcome.payloads); raw != nil {
			// The accepted batch's findings go onto the claim the way the
			// frozen path records its own before publishing: a failure in a
			// later batch leaves them on the pull request, where the re-drive
			// reads them back. The record stays a started claim — the pass
			// has not settled.
			marker.Findings = raw
			out.Marker.Findings = raw
			recorded := marker
			recorded.State = core.PassStarted
			if err := l.editClaim(ctx, loaded.Repo, claimID, recordedFindingsBody(pass, loaded.Config), recorded); err != nil {
				return err
			}
		}
	}
	if plan.HaltReason != "" || len(plan.Carried) > 0 {
		return l.haltPass(ctx, req, loaded, pass, claimID, out, &batchBound{plan: plan, scope: scope, accepted: acceptedIDs})
	}
	return l.finishCoveredPass(ctx, req, loaded, settings, pass, claimID, marker, scope, outcome, pair, out)
}

// batchBound carries a bounded halt out of the batch loop: the 400-file pass
// bound, the 32-shard ledger bound, or the single-file input bound.
type batchBound struct {
	plan     intel.BatchPlan
	scope    intel.Scope
	accepted map[core.UnitID]bool
}

func (e *batchBound) Error() string {
	if e.plan.HaltReason != "" {
		return fmt.Sprintf("halted: %s on %s", e.plan.HaltReason, e.plan.HaltPath)
	}
	return fmt.Sprintf("halted: %s carries %d files", intel.CarryReviewBudgetReached, len(e.plan.Carried))
}

// ObservePublishedCandidate receives each coverage generation candidate the
// leg hands to publication, before the store encodes it. Production leaves
// it nil; tests set it to observe the in-memory candidate, whose supplied
// measurements the v1 comment codec cannot carry until the ref-store
// cutover encodes the field.
var ObservePublishedCandidate func(prstate.Generation)

// publishBatchGeneration publishes one complete generation after an accepted
// batch. The generation accounts for every required file: covered units
// carry their accepted verdict, the rest stay outstanding.
func (l *Leg) publishBatchGeneration(ctx context.Context, req Request, loaded Context, scope intel.Scope, advisory intel.AdvisorySummary, gen int, verdicts map[core.UnitID]recordVerdict, supplied map[core.UnitID]prstate.SuppliedInput, examined []string, limits []string) (prstate.Manifest, prstate.CoverageStop, error) {
	store := ledgerStoreFor(l)
	paths := make([]string, 0, len(scope.Required))
	pathIndex := make(map[string]int, len(scope.Required))
	for _, unit := range scope.Required {
		pathIndex[unit.Path] = len(paths)
		paths = append(paths, unit.Path)
	}
	examinedScope := "reviewed the required files at the current head"
	for i := len(examined) - 1; i >= 0; i-- {
		if examined[i] != "" {
			examinedScope = examined[i]
			break
		}
	}
	candidate := prstate.Generation{
		Gen:         gen,
		Revision:    core.RevisionPair{Base: scope.Base, Head: scope.Head},
		Engine:      scope.Engine,
		Paths:       paths,
		Records:     generationRecords(scope, verdicts, pathIndex, supplied),
		Advisory:    prstate.Advisory{Count: advisory.Count, Rules: advisory.Rules, Limits: advisoryLimits(advisory)},
		Excluded:    excludedRecords(scope),
		ScopeReport: scopeReportOf(examinedScope, limits),
	}
	stillCurrent := func() error {
		// The pair is re-read from the forge rather than compared against the
		// snapshot the leg loaded: loaded.PR and scope come from the same
		// read, so comparing them cannot see a push that landed during the
		// model invocation.
		current, err := l.Forge.PullRequest(ctx, loaded.Repo, req.PR)
		if err != nil {
			return err
		}
		if current.BaseRefOid.SHA() != scope.Base.SHA() || current.HeadRefOid.SHA() != scope.Head.SHA() {
			return fmt.Errorf("the base or head moved during publication")
		}
		return nil
	}
	if store == nil {
		return prstate.Manifest{}, prstate.CoverageStop{}, fmt.Errorf("no ledger store")
	}
	if ObservePublishedCandidate != nil {
		ObservePublishedCandidate(candidate)
	}
	return prstate.PublishGeneration(ctx, store, loaded.Repo, req.PR, candidate, stillCurrent)
}

// publishInitialGeneration publishes the opening complete generation after
// the claim exists: every uncovered unit outstanding, so a crash before the
// first accepted batch leaves the started claim and the same scope to
// rebuild from.
func (l *Leg) publishInitialGeneration(ctx context.Context, req Request, loaded Context, scope intel.Scope, advisory intel.AdvisorySummary, gen int, carried map[core.UnitID]recordVerdict, supplied map[core.UnitID]prstate.SuppliedInput) (prstate.Manifest, prstate.CoverageStop, error) {
	return l.publishBatchGeneration(ctx, req, loaded, scope, advisory, gen, carried, supplied, nil, nil)
}

// verdictsFromPayload reads the accepted verdicts out of one accepted
// batch payload: one entry per numbered unit, in prompt order.
//
// finding_ids on the record are the batch-local 1-based finding positions
// the reviewer reported, not stable finding ids: the reviewer numbers
// findings per batch, and the stable id is minted later, at enrich time,
// from path, title and anchor. Nothing joins these positions to posted
// comments; they record which payload entries the verdict named.
func verdictsFromPayload(payload json.RawMessage, files []intel.FileUnit) (map[core.UnitID]recordVerdict, string, []string, error) {
	var doc struct {
		Coverage []struct {
			UnitNumber     int                `json:"unit_number"`
			Verdict        string             `json:"verdict"`
			FindingNumbers []int              `json:"finding_numbers"`
			Evidence       []prstate.Evidence `json:"evidence"`
			Reason         *string            `json:"reason"`
		} `json:"coverage"`
		ExaminedScope string   `json:"examined_scope"`
		KnownLimits   []string `json:"known_limits"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, "", nil, err
	}
	out := make(map[core.UnitID]recordVerdict, len(files))
	for _, entry := range doc.Coverage {
		if entry.UnitNumber < 1 || entry.UnitNumber > len(files) {
			continue
		}
		unit := files[entry.UnitNumber-1]
		var ids []string
		for _, n := range entry.FindingNumbers {
			ids = append(ids, fmt.Sprintf("%d", n))
		}
		reason := ""
		if entry.Reason != nil {
			reason = *entry.Reason
		}
		out[unit.ID] = recordVerdict{Verdict: entry.Verdict, FindingIDs: ids, Evidence: entry.Evidence, Reason: reason}
	}
	return out, doc.ExaminedScope, doc.KnownLimits, nil
}

// findingsFromPayload reads the findings out of one accepted batch payload
// for the finding-publication path the pass keeps.
func findingsFromPayload(payload json.RawMessage) []Finding {
	var doc struct {
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil
	}
	return doc.Findings
}

// advisoryPromptRefs renders the advisory and exclusion summaries one batch
// prompt carries beside its numbered files.
func advisoryPromptRefs(scope intel.Scope, advisory intel.AdvisorySummary) ([]prompt.AdvisoryRef, []prompt.ExclusionRef) {
	var advisoryRefs []prompt.AdvisoryRef
	for _, file := range advisory.Files {
		advisoryRefs = append(advisoryRefs, prompt.AdvisoryRef{Path: file.Path, Rule: file.Rule, Term: file.Term})
	}
	var excludedRefs []prompt.ExclusionRef
	for _, e := range scope.Excluded {
		excludedRefs = append(excludedRefs, prompt.ExclusionRef{Path: e.Path, Reason: e.Reason})
	}
	return advisoryRefs, excludedRefs
}

func advisoryLimits(advisory intel.AdvisorySummary) []prstate.AdvisoryLimit {
	var out []prstate.AdvisoryLimit
	for _, limit := range advisory.Limits {
		out = append(out, prstate.AdvisoryLimit{Rule: limit.Rule, Observed: limit.Observed, Limit: limit.Limit, Reason: limit.Reason})
	}
	return out
}

func excludedRecords(scope intel.Scope) []prstate.CoverageExclusion {
	var out []prstate.CoverageExclusion
	for _, e := range scope.Excluded {
		out = append(out, prstate.CoverageExclusion{Path: e.Path, Reason: e.Reason})
	}
	return out
}

// markerForPass returns the pass marker under construction, or a blank one
// the batch loop fills in.
func markerForPass(markers []prstate.Marker, pass int) prstate.Marker {
	if done, ok := prstate.MarkerFor(markers, pass, core.LegReview); ok {
		return done
	}
	return prstate.Marker{Pass: pass}
}

// haltPass records a bounded incomplete outcome on the claim without
// deleting the last complete generation: state incomplete, the halt word,
// the stop counts, the halted label and the outstanding paths.
func (l *Leg) haltPass(ctx context.Context, req Request, loaded Context, pass int, claimID int64, out *Result, bound *batchBound) error {
	stop := stopForBound(bound)
	marker := markerForPass(loaded.Markers, pass)
	claim := out.Marker
	if claim.Harness.Present() {
		marker.Harness = claim.Harness
	}
	if claim.Model.Present() {
		marker.Model = claim.Model
	}
	if claim.Effort.Present() {
		marker.Effort = claim.Effort
	}
	if claim.Endpoint.Present() {
		marker.Endpoint = claim.Endpoint
	}
	if claim.RunID.Present() {
		marker.RunID = claim.RunID
	}
	if claim.HeadSHA.Present() {
		marker.HeadSHA = claim.HeadSHA
	}
	marker.TS = claim.TS
	marker.Leg = core.LegReview
	marker.Pass = pass
	marker.Version = core.MarkerVersion
	marker.State = core.PassIncomplete
	marker.CoverageStop = prstate.Some(stop)
	if findingCount(claim.Findings) > 0 {
		// Findings the accepted batches recorded stay on the halted claim,
		// so the re-drive restores them beside the outstanding paths.
		marker.Findings = claim.Findings
	}
	outstanding := outstandingPaths(bound.scope, bound.accepted)
	body := haltBody(outstanding, stop, stop.Limit)
	if err := l.editClaim(ctx, loaded.Repo, claimID, body, marker); err != nil {
		return err
	}
	_, _ = l.applyPassLabels(ctx, req, loaded, pass, policy.PassHalted)
	out.Outcome = OutcomeHalted
	out.Reason = stop.Limit
	out.Marker = marker
	out.Messages = append(out.Messages, haltUILines(outstanding, stop)...)
	return nil
}

// stopForBound renders the stop counts one bounded halt records: required,
// covered and outstanding totals with the limit name. Covered derives only
// from accepted verdicts — an admitted batch that never ran is outstanding,
// never covered.
func stopForBound(bound *batchBound) prstate.CoverageStop {
	limit := intel.CarryReviewBudgetReached
	if bound.plan.HaltReason != "" {
		limit = bound.plan.HaltReason
	}
	outstanding := 0
	for _, unit := range bound.scope.Required {
		if !bound.accepted[unit.ID] {
			outstanding++
		}
	}
	return prstate.CoverageStop{RequiredCount: len(bound.scope.Required), CoveredCount: len(bound.scope.Required) - outstanding, OutstandingCount: outstanding, Limit: limit}
}

// planForStop carries a ledger-bound halt back into a batch plan shape: the
// ledger's own limit becomes the halt word, so stopForBound names the same
// halt the ledger reported.
func planForStop(plan intel.BatchPlan, stop prstate.CoverageStop) intel.BatchPlan {
	plan.HaltReason = stop.Limit
	plan.HaltPath = ""
	plan.CarryReason = stop.Limit
	return plan
}

// batchStop reads the ledger's shard-bound stop out of a publication error.
// PublishGeneration reports the bound as a nil-error stop rather than a
// failure, so this stays a backstop for a store that reports it as an error.
func batchStop(err error) (prstate.CoverageStop, bool) {
	if err == nil {
		return prstate.CoverageStop{}, false
	}
	if strings.Contains(err.Error(), prstate.CoverageStopLimit) {
		return prstate.CoverageStop{Limit: prstate.CoverageStopLimit}, true
	}
	return prstate.CoverageStop{}, false
}

// haltUILines renders the accounted and outstanding paths the halted pass
// reports, so the operator sees what ran and what a re-drive resumes.
func haltUILines(outstanding []string, stop prstate.CoverageStop) []ui.Line {
	var lines []ui.Line
	lines = append(lines, ui.Say(fmt.Sprintf("Required %d, covered %d, outstanding %d — %s.", stop.RequiredCount, stop.CoveredCount, stop.OutstandingCount, stop.Limit)))
	for _, path := range outstanding {
		lines = append(lines, ui.Say(fmt.Sprintf("Outstanding: `%s`.", path)))
	}
	return lines
}

// finishCoveredPass folds a fully covered pass into the result: the marker
// carries the current coverage manifest id and the batch findings, and the
// caller continues to the existing enrich-and-publish path with them.
func (l *Leg) finishCoveredPass(ctx context.Context, req Request, loaded Context, settings legSettings, pass int, claimID int64, marker prstate.Marker, scope intel.Scope, outcome batchOutcome, pair confirmationPair, out *Result) error {
	_ = ctx
	_ = req
	_ = loaded
	_ = settings
	_ = pass
	_ = claimID
	_ = scope
	verdict := outcome.verdict
	if verdict == "" {
		verdict = verdictForResumed(outcome.verdicts)
	}
	if pair.set {
		marker.ConfirmationBaseSHA = prstate.Some(pair.base.SHA())
		marker.ConfirmationHeadSHA = prstate.Some(pair.head.SHA())
	} else {
		marker.ConfirmationBaseSHA = prstate.Null[string]()
		marker.ConfirmationHeadSHA = prstate.Null[string]()
	}
	// A settled pass carries no stop. The marker under construction may be
	// a resumed halt, and a complete marker that still carries one cannot
	// underwrite green — MarkerConverges refuses it — while the ledger
	// predicate has already applied the converged label.
	marker.CoverageStop = prstate.Null[prstate.CoverageStop]()
	out.Marker = marker
	out.Covered = coveredPass{findings: outcome.findings, verdict: verdict, envelope: outcome.envelope, payload: mergePayloads(outcome.payloads, verdict), examined: outcome.examined, limits: outcome.limits}
	return nil
}

// verdictForResumed reports the verdict for a pass that accepted no batch in
// this run: issues-remain when any carried verdict names a finding,
// converged otherwise. A fully resumed pass re-confirms prior coverage
// rather than re-judging it.
func verdictForResumed(verdicts map[core.UnitID]recordVerdict) string {
	for _, disp := range verdicts {
		if disp.Verdict == "finding" {
			return "issues-remain"
		}
	}
	return "converged"
}

// unionRawFindings folds the raw finding objects out of every accepted batch
// payload into one JSON array, preserving each payload's own bytes: the claim
// carries them verbatim, so a resumed pass restores exactly what the reviewer
// said, and enrichment derives the same stable finding ids from them.
func unionRawFindings(payloads []json.RawMessage) json.RawMessage {
	var findings []json.RawMessage
	for _, payload := range payloads {
		var doc struct {
			Findings []json.RawMessage `json:"findings"`
		}
		if err := json.Unmarshal(payload, &doc); err != nil {
			continue
		}
		findings = append(findings, doc.Findings...)
	}
	if len(findings) == 0 {
		return nil
	}
	raw, err := json.Marshal(findings)
	if err != nil {
		return nil
	}
	return raw
}

// findingsOnlyPayload wraps restored findings in the payload shape
// mergePayloads folds: no verdict and no scope claims, which this run's
// accepted batches and the carried verdicts supply.
func findingsOnlyPayload(findings json.RawMessage) json.RawMessage {
	raw, err := json.Marshal(struct {
		Findings json.RawMessage `json:"findings"`
	}{Findings: findings})
	if err != nil {
		return nil
	}
	return raw
}

// recordedFindingsBody is the claim body while a batched pass holds accepted
// findings mid-run: the same findings-recorded record the frozen path writes,
// with the pass still running underneath it.
func recordedFindingsBody(pass int, cfg *config.Config) string {
	return fmt.Sprintf("**crossrev — reviewing, %s**\n\nFindings recorded; the remaining batches are still running.",
		PassLabel(pass, atoi(cfg.Get(".policy.max_passes_per_cycle"))))
}

// mergePayloads folds every accepted batch payload into one findings
// document for the enrich-and-publish path: the union of findings with the
// last batch's verdict, examined scope and known limits. Generations already
// carry the per-batch coverage; the marker and summary need the union. With
// no payload this run accepted nothing new, so the document is empty rather
// than a judgement: the caller sets the verdict from the carried
// verdicts instead.
func mergePayloads(payloads []json.RawMessage, fallbackVerdict string) json.RawMessage {
	var findings []json.RawMessage
	verdict := ""
	examined := ""
	var limits []string
	for _, payload := range payloads {
		var doc struct {
			Verdict       string            `json:"verdict"`
			Findings      []json.RawMessage `json:"findings"`
			ExaminedScope string            `json:"examined_scope"`
			KnownLimits   []string          `json:"known_limits"`
		}
		if err := json.Unmarshal(payload, &doc); err != nil {
			continue
		}
		findings = append(findings, doc.Findings...)
		if doc.Verdict != "" {
			verdict = doc.Verdict
		}
		if doc.ExaminedScope != "" {
			examined = doc.ExaminedScope
		}
		limits = append(limits, doc.KnownLimits...)
	}
	if verdict == "" {
		verdict = fallbackVerdict
	}
	if verdict == "" {
		verdict = "issues-remain"
	}
	merged, err := json.Marshal(struct {
		Verdict       string            `json:"verdict"`
		BlockedReason *string           `json:"blocked_reason"`
		Findings      []json.RawMessage `json:"findings"`
		ExaminedScope string            `json:"examined_scope"`
		KnownLimits   []string          `json:"known_limits"`
	}{Verdict: verdict, BlockedReason: nil, Findings: findings, ExaminedScope: examined, KnownLimits: limits})
	if err != nil {
		return payloads[0]
	}
	return merged
}

// verdictFromPayload reads the reviewer's verdict out of one accepted batch
// payload. The last accepted batch's verdict stands for the pass; every
// batch answers the same verdict field the frozen path reads.
func verdictFromPayload(payload json.RawMessage) string {
	var doc struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return ""
	}
	return doc.Verdict
}

// coveredPass carries a fully covered pass's findings, verdict and scope
// claims to the existing publication path. Result carries it as an
// unexported value through Covered so the frozen path keeps its own
// signature.
type coveredPass struct {
	findings []Finding
	verdict  string
	envelope *harness.Envelope
	payload  json.RawMessage
	examined []string
	limits   []string
}

var _ validate.ReviewExpectations
