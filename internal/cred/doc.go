// Package cred stages subscription credentials that have to survive a runner,
// reads their expiry, decides what a model-facing process may hold, and
// refreshes the one credential that rotates.
//
// It is the port of lib/credentials.sh, the hosted-runner cases in
// lib/preflight.sh:236-263, and the refresher path at lib/auth.sh:1028-1072.
//
// # The rule the package exists to enforce
//
// Three of the five harnesses authenticate with a rotating OAuth credential
// rather than a static key, and using a refresh token consumes it: the vendor
// hands back a replacement and invalidates what was presented. One holder is
// fine, several are not, and a job holding a dead copy that writes back
// overwrites the good one (lib/credentials.sh:4-13).
//
// So a leg restores, reads and discards. It never refreshes and it never
// writes back. Exactly one job — the refresher workflow, on its own
// concurrency group — calls Refresh, and it is the only writer.
//
// # What is preserved exactly
//
//   - MinSeconds is 3600, the floor lib/credentials.sh:36 declares. A leg with
//     less than an hour left refuses rather than running, because the refresh
//     it would trigger mid-flight is the one that breaks the chain.
//   - A staged credential lands in a scratch directory that is thrown away when
//     the leg finishes (lib/credentials.sh:99-105). The harness may refresh and
//     write back on its own and there is no flag to stop it, so the write has to
//     reach a directory nobody reads again.
//   - Nothing here writes a credential back to where it came from. Refresh
//     returns bytes; the caller decides what to do with them, and only the
//     refresher workflow has anywhere to put them.
//
// # What it does not do
//
// It starts no child process. Every external tool lib/credentials.sh reaches
// for has a standard-library answer here — openssl base64 is encoding/base64,
// jq is encoding/json, mktemp -d is os.MkdirTemp, and curl is net/http — so
// this package needs no exec.Runner and holds none. That also keeps it clear of
// exec.NewOrchestratorRunner, which internal/archtest confines to
// internal/exec, internal/forge/ghexec and internal/vcs.
//
// It also holds no package-level state. lib/credentials.sh keeps four globals
// (CRED_SCRATCH and the three CRED_STAGING_ENV_* variables) because a shell
// function cannot return a handle; Prepare returns a *Staged instead, so two
// legs in one process cannot overwrite each other's scratch directory.
package cred
