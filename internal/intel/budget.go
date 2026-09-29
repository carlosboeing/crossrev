package intel

// Budget arithmetic for one review or resolve prompt.
//
// Every limit derives from the harness's input window W in tokens: the
// descriptor's window_tokens, narrowed by a pinned model's max_input_tokens
// where the price table names a smaller one. Tokens become bytes at
// BytesPerToken, the measured mean for the prompts CrossRev renders.
//
// The soft budget B is the per-call packing target: the smaller of
// SoftBudgetTokenCap tokens and SoftBudgetFraction of the window. The hard
// limit H is HardBudgetFraction of the window, except on argv transports,
// where the process argument limit caps it at ArgvHardCapBytes whatever the
// window says. The packing limit P is the smaller of the two, measured over
// the whole rendered prompt including shared context.
const (
	// BytesPerToken is the measured mean byte cost of one prompt token.
	BytesPerToken = 3.9
	// SoftBudgetTokenCap bounds the soft per-call budget in tokens.
	SoftBudgetTokenCap = 100000
	// SoftBudgetFraction is the soft per-call budget as a share of W.
	SoftBudgetFraction = 0.4
	// HardBudgetFraction is the hard prompt limit as a share of W.
	HardBudgetFraction = 0.8
	// ArgvHardCapBytes caps the hard limit on argv transports: 120 KiB.
	ArgvHardCapBytes = 120 * 1024
	// OverBudgetFraction is where shared context alone turns a call
	// over budget: between OverBudgetFraction of P and H the pass runs
	// and records over_budget; past H it halts instead.
	OverBudgetFraction = 0.75
)

// Limits is one harness's prompt budget in bytes: the packing target P,
// the hard limit H, and the window they derive from.
type Limits struct {
	// WindowTokens is W, the harness's input window in tokens.
	WindowTokens int
	// PackBytes is P, the per-call packing limit over the whole
	// rendered prompt including shared context.
	PackBytes int
	// HardBytes is H, the hard prompt limit.
	HardBytes int
}

// ComputeLimits derives the byte budget from a token window. argv caps the
// hard limit at ArgvHardCapBytes; every other transport takes the window's
// own hard limit. A non-positive window answers zero limits, so a caller
// that budgets from an unknown harness packs nothing rather than guessing.
func ComputeLimits(windowTokens int, argv bool) Limits {
	if windowTokens <= 0 {
		return Limits{}
	}
	softTokens := int(SoftBudgetFraction * float64(windowTokens))
	if softTokens > SoftBudgetTokenCap {
		softTokens = SoftBudgetTokenCap
	}
	hardTokens := int(HardBudgetFraction * float64(windowTokens))
	softBytes := int(BytesPerToken * float64(softTokens))
	hardBytes := int(BytesPerToken * float64(hardTokens))
	if argv && hardBytes > ArgvHardCapBytes {
		hardBytes = ArgvHardCapBytes
	}
	packBytes := softBytes
	if hardBytes < packBytes {
		packBytes = hardBytes
	}
	return Limits{WindowTokens: windowTokens, PackBytes: packBytes, HardBytes: hardBytes}
}

// OverBudget reports whether shared context of sharedBytes, alone, runs a
// call over budget without halting it: at or past OverBudgetFraction of P
// and at or below H. Past H the caller halts with
// shared_context_exceeds_window instead.
func (l Limits) OverBudget(sharedBytes int) bool {
	if l.PackBytes <= 0 {
		return false
	}
	floor := int(OverBudgetFraction * float64(l.PackBytes))
	return sharedBytes >= floor && sharedBytes <= l.HardBytes
}

// SharedContextHalts reports whether shared context of sharedBytes, alone,
// exceeds the hard limit: no file fits a prompt whose fixed part already
// overflows it, so the pass halts with shared_context_exceeds_window.
func (l Limits) SharedContextHalts(sharedBytes int) bool {
	return l.HardBytes > 0 && sharedBytes > l.HardBytes
}
