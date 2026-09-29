package intel

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// Packing budgets for one review pass. A pass admits at most MaxUnitsPerPass
// reviewable required files; skipped generated files do not consume a slot.
// Whole-file calls hold at most MaxFilesPerBatch files each; every call's
// fully rendered prompt holds at most the packing limit P the harness's
// input window derives (see budget.go), measured over the whole prompt
// including shared context. A file that fits no call alone splits into
// parts rather than halting the pass.
const (
	MaxFilesPerBatch = 40
	MaxUnitsPerPass  = 400
)

// Carry and halt reasons. review_budget_reached marks files carried past the
// 400-file pass budget for a later pass; shared_context_exceeds_window
// marks shared context alone past the hard limit, which no file fits
// beside. over_budget is recorded, not halted: shared context between
// 0.75 x P and H still runs, with every call measured against H.
const (
	CarryReviewBudgetReached      = "review_budget_reached"
	HaltSharedContextExceedsWindow = "shared_context_exceeds_window"
	LimitOverBudget               = "over_budget"
)

// partSlackBytes reserves the per-call framing a part's content does not
// measure: the numbered file section around it. The verifier loop upstairs
// measures the whole rendered prompt and shrinks the content budget until
// each part's call fits, so this only seeds the first split.
const partSlackBytes = 4 * 1024

// verifyRounds bounds the measure-and-shrink loop that fits each part's
// rendered call inside the call budget.
const verifyRounds = 5

// MeasureCall answers one call's whole rendered prompt in bytes — headers,
// prior context and file content together. Packing calls it rather than
// summing file bodies, because headers and context are what push a
// nominally small call over the budget. The function must be deterministic:
// the same call measures the same size on every call.
type MeasureCall func(call Call) int

// Call is one prompt's worth of input, in path order: whole files, or one
// part of a file that fits no call alone.
type Call struct {
	Files []FileUnit
	Part  *FilePart
}

// FilePart is one slice of a split file: the unit it belongs to, its
// position among the file's parts, and the diff it renders — the repeated
// header with its hunk subset, numbered the way a whole file is.
type FilePart struct {
	Unit  FileUnit
	Index int
	Count int
	Diff  []byte
}

// BatchPlan is the whole pass input: the calls to review in order, the
// files carried past the pass budget, the generated files skipped, whether
// the pass runs over budget, and any halt on shared context the hard limit
// refuses. The next prompt task renders Calls directly — each FileUnit
// carries its change kind, content revision, readable body or access
// reason, each FilePart its positioned diff — so no compatibility layer
// sits between this shape and its consumer.
type BatchPlan struct {
	// Calls holds the scheduled calls in review order. Every call fits
	// the file-count and the rendered-byte budgets.
	Calls []Call
	// Carried holds outstanding files past the 400-reviewable-file pass budget, in path
	// order, for a later pass. Empty means the pass admitted everything.
	Carried []FileUnit
	// CarryReason is review_budget_reached when Carried is non-empty, and
	// empty otherwise.
	CarryReason string
	// Skipped holds the admitted generated files that would need splitting
	// to fit one rendered prompt, in path order. A recognised generated
	// file is skipped rather than split: its content is machine-shaped,
	// and a split review of it costs calls without judging intent.
	// Packing continues past a skip; the caller records each in the
	// scope's exclusions with SkipReason.
	Skipped []FileUnit
	// HaltReason is shared_context_exceeds_window when the shared context
	// alone overflows the hard limit, and empty otherwise. No call is
	// scheduled under a halt.
	HaltReason string
	// OverBudget reports that the shared context alone sits between
	// 0.75 x P and H: the pass runs with every call measured against H
	// and records over_budget.
	OverBudget bool
}

