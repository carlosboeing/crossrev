// Package initcmd is `crossrev init`, the upgrade from local to automated.
//
// The Bash command lives in lib/init.sh. It is the most consequential command
// CrossRev has: it registers a GitHub identity, writes organisation secrets,
// and adds files to a repository. So it prints an itemised plan naming every
// path, secret and label, the resolved destination for deferred work and where
// that resolution came from, flags anything it would overwrite, and asks once.
//
// # Read, print, then write
//
// The package is in three parts, and the split is the gate.
//
//   - Resolve (resolve.go) settles every value the plan states. It reads
//     GitHub, the configuration and the working tree, and writes nothing.
//   - Print (plan.go) states the plan. It reads the label colours and the
//     branch protection rule while printing, because the plan has to say what
//     execution would actually do rather than what it intended to do.
//   - Execute (execute.go, labels.go, secrets.go, config.go) is the only part
//     that changes anything, and Run reaches it only past the confirmation.
//     `--dry-run` returns before an Execution is used at all.
//
// Everything a run may read arrives on Request; everything it may change
// arrives on Execution. A command that only prints a plan builds no Execution,
// so there is no wiring through which a dry run could reach a write.
//
// # The ports are here because this package is tier 3
//
// It may import tiers 0 to 2 and no tier-3 peer, so it cannot name
// internal/app for the App identity or internal/preflight for the pairing
// report. Both arrive as small interfaces the composition root implements over
// those packages, and so do the GitHub reads, the git reads and the working
// tree. Nothing here opens a socket. The two places a process starts —
// `gh secret set` and a harness's own seed command — go through an
// internal/exec Runner the composition root hands in.
//
// # The copies under assets/ are generated
//
// A `go:embed` pattern is package-relative and cannot contain `..`, so this
// package cannot embed templates/ at the repository root directly.
// scripts/sync-embedded-assets.sh keeps a byte-identical copy under
// assets/templates/. Edit the root file and run the script; a hand edit to a
// copy is caught by `--check` in lint and by assets_test.go in the suite.
//
// # Nothing calls this yet
//
// There is no composition root. `crossrev init` is still the Bash command, and
// every function here runs only under its own tests.
package initcmd
