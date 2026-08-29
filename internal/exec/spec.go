package exec

import (
	"slices"
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

	// Streams says whether the child's two output streams stay apart or arrive
	// as one, and the zero value keeps them apart.
	Streams Streams

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
// nobody wrote down — but the four names that matter are exactly the ones a
// caller can write down, whether by naming one in an allowlist, reading one
// with os.LookupEnv, or taking one as an argument and putting it in Env. None
// of those is a mistake a type can prevent, so Run refuses them instead, and
// this field says when to.
//
// # Why a struct with an unexported field rather than an integer
//
// It was `type Audience int` with two constants, and the confinement rule in
// internal/archtest scanned for the spellings that reach the opt-out value.
// That scan can never be complete, because the value can be computed. Two
// rounds of review planted eight spellings; the rule grew a clause for each of
// the first seven, and the eighth walked past all of them:
//
//	var a exec.Audience
//	_ = json.Unmarshal([]byte("1"), &a)   // orchestrator-facing at run time
//
// No name, no conversion, no constant, no assignment target a scan can read.
// Enumerating spellings is a losing game, so the type plays a different one:
// the orchestrator state lives in an unexported field.
//
// # What the type closes: building the value from nothing
//
// Outside internal/exec there is no expression that produces an Audience whose
// field is true other than the variable declared below. A conversion from an
// integer, arithmetic on the strict sibling, an increment of the field or of an
// array element, a typed constant declaration, a typed variable declaration and
// a keyed field in a Spec literal are all compile errors, because the field
// cannot be named. The unmarshal is a no-op for the same reason: encoding/json
// ignores an unexported field, so the value stays model-facing and Run refuses
// the child. internal/exec/credential_test.go asserts that last one.
//
// # What the type does not close: capture, and unsafe
//
// Two routes reach the value without constructing it, both found by review.
// They are written down here because an overstated security claim is worse than
// no claim: the next reader trusts it and stops looking.
//
// The first is capture. internal/vcs and internal/forge/ghexec hand every Spec
// they build to an injected Runner, and every one of those Specs carries
// AudienceOrchestrator. A package that can supply the Runner copies the value
// straight off the Spec, naming nothing:
//
//	func (t *thief) Run(_ context.Context, spec exec.Spec) exec.Result {
//		t.stolen = spec.Audience
//		return exec.Result{}
//	}
//
// The second is unsafe. The value is one bool, so
// *(*bool)(unsafe.Pointer(&a)) = true writes it from any package that may
// import unsafe.
//
// internal/archtest answers the first with a rule on references to
// Spec.Audience and the second with a rule on importing unsafe. Neither is
// airtight and neither can be. A value that travels between packages through an
// exported struct field can be copied by anything the field passes through, and
// intercepting the Spec is exactly what the injected Runner is for; reflection
// over an exported field walks past a scan that reads names.
//
// # What the confinement is worth, stated exactly
//
// Both routes need a Go file committed to this repository. Anyone who can add
// that file can also edit internal/archtest, so those rules sit at the same
// trust level as the thing they check. Their job is to catch an accident during
// review — a Spec that picked up the opt-out on its way somewhere it should not
// go — and they do not stop somebody who means it. Code review stops that.
//
// The guard that runs is Run's own refusal. OSRunner.Run reads
// spec.Audience.orchestrator before it builds an environment and before it
// starts a child, so a Spec that never obtained the opt-out is refused whatever
// the rest of the tree looks like. What it cannot do is tell a stolen opt-out
// from an honest one, because the field is the whole decision — which is why
// the rules above are described as review aids rather than as a boundary.
//
// The zero value is still the strict one, which is the property the whole
// design rests on: a caller that says nothing is refused a forge credential.
type Audience struct {
	// orchestrator is the whole value. Unexported, so no package outside this
	// one can write it: the only route the language offers to a true is
	// AudienceOrchestrator, declared below and confined by internal/archtest.
	// Copying a true off a Spec, and unsafe, are not routes the language
	// offers — the type comment above says what that is worth.
	//
	// encoding/json ignores an unexported field entirely, so unmarshalling into
	// an Audience cannot set it — which is the route that motivated the change.
	orchestrator bool
}

// The two audiences are variables rather than constants because a struct value
// cannot be a Go constant. That trades away immutability, so nothing reads them
// to make the decision: OSRunner.Run tests spec.Audience.orchestrator directly.
// Reassigning either variable can therefore only change what a caller stores in
// a Spec, and storing the orchestrator value already required naming it — which
// is what the confinement rule reads.
var (
	// AudienceModelFacing is the zero value, and it is the strict one. Run
	// refuses to hand this child a forge credential.
	//
	// There is a real argument against a field like this: every caller carries
	// something it does not set, and the day somebody sets it wrong is silent.
	// That argument holds where forgetting the field fails open. Here it fails
	// closed — the value a caller gets by saying nothing is the one that
	// refuses — so the silent mistake is a refused run with a message naming
	// the variable, not a leaked token with no message at all.
	AudienceModelFacing = Audience{}

	// AudienceOrchestrator is a tool crossrev drives on its own behalf, which
	// may hold a forge credential.
	//
	// It is required rather than decorative. lib/github.sh never sets GH_TOKEN
	// anywhere — gh inherits it ambiently at every call site, lines 39, 74, 99
	// and 120 among them — so the forge adapter this package will carry must be
	// able to pass one. A blanket refusal in Run would break it.
	AudienceOrchestrator = Audience{orchestrator: true}
)

// String names the audience, so a test failure reads as the name a caller wrote
// rather than as the struct's one field.
func (a Audience) String() string {
	if a.orchestrator {
		return "AudienceOrchestrator"
	}
	return "AudienceModelFacing"
}

// Streams says how the child's stdout and stderr reach the caller.
//
// Two captures cannot be merged afterwards, which is the whole reason this
// exists. A caller that concatenates them has invented an order: the child
// wrote them interleaved, and nothing in either buffer records where one
// stream's line sat relative to the other's.
//
// That is not a cosmetic loss. lib/github.sh:510 captures a failing push with
// `2>&1` and hands the text to _gh_git_tail (lib/github.sh:404-410), which
// keeps the last five non-blank lines — a selection by position. With a
// pre-push hook writing to both streams, git sends the hook's stdout to git's
// stdout and the hook's stderr plus its own `error: failed to push some refs`
// to stderr; concatenating stdout ahead of stderr then pushes every stdout line
// out of the five-line window, so the two implementations keep a different SET
// of lines rather than the same lines in a different order. That text is
// published: lib/github.sh:513 makes it the ui_die reason, lib/ui.sh:115 stores
// it, and lib/run.sh:144-146 writes it into a pull request comment.
type Streams int

const (
	// StreamsSeparate is the zero value: stdout and stderr are captured
	// independently, into Result.Stdout and Result.Stderr.
	//
	// It is the zero value because it is what every caller had before this
	// field existed, and because it is the answer that loses nothing a caller
	// might want — a caller that needs the two apart cannot recover them from a
	// merged stream either.
	StreamsSeparate Streams = iota

	// StreamsCombined puts both of the child's output descriptors on one pipe,
	// so the capture is the child's own write order.
	//
	// It is `2>&1`, done the way the shell does it — at the descriptor, before
	// the child starts — rather than by reordering afterwards. Result.Stdout
	// carries the merged stream and Result.Stderr carries nothing, which is
	// what `2>&1` leaves behind.
	StreamsCombined
)

// forgeCredentialNames are the four the Bash adapters strip before starting a
// model-facing process (lib/adapters/claude.sh:72, and the same line in
// codex.sh:88, agy.sh:90, grok.sh:75 and opencode.sh:181).
//
// Four, not three. `gh help environment` documents GH_ENTERPRISE_TOKEN and
// GITHUB_ENTERPRISE_TOKEN "in order of precedence", so on a GitHub Enterprise
// Server installation the second name is the credential in use whenever the
// first is unset. A list naming only the first hands it to the process that
// reads attacker-controlled text.
var forgeCredentialNames = []string{
	"GH_TOKEN",
	"GITHUB_TOKEN",
	"GH_ENTERPRISE_TOKEN",
	"GITHUB_ENTERPRISE_TOKEN",
}

// ForgeCredentialNames is the list above, for a caller that has to remove those
// names from an environment before it reaches Run.
//
// Run refuses a model-facing Spec that still carries one, which is the guard
// that cannot be forgotten. This is the list that lets a caller not get there:
// lib/adapters/claude.sh:72 builds `env -u GH_TOKEN …` and the same line appears
// in codex.sh:88, agy.sh:90, grok.sh:75 and opencode.sh:181. Exported rather
// than copied into internal/cred, because two lists of credential names drift
// apart silently and the whole point of the four is that none is missed.
//
// It answers a fresh slice each time, for the reason internal/validate's asset
// accessors do: an exported slice variable is writable from any package in the
// binary, and shortening this one would widen the boundary everywhere at once.
func ForgeCredentialNames() []string { return slices.Clone(forgeCredentialNames) }

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
