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
//     at internal/resolve/invoke.go:517.
//   - A harness secret the descriptor does not name. lib/auth.sh:1034 falls back
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
var environment = []Variable{
	{
		Name:       "ANTHROPIC_API_KEY",
		Class:      ClassCredential,
		Descriptor: true,
		Readers:    []string{"internal/harness", "internal/resolve", "internal/review", "cmd/crossrev"},
	},
	{
		Name:    "ANTHROPIC_AUTH_TOKEN",
		Class:   ClassEndpoint,
		Readers: []string{"internal/harness", "internal/policy", "cmd/crossrev"},
	},
	{
		Name:    "ANTHROPIC_BASE_URL",
		Class:   ClassEndpoint,
		Readers: []string{"internal/harness", "internal/policy", "cmd/crossrev"},
	},
	{
		Name:       "CLAUDE_CODE_OAUTH_TOKEN",
		Readers:    []string{"cmd/crossrev"},
		Class:      ClassCredential,
		Descriptor: true,
	},
	{
		Name:       "CODEX_HOME",
		Readers:    []string{"cmd/crossrev"},
		Class:      ClassPathOverride,
		Descriptor: true,
	},
	{
		Name:    "CROSSREV_APP_SLUG",
		Class:   ClassOperatorInput,
		Readers: []string{"internal/resolve", "internal/review", "cmd/crossrev"},
	},
	{
		Name:    "CROSSREV_ASSUME_YES",
		Readers: []string{"cmd/crossrev"},
		Class:   ClassOperatorInput,
	},
	{
		Name:       "CROSSREV_CODEX_AUTH",
		Readers:    []string{"cmd/crossrev"},
		Class:      ClassCredential,
		Descriptor: true,
	},
	{
		Name:  "CROSSREV_DIFF_EXCLUDE",
		Class: ClassChildOutput,
	},
	{
		Name:  "CROSSREV_DIFF_PATH",
		Class: ClassChildOutput,
	},
	{
		Name:  "CROSSREV_DIFF_SIDE",
		Class: ClassChildOutput,
	},
	{
		Name:    "CROSSREV_GIT_EMAIL",
		Class:   ClassOperatorInput,
		Readers: []string{"internal/resolve"},
	},
	{
		Name:    "CROSSREV_GIT_NAME",
		Class:   ClassOperatorInput,
		Readers: []string{"internal/resolve"},
	},
	{
		Name:       "CROSSREV_GROK_AUTH",
		Readers:    []string{"cmd/crossrev"},
		Class:      ClassCredential,
		Descriptor: true,
	},
	{
		Name:    "CROSSREV_HARNESS_FILE",
		Class:   ClassPathOverride,
		Readers: []string{"cmd/crossrev"},
	},
	{
		Name:  "CROSSREV_HARNESS_INSTALL",
		Class: ClassChildOutput,
	},
	{
		Name:  "CROSSREV_LOG_RETENTION_DAYS",
		Class: ClassOperatorInput,
	},
	{
		Name:    "CROSSREV_NO_TIPS",
		Readers: []string{"cmd/crossrev"},
		Class:   ClassOperatorInput,
	},
	{
		Name:       "CROSSREV_OPENCODE_AUTH",
		Readers:    []string{"cmd/crossrev"},
		Class:      ClassCredential,
		Descriptor: true,
	},
	{
		Name:    "CROSSREV_OWNER",
		Readers: []string{"cmd/crossrev"},
		Class:   ClassOperatorInput,
	},
	{
		Name:  "CROSSREV_TRANSCRIPT_BASE",
		Class: ClassPathOverride,
	},
	{
		Name:    "GH_ENTERPRISE_TOKEN",
		Class:   ClassCredential,
		Readers: []string{"internal/exec", "internal/forge/ghexec", "internal/preflight", "cmd/crossrev"},
	},
	{
		Name:    "GH_TOKEN",
		Class:   ClassCredential,
		Readers: []string{"internal/exec", "internal/forge/ghexec", "internal/preflight", "cmd/crossrev"},
	},
	{
		Name:    "GITHUB_ACTIONS",
		Class:   ClassRunnerSignal,
		Readers: []string{"internal/preflight"},
	},
	{
		Name:    "GITHUB_ENTERPRISE_TOKEN",
		Class:   ClassCredential,
		Readers: []string{"internal/exec", "internal/forge/ghexec", "internal/preflight", "cmd/crossrev"},
	},
	{
		Name:    "GITHUB_RUN_ID",
		Class:   ClassRunnerSignal,
		Readers: []string{"internal/resolve", "internal/runlog"},
	},
	{
		Name:    "GITHUB_TOKEN",
		Class:   ClassCredential,
		Readers: []string{"internal/exec", "internal/forge/ghexec", "internal/preflight", "cmd/crossrev"},
	},
	{
		Name:    "GIT_INDEX_FILE",
		Class:   ClassChildOutput,
		Readers: []string{"internal/vcs"},
	},
	{
		Name:       "GROK_HOME",
		Readers:    []string{"cmd/crossrev"},
		Class:      ClassPathOverride,
		Descriptor: true,
	},
	{
		Name:  "HOME",
		Class: ClassPathOverride,
		Readers: []string{
			"internal/app", "internal/config", "internal/forge/ghexec",
			"internal/preflight", "internal/runlog", "internal/vcs",
			"cmd/crossrev",
		},
	},
	{
		Name:    "LC_ALL",
		Class:   ClassChildOutput,
		Readers: []string{"internal/app", "cmd/crossrev"},
	},
	{
		Name:    "NO_COLOR",
		Class:   ClassOperatorInput,
		Readers: []string{"cmd/crossrev"},
	},
	{
		Name:    "OPENCODE_CONFIG",
		Class:   ClassChildOutput,
		Readers: []string{"internal/harness", "cmd/crossrev"},
	},
	{
		Name:    "OPENCODE_CONFIG_DIR",
		Class:   ClassChildOutput,
		Readers: []string{"internal/harness", "cmd/crossrev"},
	},
	{
		Name:  "PATH",
		Class: ClassPathOverride,
		Readers: []string{
			"internal/app", "internal/exec", "internal/forge/ghexec",
			"internal/preflight",
			"cmd/crossrev",
		},
	},
	{
		Name:    "RUNNER_ENVIRONMENT",
		Class:   ClassRunnerSignal,
		Readers: []string{"internal/cred", "internal/preflight"},
	},
	{
		// Where auth_login writes the registration page and the redirect file
		// (lib/auth.sh:626). Read with a default and assigned nowhere in the
		// shell, so it is an inherited value the operator sets.
		//
		// The readers are the two allowlists that hand it to a child —
		// internal/app/listener.go:356 for the browser opener and
		// cmd/crossrev/legs.go:43 for a harness. The port's own use of it is
		// internal/app/login.go:363 and :368, which reach it through
		// os.CreateTemp and os.TempDir rather than by name, so the contract's
		// os.Getenv walk does not see it and internal/app is a reader here on
		// the strength of the allowlist alone.
		Name:    "TMPDIR",
		Class:   ClassPathOverride,
		Readers: []string{"internal/app", "cmd/crossrev"},
	},
	{
		Name:    "XDG_CONFIG_HOME",
		Class:   ClassPathOverride,
		Readers: []string{"internal/app", "internal/config", "internal/forge/ghexec", "internal/preflight", "cmd/crossrev"},
	},
	{
		Name:       "XDG_DATA_HOME",
		Class:      ClassPathOverride,
		Descriptor: true,
		Readers:    []string{"internal/app", "cmd/crossrev"},
	},
	{
		Name:    "XDG_STATE_HOME",
		Class:   ClassPathOverride,
		Readers: []string{"internal/preflight", "internal/runlog", "internal/vcs", "cmd/crossrev"},
	},
}

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
