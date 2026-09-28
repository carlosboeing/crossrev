package harness_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// A transient harness failure is a server-side or transport failure worth
// asking once more about: the harness failed before answering rather than
// answering badly. Authentication, quota and refusal errors are never
// transient: retrying those immediately fixes nothing.
func TestIsTransientHarnessError(t *testing.T) {
	tests := []struct {
		reason string
		want   bool
	}{
		// The shapes that ended an agy review leg on its first attempt.
		{"UNAVAILABLE (code 503): Deadline expired", true},
		{"The stream was interrupted", true},
		{"the stream was interrupted mid-response", true},
		// Server-side 5xx equivalents in other harnesses' words.
		{"http 503 Service Unavailable", true},
		{"502 Bad Gateway", true},
		{"504 Gateway Timeout", true},
		{"upstream error: 500 Internal Server Error", true},
		{"request failed with status 529", true},
		{"the model is overloaded, try again shortly", true},
		{"Deadline exceeded after 110s", true},
		{"rpc error: code = Unavailable desc = transport closing", true},
		// Never transient: authentication and credential failures.
		{"", false},
		{"invalid api key", false},
		{"Not signed in. Run the login flow first", false},
		{"Grok rejected the credential. CrossRev classifies this as a credential failure, not a generic harness error.", false},
		{"request failed with status 401", false},
		{"request failed with status 403", false},
		{"UNAUTHENTICATED: invalid credentials", false},
		{"permission denied", false},
		// Never transient: quota and rate limits wait out a window.
		{"RESOURCE_EXHAUSTED (code 429): quota exceeded", false},
		{"quota exceeded", false},
		{"rate limit exceeded, try again in 20s", false},
		{"UNAVAILABLE (code 503): check your plan and billing details", false},
		{"context limit reached", false},
		{"spend limit reached", false},
		// Never transient: refusals, installs and ordinary failures.
		{"the model refused", false},
		{"claude exited 9 with no output on either stream", false},
		{"test tripwire", false},
		{"the harness CLI is not installed", false},
		{"No reason was given.", false},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			if got := harness.IsTransientHarnessError(tt.reason); got != tt.want {
				t.Errorf("IsTransientHarnessError(%q) = %v, want %v", tt.reason, got, tt.want)
			}
		})
	}
}
