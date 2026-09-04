// Package cycle is the local-mode driver: one process runs a review leg and a
// resolve leg in sequence until the pass bound or a terminal state
// (lib/run.sh:2901-3022).
//
// Every decision the loop takes is read back off the pull request rather than
// carried in memory, because that is where the state lives. The driver reloads
// the context after each leg and reads the pass number, the markers and the
// labels again, so a leg run by hand, a second process, or a person applying
// crossrev/stop changes what the next iteration does. That is also what makes
// this a thin driver over the same legs the workflows invoke, and what lets the
// two modes be compared on real pull requests without touching leg code.
//
// # What is here and what is injected
//
// The loop, its terminal lines and the counts those lines turn on. The two legs,
// the context load, the pairing check and the upgrade tip arrive as fields on
// Driver, for two reasons. The tier rule forbids importing internal/review or
// internal/resolve from here, so the leg interfaces are this package's own
// narrow shapes. And the loop's whole subject is the order of the calls and what
// each is told, which is exactly what a fake collaborator can pin.
//
// # The counts
//
// actionableCount and escalatedCount answer the two questions the loop turns
// on: whether a finished review pass left anything to resolve, and whether an
// empty pass is a convergence or a halt. internal/review and internal/resolve
// each hold a sibling of both; the rank table they all read is the one copy in
// internal/policy.
package cycle
