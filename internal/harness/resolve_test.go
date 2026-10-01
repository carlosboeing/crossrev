package harness_test

import (
	"reflect"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// WorkingResolvers is the descriptor's resolve list, in descriptor
// order — the install hint every refusal below names.
func TestWorkingResolversOnShippedDescriptor(t *testing.T) {
	doc := descriptors(t)
	if got := harness.WorkingResolvers(doc); !reflect.DeepEqual(got, []string{"claude", "codex", "agy", "grok", "opencode"}) {
		t.Errorf("WorkingResolvers() = %v, want claude, codex, agy, grok and opencode", got)
	}
	if got := harness.NamesHuman(harness.WorkingResolvers(doc)); got != "claude, codex, agy, grok and opencode" {
		t.Errorf("NamesHuman(WorkingResolvers()) = %q", got)
	}
}

// The shipped default pairing serves both legs: codex reviews, claude
// resolves, and the descriptor lists each side for its leg.
func TestDefaultPairingSurvivesTheResolverRule(t *testing.T) {
	doc := descriptors(t)
	pairing := doc.DefaultPairing()
	if !doc.ServesLeg(pairing.Reviewer, harness.LegReview) {
		t.Errorf("the default reviewer %q does not serve the review leg", pairing.Reviewer)
	}
	if !doc.ServesLeg(pairing.Resolver, harness.LegResolve) {
		t.Errorf("the default resolver %q does not serve the resolve leg", pairing.Resolver)
	}
}
