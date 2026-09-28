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
