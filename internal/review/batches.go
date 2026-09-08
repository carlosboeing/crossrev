package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prompt"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// batchOutcome is what one batch run settled: the dispositions it accepted,
// the findings they name, and the scope claims the manifest carries.
type batchOutcome struct {
	dispositions map[core.UnitID]recordDisposition
	findings     []Finding
	verdict      string
	envelope     *harness.Envelope
	payload      json.RawMessage
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
	outcome := batchOutcome{dispositions: map[core.UnitID]recordDisposition{}}
	current, err := l.currentGeneration(ctx, loaded, scope.Base, scope.Head, scope.Engine)
	if err != nil {
		return err
	}
	gen := current.Gen
	accepted := acceptedFromGeneration(current, scope.Base, scope.Head, scope.Engine)
	for unitID := range accepted {
		outcome.dispositions[unitID] = recordDisposition{}
	}
	advisory := intel.AdvisoryFiles(ctx, scope, scopeSearcher{vcs: l.VCS})
	render := func(files []intel.FileUnit) int {
		return len(l.renderBatchPrompt(ctx, req, loaded, settings, pass, files, scope))
	}
	plan := intel.Batches(scope, accepted, render)
	if plan.HaltReason != "" || len(plan.Carried) > 0 {
		return l.haltPass(ctx, req, loaded, pass, claimID, out, &batchBound{plan: plan, scope: scope})
	}
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
	if _, err := l.publishInitialGeneration(ctx, req, loaded, scope, advisory, gen+1); err != nil {
		return err
	}
	for _, batch := range plan.Batches {
		expected, units := batchExpectations(batch.Files, scope.Base, scope.Head)
		advisoryRefs, excludedRefs := advisoryPromptRefs(scope, advisory)
		payload, envelope, batchMsgs, err := l.invokeBatch(ctx, req, loaded, settings, pass, units, advisoryRefs, excludedRefs, expected)
		out.Messages = append(out.Messages, batchMsgs...)
		if err != nil {
			return err
		}
		dispositions, examined, limits, err := dispositionsFromPayload(payload, batch.Files)
		if err != nil {
			return err
		}
		for id, disp := range dispositions {
			outcome.dispositions[id] = disp
		}
		outcome.verdict = verdictFromPayload(payload)
		if outcome.envelope == nil {
			outcome.envelope = &envelope
			outcome.payload = payload
		}
		outcome.examined = append(outcome.examined, examined)
		outcome.limits = append(outcome.limits, limits...)
		for _, finding := range findingsFromPayload(payload) {
			outcome.findings = append(outcome.findings, finding)
		}
		outcome.batches++
		manifest, _, err := l.publishBatchGeneration(ctx, req, loaded, scope, advisory, gen+outcome.batches+1, outcome.dispositions, outcome.examined, outcome.limits)
		if err != nil {
			if stop, ok := batchStop(err); ok {
				return l.haltPass(ctx, req, loaded, pass, claimID, out, &batchBound{plan: planForStop(plan, stop), scope: scope})
			}
			return err
		}
		marker.CoverageManifestID = prstate.Some(manifest.CommentID())
	}
	return l.finishCoveredPass(ctx, req, loaded, settings, pass, claimID, marker, scope, outcome, out)
}

// batchBound carries a bounded halt out of the batch loop: the 400-file pass
// bound, the 32-shard ledger bound, or the single-file input bound.
type batchBound struct {
	plan  intel.BatchPlan
	scope intel.Scope
}

func (e *batchBound) Error() string {
	if e.plan.HaltReason != "" {
		return fmt.Sprintf("halted: %s on %s", e.plan.HaltReason, e.plan.HaltPath)
	}
	return fmt.Sprintf("halted: %s carries %d files", intel.CarryReviewBudgetReached, len(e.plan.Carried))
}

