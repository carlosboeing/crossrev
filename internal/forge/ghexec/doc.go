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
// Every Spec this package builds sets exec.AudienceOrchestrator. The reason is
// in client.go, beside the code that sets it.
//
// Three rules in internal/archtest hold it, because the audience is the one
// field in this tree whose misuse is silent: the constant may be named in this
// package alone, a Spec may be built in client.go alone, and every Spec built
// there runs the `program` constant. The first two read types rather than text,
// so an aliased import or a var with no literal does not slip past them.
package ghexec
