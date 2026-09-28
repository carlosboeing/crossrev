// transient.go — which harness failures are worth asking once more about.
//
// A transient failure is a server-side or transport failure: the harness
// failed before answering rather than answering badly. The legs retry one of
// those once before failing, and never retry authentication, quota or refusal
// errors — retrying those immediately fixes nothing.
//
// The shapes come from observed runs, not from a vendor error reference:
// agy 1.2.12 ended a review leg three ways on its first attempt —
// `UNAVAILABLE (code 503): Deadline expired`, `The stream was interrupted`,
// and `status: SUCCESS` with an empty `response`. The 5xx half follows the
// gRPC retry guidance, where UNAVAILABLE and DEADLINE_EXCEEDED are retryable
// and UNAUTHENTICATED is not; it is matched as text because the adapters
// surface vendor sentences rather than status codes. The empty answer is not
// matched here at all: a successful envelope carries no error text, so the
// legs detect the empty payload themselves.
//
// The status patterns use the same standalone-digits form as quota.go's
// status429Pattern, so a duration such as 142900ms never reads as a status.
package harness

import (
	"regexp"
	"strings"
)

// transient5xxPattern is a standalone 5xx status, including 529, the
// overloaded answer the Claude API documents as retryable.
var transient5xxPattern = regexp.MustCompile(`(?:^|[^0-9])(500|502|503|504|529)(?:[^0-9]|$)`)

// transientAuthStatusPattern is a standalone authentication status. It vetoes
// before the 5xx half runs, so a message carrying both still does not retry.
var transientAuthStatusPattern = regexp.MustCompile(`(?:^|[^0-9])(401|403)(?:[^0-9]|$)`)

// transientSubstrings are the server-side and transport failures worth one
// more attempt, matched case-insensitively.
var transientSubstrings = []string{
	"unavailable",
	"deadline expired",
	"deadline exceeded",
	"deadline_exceeded",
	"bad gateway",
	"gateway timeout",
	"gateway timed out",
	"server error",
	"overload",
}

// transientExclusions are stops that read transient-adjacent but must never
// retry: credential and authentication failures, billing stops, refusals,
// and legs that never started. Exclusions win over every inclusion.
var transientExclusions = []string{
	"unauthor",
	"forbidden",
	"not signed in",
	"xai_api_key",
	"api key",
	"apikey",
	"credential",
	"authentica",
	"login",
	"sign in",
	"signin",
	"permission denied",
	"access denied",
	"billing",
	"billed",
	"spend",
	"went unanswered",
	"consent",
	"refus",
	"not installed",
	"no adapter",
	"endpoint",
}

// IsTransientHarnessError reports whether reason describes a transient
// server-side or transport failure: a 5xx status, an unavailable or
// overloaded service, an expired or exceeded deadline, or an interrupted
// stream. Quota stops wait out a window rather than retrying, so
// IsQuotaError is consulted first and wins.
func IsTransientHarnessError(reason string) bool {
	if reason == "" {
		return false
	}
	if IsQuotaError(reason) {
		return false
	}
	lower := strings.ToLower(reason)
	if transientAuthStatusPattern.MatchString(reason) {
		return false
	}
	for _, ex := range transientExclusions {
		if strings.Contains(lower, ex) {
			return false
		}
	}
	if transient5xxPattern.MatchString(reason) {
		return true
	}
	for _, sub := range transientSubstrings {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	// The interrupted stream the harness reports on a normal exit. Both
	// halves are required, so the signal death the legs already own — "the
	// harness was interrupted", with no stream in it — never matches.
	return strings.Contains(lower, "stream") && strings.Contains(lower, "interrupt")
}
