package preflight

import "context"

// Doctor is the whole `crossrev doctor` report and its exit code
// (bin/crossrev:163-180).
//
// Five probes in this order, because the reader works down the page: what is
// installed, what a killed run left in the checkout, what the configured runner
// can serve, what the coverage ledger would do, and which worktrees are still
// sitting in the state directory. The first four can fail the command; the
// fifth never does, which is why the Bash call carries no `|| doctor_ok=1`.
//
// Two things the caller does first, matching what bin/crossrev does around this
// branch. It does not source the harness adapters — doctor's whole job is to
// report missing dependencies, and Check probes the harnesses itself. And it
// loads the configuration, the way `cfg_load ""` runs inside this branch: a
// configuration that refuses ends the command with that refusal, so a Checker
// reaching here with a nil Config had none to load. That skips the pairing
// report and the coverage probe.
func (c *Checker) Doctor(ctx context.Context) int {
	ok := true

	if !c.Check(ctx, NeedHarness) {
		ok = false
	}
	if !c.CheckQuarantine() {
		ok = false
	}

	// Which pairings the configured runner can serve is the other half of "is
	// this set up correctly", and it is invisible until a CI run fails to
	// authenticate. Reported here rather than discovered there. Both config
	// probes run under the one condition: a Checker reaching here with a nil
	// Config had none to load.
	if c.Config != nil {
		if !c.ReportPairings(c.Config.Get(".runner")) {
			ok = false
		}
		if !c.ReportCoverage(ctx) {
			ok = false
		}
	}

	c.ReportWorktrees()

	if ok {
		c.io().End("Everything CrossRev needs is installed.")
		return 0
	}
	c.io().End("Fix what is marked ✗ above, then run this again.")
	return 1
}