// PlanOptions tunes one PlanCalls run.
type PlanOptions struct {
	// Limits carries P and H in bytes for the calling harness and model.
	Limits Limits
	// SharedBytes is the rendered shared context alone: the prompt with
	// no file in it. Calls measure the whole prompt, so packing charges
	// every candidate against this base.
	SharedBytes int
	// MaxFilesPerCall caps whole files per call. Zero reads
	// MaxFilesPerBatch.
	MaxFilesPerCall int
	// MaxUnitsPerPass caps admitted reviewable files per pass. Zero
	// reads MaxUnitsPerPass.
	MaxUnitsPerPass int
}

// SkipReason records why a generated file was skipped: the signal that
// matched, the file's byte size and the packing limit it would need a
// split to fit. The reason text is the exclusion record the generation
// and the warning carry.
func SkipReason(unit FileUnit, budgetBytes int) string {
	return SkipReasonText(unit.Generated, len(unit.Body), budgetBytes)
}

// SkipReasonText is SkipReason for a signal, a byte size and the packing
// limit in force. The warning renders from this text, so a caller that
// already measured the body does not allocate it again. ParseSkipReason
// reads the same text back.
func SkipReasonText(signal string, size, budget int) string {
	return fmt.Sprintf("generated (%s), %d bytes, over the %d-byte prompt budget", signal, size, budget)
}

// ParseSkipReason reads a reason SkipReasonText wrote. ok is false for a
// policy exclusion or any other text, so a generation's exclusion list can
// be split into skips and repository policy without a second field.
func ParseSkipReason(reason string) (signal string, size, budget int, ok bool) {
	const prefix = "generated ("
	const afterSignal = "), "
	const afterSize = " bytes, over the "
	const budgetTail = "-byte prompt budget"
	rest, found := strings.CutPrefix(reason, prefix)
	if !found {
		return "", 0, 0, false
	}
	signal, rest, found = strings.Cut(rest, afterSignal)
	if !found || signal == "" {
		return "", 0, 0, false
	}
	sizeText, rest, found := strings.Cut(rest, afterSize)
	if !found || !strings.HasSuffix(rest, budgetTail) {
		return "", 0, 0, false
	}
	budgetText := strings.TrimSuffix(rest, budgetTail)
	var err error
	size, err = strconv.Atoi(sizeText)
	if err != nil || size < 0 {
		return "", 0, 0, false
	}
	budget, err = strconv.Atoi(budgetText)
	if err != nil || budget < 0 {
		return "", 0, 0, false
	}
	return signal, size, budget, true
}

