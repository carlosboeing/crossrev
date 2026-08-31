// adapter.go — the interface every harness is reached through, and what is
// shared between the five.
//
// # An adapter builds a child process; it never starts one
//
// The Bash adapters do both: they assemble `env -u … <cli> …` and then run it
// inside a subshell (lib/adapters/claude.sh:111). Here the two halves are Spec
// and Envelope, with internal/exec's Runner in between. That split is not
// tidiness. internal/archtest forbids os/exec outside internal/exec, so an
// adapter physically cannot start anything; and every property exec.Spec
// carries — the exact environment, the closed stdin — is decided in one place
// rather than five.
//
// # Every adapter starts a model-facing process
//
// This is the process that reads attacker-controlled text
// (lib/adapters/codex.sh:79-82). The child is started through NewOSRunner,
// which refuses a forge credential. Nothing here may name
// exec.NewOrchestratorRunner, and internal/archtest refuses the package if it
// does (internal/archtest/audience_test.go).
//
// The environment is built by childEnv, which subtracts cred.StripFor's answer
// — the four forge credentials plus every vendor credential belonging to a
// harness that is not this one. NewOSRunner's refusal is the guard that cannot
// be forgotten; childEnv is what stops a leg reaching it.

package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/cred"
	"github.com/carlosboeing/crossrev/internal/exec"
)

// Adapter is one harness, reached the way lib/adapters/<name>.sh reaches it.
//
// There is no fallback in this interface and no routing. A leg names one
// harness, and an adapter that cannot serve the request refuses rather than
// choosing another — which is what every `ui_die` in the Bash adapters does.
type Adapter interface {
	// Name is the descriptor's name for this harness, and the name of the
	// Bash file this adapter was ported from.
	Name() string

	// NotInstalled is the refusal for a leg configured to use a CLI that is
	// not on the PATH. The Bash adapters answer it from `command -v` before
	// building anything (lib/adapters/claude.sh:19-21); Go has no equivalent
	// here, because looking a program up is os/exec's job and this package
	// cannot import it. The caller raises this when exec.IsNotFound answers
	// for the Result.
	NotInstalled() *Refusal

	// Spec is the child process that serves this invocation.
	//
	// It may write files under Invocation.Scratch, and does for the one
	// harness whose grant travels in a config file rather than in a flag. It
	// starts nothing.
	Spec(Invocation) (exec.Spec, error)

	// Envelope reads what the child produced.
	//
	// It never fails: a harness that errored answers an Envelope whose OK is
	// false and whose Error says why, because the Bash adapters print that
	// object and return 1 rather than dying (lib/adapters/claude.sh:124-133).
	// A Result whose Err reports a start failure is not that case — the Bash
	// adapter refuses at `command -v` before running anything — so the caller
	// translates one with NotInstalled first.
	Envelope(Invocation, exec.Result) Envelope
}

// endpointHostName is the harness a named endpoint is reached through, as the
// three adapters that hardcode it spell it (lib/adapters/codex.sh:28,
// agy.sh:38, opencode.sh:91). The grok adapter reads `.endpoint_host` from the
// descriptor instead (lib/adapters/grok.sh:28-31), and this package keeps that
// difference rather than smoothing it: the two say the same thing today, and
// which of them a descriptor edit would change is a fact about the shell that a
// port should not quietly rewrite.
const endpointHostName = "claude"

// For answers the adapter for a harness the descriptor names.
//
// An unknown name has no adapter, and that is the whole answer: there is no
// default and no nearest match. lib/harnesses.sh:110-114 refuses at load when a
// named harness has no lib/adapters/<name>.sh, and a name the descriptor does
// not carry never reaches an adapter at all.
func For(doc Document, name string) (Adapter, bool) {
	entry, known := doc.For(name)
	if !known {
		return nil, false
	}
	shared := base{doc: doc, descriptor: entry}
	switch name {
	case "claude":
		return &Claude{base: shared}, true
	case "codex":
		return &Codex{base: shared}, true
	case "agy":
		return &Agy{base: shared}, true
	case "grok":
		return &Grok{base: shared}, true
	case "opencode":
		return &Opencode{base: shared}, true
	}
	return nil, false
}

// Adapters is every adapter the descriptor names, in descriptor order.
//
// It answers an error for a named harness with no adapter, which is the
// filesystem check lib/harnesses.sh:110-114 does at load — "the descriptor
// names the harness 'n', and there is no lib/adapters/n.sh". A compiled binary
// has no lib/adapters/ to look in, so the switch above is the directory.
func Adapters(doc Document) ([]Adapter, error) {
	adapters := make([]Adapter, 0, len(doc.Names()))
	for _, name := range doc.Names() {
		adapter, known := For(doc, name)
		if !known {
			return nil, &Refusal{
				Reason: fmt.Sprintf("the descriptor names the harness '%s', and this build carries no adapter for it", name),
				Action: "Add the adapter, or remove the entry.",
				Kind:   ErrDescriptor,
			}
		}
		adapters = append(adapters, adapter)
	}
	return adapters, nil
}

// base is what the five share: the descriptor they were built from, and the
// helpers every one of them needs.
type base struct {
	doc        Document
	descriptor Descriptor
}

func (b base) Name() string { return b.descriptor.Name }

