package harness_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

func TestIsQuotaError(t *testing.T) {
	tests := []struct {
		reason string
		want   bool
	}{
		{"the codex harness failed: quota exceeded", true},
		{"the claude harness failed: quota exhausted", true},
		{"the claude harness failed: rate limit exceeded", true},
		{"the grok harness failed: rate limited", true},
		{"the opencode harness failed: 429 Too Many Requests", true},
		{"the agy harness failed: RESOURCE_EXHAUSTED", true},
		{"the agy harness failed: resource has been exhausted", true},
		{"the claude harness failed: usage limit reached", true},
		{"insufficient quota for requested model", true},
		{"You've hit your session limit · resets 3:45pm", true},
		{"You've hit your weekly limit · resets Mon 12:00am", true},
		{"You've hit your Opus limit · resets 3:45pm", true},
		{"You've hit your Sonnet limit · resets 3:45pm", true},
		{"You\u2019ve hit your usage limit. Upgrade to Plus to continue using Codex (https://chatgpt.com/explore/plus), or try again at Sep 28th, 2026 2:14 AM.", true},
		{"You\u2019ve hit your usage limit for codex. Switch to another model now, or try again at 1:00 PM.", true},
		{"You've hit the rate limit for your plan. Try again later.", true},
		{"Rate limited (429)", true},
		{"API Error: Request rejected (429) · this may be a temporary capacity issue. If it persists, check https://status.claude.com.", true},
		{"exceeded retry limit, last status: 429", true},
		{"RESOURCE_EXHAUSTED (code 429): Individual quota reached. Resets in 167h39m40s.", true},
		{"rpc error: code = ResourceExhausted desc = Quota exceeded", true},
		{"Usage limit reached · continuing automatically at 3:45pm · esc to cancel", true},
		{"You've hit your monthly spend limit · raise it at claude.ai/settings/usage", false},
		{"You've hit your individual spend limit · ask your admin for a higher limit", false},
		{"You've hit your org's monthly spend limit · visit claude.ai/admin-settings/usage to raise it", false},
		{"You've hit your team's shared budget", false},
		{"You've hit your individual usage limit", false},
		{"spend limit reached", false},
		{"spend limit unavailable", false},
		{"process exited after 142900ms: timeout", false},
		{"Your disk quota is full on the filesystem with Claude Code's temp directory /tmp/x (EDQUOT)", false},
		{"Context limit reached · /compact or /clear to continue", false},
		{"agent thread limit reached", false},
		{"API Error: Server is temporarily limiting requests (not your usage limit)", false},
		{"the harness CLI is not installed", false},
		{"the model twice returned an answer that contradicts what it was given", false},
		{"permission denied", false},
		{"No reason was given.", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			if got := harness.IsQuotaError(tt.reason); got != tt.want {
				t.Errorf("IsQuotaError(%q) = %v, want %v", tt.reason, got, tt.want)
			}
		})
	}
}

