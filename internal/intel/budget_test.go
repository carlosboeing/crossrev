package intel_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/intel"
)

// The per-harness byte budgets, pinned: the soft per-call packing limit P
// and the hard prompt limit H derive from the descriptor window at 3.9
// bytes per token, with argv transports capped at 120 KiB.
func TestComputeLimitsPinsTheShippedWindows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window int
		argv   bool
		pack   int
		hard   int
	}{
		// Codex: B = min(100000, 0.4 x 258400) = 100000 tokens.
		{name: "codex", window: 258400, argv: false, pack: 390000, hard: 806208},
		// Claude: B = 0.4 x 200000 = 80000 tokens.
		{name: "claude", window: 200000, argv: false, pack: 312000, hard: 624000},
		// Argv transports pack under 120 KiB whatever the window says.
		{name: "agy", window: 128000, argv: true, pack: 120 * 1024, hard: 120 * 1024},
		{name: "opencode", window: 128000, argv: true, pack: 120 * 1024, hard: 120 * 1024},
		// A file transport takes the window's own hard limit.
		{name: "grok", window: 128000, argv: false, pack: 199680, hard: 399360},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := intel.ComputeLimits(tc.window, tc.argv)
			if got.WindowTokens != tc.window {
				t.Errorf("WindowTokens = %d, want %d", got.WindowTokens, tc.window)
			}
			if got.PackBytes != tc.pack {
				t.Errorf("PackBytes = %d, want %d", got.PackBytes, tc.pack)
			}
			if got.HardBytes != tc.hard {
				t.Errorf("HardBytes = %d, want %d", got.HardBytes, tc.hard)
			}
		})
	}
}

// A non-positive window packs nothing rather than guessing a budget.
func TestComputeLimitsFailsClosedOnNoWindow(t *testing.T) {
	for _, window := range []int{0, -128000} {
		got := intel.ComputeLimits(window, false)
		if got.PackBytes != 0 || got.HardBytes != 0 {
			t.Errorf("ComputeLimits(%d) = %+v, want zero limits", window, got)
		}
		if got.OverBudget(1) || got.SharedContextHalts(1<<20) {
			t.Errorf("ComputeLimits(%d) answers budget questions with no window", window)
		}
	}
}

// Shared context between 0.75 x P and H runs over budget; past H it halts.
func TestSharedContextBudgetBands(t *testing.T) {
	limits := intel.ComputeLimits(200000, false)
	pack, hard := limits.PackBytes, limits.HardBytes
	floor := int(0.75 * float64(pack))
	for _, shared := range []int{floor, pack, hard} {
		if !limits.OverBudget(shared) {
			t.Errorf("OverBudget(%d) = false, want true between 0.75P=%d and H=%d", shared, floor, hard)
		}
		if limits.SharedContextHalts(shared) {
			t.Errorf("SharedContextHalts(%d) = true, want false at or below H=%d", shared, hard)
		}
	}
	if limits.OverBudget(floor - 1) {
		t.Errorf("OverBudget(%d) = true, want false below 0.75P=%d", floor-1, floor)
	}
	if !limits.SharedContextHalts(hard + 1) {
		t.Errorf("SharedContextHalts(%d) = false, want true past H=%d", hard+1, hard)
	}
	if limits.OverBudget(hard + 1) {
		t.Errorf("OverBudget(%d) = true, want false past H=%d", hard+1, hard)
	}
}