// notInstalled is the refusal shape every adapter prints, differing only in the
// sentence naming how to install this one.
func (b base) notInstalled(action string) *Refusal {
	return &Refusal{
		Reason: fmt.Sprintf("the %s CLI is not installed, and this leg is configured to use it", b.descriptor.Binary),
		Action: action,
		Kind:   ErrNotInstalled,
	}
}

// endpointRefusal is the answer of the four adapters that cannot reach a named
// endpoint. Only one adapter can, because a named endpoint is
// Anthropic-compatible.
func (b base) endpointRefusal(endpoint Endpoint, host, extra string) *Refusal {
	action := fmt.Sprintf("Named endpoints are Anthropic-compatible and reached through the %s adapter.", host)
	if extra != "" {
		action += " " + extra
	}
	action += fmt.Sprintf(" Use harness: %s with endpoint: %s, or drop the endpoint for this leg.", host, endpoint.Name)
	return &Refusal{
		Reason: fmt.Sprintf("the %s adapter cannot use the endpoint '%s'", b.descriptor.Name, endpoint.Name),
		Action: action,
		Kind:   ErrEndpointUnsupported,
	}
}

// spec is the shape every adapter's child has in common: the harness's own
// binary, the checkout as the working directory, stdin at EOF, and an
// environment with this harness's strip set removed.
//
// Stdin nil is `</dev/null`, and it is required rather than defensive. With a
// terminal attached Claude Code waits for piped input (lib/adapters/claude.sh:110),
// `codex exec` blocks indefinitely on "Reading additional input from stdin..."
// (lib/adapters/codex.sh:92-94), and opencode was measured silent for five
// minutes with zero bytes on either stream (lib/adapters/opencode.sh:47-50).
func (b base) spec(inv Invocation, args []string, additions ...string) exec.Spec {
	return exec.Spec{
		Path: b.descriptor.Binary,
		Args: args,
		Dir:  inv.Workdir,
		Env:  childEnv(b.doc, b.descriptor.Name, inv.Env, additions...),
	}
}

// stripFor is cred.StripFor over the credential view of the same descriptor.
//
// It is a function here rather than a second list. internal/cred already joins
// the four forge names to the vendor names this harness must not hold, and two
// lists of credential names drift apart silently
// (internal/cred/strip.go:32-44).
func stripFor(doc Document, harness string) []string {
	return cred.StripFor(doc.Credentials(), harness)
}

// --- reading a payload back out of a harness's answer ------------------------

// rawMember answers one top-level key of a JSON object, as the bytes the
// harness wrote.
//
// Key order does not matter for a single lookup, so this decodes into a map
// where usage.go's ordered tree would be overkill. What it keeps that the
// ordered tree does not is the ORIGINAL text of the value, which is what a
// payload is: crossrev hands it to a schema check and never compares it as a
// string, so re-serializing it would be work with no reader.
func rawMember(raw []byte, key string) (json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, false
	}
	value, found := object[key]
	return value, found
}

// alternativeValue is `// empty` over raw bytes: a null and a false are not
// values, and neither is an absent key.
func alternativeValue(raw json.RawMessage, found bool) (json.RawMessage, bool) {
	if !found {
		return nil, false
	}
	text := strings.TrimSpace(string(raw))
	if text == "null" || text == "false" {
		return nil, false
	}
	return raw, true
}

// firstAlternative is `jq -r '.a // .b // empty'` over an answer's members.
//
// jq's `//` steps past a null and a false and NOTHING else, so a member that is
// the empty string wins over every alternative after it. Measured:
//
//	{"error":"","response":"real"}    -> (empty)
//	{"error":null,"response":"real"}  -> real
//	{"error":false,"response":"real"} -> real
//
// The adapters then test the RESULT with `[[ -n "$msg" ]]`
// (lib/adapters/agy.sh:106, grok.sh:91), so an empty `error` falls through to
// the stderr diagnosis rather than to the next member. Reading the members as
// "the first non-empty string" put the harness's own response text where the
// stderr belonged, on exactly the runs where stderr holds the only diagnosis.
//
// # The declared divergence
//
// A member that is truthy but not a string answers the empty string here, where
// `jq -r` renders it: `{"error":5}` gives `5` and an object gives its JSON
// across several lines, because there is no `-c`. Both fall through to stderr
// here. It is left as a divergence rather than fixed because reproducing jq's
// multi-line rendering is more machinery than a case no harness produces is
// worth, and because both implementations still report a failure — a different
// sentence, never a success.
func firstAlternative(answer node, keys ...string) string {
	for _, key := range keys {
		member := answer.member(key)
		if !member.truthy() {
			continue
		}
		text, _ := member.asString()
		return text
	}
	return ""
}

// parseJSON is jq's `fromjson` followed by `jq -c .`: the text if it is one
// JSON value, compacted, and nothing if it is not.
func parseJSON(text string) (json.RawMessage, bool) {
	trimmed := []byte(text)
	if !json.Valid(trimmed) {
		return nil, false
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return nil, false
	}
	return json.RawMessage(compact.Bytes()), true
}

// itoa spells an exit status the way `$?` reaches the adapters' message.
func itoa(status int) string { return strconv.Itoa(status) }
