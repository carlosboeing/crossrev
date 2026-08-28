package exec

import "time"

// Spec describes one child process: a program, an argument array, an exact
// environment, and nothing that a shell would have to interpret.
//
// The zero Spec imposes nothing, and that is a parity decision rather than an
// oversight. The Bash adapters run a harness as
//
//	( cd "$workdir" && "${run[@]}" ... ) >"$out" 2>"$err" </dev/null
//
// in lib/adapters/claude.sh:106, and the same line appears in codex.sh:90,
// agy.sh:89 and opencode.sh:182. There is no timeout in it and no head, tail or
// truncation of either capture file. A default cap here would silently cut a
// model payload that Bash delivered whole, and a default timeout would fail a
// long review that Bash completed. A caller that wants either says so.
type Spec struct {
	// Path is the program to run. A name with no separator is resolved on the
	// PATH of the calling process, which is what `env ... claude` does in
	// lib/adapters/claude.sh:67 — `env` strips names from the child's
	// environment and still resolves the program from its own.
	Path string

	// Args are the arguments after the program name. Each element reaches the
	// child as one argv entry whatever it contains, so spaces, newlines and the
	// empty string need no quoting and get none.
	Args []string

	// Dir is the working directory. Empty means the calling process's, which is
	// the `cd "$workdir"` of the adapters when workdir is empty.
	Dir string

	// Env is the complete environment the child receives, as NAME=VALUE entries.
	//
	// Nil and empty both mean an empty child environment. The runner never falls
	// back to the environment of the calling process: os/exec inherits the
	// parent's when Cmd.Env is nil, and inheriting is the credential leak this
	// package exists to stop (ADR 0001, SECURITY.md). Build the value with
	// Inherit, which is an allowlist.
	Env []string

	// Stdin is the input handed to the child. Nil means the child reads EOF at
	// once, which is the `</dev/null` every adapter redirects. It is required
	// rather than defensive: lib/adapters/opencode.sh:47 and
	// lib/adapters/codex.sh:87 both record the CLI blocking indefinitely on an
	// open stdin, with no output on either stream.
	Stdin []byte

	// MaxOutputBytes caps each captured stream independently, keeping the first
	// N bytes. Zero means uncapped. The child is never told: a write past the
	// cap is discarded and reported to the child as accepted, so a cap changes
	// what the caller sees and not whether the child completes.
	MaxOutputBytes int

	// Timeout bounds the child's run. Zero means no deadline. A deadline from
	// the context passed to Run applies as well, and the earlier of the two wins.
	Timeout time.Duration
}
