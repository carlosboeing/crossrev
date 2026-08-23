#!/usr/bin/env bash
#
# lib/usage.sh in isolation: the identity of total, the two refuse rules,
# billing derivation, footnote composition and the vendored price extract.
#
# No harness CLI is invoked here. The per-harness parser cases live in this
# file too, fed by the captured probe fixtures under tests/fixtures/usage/.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
source "$HERE/harness.sh"
source "$ROOT/lib/ui.sh"
source "$ROOT/lib/usage.sh"

# Identity: reasoning is stored and never added.
u='{"input_fresh":10,"cache_read":4,"cache_write_5m":0,"cache_write_1h":0,"cache_write_unsplit":0,"output":2,"reasoning":1}'
got="$(usage_with_total "$u" | jq -c '{total,reasoning}')"
is "total does not include reasoning" "$got" '{"total":16,"reasoning":1}'

# Pricing the measured claude buckets against the extract.
buckets='{"input_fresh":8,"cache_read":158501,"cache_write_5m":0,"cache_write_1h":31848,"cache_write_unsplit":0,"output":254,"reasoning":0}'
priced="$(usage_price "$(usage_with_total "$buckets")" "claude-opus-5")"
is "extract reproduces the harness figure to seven decimal places" \
  "$(jq -r '.cost_usd' <<<"$priced")" "0.4041205"
is "that path is table-sourced" "$(jq -r '.cost_source' <<<"$priced")" "table"
has "price_table carries the extract version" "$(jq -r '.price_table' <<<"$priced")" "@"

# 5m + 1h at Opus 5 rates: 1000 * 6.25e-6 + 1000 * 1e-5 = 0.01625
mixed='{"input_fresh":0,"cache_read":0,"cache_write_5m":1000,"cache_write_1h":1000,"cache_write_unsplit":0,"output":0,"reasoning":0}'
is "two write TTLs price at their own rates" \
  "$(usage_price "$(usage_with_total "$mixed")" "claude-opus-5" | jq -r '.cost_usd')" "0.01625"

# Unsplit writes on Opus 5, whose 5m and 1h rates differ.
unsplit='{"input_fresh":0,"cache_read":0,"cache_write_5m":0,"cache_write_1h":0,"cache_write_unsplit":1000,"output":0,"reasoning":0}'
is "unsplit writes refuse when the two write rates differ" \
  "$(usage_price "$(usage_with_total "$unsplit")" "claude-opus-5" | jq -c '{cost_usd,cost_source}')" \
  '{"cost_usd":null,"cost_source":null}'

# Context break on gpt-5.6-terra. Standard input 2e-6, cache_read 2e-7, output 1.2e-5.
below='{"input_fresh":1000,"cache_read":1000,"cache_write_5m":0,"cache_write_1h":0,"cache_write_unsplit":0,"output":10,"reasoning":0}'
is "cumulative input below 272k prices at standard terra rates" \
  "$(usage_price "$(usage_with_total "$below")" "gpt-5.6-terra" | jq -r '.cost_usd')" "0.00232"
above='{"input_fresh":200000,"cache_read":80000,"cache_write_5m":0,"cache_write_1h":0,"cache_write_unsplit":0,"output":10,"reasoning":0}'
is "cumulative input at or above 272k refuses" \
  "$(usage_price "$(usage_with_total "$above")" "gpt-5.6-terra" | jq -r '.cost_usd')" "null"
writes_over='{"input_fresh":1000,"cache_read":1000,"cache_write_5m":0,"cache_write_1h":280000,"cache_write_unsplit":0,"output":10,"reasoning":0}'
is "writes that push cumulative input over the break refuse" \
  "$(usage_price "$(usage_with_total "$writes_over")" "gpt-5.6-terra" | jq -r '.cost_usd')" "null"

# Billing. API key wins when both names are present. Named endpoint wins over the key.
unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN
is "bare claude is subscription" "$(usage_billing_for claude vendor)" "subscription"
# Both names present is the case under test: the token must not flip the
# answer once the API key is visible, so it is set and never read.
# shellcheck disable=SC2034
ANTHROPIC_API_KEY=sk-test CLAUDE_CODE_OAUTH_TOKEN=oauth-test
is "API key beats the oauth token" "$(usage_billing_for claude vendor)" "api"
is "named endpoint beats the API key" "$(usage_billing_for claude kimi)" "endpoint"
unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN

# Attach: endpoint discards a harness cost.
harnessed='{"input_fresh":8,"cache_read":0,"cache_write_5m":0,"cache_write_1h":0,"cache_write_unsplit":0,"output":1,"reasoning":0,"total":9,"cost_usd":0.4041205,"cost_source":"harness","price_table":null}'
cleared="$(usage_attach "$harnessed" claude kimi claude-opus-5)"
is "endpoint attach nulls a harness cost" "$(jq -c '{cost_usd,cost_source,billing}' <<<"$cleared")" \
  '{"cost_usd":null,"cost_source":null,"billing":"endpoint"}'

# Footnote, four live combinations.
has "harness path omits nearest-model" "$(usage_footnote harness subscription)" "harness's own estimate"
hasnt "harness path has no nearest-model clause" "$(usage_footnote harness subscription)" "nearest listed model"
has "table path names nearest-model" "$(usage_footnote table subscription)" "nearest listed model"
has "subscription closes invoiced nothing" "$(usage_footnote harness subscription)" "invoiced nothing"
has "api closes with the invoice" "$(usage_footnote harness api)" "provider's invoice remains authoritative"
hasnt "api close does not claim invoiced nothing" "$(usage_footnote harness api)" "invoiced nothing"
is "endpoint prints no cost clause" "$(usage_footnote harness endpoint)" ""
is "null cost_source prints no cost clause" "$(usage_footnote "" subscription)" ""
has "a cost footnote always carries the cache-rate sentence" \
  "$(usage_footnote table subscription)" "token columns alone do not indicate cost"