// PlanCalls packs the scope's outstanding required files — those with no accepted
// verdict — into deterministic path-ordered calls under the file-count and
// rendered-byte budgets. Accepted verdicts come from the current coverage
// generation, so a resumed pass packs only what remains. Packing measures
// only through render: a call is admitted when measure says its complete
// prompt fits.
//
// Files pack in path order: a file that fits alone joins the current call,
// else closes it. A file alone in an empty call splits into parts at hunk
// boundaries — one oversized hunk into line chunks, each with its header
// and gutter numbers — and each part becomes its own call. A recognised
// generated file whose rendering would need splitting is skipped instead,
// and packing continues; plain files always split rather than halting.
// Only shared context alone past the hard limit halts the pass.
func PlanCalls(scope Scope, accepted map[core.UnitID]bool, opts PlanOptions, measure MeasureCall) BatchPlan {
	var plan BatchPlan
	maxFiles := opts.MaxFilesPerCall
	if maxFiles <= 0 {
		maxFiles = MaxFilesPerBatch
	}
	maxUnits := opts.MaxUnitsPerPass
	if maxUnits <= 0 {
		maxUnits = MaxUnitsPerPass
	}
	callBudget := opts.Limits.PackBytes
	if callBudget <= 0 || opts.Limits.HardBytes <= 0 {
		plan.HaltReason = HaltSharedContextExceedsWindow
		return plan
	}
	if opts.Limits.SharedContextHalts(opts.SharedBytes) {
		plan.HaltReason = HaltSharedContextExceedsWindow
		return plan
	}
	// Shared context between 0.75 x P and H still runs and records
	// over_budget; past P every call measures against H instead.
	plan.OverBudget = opts.Limits.OverBudget(opts.SharedBytes)
	if opts.SharedBytes > callBudget {
		callBudget = opts.Limits.HardBytes
	}
	outstanding := make([]FileUnit, 0, len(scope.Required))
	for _, unit := range scope.Required {
		if accepted != nil && accepted[unit.ID] {
			continue
		}
		outstanding = append(outstanding, unit)
	}
	sort.Slice(outstanding, func(i, j int) bool { return outstanding[i].Path < outstanding[j].Path })

	var current []FileUnit
	admittedCount := 0
	flush := func() {
		if len(current) > 0 {
			plan.Calls = append(plan.Calls, Call{Files: current})
			current = nil
		}
	}
	fits := func(files []FileUnit) bool {
		return measure(Call{Files: files}) <= callBudget
	}
	for i, unit := range outstanding {
		if admittedCount == maxUnits {
			plan.Carried = append(plan.Carried, outstanding[i:]...)
			plan.CarryReason = CarryReviewBudgetReached
			break
		}
		if len(current) >= maxFiles {
			flush()
		}
		candidate := make([]FileUnit, len(current)+1)
		copy(candidate, current)
		candidate[len(current)] = unit
		if fits(candidate) {
			current = candidate
			admittedCount++
			continue
		}
		if len(current) > 0 {
			flush()
			if single := []FileUnit{unit}; fits(single) {
				current = single
				admittedCount++
				continue
			}
		}
		// The file fits no call alone: a recognised generated file
		// skips, and packing continues past it; anything else splits.
		if unit.Generated != "" {
			plan.Skipped = append(plan.Skipped, unit)
			continue
		}
		for _, part := range splitAndVerify(unit, callBudget, opts.SharedBytes, measure) {
			part := part
			plan.Calls = append(plan.Calls, Call{Part: &part})
		}
		admittedCount++
	}
	flush()
	return plan
}

// splitAndVerify cuts one oversized file into parts and fits each part's
// rendered call inside the call budget: split at the content budget, then
// measure every part and shrink the ones still over until they fit or the
// rounds run out. A single line past the budget is kept whole — it cannot
// split further — so the caller always reviews the whole file.
func splitAndVerify(unit FileUnit, callBudget, sharedBytes int, measure MeasureCall) []FilePart {
	contentBudget := callBudget - sharedBytes - partSlackBytes
	if contentBudget < 512 {
		contentBudget = 512
	}
	header, blocks := unitBlocks(unit)
	if len(blocks) == 0 {
		return []FilePart{{Unit: unit, Index: 0, Count: 1}}
	}
	contents := repackContent(header, blocks, contentBudget)
	for round := 0; round < verifyRounds; round++ {
		var rebuilt []partContent
		settled := true
		for _, pc := range contents {
			part := FilePart{Unit: unit, Diff: pc.bytes()}
			if measure(Call{Part: &part}) <= callBudget {
				rebuilt = append(rebuilt, pc)
				continue
			}
			settled = false
			rebuilt = append(rebuilt, shrinkContent(pc, contentBudget)...)
		}
		contents = rebuilt
		if settled {
			break
		}
	}
	out := make([]FilePart, 0, len(contents))
	for i, pc := range contents {
		out = append(out, FilePart{Unit: unit, Index: i, Count: len(contents), Diff: pc.bytes()})
	}
	return out
}

// shrinkContent repacks one over-budget part's blocks at half its hunk
// bytes, halving again toward single lines: each round moves strictly
// toward parts the call budget holds.
func shrinkContent(pc partContent, contentBudget int) []partContent {
	half := pc.contentBytes() / 2
	if half < 1 {
		half = 1
	}
	if half >= contentBudget {
		half = contentBudget / 2
		if half < 1 {
			half = 1
		}
	}
	return repackContent(pc.header, pc.blocks, half)
}
