package exec

import (
	"strings"
	"time"
)

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
	// PATH of the calling process, which is what the `env ... claude` construct
	// at lib/adapters/claude.sh:67-89 does — `env` strips names from the
	// child's environment and still resolves the program from its own.
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
	//
	// An allowlist withholds a credential nobody named. Audience is what
	// withholds one somebody did.
	Env []string

	// Audience says who the child is, and the zero value says a model.
	Audience Audience

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

// Audience says whether the child is a model-facing process or the
// orchestrator's own tool.
//
// ADR 0001 makes one promise: no GitHub credential reaches a process that reads
// attacker-controlled text. Inherit is an allowlist, so it withholds every name
// nobody wrote down — but the three names that matter are exactly the ones a
// caller can write down, whether by naming one in an allowlist, reading one
// with os.LookupEnv, or taking one as an argument and putting it in Env. None
// of those is a mistake a type can prevent, so Run refuses them instead, and
// this field says when to.
type Audience int

const (
	// AudienceModelFacing is the zero value, and it is the strict one. Run
	// refuses to hand this child a forge credential.
	//
	// There is a real argument against a field like this: every caller carries
	// something it does not set, and the day somebody sets it wrong is silent.
	// That argument holds where forgetting the field fails open. Here it fails
	// closed — the value a caller gets by saying nothing is the one that
	// refuses — so the silent mistake is a refused run with a message naming
	// the variable, not a leaked token with no message at all.
	AudienceModelFacing Audience = iota

	// AudienceOrchestrator is a tool crossrev drives on its own behalf, which
	// may hold a forge credential.
	//
	// It is required rather than decorative. lib/github.sh never sets GH_TOKEN
	// anywhere — gh inherits it ambiently at every call site, lines 39, 74, 99
	// and 120 among them — so the forge adapter this package will carry must be
	// able to pass one. A blanket refusal in Run would break it.
	AudienceOrchestrator
)

// forgeCredentialNames are the three the Bash adapters strip before starting a
// model-facing process (lib/adapters/claude.sh:67, and the same line in
// codex.sh:83, agy.sh:85, grok.sh:70 and opencode.sh:176).
var forgeCredentialNames = []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN"}

// forgeCredentialIn returns the first forge credential named in env, and whether
// there was one. It reads names only; the value never leaves this function.
func forgeCredentialIn(env []string) (string, bool) {
	for _, entry := range env {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			// An entry with no separator names nothing. Skipping it is safe:
			// it cannot set a variable either.
			continue
		}
		for _, credential := range forgeCredentialNames {
			if name == credential {
				return name, true
			}
		}
	}
	return "", false
}
