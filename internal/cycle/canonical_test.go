package cycle

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
)

// cycle/pairing.go reads .reviewer.harness today. A plural-only config must
// pair on the harness it names, not on the singular default sitting
// underneath it in the merge: reviewer codex serves review here while the
// named agy does not, so a pairing that read the singular key would serve.
func TestPairingReadsTheCanonicalReviewer(t *testing.T) {
	doc := descriptorWith(t, map[string][]string{"agy": {"resolve"}})
	merged := config.NewObject()
	reviewer := config.NewObject()
	reviewer.Set("harness", "codex")
	merged.Set("reviewer", reviewer)
	slot := config.NewObject()
	slot.Set("id", "reviewer1")
	slot.Set("harness", "agy")
	merged.Set("reviewers", []any{slot})
	merged.Set("resolver", legObject("claude"))
	cfg := &config.Config{Merged: merged}

	wantFatal(t, Pairing(doc, cfg)(""),
		"the harness 'agy' cannot serve the review leg",
		"CrossRev runs the review leg on claude, codex, grok and opencode. Antigravity is limited to the resolve leg.")
}
