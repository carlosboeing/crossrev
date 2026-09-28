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

// TestDocumentedHarnessSentences sweeps every limit, quota, rate, spend,
// billing and consent sentence the driven harnesses document or print in
// their own sources: Claude Code, Codex, Grok, Antigravity, OpenCode and
// Kimi. A sentence is a quota stop only when waiting fixes it: a printed
// reset time is kept, otherwise the harness default applies. Billing caps,
// spend sentences with no printed reset, and consent prompts are human
// stops. Each row cites the source of its sentence.
func TestDocumentedHarnessSentences(t *testing.T) {
	tests := []struct {
		name      string
		harness   string
		reason    string
		wantReset string
		wantQuot  bool
	}{
		// Claude Code error reference, usage-limits section:
		// https://code.claude.com/docs/en/errors
		{name: "claude session limit with clock", harness: "claude", reason: "You've hit your session limit · resets 3:45pm", wantReset: "3:45pm", wantQuot: true},
		{name: "claude weekly limit with weekday", harness: "claude", reason: "You've hit your weekly limit · resets Mon 12:00am", wantReset: "Mon 12:00am", wantQuot: true},
		{name: "claude opus limit with clock", harness: "claude", reason: "You've hit your Opus limit · resets 3:45pm", wantReset: "3:45pm", wantQuot: true},
		{name: "claude sonnet limit with clock", harness: "claude", reason: "You've hit your Sonnet limit · resets 3:45pm", wantReset: "3:45pm", wantQuot: true},
		// Same reference, interactive wait line: waiting continues the task.
		{name: "claude wait line keeps its clock", harness: "claude", reason: "Usage limit reached · continuing automatically at 3:45pm · esc to cancel", wantReset: "3:45pm", wantQuot: true},
		// Same reference, "Before a window runs out": a warning, not a halt.
		{name: "claude pre-limit warning is not a halt", harness: "claude", reason: "You've used 85% of your session limit · resets 3:45pm", wantReset: "", wantQuot: false},
		// Same reference, spend-limit section: each names who raises the cap.
		{name: "claude monthly spend limit", harness: "claude", reason: "You've hit your monthly spend limit · raise it at claude.ai/settings/usage", wantReset: "", wantQuot: false},
		{name: "claude individual spend limit", harness: "claude", reason: "You've hit your individual spend limit · ask your admin for a higher limit", wantReset: "", wantQuot: false},
		{name: "claude org monthly spend limit", harness: "claude", reason: "You've hit your org's monthly spend limit · visit claude.ai/admin-settings/usage to raise it", wantReset: "", wantQuot: false},
		{name: "claude channel monthly spend limit", harness: "claude", reason: "You've hit your channel's monthly spend limit · an org owner or channel manager can raise it in the channel's Claude settings", wantReset: "", wantQuot: false},
		{name: "claude shared budget without reset", harness: "claude", reason: "You've hit your team's shared budget · ask your admin to raise it at claude.ai/admin-settings/usage", wantReset: "", wantQuot: false},
		// Same section: "the message also says when that window resets, for
		// example `· your session limit resets 3:45pm`, and access returns
		// then without anyone raising the limit".
		{name: "claude shared budget with window reset waits", harness: "claude", reason: "You've hit your team's shared budget · your session limit resets 3:45pm", wantReset: "3:45pm", wantQuot: true},
		// Same section: usage-billing organisations say "usage limit" in
		// place of "spend limit".
		{name: "claude usage-billing cap without reset", harness: "claude", reason: "You've hit your individual usage limit", wantReset: "", wantQuot: false},
		// Combined form of those two documented fragments.
		{name: "claude usage-billing cap with window reset waits", harness: "claude", reason: "You've hit your individual usage limit · your session limit resets 3:45pm", wantReset: "3:45pm", wantQuot: true},
		// Same reference, gateway spend-caps section: "The gateway blocks
		// your requests until the named period resets".
		{name: "claude gateway cap with dated reset waits", harness: "claude", reason: "spend limit reached (daily; resets 2026-08-09 00:00 UTC)", wantReset: "2026-08-09 00:00 UTC", wantQuot: true},
		// Same section: pre-v2.1.225 gateways send the short form with no reset.
		{name: "claude gateway cap without reset", harness: "claude", reason: "spend limit reached", wantReset: "", wantQuot: false},
		// Same section: a precautionary block, not an exhausted cap.
		{name: "claude spend records unreadable", harness: "claude", reason: "spend limit unavailable", wantReset: "", wantQuot: false},
		// Same reference, credit-balance section: prepaid credits or a wrong key.
		{name: "claude prepaid balance empty", harness: "claude", reason: "Credit balance is too low", wantReset: "", wantQuot: false},
		// Same reference, spend-limit-change section, generic retry form.
		{name: "claude rejected spend change", harness: "claude", reason: "Could not update your spend limit. Press Enter to retry.", wantReset: "", wantQuot: false},
		// Same reference, Fable consent section: answer the prompt or switch
		// model; the current form names the session's Fable model.
		{name: "claude fable consent unanswered", harness: "claude", reason: "Fable limit reached · continuing on Fable 5.1 uses usage credits, and the prompt to confirm went unanswered — nothing was sent", wantReset: "", wantQuot: false},
		// Same section: before v2.1.257 the first message began "Fable 5
		// limit reached"; suffix as above.
		{name: "claude fable consent older form", harness: "claude", reason: "Fable 5 limit reached · continuing on Fable 5 uses usage credits, and the prompt to confirm went unanswered — nothing was sent", wantReset: "", wantQuot: false},
		// Same section, second message.
		{name: "claude fable consent second form", harness: "claude", reason: "Fable 5.1 now uses usage credits · the prompt to confirm went unanswered — nothing was sent", wantReset: "", wantQuot: false},
		// Same reference, 1M-context section: an entitlement check, not exhaustion.
		{name: "claude 1m entitlement check", harness: "claude", reason: "Usage credits required for 1M context", wantReset: "", wantQuot: false},
		// Same reference, server-throttle section: unrelated to the plan quota.
		{name: "claude server-side throttle", harness: "claude", reason: "API Error: Server is temporarily limiting requests (not your usage limit)", wantReset: "", wantQuot: false},
		// Same reference, key/project rate-limit section: a real rate limit
		// with no printed reset, so the harness default applies.
		{name: "claude key rate limit without reset", harness: "claude", reason: "API Error: Request rejected (429) · this may be a temporary capacity issue. If it persists, check https://status.claude.com.", wantReset: "5h", wantQuot: true},
		// Same reference, server-errors section: "A 529 is not your usage
		// limit and doesn't count against your quota".
		{name: "claude overload is not a usage limit", harness: "claude", reason: "API Error: Repeated 529 Overloaded errors. The API is at capacity — this is usually temporary. Try again in a moment. If it persists, check https://status.claude.com.", wantReset: "", wantQuot: false},
		// Same section: per-model capacity; the fix is switching, not waiting.
		{name: "claude model high load switches", harness: "claude", reason: "Opus is experiencing high load, please use /model to switch to Sonnet", wantReset: "", wantQuot: false},
		// Same reference, tool-errors section: a full local disk (EDQUOT).
		{name: "claude disk quota full", harness: "claude", reason: "Your disk quota is full on the filesystem with Claude Code's temp directory /tmp/x (EDQUOT)", wantReset: "", wantQuot: false},
		// Same reference, request-errors section: compact, don't wait.
		{name: "claude context limit", harness: "claude", reason: "Context limit reached · /compact or /clear to continue", wantReset: "", wantQuot: false},
		// Same reference index, gateway upstream messages: a rate limit with
		// no printed reset, so the harness default applies.
		{name: "claude gateway upstream rate limit", harness: "claude", reason: "upstream rate limit exceeded", wantReset: "5h", wantQuot: true},
		// Codex UsageLimitReachedError display impls in
		// codex-rs/protocol/src/error.rs of openai/codex: Free/Go plan
		// branch with a retry timestamp.
		{name: "codex usage limit plus upsell with datetime", harness: "codex", reason: "You’ve hit your usage limit. Upgrade to Plus to continue using Codex (https://chatgpt.com/explore/plus), or try again at Sep 28th, 2026 2:14 AM.", wantReset: "Sep 28th, 2026 2:14 AM", wantQuot: true},
		// Same file, Plus plan branch composed with retry_suffix_after_or.
		{name: "codex usage limit pro credits with datetime", harness: "codex", reason: "You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit https://chatgpt.com/codex/settings/usage to purchase more credits or try again at Sep 28th, 2026 2:14 AM.", wantReset: "Sep 28th, 2026 2:14 AM", wantQuot: true},
		// Same file, Pro plan branch with no resets_at: "or try again later".
		{name: "codex usage limit pro credits without reset", harness: "codex", reason: "You’ve hit your usage limit. Visit https://chatgpt.com/codex/settings/usage to purchase more credits or try again later.", wantReset: "5h", wantQuot: true},
		// Same file, Team/business branch with no resets_at.
		{name: "codex usage limit admin request without reset", harness: "codex", reason: "You’ve hit your usage limit. To get more access now, send a request to your admin or try again later.", wantReset: "5h", wantQuot: true},
		// Same file, Enterprise/unknown branch composed with retry_suffix
		// and no resets_at.
		{name: "codex usage limit bare without reset", harness: "codex", reason: "You’ve hit your usage limit. Try again later.", wantReset: "5h", wantQuot: true},
		// Same file and branch with a same-day resets_at ("%I:%M %p").
		{name: "codex usage limit bare with same-day clock", harness: "codex", reason: "You’ve hit your usage limit. Try again at 1:00 PM.", wantReset: "1:00 PM", wantQuot: true},
		// Same file, WorkspaceOwnerCreditsDepleted: billing, not a window.
		{name: "codex workspace out of credits owner", harness: "codex", reason: "Your workspace is out of credits. Add credits to continue.", wantReset: "", wantQuot: false},
		// Same file, WorkspaceMemberCreditsDepleted.
		{name: "codex workspace out of credits member", harness: "codex", reason: "Your workspace is out of credits. Ask your workspace owner to refill in order to continue.", wantReset: "", wantQuot: false},
		// Same file, WorkspaceOwnerUsageLimitReached: a spend cap, no reset.
		{name: "codex workspace spend cap owner", harness: "codex", reason: "You hit your spend cap set in your workspace. Increase your spend cap to continue.", wantReset: "", wantQuot: false},
		// Same file, WorkspaceMemberUsageLimitReached.
		{name: "codex workspace spend cap member", harness: "codex", reason: "You hit your spend cap set by the owner of your workspace. Ask an owner to increase your spend cap to continue.", wantReset: "", wantQuot: false},
		// Same file, QuotaExceeded: retry_delay returns None, so it is a
		// billing stop, not a usage window.
		{name: "codex quota exceeded billing", harness: "codex", reason: "Quota exceeded. Check your plan and billing details.", wantReset: "", wantQuot: false},
		// Same file, UsageNotIncluded: terminal, needs an upgrade.
		{name: "codex plan without usage", harness: "codex", reason: "To use Codex with your ChatGPT plan, upgrade to Plus: https://chatgpt.com/explore/plus.", wantReset: "", wantQuot: false},
		// Same file, AgentLimitReached: too many threads, not a window.
		{name: "codex agent thread limit", harness: "codex", reason: "agent thread limit reached", wantReset: "", wantQuot: false},
		// Same file, ContextWindowExceeded: start a new thread, don't wait.
		{name: "codex context window", harness: "codex", reason: "Codex ran out of room in the model's context window. Start a new thread or clear earlier history before retrying.", wantReset: "", wantQuot: false},
		// Same file, SessionBudgetExceeded: terminal, retry_delay is None.
		{name: "codex session budget exhausted", harness: "codex", reason: "shared rollout token budget exhausted", wantReset: "", wantQuot: false},
		// Same file, RateLimitExceeded: "a retryable upstream rate limit",
		// retry_delay returns server advice; detail varies per response.
		{name: "codex retryable rate limit", harness: "codex", reason: "rate limit exceeded: retry shortly", wantReset: "5h", wantQuot: true},
		// Grok RATE_LIMITED_USER_MESSAGE_OAUTH in error.rs of
		// xai-org/grok-build, cited by independent CLI contract notes; no
		// printed reset, so the harness default applies.
		{name: "grok plan rate limit", harness: "grok", reason: "You've hit the rate limit for your plan. Try again later.", wantReset: "2h", wantQuot: true},
		// Same file, RATE_LIMITED_USER_MESSAGE_API_KEY.
		{name: "grok api key rate limit", harness: "grok", reason: "You've hit the rate limit for your API key. Try again later.", wantReset: "2h", wantQuot: true},
		// xAI rate-limits documentation: exceeding any limit returns a 429:
		// https://docs.x.ai/developers/rate-limits
		{name: "grok api 429", harness: "grok", reason: "429 Too Many Requests", wantReset: "2h", wantQuot: true},
		// Antigravity has no vendor-published error reference; this shape is
		// corroborated across independent observed reports, fuller form with
		// the overages line.
		{name: "antigravity quota with overages line", harness: "agy", reason: "RESOURCE_EXHAUSTED (code 429): Individual quota reached. Contact your administrator to enable overages. Resets in 4h5m58s.", wantReset: "4h5m58s", wantQuot: true},
		// Same corroborated shape, long and short durations.
		{name: "antigravity quota long duration", harness: "agy", reason: "RESOURCE_EXHAUSTED (code 429): Individual quota reached. Resets in 167h39m40s.", wantReset: "167h39m40s", wantQuot: true},
		{name: "antigravity quota short duration", harness: "agy", reason: "RESOURCE_EXHAUSTED (code 429): Individual quota reached. Resets in 1h27m57s.", wantReset: "1h27m57s", wantQuot: true},
		// Same signal with no printed reset, so the harness default applies.
		{name: "antigravity bare exhaustion", harness: "agy", reason: "the agy harness failed: RESOURCE_EXHAUSTED", wantReset: "24h", wantQuot: true},
		// OpenCode authors no quota sentence of its own; provider errors
		// surface through it, corroborated across independent reports.
		{name: "opencode provider 429 with retry after", harness: "opencode", reason: "429 Too Many Requests (retry after 30s)", wantReset: "30s", wantQuot: true},
		// FreeUsageLimitError text forwarded verbatim; no printed reset, so
		// the harness default applies.
		{name: "opencode free-tier rate limit", harness: "opencode", reason: "Rate limit exceeded. Please try again later.", wantReset: "1h", wantQuot: true},
		// GoUsageLimitError text; "5 hour" names the window, not a reset.
		{name: "opencode plan window usage limit", harness: "opencode", reason: "OpenCode Go 5 hour usage limit reached.", wantReset: "1h", wantQuot: true},
		// GoUsageLimitError text built in packages/opencode/src/session/retry.ts
		// of sst/opencode: "<limit> usage limit reached. It will reset in
		// <reset>." Below 60 seconds of retry-after <reset> is the words
		// "less than a minute", kept verbatim instead of the harness
		// default.
		{name: "opencode usage limit resets in less than a minute", harness: "opencode", reason: "Pro usage limit reached. It will reset in less than a minute. To continue using this model now, enable usage from your available balance", wantReset: "less than a minute", wantQuot: true},
		// Same builder with a numeric retry-after: the minute count is the
		// printed reset.
		{name: "opencode usage limit resets in minutes", harness: "opencode", reason: "Pro usage limit reached. It will reset in 5 minutes. To continue using this model now, enable usage from your available balance", wantReset: "5 minutes", wantQuot: true},
		// Kimi is not a driven harness (not_driven in assets/harnesses.json):
		// it is reached through the claude adapter, so the Claude Code
		// sentences apply to it.
		{name: "kimi session limit via claude adapter", harness: "kimi", reason: "You've hit your session limit · resets 3:45pm", wantReset: "3:45pm", wantQuot: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReset, gotQuot := harness.QuotaReset(tt.harness, tt.reason)
			if gotQuot != tt.wantQuot {
				t.Fatalf("QuotaReset(%q, %q) isQuota = %v, want %v", tt.harness, tt.reason, gotQuot, tt.wantQuot)
			}
			if gotReset != tt.wantReset {
				t.Errorf("QuotaReset(%q, %q) reset = %q, want %q", tt.harness, tt.reason, gotReset, tt.wantReset)
			}
		})
	}
}
