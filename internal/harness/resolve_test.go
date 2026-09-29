package harness_test

import (
	"reflect"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// The shared resolver rule, read by the resolve leg's settings, the cycle
// pairing check, and the preflight pairing report behind `doctor` and `init`.
// Nothing is refused today: the served read tool serves every resolve leg,
// codex included, so the rule that once refused codex is lifted.
func TestRefusedAsResolver(t *testing.T) {
	for _, name := range []string{"codex", "claude", "agy", "grok", "opencode", "nosuch", "kimi", ""} {
		if harness.RefusedAsResolver(name) {
			t.Errorf("RefusedAsResolver(%q) = true, want false: the served read tool serves every resolve leg", name)
		}
	}
}

// WorkingResolvers is the descriptor's resolve list minus the refused names,
// in descriptor order — the install hint every refusal below names.
func TestWorkingResolversOnShippedDescriptor(t *testing.T) {
	doc := descriptors(t)
	if got := harness.WorkingResolvers(doc); !reflect.DeepEqual(got, []string{"claude", "codex", "agy", "grok", "opencode"}) {
		t.Errorf("WorkingResolvers() = %v, want claude, codex, agy, grok and opencode", got)
	}
	if got := harness.NamesHuman(harness.WorkingResolvers(doc)); got != "claude, codex, agy, grok and opencode" {
		t.Errorf("NamesHuman(WorkingResolvers()) = %q", got)
	}
}

// The shipped default pairing survives the rule: codex reviews, claude
// resolves, and neither side is a refused combination.
func TestDefaultPairingSurvivesTheResolverRule(t *testing.T) {
	doc := descriptors(t)
	pairing := doc.DefaultPairing()
	if harness.RefusedAsResolver(pairing.Resolver) {
		t.Errorf("the default resolver %q is refused by the resolver rule", pairing.Resolver)
	}
	if !doc.ServesLeg(pairing.Reviewer, harness.LegReview) {
		t.Errorf("the default reviewer %q does not serve the review leg", pairing.Reviewer)
	}
	if !doc.ServesLeg(pairing.Resolver, harness.LegResolve) {
		t.Errorf("the default resolver %q does not serve the resolve leg", pairing.Resolver)
	}
}
