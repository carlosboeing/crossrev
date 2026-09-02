package cycle

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// --- fixtures ---------------------------------------------------------------

// descriptorWith is the shipped lib/harnesses.json with the named harnesses'
// `legs` rewritten, which is how every refusal below was measured:
//
//	$ jq '(.harnesses[] | select(.name=="codex") | .legs) |= ["review"]' \
//	    lib/harnesses.json > /tmp/d.json
//	$ NO_COLOR=1 CROSSREV_HARNESS_FILE=/tmp/d.json bash -c 'ROOT=$PWD;
//	    source lib/ui.sh; source lib/harnesses.sh; source lib/run.sh;
//	    _run_assert_harness_serves_leg codex resolve'
//
// Rewriting the shipped file rather than writing a small one keeps the names,
// the product names and the descriptor order the operator's messages are built
// from, and every shipped entry serves both legs, so nothing here can be
// measured against the file as it ships.
func descriptorWith(t *testing.T, legs map[string][]string) harness.Document {
	t.Helper()
	var tree map[string]any
	if err := json.Unmarshal(harness.DescriptorJSON(), &tree); err != nil {
		t.Fatalf("decode the shipped descriptor: %v", err)
	}
	entries, ok := tree["harnesses"].([]any)
	if !ok {
		t.Fatalf("the shipped descriptor has no harnesses array")
	}
	for _, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("a harness entry is not an object")
		}
		name, _ := object["name"].(string)
		if serves, found := legs[name]; found {
			object["legs"] = serves
		}
	}
	raw, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("re-encode the descriptor: %v", err)
	}
	doc, err := harness.Load(raw)
	if err != nil {
		t.Fatalf("harness.Load: %v", err)
	}
	return doc
}

// shippedDescriptor is lib/harnesses.json unchanged: five harnesses, every one
// of them serving both legs.
func shippedDescriptor(t *testing.T) harness.Document {
	t.Helper()
	doc, err := harness.Load(harness.DescriptorJSON())
	if err != nil {
		t.Fatalf("harness.Load: %v", err)
	}
	return doc
}

// configWith is a merge carrying only the two keys run_assert_cycle_pairing
// reads (lib/run.sh:582-583). It sets both explicitly rather than defaulting
// either, so no case here passes because the helper filled a blank in;
// TestPairingReadsAnAbsentConfiguredNameAsServing uses an empty merge instead.
func configWith(reviewer, resolver string) *config.Config {
	merged := config.NewObject()
	merged.Set("reviewer", legObject(reviewer))
	merged.Set("resolver", legObject(resolver))
	return &config.Config{Merged: merged}
}

func legObject(name string) *config.Object {
	object := config.NewObject()
	object.Set("harness", name)
	return object
}

func wantFatal(t *testing.T, err error, reason, action string) {
	t.Helper()
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("err = %v, want a *ui.FatalError", err)
	}
	if fatal.Reason != reason {
		t.Errorf("reason\n got %q\nwant %q", fatal.Reason, reason)
	}
	if fatal.Action != action {
		t.Errorf("action\n got %q\nwant %q", fatal.Action, action)
	}
}

// --- the check itself -------------------------------------------------------

// TestPairingServesAnOverrideOnBothLegs pins the decision the comment block at
// lib/run.sh:560-569 records: an override puts one harness on both sides of the
// loop, and that is served rather than refused. Measured:
//
//	override=claude   cfg codex/claude -> rc=0, nothing printed
//	override=opencode cfg codex/claude -> rc=0, nothing printed
//
// The refusal path is the only thing the shell has here. There is no warning,
// and the seam the driver holds has no writer to put one on.
func TestPairingServesAnOverrideOnBothLegs(t *testing.T) {
	check := Pairing(shippedDescriptor(t), configWith("codex", "claude"))
	for _, override := range []string{"claude", "codex", "agy", "grok", "opencode"} {
		if err := check(override); err != nil {
			t.Errorf("check(%q) = %v, want nil", override, err)
		}
	}
}

