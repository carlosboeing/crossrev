// budget.go — one leg's prompt budget from the descriptor window and the
// pinned model.
//
// The descriptor names each harness's input window in tokens
// (window_tokens: Codex measured, the rest assumed until measured). A
// pinned model whose price-table entry names a smaller max_input_tokens
// narrows it: the model cannot read past its own window whatever harness
// carries it. The byte arithmetic itself lives in internal/intel, which
// this tier may import; the legs read one value per call from here.

package harness

import (
	"github.com/carlosboeing/crossrev/internal/intel"
)

// EffectiveWindow answers the input window in tokens for a harness and
// model pair: the descriptor's window_tokens, or the smaller of the two
// where a pinned model names a max_input_tokens in the price table. An
// empty model keeps the harness window; an unknown harness answers zero
// and false, and the legs refuse it before any budget is read.
func (d Document) EffectiveWindow(name, model string) (int, bool) {
	window, found := d.WindowTokens(name)
	if !found || window <= 0 {
		return 0, false
	}
	if model == "" {
		return window, true
	}
	table, err := PriceTable()
	if err != nil {
		return window, true
	}
	if modelWindow, ok := table.MaxInputTokens(model); ok && modelWindow < window {
		return modelWindow, true
	}
	return window, true
}

// InputBudget answers the byte budget for one leg's harness and model:
// the soft per-call packing limit and the hard prompt limit over the
// whole rendered prompt including shared context. It fails closed on an
// unknown harness or a missing window, so no leg packs from a guess.
func (d Document) InputBudget(name, model string) (intel.Limits, bool) {
	window, found := d.EffectiveWindow(name, model)
	if !found {
		return intel.Limits{}, false
	}
	return intel.ComputeLimits(window, d.ArgvTransport(name)), true
}
