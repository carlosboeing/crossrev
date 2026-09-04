// Package preflight checks dependencies and environment prerequisites.
//
// It is lib/preflight.sh: the `crossrev doctor` report, and the checks every
// other command runs before it does anything (bin/crossrev:162-179).
//
// Every message names what is missing AND how to install it, because an error
// that names a problem without naming a fix has done half its job
// (lib/preflight.sh:4-5).
//
// # Four probes, in the order doctor prints them
//
//   - Checker.Check: git, gh, jq, yq and openssl, plus every harness the
//     descriptor drives. Three of the five are not this binary's own
//     dependencies any more — Go reads YAML and JSON itself and signs nothing
//     with openssl — and they stay because the report is what an operator and
//     the composite action both read.
//   - Checker.CheckQuarantine: the instruction files a killed run left behind
//     in .crossrev-quarantine, which look deleted in git status until somebody
//     finds them.
//   - Checker.ReportPairings: whether the configured runner can serve the
//     configured harnesses. Which harnesses are reachable in CI is a property
//     of the runner rather than of the config, because it comes down to whether
//     a subscription credential can live in a repository secret.
//   - Checker.ReportWorktrees: the worktrees a failed resolve run left in the
//     state directory. It never fails the command.
//
// # What the package does not decide
//
// Nothing here opens a terminal, ends the process or starts a child on its own.
// The report goes to a ui.IO, every probe runs through an exec.Runner, and PATH
// is searched through an injected lookup — so a test describes the machine it
// wants rather than inheriting the one the suite runs on, which is the whole
// point of tests/test-preflight.sh writing its own stubs.
//
// One field the caller cannot take the default on: the three gh identity probes
// are the orchestrator asking GitHub who it is, and they carry the forge
// credential, so `crossrev doctor` hands in the orchestrator runner. Every
// version probe receives the environment with that credential removed, because
// a version probe starts a harness CLI (ADR 0001, SECURITY.md).
package preflight
