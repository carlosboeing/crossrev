// strip.go — which environment variables must not reach a given harness.
//
// The workflow hands every credential the pairing might need to one process,
// and each leg needs exactly one of them. The rest are for a harness that is
// not running, so passing them on is pure exposure: the agent process is the
// one reading attacker-controlled text, and a prompt injection that reaches
// tool use can read its own environment. A model that never sees the other
// vendor's token cannot be talked into exfiltrating it
// (lib/credentials.sh:166-177).
//
// # Two lists, one answer
//
// The Bash side keeps them apart. Every adapter opens with a fixed
// `env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN
// -u GITHUB_ENTERPRISE_TOKEN` (lib/adapters/claude.sh:72, and the same line in
// codex.sh:88, agy.sh:90, grok.sh:75 and opencode.sh:181) and then appends
// whatever cred_env_strip_for prints (claude.sh:76). The four are the ADR 0001
// boundary and are not in the descriptor at all; the rest are descriptor facts.
//
// StripFor joins them, because an adapter that asked for one and forgot the
// other is the failure this is for. VendorStripFor answers the descriptor half
// on its own, which is the half tests/fixtures/parity/credentials.json freezes.

package cred

import (
	"slices"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// StripFor is every name a model-facing process must not hold: the four forge
// credentials, then this harness's foreign vendor credentials.
//
// The forge four are unconditional. They do not depend on the harness, on its
// keep list, or on whether the descriptor carries the name at all — which is
// the case worth stating, because four of the five shipped harnesses have an
// empty env_keep and would look correct with the forge names missing. An
// unknown harness gets the widest answer of all, since nothing declares that it
// may keep anything.
func StripFor(doc Document, harness string) []string {
	forge := exec.ForgeCredentialNames()
	return append(forge, VendorStripFor(doc, harness)...)
}

// VendorStripFor is cred_env_strip_for (lib/credentials.sh:178-185): the union
// of every harness's credential variables, less the ones this harness keeps.
//
// The order is jq's. `unique` sorts, and `$all - $keep` keeps the order of the
// left operand, so the answer is sorted by name — which is the order
// tests/fixtures/parity/credentials.json records.
//
// CROSSREV_CODEX_AUTH is stripped even from codex, because by then the
// credential has been written into CODEX_HOME and the raw copy in the
// environment is a second one nobody needs. ANTHROPIC_API_KEY survives for
// claude so a local run is not silently moved from API billing to subscription
// billing. Both facts live in the descriptor's env_keep array rather than here.
func VendorStripFor(doc Document, harness string) []string {
	keep := doc.For(harness).Credential.EnvKeep
	all := doc.VendorNames()

	strip := make([]string, 0, len(all))
	for _, name := range all {
		if slices.Contains(keep, name) {
			continue
		}
		strip = append(strip, name)
	}
	return strip
}
