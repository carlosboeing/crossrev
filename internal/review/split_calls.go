package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// pendingSplit is one split file's in-memory part verdicts: every part
// judged in this run, merged when the last part lands. Part verdicts and
// findings stay in memory and leave out every intermediate publication,
// and the map dies with the run, so an interrupted pass restarts the file
// from part 1 with nothing in the generation or on the claim.
type pendingSplit struct {
	unit     intel.FileUnit
	count    int
	verdicts []string
	numbers  [][]int
	reasons  []string
	evidence [][]prstate.Evidence
	findings [][]Finding
	payloads []json.RawMessage
	diffs    [][]byte
	examined []string
	limits   []string
	verdict  string
}

// invokePartCall runs one split-file part's prompt through the harness
// with the part-scoped validation seam, and accumulates the accepted
// answer in memory. It answers merged once every part of the file has a
// verdict in this pass, with the merged verdict, supplied measurement
// and payload the accept path publishes; until then it answers
// unmerged, having recorded nothing but the call's run-log line, usage
// and terminal progress. An error fails the leg the way a batch error
// does, and the in-memory parts never reach a publication.
func (l *Leg) invokePartCall(ctx context.Context, req Request, loaded Context, settings legSettings, scope intel.Scope, shared batchContext, part *intel.FilePart, withConfirmation bool, call, total int, outcome *batchOutcome, pending map[core.UnitID]*pendingSplit, out *Result) (merged bool, verdicts map[core.UnitID]recordVerdict, supplied map[core.UnitID]prstate.SuppliedInput, payload json.RawMessage, envelope harness.Envelope, examined []string, limits []string, retErr error) {
	expected, _ := partExpectations(part, scope.Base, scope.Head)
	if shared.diffErr != nil {
		return false, nil, nil, nil, harness.Envelope{}, nil, nil, shared.diffErr
	}
	promptBytes := shared.renderPart(part, scope.Base, scope.Head, withConfirmation)
	start := l.now()
	answer, env, batchMsgs, err := l.invokePrompt(ctx, req, loaded, settings, expected, promptBytes, call)
	ms := l.now().Sub(start).Milliseconds()
	out.Messages = append(out.Messages, batchMsgs...)
	if err != nil {
		return false, nil, nil, nil, harness.Envelope{}, nil, nil, err
	}
	l.logAcceptedCall(call, promptBytes, len(part.Diff), env, ms)
	parsed, ex, li, err := verdictsFromPayload(answer, []intel.FileUnit{part.Unit})
	if err != nil {
		return false, nil, nil, nil, harness.Envelope{}, nil, nil, err
	}
	disp, ok := parsed[part.Unit.ID]
	if !ok {
		return false, nil, nil, nil, harness.Envelope{}, nil, nil, fmt.Errorf("the part answer names no verdict for %s", part.Unit.Path)
	}
	ps, ok := pending[part.Unit.ID]
	if !ok {
		ps = &pendingSplit{
			unit:     part.Unit,
			count:    part.Count,
			verdicts: make([]string, part.Count),
			numbers:  make([][]int, part.Count),
			reasons:  make([]string, part.Count),
			evidence: make([][]prstate.Evidence, part.Count),
			findings: make([][]Finding, part.Count),
			payloads: make([]json.RawMessage, part.Count),
			diffs:    make([][]byte, part.Count),
		}
		pending[part.Unit.ID] = ps
	}
	numbers := make([]int, 0, len(disp.FindingIDs))
	for _, id := range disp.FindingIDs {
		n, err := strconv.Atoi(id)
		if err != nil {
			return false, nil, nil, nil, harness.Envelope{}, nil, nil, fmt.Errorf("the part answer names finding %q for %s", id, part.Unit.Path)
		}
		numbers = append(numbers, n)
	}
	ps.verdicts[part.Index] = disp.Verdict
	ps.numbers[part.Index] = numbers
	ps.reasons[part.Index] = disp.Reason
	ps.evidence[part.Index] = disp.Evidence
	ps.findings[part.Index] = findingsFromPayload(answer)
	ps.payloads[part.Index] = answer
	ps.diffs[part.Index] = part.Diff
	ps.examined = append(ps.examined, ex)
	ps.limits = append(ps.limits, li...)
	if v := verdictFromPayload(answer); v != "" {
		ps.verdict = v
	}
	out.Messages = append(out.Messages, outcome.addEnvelope(env)...)
	// The batch loop prints the merged file's own progress line after the
	// merge returns, so this call prints one only while the file still has
	// unjudged parts: printing on the merging call too would show the same
	// "Batch N of M" line twice, the first with a covered count from
	// before the verdicts were recorded.
	allJudged := true
	for _, judged := range ps.judged() {
		if !judged {
			allJudged = false
			break
		}
	}
	if !allJudged {
		covered := 0
		for _, unit := range scope.Required {
			if _, ok := outcome.verdicts[unit.ID]; ok {
				covered++
			}
		}
		line := ui.Say(batchProgressLine(call, total, covered, len(scope.Required)))
		if l.Progress != nil {
			l.Progress(line)
		} else {
			out.Messages = append(out.Messages, line)
		}
		return false, nil, nil, nil, harness.Envelope{}, nil, nil, nil
	}
	verdicts, supplied, payload = mergePendingSplit(ps)
	return true, verdicts, supplied, payload, env, ps.examined, ps.limits, nil
}