// TestPairingRefusesAnOverrideThatCannotResolve pins the resolve-leg refusal,
// byte for byte. Measured with codex rewritten to legs ["review"]:
//
//	error  the harness 'codex' cannot serve the resolve leg
//	       CrossRev runs the resolve leg on claude, agy, grok and opencode. Codex is limited to the review leg.
func TestPairingRefusesAnOverrideThatCannotResolve(t *testing.T) {
	doc := descriptorWith(t, map[string][]string{"codex": {"review"}})
	check := Pairing(doc, configWith("claude", "claude"))
	wantFatal(t, check("codex"),
		"the harness 'codex' cannot serve the resolve leg",
		"CrossRev runs the resolve leg on claude, agy, grok and opencode. Codex is limited to the review leg.")
}

// TestPairingRefusesAnOverrideThatCannotReview pins the review-leg refusal, and
// with it that the harness list and the product name are read from the
// descriptor rather than written into the sentence. Measured with agy, grok and
// opencode rewritten to legs ["resolve"]:
//
//	error  the harness 'agy' cannot serve the review leg
//	       CrossRev runs the review leg on claude and codex. Antigravity is limited to the resolve leg.
//
// The list is two names here and four in the test above, which is what pins
// _names_human's "a and b" against its "a, b, c and d".
func TestPairingRefusesAnOverrideThatCannotReview(t *testing.T) {
	doc := descriptorWith(t, map[string][]string{
		"agy": {"resolve"}, "grok": {"resolve"}, "opencode": {"resolve"},
	})
	check := Pairing(doc, configWith("claude", "claude"))
	wantFatal(t, check("agy"),
		"the harness 'agy' cannot serve the review leg",
		"CrossRev runs the review leg on claude and codex. Antigravity is limited to the resolve leg.")
}

// TestPairingChecksTheConfiguredNamesWithoutAnOverride pins the else arm at
// lib/run.sh:581-584. Measured with codex rewritten to legs ["review"]:
//
//	no override, cfg codex/claude -> rc=0
//	no override, cfg claude/codex -> rc=1, the resolve refusal naming codex
func TestPairingChecksTheConfiguredNamesWithoutAnOverride(t *testing.T) {
	doc := descriptorWith(t, map[string][]string{"codex": {"review"}})

	if err := Pairing(doc, configWith("codex", "claude"))(""); err != nil {
		t.Errorf("reviewer codex, resolver claude = %v, want nil", err)
	}

	wantFatal(t, Pairing(doc, configWith("claude", "codex"))(""),
		"the harness 'codex' cannot serve the resolve leg",
		"CrossRev runs the resolve leg on claude, agy, grok and opencode. Codex is limited to the review leg.")
}

// TestPairingIgnoresTheConfiguredNamesWhenAnOverrideIsGiven pins that the two
// cfg_get calls sit in the else arm: the configured pairing here would be
// refused on both legs, and the override serves both, so a port that read the
// config anyway would fail this.
func TestPairingIgnoresTheConfiguredNamesWhenAnOverrideIsGiven(t *testing.T) {
	doc := descriptorWith(t, map[string][]string{"codex": {"review"}, "agy": {"resolve"}})
	if err := Pairing(doc, configWith("agy", "codex"))("claude"); err != nil {
		t.Errorf("override claude = %v, want nil", err)
	}
}

// TestPairingRefusesTheReviewLegFirst pins the order of lib/run.sh:586-587.
// Measured with codex rewritten to ["review"] and agy to ["resolve"], reviewer
// agy and resolver codex — both fail, and the review one is what prints:
//
//	error  the harness 'agy' cannot serve the review leg
//	       CrossRev runs the review leg on claude, codex, grok and opencode. Antigravity is limited to the resolve leg.
func TestPairingRefusesTheReviewLegFirst(t *testing.T) {
	doc := descriptorWith(t, map[string][]string{"codex": {"review"}, "agy": {"resolve"}})
	wantFatal(t, Pairing(doc, configWith("agy", "codex"))(""),
		"the harness 'agy' cannot serve the review leg",
		"CrossRev runs the review leg on claude, codex, grok and opencode. Antigravity is limited to the resolve leg.")
}

