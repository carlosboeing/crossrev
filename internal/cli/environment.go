package cli

// environment.go — the environment contract.
//
// Every environment variable the shipped Bash reads, sets for a child, or
// strips from one, frozen as a table. It lives beside the parser because the
// CLI is where a process's environment first matters, and because nothing else
// owns the whole list: each of internal/app, internal/config, internal/cred,
// internal/preflight, internal/runlog and internal/vcs holds the two or three
// names its own port needed, and no file said what the complete set was.
//
// The table is a contract rather than a convenience. TestEnvironmentContract
// walks every production source in the module for a named environment read and
// fails on one this table does not carry, so an implicit read cannot be added
// without saying so here. It also fails on a reader named here that no longer
// names the variable, so the table cannot go stale in the other direction.
//
// The measured Bash side is testdata/environment/shell-inventory.json, which
// carries the lib/*.sh line each name was read back from. The table below and
// that file are checked against each other; neither is derived from the other.
//
// Two kinds of name cannot be in a table at all, and both are named here rather
// than left out silently:
//
//   - An endpoint's token. lib/adapters/claude.sh:82 reads `${!tok_env:-}`,
//     where tok_env is the endpoint block's token_env key (lib/config.sh:375).
//     The name is the operator's own — templates/operator-config.yml:25 ships
//     KIMI_API_KEY as the worked example — so the port reads it the same way,
//     at internal/resolve/invoke.go:415.
//   - A harness secret the descriptor does not name. lib/auth.sh:990 falls back
//     to CROSSREV_<HARNESS>_AUTH. Measured against lib/harnesses.json, the only
//     harness declaring `refresher: true` is codex and codex declares
//     CROSSREV_CODEX_AUTH, so the fallback is unreachable in the shipped
//     descriptor. CROSSREV_CLAUDE_AUTH and CROSSREV_AGY_AUTH are therefore not
//     variables this tool reads.

// Class is why the shipped shell touches a variable.
//
// The six are exhaustive over the table, and TestEnvironmentContract fails on a
// seventh, so a name whose reason is none of these has to be argued for rather
// than filed.
type Class string

const (
	// ClassOperatorInput is a lever a person sets: the author a fix commits
	// as, whether the palette is on, whether every question is answered yes.
	ClassOperatorInput Class = "operator input"

	// ClassRunnerSignal is set by GitHub, never by CrossRev, and says what
	// kind of machine this is.
	ClassRunnerSignal Class = "runner signal"

	// ClassPathOverride moves a file CrossRev reads or writes.
	ClassPathOverride Class = "path override"

	// ClassCredential is a credential stripped before a model-facing process
	// starts. This is the ADR 0001 boundary.
	ClassCredential Class = "credential to strip"

	// ClassEndpoint redirects a Claude-compatible harness process-wide, and is
	// refused when CrossRev inherited it (lib/legs.sh:494-501).
	ClassEndpoint Class = "endpoint to refuse"

	// ClassChildOutput is written onto one child's invocation and never
	// exported, so it is output rather than input.
	ClassChildOutput Class = "child-specific output"
)

// Variable is one environment name and everything the contract knows about it.
type Variable struct {
	// Name is the variable, exactly as it is spelled.
	Name string

	// Class is why the shell touches it.
	Class Class

	// Readers are the Go packages whose production sources name it, whether
	// they read it, strip it, or set it on a child. Empty means no package
	// does yet, which for a name the shell reads is a gap rather than a fact
	// about the design.
	//
	// A list rather than the single package the plan text asked for: HOME is
	// named in six packages and XDG_STATE_HOME in three, and one field would
	// have to pick one and be wrong about the rest.
	Readers []string

	// Descriptor says the name reaches the code out of lib/harnesses.json
	// rather than being written out. Every one of these is read through an
	// indirect expansion — `${!secret}` at lib/credentials.sh:143, or
	// `${!staging_env+set}` at :152 — so grepping the shell for it finds
	// nothing but a comment.
	Descriptor bool
}

// environment is the frozen table, sorted by name.
//
// Sorted rather than grouped by class: an order a reader can predict is what
// makes a diff to this list readable, and TestEnvironmentContract enforces it.
var environment = []Variable{}

// Environment answers the contract, sorted by name.
//
// A function returning a copy rather than an exported variable, for the reason
// policy.EndpointVariables is one: a package-level slice is writable by every
// importer, and `cli.Environment = nil` would leave the contract approving
// every implicit read it exists to catch.
func Environment() []Variable {
	out := make([]Variable, len(environment))
	for i, v := range environment {
		out[i] = v
		out[i].Readers = append([]string(nil), v.Readers...)
	}
	return out
}

// EnvironmentFor answers one variable, and whether the contract carries it.
func EnvironmentFor(name string) (Variable, bool) {
	for _, v := range environment {
		if v.Name == name {
			v.Readers = append([]string(nil), v.Readers...)
			return v, true
		}
	}
	return Variable{}, false
}