// judged reports which parts have an accepted verdict.
func (ps *pendingSplit) judged() []bool {
	out := make([]bool, ps.count)
	for i := 0; i < ps.count; i++ {
		out[i] = ps.verdicts[i] != ""
	}
	return out
}

// mergePendingSplit folds every part's accepted verdict into the split
// file's single verdict, supplied measurement and payload. Part-local
// finding numbers map into the file's merged finding order — the parts'
// findings concatenated in part order — so the merged finding numbers
// index the merged finding list the way a batch-local number indexes its
// batch's. The supplied digest covers the concatenated part diffs in
// order, in the file's own supplied form.
func mergePendingSplit(ps *pendingSplit) (map[core.UnitID]recordVerdict, map[core.UnitID]prstate.SuppliedInput, json.RawMessage) {
	unit := ps.unit
	split := make([]intel.SplitVerdict, ps.count)
	base := 0
	var evidence []prstate.Evidence
	var reasons []string
	var diffs []byte
	for i := 0; i < ps.count; i++ {
		mapped := make([]int, 0, len(ps.numbers[i]))
		for _, n := range ps.numbers[i] {
			mapped = append(mapped, base+n)
		}
		split[i] = intel.SplitVerdict{Verdict: ps.verdicts[i], FindingNumbers: mapped}
		base += len(ps.findings[i])
		evidence = append(evidence, ps.evidence[i]...)
		if ps.reasons[i] != "" {
			reasons = append(reasons, ps.reasons[i])
		}
		diffs = append(diffs, ps.diffs[i]...)
	}
	verdict, numbers := intel.MergeSplitVerdicts(split)
	ids := make([]string, 0, len(numbers))
	for _, n := range numbers {
		ids = append(ids, strconv.Itoa(n))
	}
	verdicts := map[core.UnitID]recordVerdict{
		unit.ID: {Verdict: verdict, FindingIDs: ids, Evidence: evidence, Reason: strings.Join(reasons, "; ")},
	}
	supplied := map[core.UnitID]prstate.SuppliedInput{
		unit.ID: {Digest: core.BodyDigestHex(diffs), Form: mergedSuppliedForm(unit.Form)},
	}
	return verdicts, supplied, mergePartPayload(ps)
}

// mergedSuppliedForm answers the supplied form a merged split file
// records: the file's own form, the way a whole-file record reads it.
func mergedSuppliedForm(form intel.InputForm) string {
	switch form {
	case intel.FormHunksContext:
		return prstate.SuppliedFormHunksContext
	case intel.FormDiffOnly:
		return prstate.SuppliedFormDiffOnly
	default:
		return prstate.SuppliedFormFullText
	}
}

// mergePartPayload folds the parts' accepted payloads into one finding
// document for the enrich-and-publish path: the union of the parts'
// findings in part order, the last part's verdict, and every part's
// scope claims. Generations already carry the merged coverage; the
// marker and summary need the union.
func mergePartPayload(ps *pendingSplit) json.RawMessage {
	verdict := ps.verdict
	if verdict == "" {
		verdict = "issues-remain"
	}
	examined := ""
	for i := len(ps.examined) - 1; i >= 0; i-- {
		if ps.examined[i] != "" {
			examined = ps.examined[i]
			break
		}
	}
	merged, err := json.Marshal(struct {
		Verdict       string            `json:"verdict"`
		BlockedReason *string           `json:"blocked_reason"`
		Findings      json.RawMessage   `json:"findings"`
		ExaminedScope string            `json:"examined_scope"`
		KnownLimits   []string          `json:"known_limits"`
	}{Verdict: verdict, BlockedReason: nil, Findings: unionRawFindings(ps.payloads), ExaminedScope: examined, KnownLimits: ps.limits})
	if err != nil {
		return ps.payloads[len(ps.payloads)-1]
	}
	return merged
}
