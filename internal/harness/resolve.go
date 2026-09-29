package harness

// The one rule every surface reads before letting a harness resolve.
//
// Codex used to be refused as a resolver: 0.158.0 with its shell disabled
// had no file-reading tool, and the resolve leg denies commands on every
// harness, so a resolve leg on codex answered `blocked` instead of editing.
// The served read tool now serves codex resolve legs too — the resolve spec
// carries the same mcp_servers.crossrev command the review leg does — so
// the refusal is lifted and codex resolves again.
//
// The refusal lives here, on the name, rather than as a `legs: ["review"]`
// field on the codex descriptor entry, because it is reason-specific: the
// descriptor keeps describing static capability. Every place that decides
// or reports which harness may resolve reads this file: the resolve leg's
// settings, the cycle pairing check, and the preflight pairing report
// behind `doctor` and `init`.

// RefusedAsResolver reports whether name may not serve the resolve leg,
// whatever the descriptor says about it. Nothing is refused today; the
// function stays because every surface reads the rule from here.
func RefusedAsResolver(name string) bool { return false }

// WorkingResolvers returns the harnesses that may serve the resolve leg, in
// descriptor order: every name the descriptor lists for the leg except the
// ones the rule above refuses.
func WorkingResolvers(doc Document) []string {
	var out []string
	for _, name := range doc.NamesForLeg(LegResolve) {
		if !RefusedAsResolver(name) {
			out = append(out, name)
		}
	}
	return out
}
