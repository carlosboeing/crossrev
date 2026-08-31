package harness_test

import (
	"strings"
	"testing"

	execpkg "github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/runlog"
)

// A credential shape on stderr never reaches Envelope.Error.
//
// Every adapter's failure branch spells `log_redact_str "$msg"` in the Bash
// (lib/adapters/claude.sh:125, codex.sh:100, agy.sh:108, grok.sh:97,
// opencode.sh:207) and runlog.Redact here. Nothing asserted it: replacing the
// call with the message itself in any of the four left the suite green, and the
// message is published — the run log keeps it, and lib/run.sh writes a harness
// failure into a pull request comment.
//
// The token below is a fabricated string in the shape the filter matches, not a
// credential. The assertion is on the SUFFIX rather than on the whole masked
// form, because the mask keeps a six-character prefix by design and a test that
// pinned the prefix would be asserting the filter rather than the call to it.
func TestEveryAdapterRedactsTheMessageItPublishes(t *testing.T) {
	// The tail each pattern in internal/runlog masks away.
	const secretTail = "AAAABBBBCCCCDDDDEEEEFFFF"

	tests := []struct {
		harness string
		// result is a failing run whose only diagnosis is on stderr, which is
		// the branch every adapter routes through Redact.
		result func(stderr []byte) execpkg.Result
	}{
		{harness: "claude", result: func(stderr []byte) execpkg.Result {
			return execpkg.Result{ExitCode: 1, Stderr: stderr}
		}},
		{harness: "codex", result: func(stderr []byte) execpkg.Result {
			return execpkg.Result{ExitCode: 1, Stderr: stderr}
		}},
		{harness: "agy", result: func(stderr []byte) execpkg.Result {
			return execpkg.Result{ExitCode: 1, Stderr: stderr}
		}},
		{harness: "grok", result: func(stderr []byte) execpkg.Result {
			return execpkg.Result{ExitCode: 1, Stderr: stderr}
		}},
		{harness: "opencode", result: func(stderr []byte) execpkg.Result {
			return execpkg.Result{ExitCode: 1, Stderr: stderr}
		}},
	}

	// One line per credential family the filter knows, so an adapter that
	// redacted only some of them would still fail.
	secrets := []string{
		"sk-ant-" + secretTail,
		"github_pat_" + secretTail,
		"ghp_" + secretTail,
		"xai-" + secretTail,
	}

	doc := descriptors(t)
	for _, tt := range tests {
		t.Run(tt.harness, func(t *testing.T) {
			adapter, known := harness.For(doc, tt.harness)
			if !known {
				t.Fatalf("the descriptor names no %s adapter", tt.harness)
			}
			for _, secret := range secrets {
				// The secret has to sit on a line legs_harness_error KEEPS,
				// or the case would pass on the selection rather than on the
				// filter: HarnessError takes the last two lines matching its
				// diagnosis pattern (lib/legs.sh:520) and drops the rest.
				stderr := []byte("starting up\nfatal: unauthorized for token " + secret + "\n")
				// The filter is the oracle: if it does not mask this shape, the
				// case proves nothing about the adapter.
				if masked := runlog.Redact(string(stderr)); strings.Contains(masked, secretTail) {
					t.Fatalf("runlog.Redact leaves %q intact, so this case cannot detect a missing call", secret)
				}

				envelope := adapter.Envelope(invocation(t, tt.harness, false), tt.result(stderr))
				if envelope.OK {
					t.Fatalf("a failing run produced an ok envelope: %+v", envelope)
				}
				message := deref(envelope.Error)
				if message == "" {
					t.Fatalf("the envelope carries no error, so nothing was redacted or published")
				}
				if strings.Contains(message, secretTail) {
					t.Errorf("Envelope.Error carries the unmasked tail of %s:\n  %s", secret, message)
				}
			}
		})
	}
}
