package intel

import (
	"fmt"
	"sort"

	"github.com/carlosboeing/crossrev/internal/core"
)

// Packing budgets for one review pass. A pass admits at most MaxUnitsPerPass
// required files; batches hold at most MaxFilesPerBatch files each; and every
// batch's fully rendered prompt holds at most MaxPromptBytes bytes. The byte
// budget is 180 KiB, matching the frozen batching fixture.
const (
	MaxFilesPerBatch = 40
	MaxUnitsPerPass  = 400
	MaxPromptBytes   = 180 * 1024
)

// Carry and halt reasons. review_budget_reached marks files carried past the
// 400-file pass budget for a later pass; input_exceeds_budget marks a file
// that cannot fit alone in one rendered prompt and stays outstanding.
const (
	CarryReviewBudgetReached = "review_budget_reached"
	HaltInputExceedsBudget   = "input_exceeds_budget"
)

// RenderBatch measures one batch's complete rendered prompt in bytes —
// headers, prior context and file content together. Packing calls it rather
// than summing file bodies, because headers and context are what push a
// nominally small batch over the budget. The function must be deterministic:
// the same files measure the same size on every call.
type RenderBatch func(files []FileUnit) int

// Batch is one prompt's worth of required files, in path order.
type Batch struct {
	Files []FileUnit
}

// BatchPlan is the whole pass input: the batches to review in order, the
// files carried past the pass budget, the generated files skipped, and any
// halt on a file that cannot fit alone. The next prompt task renders Batches
// directly — each FileUnit carries its change kind, content revision,
// readable body or access reason — so no compatibility layer sits between
// this shape and its consumer.
type BatchPlan struct {
	// Batches holds the scheduled batches in review order. Every batch fits
	// both the file-count and the rendered-byte budgets.
	Batches []Batch
	// Carried holds outstanding files past the 400-file pass budget, in path
	// order, for a later pass. Empty means the pass admitted everything.
	Carried []FileUnit
	// CarryReason is review_budget_reached when Carried is non-empty, and
	// empty otherwise.
	CarryReason string
	// Skipped holds the admitted generated files that cannot fit alone in a
	// rendered prompt, in path order. Packing continues past a skip; the
	// caller records each in the scope's exclusions with SkipReason.
	Skipped []FileUnit
	// HaltReason is input_exceeds_budget when one file cannot fit alone in a
	// rendered prompt, and empty otherwise. Batches scheduled before the halt
	// stand; nothing from the halting file on is scheduled.
	HaltReason string
	// HaltPath names the file that cannot fit alone. Set only with HaltReason.
	HaltPath string
	// Unbatched holds the admitted files left unscheduled by a halt, starting
	// with HaltPath, in path order. Empty unless HaltReason is set.
	Unbatched []FileUnit
}

// SkipReason records why a generated file was skipped: the signal that
// matched, the file's byte size and the prompt budget it could not fit. The
// reason text is the exclusion record the generation and the warning carry.
func SkipReason(unit FileUnit) string {
	return fmt.Sprintf("generated (%s), %d bytes, over the %d-byte prompt budget", unit.Generated, len(unit.Body), MaxPromptBytes)
}

// Batches packs the scope's outstanding required files — those with no accepted
// verdict — into deterministic path-ordered batches under the three
// budgets. Accepted verdicts come from the current coverage generation, so
// a resumed pass packs only what remains. Packing measures only through
// render: a batch is admitted when render says its complete prompt fits. A
// file that does not fit alone is skipped when a built-in rule recognised it
// as generated, and packing continues; any other file that does not fit
// alone halts with input_exceeds_budget rather than splitting or deferring
// silently.
func Batches(scope Scope, accepted map[core.UnitID]bool, render RenderBatch) BatchPlan {
	var plan BatchPlan
	outstanding := make([]FileUnit, 0, len(scope.Required))
	for _, unit := range scope.Required {
		if accepted != nil && accepted[unit.ID] {
			continue
		}
		outstanding = append(outstanding, unit)
	}
	sort.Slice(outstanding, func(i, j int) bool { return outstanding[i].Path < outstanding[j].Path })

	admitted := outstanding
	if len(outstanding) > MaxUnitsPerPass {
		admitted = outstanding[:MaxUnitsPerPass]
		plan.Carried = append(plan.Carried, outstanding[MaxUnitsPerPass:]...)
		plan.CarryReason = CarryReviewBudgetReached
	}

	var current []FileUnit
	flush := func() {
		if len(current) > 0 {
			plan.Batches = append(plan.Batches, Batch{Files: current})
			current = nil
		}
	}
	halt := func(index int) BatchPlan {
		plan.HaltReason = HaltInputExceedsBudget
		plan.HaltPath = admitted[index].Path
		plan.Unbatched = append(plan.Unbatched, admitted[index:]...)
		return plan
	}
	// skipOrHalt answers whether packing continues: a unit that cannot fit
	// alone skips when it carries a generated signal and halts otherwise.
	skipOrHalt := func(i int, unit FileUnit) (BatchPlan, bool) {
		if unit.Generated != "" {
			plan.Skipped = append(plan.Skipped, unit)
			return plan, true
		}
		return halt(i), false
	}
	for i, unit := range admitted {
		if len(current) >= MaxFilesPerBatch {
			flush()
		}
		candidate := make([]FileUnit, len(current)+1)
		copy(candidate, current)
		candidate[len(current)] = unit
		if render(candidate) <= MaxPromptBytes {
			current = candidate
			continue
		}
		if len(current) == 0 {
			if next, ok := skipOrHalt(i, unit); !ok {
				return next
			}
			continue
		}
		flush()
		if single := []FileUnit{unit}; render(single) > MaxPromptBytes {
			if next, ok := skipOrHalt(i, unit); !ok {
				return next
			}
		} else {
			current = single
		}
	}
	flush()
	return plan
}
