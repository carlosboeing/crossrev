// Package ghexec implements forge operations via the GitHub CLI.
//
// It runs `gh` from the PATH through internal/exec and reads what it prints.
// There is no HTTP client here and no GitHub SDK: `gh` holds the credential,
// the token handling, the enterprise host resolution and the pagination, and
// replacing it would mean reproducing all four (ADR 0001).
//
// The argument arrays are the specification. lib/github.sh is the file this
// package ports, tests/stub/gh is a fake `gh` the offline suite puts earlier on
// the PATH, and every shell suite asserts on the argv that stub logs. A Go call
// that reached the same endpoint by another route would pass its own tests and
// fail the ones that already exist, so the arrays here are transcribed rather
// than reinvented — down to `--jq` on the reads that use it, and to the piped
// filters left off the reads that do not.
//
// # The one place a GitHub credential is handed to a child
//
// A real `gh` child is started through exec.NewOrchestratorRunner. The reason
// is in client.go, beside the constructor.
//
// Two rules in internal/archtest hold it, because the opt-out is the one
// construction in this tree whose misuse is silent: NewOrchestratorRunner may
// be named in this package, internal/vcs and internal/exec, and a Spec may be
// built in client.go alone, and every Spec built there runs the `program`
// constant. The name check reads types rather than text, so an aliased import
// does not slip past it. The remaining route is a wrapper that is given the
// orchestrator runner.
package ghexec
