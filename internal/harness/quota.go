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
//     CLI copy is corroborated across repositories citing xai-org/grok-build
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
	// Claude Code spend caps need a billing change or an admin, not a wait:
	// "You've hit your monthly spend limit", "spend limit reached".
	"spend limit",
	// The same cap on usage-billing organisations, which says "usage limit"
	// in place of "spend limit".
	"individual usage limit",
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
	for _, ex := range quotaExclusions {
		if strings.Contains(lower, ex) {
			return false
		}
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
