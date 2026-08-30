package harness_test

import (
	"strings"
	"testing"

	execpkg "github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

// The credential classifiers say "the credential was rejected" only for the
// stderr that actually means it.
//
// # Why the boundary matters in both directions
//
// opencode falls through to a DIFFERENT provider when the configured one cannot
// authenticate, so calling an ordinary harness failure a credential failure
// sends the reader looking in the wrong place entirely, and calling a real
// rejection an ordinary failure hides the one thing they can fix. Both
// adapters' headers argue this and nothing tested the inputs: loosening
// opencode's `&&` to `||` made a bare error event classify as a credential
// failure and survived, and so did dropping grok's XAI_API_KEY alternative.
//
// # The case sensitivity is deliberate on both sides, and asymmetric
//
// The Bash runs `grep -qiE` over the stderr capture file and a case-SENSITIVE
// `[[ "$msg" == *"Not signed in"* ]]` over the message (lib/adapters/grok.sh:94-95).
// Reproducing one and not the other is invisible without a case that differs
// between them, which is what the grok rows below are.
func TestCredentialRejectionIsClassifiedOnTheRightInputs(t *testing.T) {
	const credentialSentence = "CrossRev classifies this as a credential failure"

	tests := []struct {
		name    string
		harness string
		result  execpkg.Result
		want    bool
	}{
		// --- opencode: both halves of the conjunction are required ---------
		{
			name:    "opencode: AI_APICallError with Unauthorized behind it",
			harness: "opencode", want: true,
			result: execpkg.Result{ExitCode: 1,
				Stderr: []byte("AI_APICallError: request failed\nUnauthorized\n")},
		},
		{
			name:    "opencode: AI_APICallError with a bare 401 behind it",
			harness: "opencode", want: true,
			result: execpkg.Result{ExitCode: 1,
				Stderr: []byte("AI_APICallError: request failed\nstatus 401\n")},
		},
		{
			name:    "opencode: AI_APICallError alone is not a credential failure",
			harness: "opencode", want: false,
			result: execpkg.Result{ExitCode: 1,
				Stderr: []byte("AI_APICallError: the provider is overloaded\n")},
		},
		{
			name:    "opencode: Unauthorized alone is not a credential failure",
			harness: "opencode", want: false,
			result: execpkg.Result{ExitCode: 1,
				Stderr: []byte("the tool call was Unauthorized by the sandbox\n")},
		},
		{
			name:    "opencode: a bare error event is not a credential failure",
			harness: "opencode", want: false,
			result: execpkg.Result{ExitCode: 1, Stderr: []byte("error: rate limited\n")},
		},
		{
			// 401 is matched as a number rather than as a substring, so an id
			// that merely contains the digits is not a rejection.
			name:    "opencode: 401 inside a longer number is not a status",
			harness: "opencode", want: false,
			result: execpkg.Result{ExitCode: 1,
				Stderr: []byte("AI_APICallError: request 94011 failed\n")},
		},

		// --- grok: two alternatives on stderr, matched case-insensitively --
		{
			name:    "grok: Not signed in on stderr",
			harness: "grok", want: true,
			result: execpkg.Result{ExitCode: 1, Stderr: []byte("fatal: Not signed in\n")},
		},
		{
			name:    "grok: not signed in in another case on stderr",
			harness: "grok", want: true,
			result: execpkg.Result{ExitCode: 1, Stderr: []byte("fatal: NOT SIGNED IN\n")},
		},
		{
			name:    "grok: XAI_API_KEY on stderr",
			harness: "grok", want: true,
			result: execpkg.Result{ExitCode: 1, Stderr: []byte("error: XAI_API_KEY is unset\n")},
		},
		{
			name:    "grok: xai_api_key in another case on stderr",
			harness: "grok", want: true,
			result: execpkg.Result{ExitCode: 1, Stderr: []byte("error: xai_api_key is unset\n")},
		},
		{
			name:    "grok: an ordinary failure on stderr",
			harness: "grok", want: false,
			result: execpkg.Result{ExitCode: 1, Stderr: []byte("error: the model is overloaded\n")},
		},

		// --- grok: the same two, in the MESSAGE, matched case-sensitively --
		{
			name:    "grok: Not signed in in the message",
			harness: "grok", want: true,
			result: execpkg.Result{ExitCode: 1, Stdout: []byte(`{"error":"Not signed in"}`)},
		},
		{
			// The Bash test on the message is `==` rather than `grep -i`, so
			// this one is NOT a credential failure. It is the case that tells
			// the two tests apart.
			name:    "grok: not signed in lowercased in the message only",
			harness: "grok", want: false,
			result: execpkg.Result{ExitCode: 1, Stdout: []byte(`{"error":"not signed in"}`)},
		},
		{
			name:    "grok: XAI_API_KEY in the message",
			harness: "grok", want: true,
			result: execpkg.Result{ExitCode: 1, Stdout: []byte(`{"error":"set XAI_API_KEY first"}`)},
		},
		{
			name:    "grok: xai_api_key lowercased in the message only",
			harness: "grok", want: false,
			result: execpkg.Result{ExitCode: 1, Stdout: []byte(`{"error":"set xai_api_key first"}`)},
		},
	}

	doc := descriptors(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, known := harness.For(doc, tt.harness)
			if !known {
				t.Fatalf("the descriptor names no %s adapter", tt.harness)
			}
			envelope := adapter.Envelope(invocation(t, tt.harness, false), tt.result)
			if envelope.OK {
				t.Fatalf("a failing run produced an ok envelope: %+v", envelope)
			}
			message := deref(envelope.Error)
			if got := strings.Contains(message, credentialSentence); got != tt.want {
				t.Errorf("classified as a credential failure = %t, want %t\n  message: %s",
					got, tt.want, message)
			}
		})
	}
}
