package harness

// The one rule every surface reads before letting a harness resolve.
//
// codex 0.158.0 with its shell disabled has no file-reading tool, and the
// resolve leg denies commands on every harness — on Codex, `--sandbox
// workspace-write` with `--disable shell_tool --disable unified_exec` — so a
// resolve leg on codex answers `blocked` instead of editing, verifies
// nothing, and the pass halts. CrossRev refuses codex as the resolver before
// the leg starts, naming the reason and the resolvers that work, until the
// served read tool also serves resolve legs. Codex as reviewer is unaffected.
//
// The refusal lives here, on the name, rather than as a `legs: ["review"]`
// field on the codex descriptor entry, because it is temporary and
// reason-specific: the descriptor keeps describing static capability, and the
// shipped default pairing — codex reviewing, claude resolving — stays valid
// under it. Every place that decides or reports which harness may resolve
// reads this file: the resolve leg's settings, the cycle pairing check, and
// the preflight pairing report behind `doctor` and `init`.

// RefusedAsResolver reports whether name may not serve the resolve leg,
// whatever the descriptor says about it.
func RefusedAsResolver(name string) bool { return name == "codex" }

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
