// pairing.go — run_assert_cycle_pairing (lib/run.sh:581-593) and the refusal it
// shares with the per-leg gate, _run_assert_harness_serves_leg
// (lib/run.sh:559-564).
//
// # One harness on both legs is served, and not warned about
//
// Cycle forwards --harness into both legs, so an override puts one harness on
// both sides of the loop. The comment at lib/run.sh:566-575 records why that is
// served rather than refused: run_leg_settings already puts both legs on one
// harness when only one is installed, and the models-diverged check returns
// early when one model was asked for, so the same pairing named through
// reviewer.harness and resolver.harness has always run. It is not warned about
// either, because the configured form is not — a warning here would restore the
// asymmetry in the other direction. Nothing in this file writes, which is what
// keeps that true rather than remembered.
//
// What the override IS checked for is what the configured names are checked
// for: whether the harness serves the leg. The check runs after the config
// loads and before the pass loop (lib/run.sh:2932), so a harness that cannot
// resolve is refused without a billed review.
package cycle

import (
	"fmt"
	"slices"
	"strings"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// Pairing builds the driver's pairing check over one descriptor and one merged
// configuration (lib/run.sh:581-593).
//
// The returned check reads the configuration only when the override is empty,
// which is the shell's `if [[ -n "$override" ]]` and not an optimisation.
func Pairing(doc harness.Document, cfg *config.Config) func(override string) error {
	return func(override string) error {
		reviewer, resolver := override, override
		if override == "" {
			// lib/run.sh:588-589
			reviewer = cfg.Get(".reviewer.harness")
			resolver = cfg.Get(".resolver.harness")
		}
		// The reviewer is checked first, so a pairing that fails both legs
		// reports the review one (lib/run.sh:592-593).
		if err := assertServesLeg(doc, reviewer, harness.LegReview); err != nil {
			return err
		}
		return assertServesLeg(doc, resolver, harness.LegResolve)
	}
}

// assertServesLeg is _run_assert_harness_serves_leg (lib/run.sh:559-564). The
// message is the product: it names the harness, the leg, the harnesses that can
// take the leg, and the legs the refused harness actually serves.
//
// # A name the descriptor does not carry serves every leg
//
// harness_serves_leg reads
//
//	((.harnesses[] | select(.name == $n) | .legs) // ["review","resolve"])
//	  | index($l) != null
//
// and for an unknown name the selection produces NO values, so `//` yields the
// default and the name serves both legs (lib/harnesses.sh:154-159). Measured
// against the shipped descriptor: `harness_serves_leg nosuch review` and
// `… nosuch resolve` are both true, and `run_assert_cycle_pairing nosuch`
// returns 0 printing nothing. harness.Document.ServesLeg answered false for
// the same name when this was written and now answers the shell's true; this
// check reads the entry itself and is right either way.
//
// The laxness is load-bearing rather than accidental. An unknown name is
// refused a few lines later by run_leg_settings' adapter test (lib/run.sh:506)
// with a message that names the fault; refusing it here would print a sentence
// with an empty product name and an empty leg list in it.
//
// # Why the hint reads the legs back off the entry
//
// The hint's leg list is `harness_get "$harness" '.legs // [] | join(", ")'`,
// whose default is the EMPTY array rather than the review-resolve pair
// harness_serves_leg defaults to. The difference cannot show: an entry that
// declares no legs serves both and never reaches this line, and the validator
// refuses a legs field that is not a non-empty array drawn from review and
// resolve (lib/harnesses.sh:66-70). So a refused entry has declared exactly one
// leg, and Descriptor.Legs is its declared list.
func assertServesLeg(doc harness.Document, name, leg string) error {
	entry, found := doc.For(name)
	if !found || slices.Contains(entry.Legs(), leg) {
		return nil
	}
	return &ui.FatalError{
		Reason: fmt.Sprintf("the harness '%s' cannot serve the %s leg", name, leg),
		Action: fmt.Sprintf("CrossRev runs the %s leg on %s. %s is limited to the %s leg.",
			leg,
			harness.NamesHuman(doc.NamesForLeg(leg)),
			entry.ProductName,
			strings.Join(entry.Legs(), ", ")),
	}
}