is "nearest-match prices grok-4.6-build as grok-4.6" \
  "$(usage_price_key grok-4.6-build)" "xai/grok-4.6"
is "bracket suffix does not need a heuristic when the key is listed" \
  "$(usage_price_key claude-opus-5)" "claude-opus-5"

# --- Claude parsing: buckets from two objects, split wins, largest share ---

u="$(usage_parse_claude "$ROOT/tests/fixtures/usage/claude-probe.json")"
is "claude probe input_fresh comes from modelUsage" "$(jq -r .input_fresh <<<"$u")" "8"
is "claude probe cache_read comes from modelUsage" "$(jq -r .cache_read <<<"$u")" "158501"
is "claude probe writes land apart by TTL" \
  "$(jq -c '{five:.cache_write_5m,one:.cache_write_1h}' <<<"$u")" '{"five":0,"one":31848}'
is "claude probe unsplit stays empty when the split reconciles" \
  "$(jq -r .cache_write_unsplit <<<"$u")" "0"
is "claude probe total is the identity" "$(jq -r .total <<<"$u")" "190611"
is "claude probe copies the harness cost" "$(jq -r .cost_usd <<<"$u")" "0.4041205"
is "claude probe names the canonical model" "$(jq -r '.models[0].id' <<<"$u")" "claude-opus-5"
is "pricing the probe buckets reproduces its own cost" \
  "$(usage_price "$u" "$(jq -r '.models[0].id' <<<"$u")" | jq -r .cost_usd)" "0.4041205"

u="$(usage_parse_claude "$ROOT/tests/fixtures/usage/claude-mixed.json")"
is "mixed session reports the larger share, not the first sorted key" \
  "$(usage_model_reported_from_models "$(jq -c '.models // []' <<<"$u")")" "claude-opus-5"
is "mixed session keeps both models" "$(jq -r '.models | length' <<<"$u")" "2"

u="$(usage_parse_claude "$ROOT/tests/fixtures/usage/claude-disagree.json")"
is "split wins over a larger sum; excess becomes unsplit" \
  "$(jq -c '{a:.cache_write_5m,b:.cache_write_1h,c:.cache_write_unsplit}' <<<"$u")" \
  '{"a":40,"b":50,"c":10}'

u="$(usage_parse_claude "$ROOT/tests/fixtures/usage/claude-disagree-reversed.json")"
is "a split larger than the sum invents no unsplit tokens" \
  "$(jq -c '{a:.cache_write_5m,b:.cache_write_1h,c:.cache_write_unsplit}' <<<"$u")" \
  '{"a":40,"b":50,"c":0}'

# --- Codex parsing: the subtraction, and the rollout that names the model ---

u="$(usage_parse_codex_events "$ROOT/tests/fixtures/usage/codex-probe.ndjson")"
is "codex input_fresh is input minus cached" "$(jq -r .input_fresh <<<"$u")" "19044"
is "codex derived names the subtraction" "$(jq -c '.derived' <<<"$u")" '["input_fresh"]'
is "codex total is 57773" "$(jq -r .total <<<"$u")" "57773"
is "codex reasoning is stored" "$(jq -r .reasoning <<<"$u")" "101"
is "codex writes are unsplit" "$(jq -r .cache_write_unsplit <<<"$u")" "0"

# Rollout miss: no CODEX_HOME
unset CODEX_HOME
is "rollout miss is a null model" "$(usage_read_codex_rollout | jq -c .)" '{"model":null,"effort":null}'

# Rollout hit
CODEX_HOME="$(mktemp -d)"
mkdir -p "$CODEX_HOME/sessions/2026/08/23"
cp "$ROOT/tests/fixtures/usage/codex-rollout.jsonl" "$CODEX_HOME/sessions/2026/08/23/rollout.jsonl"
got="$(usage_read_codex_rollout)"
is "rollout model is gpt-5.6-terra" "$(jq -r .model <<<"$got")" "gpt-5.6-terra"
is "rollout effort is medium" "$(jq -r .effort <<<"$got")" "medium"
rm -rf "$CODEX_HOME"; unset CODEX_HOME

# --- Grok, agy, opencode: summing beats every vendor total ---

u="$(usage_parse_agy "$ROOT/tests/fixtures/usage/agy-probe.json")"
is "agy ignores a vendor total that excludes cache reads" "$(jq -r .total <<<"$u")" "133830"
is "agy stores thinking as reasoning" "$(jq -r .reasoning <<<"$u")" "477"
[[ "$(jq -r .total <<<"$u")" != "48162" ]] \
  && ok "agy total is not the vendor 48162" \
  || notok "agy total is not the vendor 48162" "not 48162" "$(jq -r .total <<<"$u")"

u="$(usage_parse_grok "$ROOT/tests/fixtures/usage/grok-probe.json")"
is "grok total matches the vendor total by summing" "$(jq -r .total <<<"$u")" "130623"
is "grok copies the harness cost" "$(jq -r .cost_usd <<<"$u")" "0.01944562"
is "grok reasoning is not added" "$(jq -r .reasoning <<<"$u")" "290"

u="$(usage_parse_opencode_export "$ROOT/tests/fixtures/usage/opencode-probe.json")"
is "opencode total excludes reasoning" "$(jq -r .total <<<"$u")" "63249"
is "opencode stores reasoning" "$(jq -r .reasoning <<<"$u")" "73"
is "opencode writes are unsplit" "$(jq -r .cache_write_unsplit <<<"$u")" "0"

finish