// TestPairingServesAHarnessTheDescriptorDoesNotName pins a divergence between
// the shell and internal/harness. jq reads
//
//	((.harnesses[] | select(.name == $n) | .legs) // ["review","resolve"])
//
// and for an unknown name the selection produces NO values, so `//` yields the
// default and the name serves both legs. Measured:
//
//	harness_serves_leg nosuch review  -> true
//	harness_serves_leg nosuch resolve -> true
//	run_assert_cycle_pairing nosuch   -> rc=0, nothing printed
//
// harness.Document.ServesLeg answers false for the same name, so this check
// cannot be built on it. The shell is not lax by accident: an unknown name is
// refused a few lines later by run_leg_settings' adapter test (lib/run.sh:500),
// with a message that names the fault. Refusing it here would print
// "Codex is limited to the  leg" with two blanks in it instead.
func TestPairingServesAHarnessTheDescriptorDoesNotName(t *testing.T) {
	check := Pairing(shippedDescriptor(t), configWith("codex", "claude"))
	if err := check("nosuch"); err != nil {
		t.Errorf("check(\"nosuch\") = %v, want nil", err)
	}
	if err := Pairing(shippedDescriptor(t), configWith("nosuch", "nosuch"))(""); err != nil {
		t.Errorf("configured nosuch = %v, want nil", err)
	}
}

// TestPairingReadsAnAbsentConfiguredNameAsServing goes through Config.Get on a
// merge that carries neither key, without configWith. cfg_get renders an absent
// value as the empty string (lib/config.sh:354), and the empty string is a name
// the descriptor does not carry, so it serves. Measured:
//
//	no override, cfg empty/empty -> rc=0, nothing printed
func TestPairingReadsAnAbsentConfiguredNameAsServing(t *testing.T) {
	empty := &config.Config{Merged: config.NewObject()}
	if err := Pairing(shippedDescriptor(t), empty)(""); err != nil {
		t.Errorf("an empty merge = %v, want nil", err)
	}
}

// TestPairingDoesNotReadTheConfigWhenAnOverrideIsGiven pins the laziness the
// shell gets for free: with an override the else arm never runs, so a nil
// config cannot be reached. The driver is wired with the config it loaded, so
// this is not a supported call — it is the cheapest proof that no read happens.
func TestPairingDoesNotReadTheConfigWhenAnOverrideIsGiven(t *testing.T) {
	if err := Pairing(shippedDescriptor(t), nil)("claude"); err != nil {
		t.Errorf("override with no config = %v, want nil", err)
	}
}

// --- the check inside the driver --------------------------------------------

// TestPairingRefusesBeforeAnyLegIsBilled wires the real check into the driver
// and pins what lib/run.sh:2926 buys: the refusal lands after the context load
// and before the first leg, so a cycle whose resolver cannot resolve costs no
// review. The `Cycling …` line is printed after the check (lib/run.sh:2928), so
// a refused cycle prints nothing on stdout either.
func TestPairingRefusesBeforeAnyLegIsBilled(t *testing.T) {
	doc := descriptorWith(t, map[string][]string{"codex": {"review"}})
	r := newRig(t, []loadStep{{state: loaded(t)}})
	r.driver.Pairing = Pairing(doc, configWith("claude", "codex"))

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", got.ExitCode)
	}
	wantFatal(t, got.Err,
		"the harness 'codex' cannot serve the resolve leg",
		"CrossRev runs the resolve leg on claude, agy, grok and opencode. Codex is limited to the review leg.")
	r.wantOrder(t, "load")
	r.wantOut(t, "")
}

// TestPairingAddsNothingToTheCycleOutput pins the other half of the comment at
// lib/run.sh:568-569: a served pairing is not warned about. One harness on both
// legs runs the loop and prints exactly what a cycle without an override
// prints.
func TestPairingAddsNothingToTheCycleOutput(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewConverged, 1))},
	})
	r.driver.Pairing = Pairing(shippedDescriptor(t), configWith("codex", "claude"))

	got := r.driver.Run(context.Background(), Request{PR: 42, Trigger: TriggerHuman, HarnessOverride: "claude"})

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "review", "load", "nudge")
	r.wantOut(t, sayCycling+endLine(
		"Converged after pass 1 — nothing at or above min_fix_severity (medium) remains."))
}