// publishBatchGeneration publishes one complete generation after an accepted
// batch. The generation accounts for every required file: covered units
// carry their accepted disposition, the rest stay outstanding.
func (l *Leg) publishBatchGeneration(ctx context.Context, req Request, loaded Context, scope intel.Scope, advisory intel.AdvisorySummary, gen int, dispositions map[core.UnitID]recordDisposition, examined []string, limits []string) (prstate.Manifest, prstate.CoverageStop, error) {
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
		Records:     generationRecords(scope, dispositions, pathIndex),
		Advisory:    prstate.Advisory{Count: advisory.Count, Rules: advisory.Rules, Limits: advisoryLimits(advisory)},
		Excluded:    excludedRecords(scope),
		ScopeReport: scopeReportOf(examinedScope, limits),
	}
	stillCurrent := func() error {
		if loaded.PR.HeadRefOid.SHA() != scope.Head.SHA() {
			return fmt.Errorf("the head moved during publication")
		}
		return nil
	}
	if store == nil {
		return prstate.Manifest{}, prstate.CoverageStop{}, fmt.Errorf("no ledger store")
	}
	return prstate.PublishGeneration(ctx, store, loaded.Repo, req.PR, candidate, stillCurrent)
}

// publishInitialGeneration publishes the opening complete generation after
// the claim exists: every uncovered unit outstanding, so a crash before the
// first accepted batch leaves the started claim and the same scope to
// rebuild from.
func (l *Leg) publishInitialGeneration(ctx context.Context, req Request, loaded Context, scope intel.Scope, advisory intel.AdvisorySummary, gen int) (prstate.Manifest, error) {
	manifest, _, err := l.publishBatchGeneration(ctx, req, loaded, scope, advisory, gen, map[core.UnitID]recordDisposition{}, nil, nil)
	return manifest, err
}

// dispositionsFromPayload reads the accepted dispositions out of one accepted
// batch payload: one entry per numbered unit, in prompt order.
func dispositionsFromPayload(payload json.RawMessage, files []intel.FileUnit) (map[core.UnitID]recordDisposition, string, []string, error) {
	var doc struct {
		Coverage []struct {
			UnitNumber     int                `json:"unit_number"`
			Disposition    string             `json:"disposition"`
			FindingNumbers []int              `json:"finding_numbers"`
			Evidence       []prstate.Evidence `json:"evidence"`
			Reason         *string            `json:"reason"`
		} `json:"coverage"`
		ExaminedScope string   `json:"examined_scope"`
		KnownLimits   []string `json:"known_limits"`
		Findings      []struct {
			Number int    `json:"number"`
			ID     string `json:"id"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, "", nil, err
	}
	byNumber := make(map[int]string, len(doc.Findings))
	_ = byNumber
	out := make(map[core.UnitID]recordDisposition, len(files))
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
		out[unit.ID] = recordDisposition{Disposition: entry.Disposition, FindingIDs: ids, Evidence: entry.Evidence, Reason: reason}
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
	marker.State = core.PassIncomplete
	marker.CoverageStop = prstate.Some(stop)
	outstanding := outstandingPaths(bound.scope, map[core.UnitID]bool{})
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
// covered and outstanding totals with the limit name.
func stopForBound(bound *batchBound) prstate.CoverageStop {
	outstanding := bound.plan.Carried
	limit := intel.CarryReviewBudgetReached
	if bound.plan.HaltReason != "" {
		outstanding = bound.plan.Unbatched
		limit = bound.plan.HaltReason
	}
	covered := len(bound.scope.Required) - len(outstanding)
	if covered < 0 {
		covered = 0
	}
	return prstate.CoverageStop{RequiredCount: len(bound.scope.Required), CoveredCount: covered, OutstandingCount: len(outstanding), Limit: limit}
}

// planForStop carries a ledger-bound halt back into a batch plan shape: the
// bound names what the shard limit stopped, and the scope names what stays
// outstanding.
func planForStop(plan intel.BatchPlan, stop prstate.CoverageStop) intel.BatchPlan {
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
func (l *Leg) finishCoveredPass(ctx context.Context, req Request, loaded Context, settings legSettings, pass int, claimID int64, marker prstate.Marker, scope intel.Scope, outcome batchOutcome, out *Result) error {
	_ = ctx
	_ = req
	_ = loaded
	_ = settings
	_ = pass
	_ = claimID
	_ = scope
	out.Marker = marker
	out.Covered = coveredPass{findings: outcome.findings, verdict: outcome.verdict, envelope: outcome.envelope, payload: outcome.payload, examined: outcome.examined, limits: outcome.limits}
	return nil
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