func TestQuotaReset(t *testing.T) {
	tests := []struct {
		name       string
		harness    string
		reason     string
		wantReset  string
		wantIsQuot bool
	}{
		{
			name:       "claude with explicit clock time",
			harness:    "claude",
			reason:     "the claude harness failed: quota exhausted (resets 5pm)",
			wantReset:  "5pm",
			wantIsQuot: true,
		},
		{
			name:       "codex with explicit relative duration",
			harness:    "codex",
			reason:     "the codex harness failed: 5h limit exceeded (resets in 2h 15m)",
			wantReset:  "2h 15m",
			wantIsQuot: true,
		},
		{
			name:       "codex rate limit with try again in",
			harness:    "codex",
			reason:     "the codex harness failed: rate limit exceeded; try again in 20s",
			wantReset:  "20s",
			wantIsQuot: true,
		},
		{
			name:       "opencode with retry after",
			harness:    "opencode",
			reason:     "the opencode harness failed: 429 Too Many Requests (retry after 30s)",
			wantReset:  "30s",
			wantIsQuot: true,
		},
		{
			name:       "claude with UTC reset time",
			harness:    "claude",
			reason:     "the claude harness failed: rate limit exceeded. Resets at 17:00 UTC.",
			wantReset:  "17:00 UTC",
			wantIsQuot: true,
		},
		{
			name:       "claude session limit keeps its clock time",
			harness:    "claude",
			reason:     "You've hit your session limit · resets 3:45pm",
			wantReset:  "3:45pm",
			wantIsQuot: true,
		},
		{
			name:       "claude weekly limit keeps weekday and clock",
			harness:    "claude",
			reason:     "You've hit your weekly limit · resets Mon 12:00am",
			wantReset:  "Mon 12:00am",
			wantIsQuot: true,
		},
		{
			name:       "codex full datetime with ordinal day",
			harness:    "codex",
			reason:     "You\u2019ve hit your usage limit. Upgrade to Plus to continue using Codex (https://chatgpt.com/explore/plus), or try again at Sep 28th, 2026 2:14 AM.",
			wantReset:  "Sep 28th, 2026 2:14 AM",
			wantIsQuot: true,
		},
		{
			name:       "codex same-day clock time",
			harness:    "codex",
			reason:     "You\u2019ve hit your usage limit for codex. Switch to another model now, or try again at 1:00 PM.",
			wantReset:  "1:00 PM",
			wantIsQuot: true,
		},
		{
			name:       "codex with no reset time falls back to default",
			harness:    "codex",
			reason:     "You\u2019ve hit your usage limit. Try again later.",
			wantReset:  "5h",
			wantIsQuot: true,
		},
		{
			name:       "reset time before a newline is kept",
			harness:    "claude",
			reason:     "You've hit your session limit · resets 3:45pm\nRun /usage to see your plan limits",
			wantReset:  "3:45pm",
			wantIsQuot: true,
		},
		{
			name:       "reset time before a timezone paren is kept",
			harness:    "claude",
			reason:     "You've hit your session limit · resets 2:14 AM (Europe/Kiev)",
			wantReset:  "2:14 AM",
			wantIsQuot: true,
		},
		{
			name:       "antigravity duration is kept, not the default",
			harness:    "agy",
			reason:     "RESOURCE_EXHAUSTED (code 429): Individual quota reached. Resets in 167h39m40s.",
			wantReset:  "167h39m40s",
			wantIsQuot: true,
		},
		{
			name:       "antigravity short duration is kept",
			harness:    "agy",
			reason:     "RESOURCE_EXHAUSTED (code 429): Individual quota reached. Resets in 1h27m57s.",
			wantReset:  "1h27m57s",
			wantIsQuot: true,
		},
		{
			name:       "bare antigravity exhaustion falls back to default",
			harness:    "agy",
			reason:     "the agy harness failed: RESOURCE_EXHAUSTED",
			wantReset:  "24h",
			wantIsQuot: true,
		},
		{
			name:       "grok plan limit without a time falls back to default",
			harness:    "grok",
			reason:     "You've hit the rate limit for your plan. Try again later.",
			wantReset:  "2h",
			wantIsQuot: true,
		},
		{
			name:       "wait line keeps its clock time",
			harness:    "claude",
			reason:     "Usage limit reached · continuing automatically at 3:45pm · esc to cancel",
			wantReset:  "3:45pm",
			wantIsQuot: true,
		},
		{
			name:       "spend limit is not a session window",
			harness:    "claude",
			reason:     "You've hit your monthly spend limit · raise it at claude.ai/settings/usage",
			wantReset:  "",
			wantIsQuot: false,
		},
		{
			name:       "429 inside a longer number is not a status code",
			harness:    "codex",
			reason:     "process exited after 142900ms: timeout",
			wantReset:  "",
			wantIsQuot: false,
		},
		{
			name:       "claude default reset",
			harness:    "claude",
			reason:     "the claude harness failed: quota exhausted",
			wantReset:  "5h",
			wantIsQuot: true,
		},
		{
			name:       "codex default reset",
			harness:    "codex",
			reason:     "the codex harness failed: quota exceeded",
			wantReset:  "5h",
			wantIsQuot: true,
		},
		{
			name:       "agy default reset",
			harness:    "agy",
			reason:     "the agy harness failed: RESOURCE_EXHAUSTED",
			wantReset:  "24h",
			wantIsQuot: true,
		},
		{
			name:       "grok default reset",
			harness:    "grok",
			reason:     "the grok harness failed: rate limit exceeded",
			wantReset:  "2h",
			wantIsQuot: true,
		},
		{
			name:       "opencode default reset",
			harness:    "opencode",
			reason:     "the opencode harness failed: rate limit exceeded",
			wantReset:  "1h",
			wantIsQuot: true,
		},
		{
			name:       "infer claude from reason when harness empty",
			harness:    "",
			reason:     "the claude harness failed: quota exhausted",
			wantReset:  "5h",
			wantIsQuot: true,
		},
		{
			name:       "infer codex from reason when harness empty",
			harness:    "",
			reason:     "the codex harness failed: quota exceeded",
			wantReset:  "5h",
			wantIsQuot: true,
		},
		{
			name:       "unknown harness generic fallback",
			harness:    "custom",
			reason:     "quota exceeded",
			wantReset:  "5h",
			wantIsQuot: true,
		},
		{
			name:       "non-quota failure",
			harness:    "claude",
			reason:     "the harness CLI is not installed",
			wantReset:  "",
			wantIsQuot: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReset, gotIsQuot := harness.QuotaReset(tt.harness, tt.reason)
			if gotIsQuot != tt.wantIsQuot {
				t.Fatalf("QuotaReset(%q, %q) isQuota = %v, want %v", tt.harness, tt.reason, gotIsQuot, tt.wantIsQuot)
			}
			if gotReset != tt.wantReset {
				t.Errorf("QuotaReset(%q, %q) reset = %q, want %q", tt.harness, tt.reason, gotReset, tt.wantReset)
			}
		})
	}
}
