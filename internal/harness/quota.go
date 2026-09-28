// quota.go — classification of quota and rate-limit errors and resolution of
// per-harness reset times.
//
// Harnesses report quota exhaustion and rate limits through various error
// strings on stderr, stdout, or HTTP status codes. When a leg halts because of
// a quota stop, crossrev informs the operator of the expected resume time and
// the command to re-run rather than reporting that a human decision is needed.
//
// The sentences below come from each harness's own documentation or source,
// not from guesswork:
//   - Claude Code: the error reference at https://code.claude.com/docs/en/errors
//   - Codex: UsageLimitReachedError and format_retry_timestamp in
//     codex-rs/protocol/src/error.rs of openai/codex
//   - Antigravity: no vendor-published error reference; the
//     "RESOURCE_EXHAUSTED (code 429): Individual quota reached. … Resets in
//     <duration>" shape is corroborated across independent observed reports
//   - Grok: https://docs.x.ai/developers/rate-limits documents the 429; the
//     CLI copy is RATE_LIMITED_USER_MESSAGE_OAUTH and
//     RATE_LIMITED_USER_MESSAGE_API_KEY in error.rs of xai-org/grok-build,
//     cited by independent CLI contract notes
//   - opencode: no authored quota sentence; provider errors pass through, so
//     the generic 429 and rate-limit signals apply
//   - kimi: not a driven harness (see not_driven in assets/harnesses.json);
//     it is reached through the claude adapter, so the Claude Code sentences
//     apply

package harness

import (
	"regexp"
	"strings"
)

// quotaExclusions are documented stops that read like a quota but are not a
// session window, so the operator cannot wait them out. Each fires against at
// least one inclusion below; exclusions win over inclusions.
var quotaExclusions = []string{
	// Codex "Quota exceeded. Check your plan and billing details."
	// (QuotaExceeded in error.rs): retry_delay returns None, so it is a
	// billing stop, not a usage window.
	"check your plan and billing details",
	// Claude Code Fable consent prompt closed unanswered: answer the prompt
	// or switch model, don't wait out a window.
	"went unanswered",
	// A full local disk (EDQUOT/ENOSPC), not a subscription window.
	"disk quota",
	// "Context limit reached": the request is too large; compact, don't wait.
	"context limit",
	// Codex "agent thread limit reached": too many threads, not a window.
	"thread limit",
	// "Server is temporarily limiting requests (not your usage limit)": the
	// page's own disambiguator says it is not a plan quota.
	"not your usage limit",
}

// usageWarningPattern is the "you've used most of your window" notice
// ("You've used 85% of your session limit · resets 3:45pm"): it prints a
// reset time but the leg is still running, so it is never a halt.
var usageWarningPattern = regexp.MustCompile(`(?i)\bused \d+% of your\b`)

// spendOrBudget reports whether reason is a spend cap, shared budget, or
// usage-billing cap sentence. Those are billing stops unless the message
// also names the plan window's reset ("· your session limit resets 3:45pm",
// "spend limit reached (daily; resets 2026-08-09 00:00 UTC)"), in which
// case the error reference says access returns then without anyone raising
// the limit, so waiting fixes it.
func spendOrBudget(lower string) bool {
	return strings.Contains(lower, "spend limit") ||
		strings.Contains(lower, "spend cap") ||
		strings.Contains(lower, "shared budget") ||
		strings.Contains(lower, "individual usage limit")
}

// hitLimitPattern is the "You've hit your <name> limit" sentence family:
// the session, weekly, Opus and Sonnet forms on the Claude Code error
// reference, and Codex's "You’ve hit your usage limit", which uses a curly
// apostrophe.
var hitLimitPattern = regexp.MustCompile(`(?i)\byou['’]ve hit your [a-z0-9' \-]{1,40}?\blimit\b`)

// status429Pattern is a standalone 429 status, not digits that happen to
// contain it inside a longer number such as 142900.
var status429Pattern = regexp.MustCompile(`(?:^|[^0-9])429(?:[^0-9]|$)`)

// IsQuotaError reports whether reason describes a quota exhaustion or rate-limit
// failure.
func IsQuotaError(reason string) bool {
	lower := strings.ToLower(reason)
	if usageWarningPattern.MatchString(reason) {
		return false
	}
	for _, ex := range quotaExclusions {
		if strings.Contains(lower, ex) {
			return false
		}
	}
	if spendOrBudget(lower) {
		return extractResetTime(reason) != ""
	}
	if hitLimitPattern.MatchString(reason) {
		return true
	}
	if status429Pattern.MatchString(reason) {
		return true
	}
	return strings.Contains(lower, "quota") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "rate-limit") ||
		strings.Contains(lower, "rate_limit") ||
		strings.Contains(lower, "rate limited") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "resource exhausted") ||
		strings.Contains(lower, "resource_exhausted") ||
		strings.Contains(lower, "has been exhausted") ||
		strings.Contains(lower, "usage limit") ||
		strings.Contains(lower, "limit exceeded") ||
		strings.Contains(lower, "limit reached")
}

