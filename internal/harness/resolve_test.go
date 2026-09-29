package harness_test

import (
	"reflect"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// The shared resolver rule, read by the resolve leg's settings, the cycle
// pairing check, and the preflight pairing report behind `doctor` and `init`.
func TestRefusedAsResolver(t *testing.T) {
	if !harness.RefusedAsResolver("codex") {
		t.Error(`RefusedAsResolver("codex") = false, want true: codex 0.158.0 with its shell disabled has no file-reading tool`)
	}
	for _, name := range []string{"claude", "agy", "grok", "opencode", "nosuch", "kimi", ""} {
		if harness.RefusedAsResolver(name) {
			t.Errorf("RefusedAsResolver(%q) = true, want false: only codex is refused as a resolver", name)
		}
	}
}

// WorkingResolvers is the descriptor's resolve list minus the refused names,
// in descriptor order — the install hint every refusal below names.
func TestWorkingResolversOnShippedDescriptor(t *testing.T) {
	doc := descriptors(t)
	if got := harness.WorkingResolvers(doc); !reflect.DeepEqual(got, []string{"claude", "agy", "grok", "opencode"}) {
		t.Errorf("WorkingResolvers() = %v, want claude, agy, grok and opencode", got)
	}
	if got := harness.NamesHuman(harness.WorkingResolvers(doc)); got != "claude, agy, grok and opencode" {
		t.Errorf("NamesHuman(WorkingResolvers()) = %q", got)
	}
}

// The shipped default pairing survives the rule: codex reviews, claude
// resolves, and neither side is the refused combination. A default resolver
// of codex would be refused on first run, so this pins the descriptor
// against that regression.
func TestDefaultPairingSurvivesTheResolverRule(t *testing.T) {
	doc := descriptors(t)
	pairing := doc.DefaultPairing()
	if pairing.Resolver == "codex" {
		t.Fatalf("DefaultPairing().Resolver = codex, which the resolver rule refuses")
	}
	if !doc.ServesLeg(pairing.Reviewer, harness.LegReview) {
		t.Errorf("the default reviewer %q does not serve the review leg", pairing.Reviewer)
	}
	if harness.RefusedAsResolver(pairing.Resolver) {
		t.Errorf("the default resolver %q is refused by the resolver rule", pairing.Resolver)
	}
	if !doc.ServesLeg(pairing.Resolver, harness.LegResolve) {
		t.Errorf("the default resolver %q does not serve the resolve leg", pairing.Resolver)
	}
}
