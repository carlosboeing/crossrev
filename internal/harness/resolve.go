package harness

// Who may resolve is the descriptor's resolve list, nothing else.
//
// Codex used to be refused as a resolver: 0.158.0 with its shell disabled
// had no file-reading tool, and the resolve leg denies commands on every
// harness, so a resolve leg on codex answered `blocked` instead of editing.
// The served read tool now serves codex resolve legs too — the resolve spec
// carries the same mcp_servers.crossrev command the review leg does — so
// the refusal is lifted and codex resolves again. Which legs a harness
// serves is a static capability, so it lives on the descriptor entry
// itself rather than as a rule beside it.

// WorkingResolvers returns the harnesses that may serve the resolve leg, in
// descriptor order: every name the descriptor lists for the leg.
func WorkingResolvers(doc Document) []string {
	return doc.NamesForLeg(LegResolve)
}
