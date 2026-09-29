package harness_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// The shipped windows read back per harness: Codex measured, the rest
// assumed until measured.
func TestShippedWindowsReadBack(t *testing.T) {
	doc := descriptors(t)
	for _, tc := range []struct {
		name   string
		window int
		argv   bool
	}{
		{name: "codex", window: 258400, argv: false},
		{name: "claude", window: 200000, argv: false},
		{name: "agy", window: 128000, argv: true},
		{name: "grok", window: 128000, argv: false},
		{name: "opencode", window: 128000, argv: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			window, found := doc.WindowTokens(tc.name)
			if !found || window != tc.window {
				t.Errorf("WindowTokens(%q) = %d, %v; want %d, true", tc.name, window, found, tc.window)
			}
			if got := doc.ArgvTransport(tc.name); got != tc.argv {
				t.Errorf("ArgvTransport(%q) = %v, want %v", tc.name, got, tc.argv)
			}
		})
	}
	if _, found := doc.WindowTokens("nosuch"); found {
		t.Error("WindowTokens(nosuch) answers a window for an unknown harness")
	}
	if doc.ArgvTransport("nosuch") {
		t.Error("ArgvTransport(nosuch) answers true for an unknown harness")
	}
}

// A missing or unusable window_tokens is refused at load, after every
// check the shell-era validator pins, so no new sentence moves an old one.
func TestValidatorRefusesABrokenWindow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(map[string]any)
	}{
		{name: "absent window_tokens", apply: func(d map[string]any) {
			delete(d["harnesses"].([]any)[0].(map[string]any), "window_tokens")
		}},
		{name: "zero window_tokens", apply: func(d map[string]any) {
			d["harnesses"].([]any)[0].(map[string]any)["window_tokens"] = float64(0)
		}},
		{name: "negative window_tokens", apply: func(d map[string]any) {
			d["harnesses"].([]any)[0].(map[string]any)["window_tokens"] = float64(-128000)
		}},
		{name: "fractional window_tokens", apply: func(d map[string]any) {
			d["harnesses"].([]any)[0].(map[string]any)["window_tokens"] = 128000.5
		}},
		{name: "window_tokens as a string", apply: func(d map[string]any) {
			d["harnesses"].([]any)[0].(map[string]any)["window_tokens"] = "128000"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problem := harness.Validate(mutate(t, tc.apply))
			want := "harness claude carries a window_tokens that is not a whole number of tokens above zero"
			if problem != want {
				t.Errorf("Validate = %q, want %q", problem, want)
			}
		})
	}
}

// The effective window is the descriptor's own where no pinned model
// narrows it, and the smaller of the two where the price table names one.
func TestEffectiveWindowTakesTheSmallerOfHarnessAndModel(t *testing.T) {
	doc := descriptors(t)
	if got, found := doc.EffectiveWindow("codex", ""); !found || got != 258400 {
		t.Errorf("EffectiveWindow(codex, unset) = %d, %v; want 258400, true", got, found)
	}
	if got, found := doc.EffectiveWindow("codex", "claude-sonnet-5"); !found || got != 200000 {
		t.Errorf("EffectiveWindow(codex, claude-sonnet-5) = %d, %v; want 200000, true", got, found)
	}
	if got, found := doc.EffectiveWindow("claude", "claude-sonnet-5"); !found || got != 200000 {
		t.Errorf("EffectiveWindow(claude, claude-sonnet-5) = %d, %v; want 200000, true", got, found)
	}
	if got, found := doc.EffectiveWindow("agy", "xai/grok-4.6"); !found || got != 128000 {
		t.Errorf("EffectiveWindow(agy, xai/grok-4.6) = %d, %v; want 128000, true", got, found)
	}
	if _, found := doc.EffectiveWindow("nosuch", ""); found {
		t.Error("EffectiveWindow(nosuch) answers a window for an unknown harness")
	}
}

// The byte budget follows the transport: argv harnesses pack under
// 120 KiB, stdin harnesses to their window's soft budget.
func TestInputBudgetFollowsTheTransport(t *testing.T) {
	doc := descriptors(t)
	agy, found := doc.InputBudget("agy", "")
	if !found {
		t.Fatal("InputBudget(agy) is not found")
	}
	if agy.PackBytes != 120*1024 || agy.HardBytes != 120*1024 {
		t.Errorf("agy budget = %+v, want pack and hard at 120 KiB", agy)
	}
	codex, found := doc.InputBudget("codex", "")
	if !found {
		t.Fatal("InputBudget(codex) is not found")
	}
	if codex.PackBytes != 390000 {
		t.Errorf("codex pack = %d, want 390000", codex.PackBytes)
	}
	if _, found := doc.InputBudget("nosuch", ""); found {
		t.Error("InputBudget(nosuch) budgets from an unknown harness")
	}
}

// The price table names a window per listed model, and none where it
// lists none.
func TestPriceTableNamesAModelWindow(t *testing.T) {
	table, err := harness.PriceTable()
	if err != nil {
		t.Fatalf("PriceTable: %v", err)
	}
	for model, want := range map[string]int{
		"claude-sonnet-5": 200000,
		"gpt-5.6":         400000,
		"xai/grok-4.6":    2000000,
	} {
		if got, ok := table.MaxInputTokens(model); !ok || got != want {
			t.Errorf("MaxInputTokens(%q) = %d, %v; want %d, true", model, got, ok, want)
		}
	}
	if _, ok := table.MaxInputTokens("nosuch-model"); ok {
		t.Error("MaxInputTokens(nosuch-model) answers a window for an unlisted model")
	}
}
