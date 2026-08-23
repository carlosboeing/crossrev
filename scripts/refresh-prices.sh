#!/usr/bin/env bash
#
# Regenerate lib/prices.json from LiteLLM's model_prices_and_context_window.json.
#
# A maintainer tool, never on a runtime path: lib/usage.sh only reads the
# committed extract, so rendering a review comment needs no network and a
# historical pass re-prices against exactly the revision its marker names.
#
# The extract holds the models CrossRev can name and the fields pricing needs:
# five per-token rates plus the unsuffixed long-context break fields the second
# refuse rule reads. Everything else upstream ships — search-context queries,
# flex, priority and batches variants — is dropped here.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEST="$ROOT/lib/prices.json"
UPSTREAM_URL="https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
COMMITS_URL="https://api.github.com/repos/BerriAI/litellm/commits?path=model_prices_and_context_window.json&per_page=1"

# The models CrossRev can name. Extending coverage is one edit to this array,
# which is why the list is data rather than logic.
MODELS=(
  "claude-opus-5"
  "claude-sonnet-5"
  "claude-haiku-4-5"
  "claude-opus-4-8"
  "gpt-5.6"
  "gpt-5.6-cyber"
  "gpt-5.6-luna"
  "gpt-5.6-sol"
  "gpt-5.6-terra"
  "gpt-5.5"
  "xai/grok-4.6"
  "xai/grok-4.5"
)

need() {
  command -v "$1" >/dev/null 2>&1 || { printf '%s is required\n' "$1" >&2; exit 1; }
}
need curl
need jq

tmp="$(mktemp)"
upstream="$(mktemp)"
draft="$(mktemp)"
trap 'rm -f "$tmp" "$upstream" "$draft"' EXIT

# The version stamps the upstream revision the rates came from, truncated to
# twelve characters, plus the extraction date. Markers copy this string, so an
# old pass never silently re-prices when the extract is refreshed.
sha="$(curl -fsSL "$COMMITS_URL" | jq -r '.[0].sha // empty')"
[[ -n "$sha" ]] || { printf 'could not resolve the upstream commit\n' >&2; exit 1; }
version="${sha:0:12}@$(date -u +%Y-%m-%d)"

curl -fsSL "$UPSTREAM_URL" -o "$tmp"

keys_json="$(printf '%s\n' "${MODELS[@]}" | jq -Rs 'split("\n") | map(select(length > 0))')"

# A model disappearing upstream stops the refresh loudly rather than shipping
# an extract that silently lost a row.
jq --argjson keys "$keys_json" --arg version "$version" '
  def base_rate:
    IN("input_cost_per_token",
       "output_cost_per_token",
       "cache_read_input_token_cost",
       "cache_creation_input_token_cost",
       "cache_creation_input_token_cost_above_1hr");
  def tier_break:
    test("^[a-z_]+_above_[0-9]+k_tokens$")
    and (test("flex|priority|batches") | not);
  . as $all
  | {version: $version}
    + (reduce ($keys[]) as $k ({};
        .[$k] = ($all[$k]
                 // error("model \($k) is not in the upstream extract")
                 | with_entries(select(.key | base_rate or tier_break))))
)' "$tmp" >"$draft"

mv "$draft" "$DEST"
printf 'wrote %s at %s\n' "$DEST" "$version"
