// errors.go — the refusals an adapter can raise, and the part of a harness's
// stderr worth showing.
//
// Every fatal path in the Bash adapters is a `ui_die "<reason>" "<action>"`
// (lib/adapters/claude.sh:19-21, codex.sh:22-29, agy.sh:32-39, grok.sh:23-32,
// opencode.sh:85-92). internal/ui already models that pair as ui.FatalError, and
// this package cannot use it: the tier rules in
// internal/archtest/dependencies_test.go:38 allow internal/harness internal/exec,
// internal/cred and internal/runlog, and internal/ui is a tier-2 peer. So the
// pair travels as a value of its own and the command layer turns it into a
// ui.FatalError — the same arrangement internal/cred/errors.go:1-9 records.
//
// No message here quotes a credential or a prompt. A harness's stderr can carry
// either, so HarnessError's answer goes through runlog.Redact at the adapter
// before it reaches an Envelope.

package harness

import (
	"errors"
	"regexp"
	"strings"
)

// The kinds a caller can ask about with errors.Is. Each is the sentinel a
// Refusal reports itself as; none is ever returned on its own.
var (
	// ErrNotInstalled is a leg configured to use a CLI that is not on the PATH
	// (lib/adapters/claude.sh:19-21).
	ErrNotInstalled = errors.New("the harness CLI is not installed")

	// ErrEndpointUnsupported is a named endpoint given to an adapter that
	// cannot reach one. Named endpoints are Anthropic-compatible and reached
	// through one adapter (lib/adapters/codex.sh:26-29).
	ErrEndpointUnsupported = errors.New("this adapter cannot use a named endpoint")

	// ErrEndpointToken is a named endpoint whose token variable is unset.
	// CrossRev will not fall back to the vendor's own API
	// (lib/adapters/claude.sh:83-85).
	ErrEndpointToken = errors.New("the endpoint's token variable is unset")

	// ErrHardening is a harness whose hardening arguments could not be
	// resolved, which halts rather than running unhardened
	// (lib/adapters/codex.sh:52-55).
	ErrHardening = errors.New("the hardening arguments could not be resolved")

	// ErrSchemaUnavailable is a schema named by path where the adapter needs
	// its text, or the other way round. It has no Bash counterpart: the shell
	// holds one variable, a file path, and reads the file itself.
	ErrSchemaUnavailable = errors.New("the schema is not available in the form this harness takes")

	// ErrScratch is an adapter that needs somewhere to write a file it must
	// hand the CLI, and was given nowhere (lib/adapters/opencode.sh:122-124).
	ErrScratch = errors.New("the adapter needs a scratch directory and was given none")

	// ErrEndpointLeaked is an endpoint variable set in the environment CrossRev
	// inherited, which redirects a harness process-wide
	// (lib/legs.sh:494-501).
	ErrEndpointLeaked = errors.New("an endpoint variable is set in the inherited environment")

	// ErrModelsConverged is two legs configured to differ that the same model
	// answered (lib/legs.sh:554-561).
	ErrModelsConverged = errors.New("both legs were configured to differ and one model answered each")
)

// Refusal is a fatal harness decision: what went wrong, and what to do about it.
type Refusal struct {
	// Reason is the sentence a `ui_die` prints first, and the text a pull
	// request marker carries when a leg dies.
	Reason string
	// Action is the second half — rule 4 of the output voice. Printed, never
	// stored on the pull request.
	Action string
	// Kind is the sentinel this refusal matches under errors.Is.
	Kind error
	// Err is the underlying failure, when there was one.
	Err error
}

func (e *Refusal) Error() string { return e.Reason }

// Is answers errors.Is for the sentinel this refusal was minted under.
func (e *Refusal) Is(target error) bool { return target == e.Kind }

// Unwrap exposes the underlying failure.
func (e *Refusal) Unwrap() error { return e.Err }

// harnessErrorCap is the default second argument of legs_harness_error
// (lib/legs.sh:518).
const harnessErrorCap = 400

// diagnosisPattern is the `grep -iE` of lib/legs.sh:520.
var diagnosisPattern = regexp.MustCompile(
	`(?i)error|fatal|denied|unauthor|forbidden|invalid|expired|timed out|refused|not found`)

// HarnessError is legs_harness_error (lib/legs.sh:517-534): the part of a
// harness's stderr worth showing, capped.
//
// `head -c 400` reads the wrong end of the stream. Every harness opens with a
// banner — version, workdir, model, sandbox, session id — and codex echoes the
// prompt after it, so on a real leg the first 400 bytes are banner and diff and
// the error is nowhere in them. Measured on a captured unauthenticated run: the
// first "401" sat at byte 402, two bytes past the window, with a two-word
// prompt. A review prompt pushes it thousands of bytes out.
//
// So: search for a line that looks like a diagnosis, and prefer the last one.
// Harnesses retry, and the final message is the one that stuck — codex reports
// the same 401 nine times and only the last carries the reason phrase. Falling
// back to the tail rather than the head keeps that property when no keyword
// matches, because the banner is always at the top and never worth the budget.
//
// The cap counts characters rather than bytes. Bash's `${#picked}` and
// `${picked: -cap}` are both character-oriented under the UTF-8 locale the
// oracle was captured in, so runes are what reproduces it; the Bash comment's
// word "bytes" describes the intent rather than the arithmetic.
func HarnessError(stderr []byte) string {
	if len(stderr) == 0 {
		return ""
	}
	lines := splitLines(string(stderr))

	var matched []string
	for _, line := range lines {
		if diagnosisPattern.MatchString(line) {
			matched = append(matched, line)
		}
	}
	picked := strings.Join(lastLines(matched, 2), "\n")
	if picked == "" {
		picked = strings.Join(lastLines(lines, 3), "\n")
	}

	// Trimming to the cap takes the end, for the same reason the search does. A
	// cut that lands mid-line then drops that partial line, so the output never
	// opens halfway through a sentence — unless dropping it would leave
	// nothing, which is one line longer than the whole budget and better shown
	// cut than not at all.
	runes := []rune(picked)
	if len(runes) > harnessErrorCap {
		picked = string(runes[len(runes)-harnessErrorCap:])
		if at := strings.Index(picked, "\n"); at >= 0 && at+1 < len(picked) {
			picked = picked[at+1:]
		}
	}
	return picked
}

// splitLines is what grep and tail see: a trailing newline does not make an
// extra empty line, and a final line without one is still a line.
func splitLines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func lastLines(lines []string, count int) []string {
	if len(lines) <= count {
		return lines
	}
	return lines[len(lines)-count:]
}
