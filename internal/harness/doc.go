// Package harness is the descriptor, the five adapters, and the token
// telemetry every one of them answers with.
//
// It is lib/harnesses.sh, lib/adapters/*.sh and lib/usage.sh, ported. The
// descriptor is a trusted input validated at load; each adapter turns one leg's
// Invocation into an exec.Spec and that child's exec.Result into an Envelope;
// and usage.go normalizes whatever a vendor called its token counts into one
// record whose `total` means the same thing on every row.
//
// # An adapter builds a child process and never starts one
//
// Adapters here build a Spec for a model-facing process — the one that
// reads attacker-controlled text off a pull request — and start nothing
// themselves. The child is started through NewOSRunner, and internal/archtest
// refuses this package if it ever names exec.NewOrchestratorRunner. The
// constructor scan uses default build tags. The environment is built by subtracting
// cred.StripFor's answer, so no GitHub credential and no other harness's
// vendor credential travels with the child (ADR 0001, SECURITY.md).
//
// # There is no fallback and no routing
//
// A leg names one harness. An adapter that cannot serve the request refuses and
// says where to take it; it never picks another. That is what every `ui_die` in
// the Bash adapters does, and it is the reason a cross-model loop can promise
// that two different models ran.
package harness
