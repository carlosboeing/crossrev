package preflight

import (
	"github.com/carlosboeing/crossrev/internal/harness"
)

// ReportReadModes is the review-reads half of `crossrev doctor`: each
// harness's read mode, whether its served-or-tripwire command block is
// verified at the shipped pin, and where the tripwire cannot run. It never
// fails the command: an unverified block refuses the review leg at runtime
// with review_isolation_unverified, and doctor's job here is to name it
// before that happens.
func (c *Checker) ReportReadModes() {
	io := c.io()
	io.Section("Review reads")
	for _, name := range c.Harness.Names() {
		entry, _ := c.Harness.For(name)
		io.Say(readModeLine(name, entry))
	}
}

// readModeLine is one harness's reads posture in one line: the mode, the
// verification of the command block where there is one, and the tripwire
// gap where there is none.
func readModeLine(name string, entry harness.Descriptor) string {
	switch entry.ReadMode() {
	case harness.ReadModeServed:
		if harness.IsolationVerified(name, entry.Install.PinnedVersion) {
			return name + " — served reads, command block verified at pin " + entry.Install.PinnedVersion
		}
		return name + " — served reads, command block UNVERIFIED at pin " + entry.Install.PinnedVersion + " (review_isolation_unverified)"
	case harness.ReadModeFileTool:
		return name + " — file_tool reads, unwired: reviews run supplied until slice 9"
	default:
		return name + " — supplied reads" + suppliedTripwireNote(name, entry)
	}
}

// suppliedTripwireNote names where the review tripwire cannot run: grok's
// tripwire is verified with its block, opencode is denied through its
// isolation config with no command record to watch, and agy emits no tool
// events at all.
func suppliedTripwireNote(name string, entry harness.Descriptor) string {
	switch name {
	case "grok":
		if harness.IsolationVerified(name, entry.Install.PinnedVersion) {
			return ", tripwire verified at pin " + entry.Install.PinnedVersion
		}
		return ", tripwire UNVERIFIED at pin " + entry.Install.PinnedVersion + " (review_isolation_unverified)"
	case "opencode":
		return ", no tripwire: the five read tools are denied through the isolation config with no command record to watch"
	case "agy":
		return ", no tripwire: agy emits no tool events to watch"
	default:
		return ""
	}
}
