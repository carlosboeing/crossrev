// quota.go — classification of quota and rate-limit errors and resolution of
// per-harness reset times.
//
// Harnesses report quota exhaustion and rate limits through various error
// strings on stderr, stdout, or HTTP status codes. When a leg halts because of
// a quota stop, crossrev informs the operator of the expected resume time and
// the command to re-run rather than reporting that a human decision is needed.

package harness

import (
	"regexp"
	"strings"
	"unicode"
)

var resetPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bresets?\s+(?:at\s+|in\s+)?([0-9a-zA-Z: ]+?)(?:[.)\],;]|\s+and\b|$)`),
	regexp.MustCompile(`(?i)\b(?:retry[- ]after|try again (?:in|at))\s*:?\s*([0-9a-zA-Z: ]+?)(?:[.)\],;]|\s+and\b|$)`),
}

// IsQuotaError reports whether reason describes a quota exhaustion or rate-limit
// failure.
func IsQuotaError(reason string) bool {
	lower := strings.ToLower(reason)
	return strings.Contains(lower, "quota") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "rate-limit") ||
		strings.Contains(lower, "rate_limit") ||
		strings.Contains(lower, "rate limited") ||
		strings.Contains(lower, "429") ||
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

func extractResetTime(reason string) string {
	for _, re := range resetPatterns {
		m := re.FindStringSubmatch(reason)
		if len(m) >= 2 {
			val := strings.TrimSpace(m[1])
			val = strings.TrimRight(val, ".)];, ")
			if hasDigit(val) && len(val) >= 2 && len(val) <= 35 {
				return val
			}
		}
	}
	return ""
}

func hasDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
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