// QuotaReset checks whether reason is a quota stop. If so, it returns the
// resume time T (extracted from reason or from harness default) and true.
// If reason is not a quota error, it returns ("", false).
func QuotaReset(harn, reason string) (string, bool) {
	if !IsQuotaError(reason) {
		return "", false
	}
	if t := extractResetTime(reason); t != "" {
		return t, true
	}
	if harn == "" {
		harn = detectHarness(reason)
	}
	return HarnessDefaultReset(harn), true
}

// resetKeyword finds the words introducing a printed reset time.
var resetKeyword = regexp.MustCompile(`(?i)\bresets?\b|\btry again (?:in|at)\b|\bretry(?:-| )after\b|\bcontinuing automatically at\b`)

// skipAfterKeyword consumes the separators between the keyword and the time:
// "at", "in", colons, and the middle dot Claude Code uses.
var skipAfterKeyword = regexp.MustCompile(`^[\s:·\-–—]*(?:(?:at|in)\b[\s:·\-–—]*)*`)

// resetShapes are the time forms a harness prints, tried in order at the text
// following each keyword. Every shape captures the time in group 1 and
// consumes one terminator (or the end) outside it, so a clock before a
// newline, a "·" separator, or a "(Zone/Name)" paren is kept rather than
// discarded.
var resetShapes = []*regexp.Regexp{
	// Gateway dated reset: "resets 2026-08-09 00:00 UTC".
	regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{1,2}:\d{2}(?::\d{2})?\s?(?:UTC|GMT)?)(?:[^a-zA-Z]|$)`),
	// Codex full datetime: "try again at Sep 28th, 2026 2:14 AM".
	regexp.MustCompile(`^([A-Za-z]{3,9} \d{1,2}(?:st|nd|rd|th)?, \d{4} \d{1,2}:\d{2}\s?(?:[APap][Mm])?)(?:[^a-zA-Z]|$)`),
	// Weekday and clock: "resets Mon 12:00am".
	regexp.MustCompile(`(?i)^((?:Mon|Tue|Tues|Wed|Wednes|Thu|Thur|Thurs|Fri|Sat|Satur|Sun)(?:day)?\b \d{1,2}:\d{2}\s?(?:[APap][Mm])?)(?:[^a-zA-Z]|$)`),
	// Clock with optional zone: "resets 3:45pm", "Resets at 17:00 UTC".
	regexp.MustCompile(`^(\d{1,2}:\d{2}(?::\d{2})?\s?(?:[APap][Mm])?(?:\s+(?:UTC|GMT|EST|EDT|CST|CDT|MST|MDT|PST|PDT|AKST|AKDT|HST|BST|CET|CEST|WET|EET|MSK|IST|JST|KST|HKT|SGT|AEST|AEDT|NZST|NZDT))?)(?:[^a-zA-Z]|$)`),
	// Hour and meridiem: "resets 5pm".
	regexp.MustCompile(`^(\d{1,2}\s?(?:[APap][Mm]))(?:[^a-zA-Z]|$)`),
	// Duration: "Resets in 167h39m40s", "resets in 2h 15m", "try again in 20s".
	regexp.MustCompile(`(?i)^(\d+\s*(?:hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s|days?|d|weeks?|w)(?:\s*\d+\s*(?:hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s|days?|d|weeks?|w))*)(?:[^a-zA-Z]|$)`),
}

func extractResetTime(reason string) string {
	for _, loc := range resetKeyword.FindAllStringIndex(reason, -1) {
		rest := skipAfterKeyword.ReplaceAllString(reason[loc[1]:], "")
		for _, re := range resetShapes {
			if m := re.FindStringSubmatch(rest); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

func detectHarness(reason string) string {
	lower := strings.ToLower(reason)
	for _, name := range []string{"claude", "codex", "agy", "antigravity", "grok", "opencode"} {
		if strings.Contains(lower, name) {
			return name
		}
	}
	return ""
}

// HarnessDefaultReset returns the default reset window for the named harness.
func HarnessDefaultReset(harn string) string {
	switch strings.ToLower(harn) {
	case "claude":
		return "5h"
	case "codex":
		return "5h"
	case "agy", "antigravity":
		return "24h"
	case "grok":
		return "2h"
	case "opencode":
		return "1h"
	default:
		return "5h"
	}
}
