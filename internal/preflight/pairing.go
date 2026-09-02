package preflight

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// Which pairings the configured runner can actually serve
// (lib/preflight.sh:170-292).
//
// Which harnesses are reachable in CI is a property of the runner, not of the
// config, because it comes down to whether a subscription credential can live
// in a repository secret. Saying so here — before anything is installed — beats
// failing at the first API call with an authentication error that reads like a
// wrong password.

// PairingSupported answers whether this harness can authenticate by
// subscription on this runner, and the reason when it cannot
// (preflight_pairing_supported, lib/preflight.sh:188-233).
//
// leg is optional and names a leg in the descriptor's vocabulary — review or
// resolve. A harness that lists its legs is refused for the others here, so
// doctor reports the limit for automated mode rather than leaving it to be
// discovered by a failing job; with an empty leg the answer stays the
// credential-only question it always was.
func PairingSupported(doc harness.Document, runner, name, leg string) (string, bool) {
	// A descriptor fact, not a runner fact: self-hosted skips the credential
	// checks below because the machine already holds the login, but a harness
	// that does not serve this leg is refused on every runner.
	if leg != "" && !servesLeg(doc, name, leg) {
		return fmt.Sprintf("%s is limited to the %s leg, and cannot serve the %s leg",
			productName(doc, name), strings.Join(declaredLegs(doc, name), ", "), leg), false
	}

	if runner == "self-hosted" {
		return "", true
	}

	entry, known := doc.For(name)
	if !known {
		if reason, found := doc.NotDrivenReason(name); found {
			return fmt.Sprintf("CrossRev has no adapter for '%s' (%s)", name, reason), false
		}
		return fmt.Sprintf("CrossRev has no adapter for '%s'", name), false
	}

	credential := entry.Credential
	switch {
	case credential.Archetype == "A":
		return "", true
	case credential.Archetype == "B" && credential.Refresher:
		return "", true
	case credential.Archetype == "C" && credential.Provenance == "measured" && credential.Secret != "":
		return "", true
	}

	var seconds int64
	if credential.AccessTokenSeconds != nil {
		seconds = *credential.AccessTokenSeconds
	}
	return fmt.Sprintf(
		"%s's subscription token lives about %d minutes, and CrossRev has no way to seed it into a hosted runner yet",
		entry.ProductName, seconds/60), false
}

// servesLeg is harness_serves_leg as jq answers it (lib/harnesses.sh:154-159).
//
// The jq expression is `((.harnesses[] | select(.name == $n) | .legs) //
// ["review","resolve"]) | index($l) != null`. An unknown name selects nothing,
// and `//` supplies its default over an empty stream as well as over a null —
// so an unknown harness serves both legs, and `preflight_pairing_supported
// github-hosted bogus review` falls through to the adapter refusal below
// rather than to the leg refusal above. Measured against the shell.
//
// harness.Document.ServesLeg answers false for an unknown name instead, which
// is a different question. It is not used here for that reason.
func servesLeg(doc harness.Document, name, leg string) bool {
	legs := []string{harness.LegReview, harness.LegResolve}
	if entry, found := doc.For(name); found {
		legs = entry.Legs()
	}
	return slices.Contains(legs, leg)
}

// declaredLegs is `.legs // []`: the legs the descriptor writes down, and
// nothing for an entry that writes none. It is what the refusal above joins,
// so a harness with no legs key names an empty set there.
func declaredLegs(doc harness.Document, name string) []string {
	if entry, found := doc.For(name); found && entry.DeclaresLegs() {
		return entry.Legs()
	}
	return nil
}

// productName is `.product_name`, empty for a name the descriptor does not
// drive.
func productName(doc harness.Document, name string) string {
	entry, _ := doc.For(name)
	return entry.ProductName
}

// HarnessSecret is which secret carries a harness's subscription credential in
// automated mode (preflight_harness_secret, lib/preflight.sh:236-241). A
// harness with no secret answers false.
func HarnessSecret(doc harness.Document, name string) (string, bool) {
	entry, found := doc.For(name)
	if !found || entry.Credential.Secret == "" {
		return "", false
	}
	return entry.Credential.Secret, true
}

// HostedRunner reports a runner where a credential can only arrive as a secret
// (preflight_hosted_runner, lib/preflight.sh:251).
//
// GitHub sets RUNNER_ENVIRONMENT, and it is the only signal that separates the
// three environments CrossRev runs in. GITHUB_ACTIONS does not: it is true on a
// self-hosted runner too, where the harness is logged in on disk and no secret
// is expected — which is why the templates filter those env lines out of the
// workflow they generate for one. The values are GitHub's and happen to be the
// same two the `runner:` config key already uses.
func HostedRunner() bool { return os.Getenv("RUNNER_ENVIRONMENT") == "github-hosted" }

// NeedsRefresher reports a pairing that needs the single-writer refresher
// (preflight_needs_refresher, lib/preflight.sh:258-263).
//
// Only one situation does: a harness whose credential rotates, authenticating
// by subscription, on an ephemeral runner. Change any one of the three and it
// disappears — which is why it is derived rather than asked.
func NeedsRefresher(doc harness.Document, runner, name, endpoint string) bool {
	if runner != "github-hosted" {
		return false
	}
	if endpoint != "" && endpoint != "null" {
		return false // a static token, which never rotates
	}
	entry, found := doc.For(name)
	return found && entry.Credential.Refresher
}

// ReportPairings prints one line per leg, for `crossrev doctor` and the `init`
// plan (preflight_report_pairings, lib/preflight.sh:266-292).
//
// It stops at the first leg it has to refuse, which is the Bash `return 1`
// inside the loop: the second leg is never reported.
func (c *Checker) ReportPairings(runner string) bool {
	c.io().Section("Pairings on runner: " + runner)
	for _, leg := range []string{"reviewer", "resolver"} {
		name := c.Config.Get("." + leg + ".harness")
		endpoint := c.Config.Get("." + leg + ".endpoint")

		// The loop names the config keys; PairingSupported speaks the
		// descriptor's vocabulary.
		legName := harness.LegResolve
		if leg == "reviewer" {
			legName = harness.LegReview
		}

		// A named endpoint short-circuits the credential question, so
		// PairingSupported is not asked at all — the same order the Bash
		// if/elif takes.
		if endpoint != "" && endpoint != "null" {
			c.io().OK(leg + " — " + name + " via the '" + endpoint + "' endpoint, a static token in a secret")
			continue
		}
		reason, ok := PairingSupported(c.Harness, runner, name, legName)
		if !ok {
			c.io().No(leg + " — " + name + " by subscription cannot run on a " + runner + " runner")
			c.io().Line("   " + reason)
			c.io().Line("   Fixes: set runner: self-hosted, or name a different harness for this leg.")
			return false
		}
		if NeedsRefresher(c.Harness, runner, name, endpoint) {
			c.io().OK(leg + " — " + name + " by subscription, kept warm by the refresher workflow")
			continue
		}
		c.io().OK(leg + " — " + name + " by subscription")
	}
	return true
}
