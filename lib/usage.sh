# shellcheck shell=bash
# lib/usage.sh — normalized token telemetry.
#
# Every adapter returns one shape of usage record, whatever its vendor calls
# the fields. `total` is a defined identity rather than a vendor number, so it
# means the same thing on every row: input_fresh + cache_read + the three
# cache-write counts + output. `reasoning` is persisted beside that total and
# never added to it, because every harness that reports reasoning nests it
# inside output already.
#
# Two sides write the record. An adapter fills buckets and, where its harness
# supplies one, the harness's own cost. The orchestrator attaches billing mode
# and, when no harness cost survived, a table-priced estimate. Nothing here
# ever reads a credential or talks to a network; pricing reads the committed
# extract at lib/prices.json and nothing else.
#
# Conventions: functions print JSON or a scalar on stdout, and none of them
# fail a leg. A miss is null.

_USAGE_LIB_ROOT="${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"

_usage_prices_file() {
  local root="${ROOT:-$_USAGE_LIB_ROOT}"
  printf '%s' "$root/lib/prices.json"
}

_usage_clear_cost() {
  jq -c '.cost_usd = null | .cost_source = null | .price_table = null'
}

usage_zero() {
  jq -cn '{input_fresh:0,cache_read:0,cache_write_5m:0,cache_write_1h:0,
           cache_write_unsplit:0,output:0,reasoning:null,
           cost_usd:null,cost_source:null,price_table:null,billing:null,
           models:null,derived:[]}'
}

usage_with_total() {
  jq -c '.total = ((.input_fresh // 0) + (.cache_read // 0)
                   + (.cache_write_5m // 0) + (.cache_write_1h // 0)
                   + (.cache_write_unsplit // 0) + (.output // 0))' <<<"$1"
}

# Claude splits four buckets across every modelUsage entry and holds the
# cache-write TTL split plus the thinking count only on top-level .usage.
# Where the modelUsage write sum disagrees with the split, the split wins:
# it is the only source that says which rate applies, and an excess lands in
# cache_write_unsplit instead of being dropped.
usage_parse_claude() {
  local rec
  rec="$(jq -c '
    ((.modelUsage // {}) | to_entries) as $es
    | (.usage.cache_creation // null) as $split
    | ([ $es[].value.inputTokens // 0 ] | add // 0) as $if
    | ([ $es[].value.cacheReadInputTokens // 0 ] | add // 0) as $cr
    | ([ $es[].value.outputTokens // 0 ] | add // 0) as $out
    | ([ $es[].value.cacheCreationInputTokens // 0 ] | add // 0) as $s
    | (if ($es | length) == 0 and .usage == null
          and ((.total_cost_usd // null) | type != "number")
       then null
       else
         (if $split == null then $s
          elif $s > (($split.ephemeral_5m_input_tokens // 0)
                     + ($split.ephemeral_1h_input_tokens // 0))
          then $s - ($split.ephemeral_5m_input_tokens // 0)
               - ($split.ephemeral_1h_input_tokens // 0)
          else 0 end) as $unsplit
         | (if ($es | length) == 0 then null
            else [ $es[]
                   | { id: (.value.canonicalModel
                            // (.key | sub("\\[.*\\]$"; ""))),
                       total: ((.value.inputTokens // 0)
                               + (.value.outputTokens // 0)
                               + (.value.cacheReadInputTokens // 0)
                               + (.value.cacheCreationInputTokens // 0)) } ]
                   | sort_by(-.total)
            end) as $models
         | {
             input_fresh: $if,
             cache_read: $cr,
             cache_write_5m: ($split.ephemeral_5m_input_tokens // 0),
             cache_write_1h: ($split.ephemeral_1h_input_tokens // 0),
             cache_write_unsplit: $unsplit,
             output: $out,
             reasoning: (.usage.output_tokens_details.thinking_tokens // null),
             cost_usd: (if (.total_cost_usd | type) == "number"
                        then .total_cost_usd else null end),
             cost_source: (if (.total_cost_usd | type) == "number"
                           then "harness" else null end),
             price_table: null,
             billing: null,
             models: $models,
             derived: []
           }
       end)' "$1" 2>/dev/null)" || rec="null"
  [[ "$rec" != "null" ]] && rec="$(usage_with_total "$rec")"
  printf '%s' "${rec:-null}"
}

usage_models_claude() {
  jq -c '(((.modelUsage // {}) | to_entries))
         | if length == 0 then null
           else [ .[]
                  | { id: (.value.canonicalModel // (.key | sub("\\[.*\\]$"; ""))),
                      total: ((.value.inputTokens // 0)
                              + (.value.outputTokens // 0)
                              + (.value.cacheReadInputTokens // 0)
                              + (.value.cacheCreationInputTokens // 0)) } ]
                  | sort_by(-.total)
           end' "$1" 2>/dev/null
}

usage_model_reported_from_models() {
  jq -r '(.[0].id // empty)' <<<"$1" 2>/dev/null
}

# Codex folds cached tokens inside input_tokens, so fresh input is a
# subtraction and the record's one derived field. Its writes carry no TTL, so
# they land in cache_write_unsplit. No vendor total is read: the identity is
# the total.
usage_parse_codex_events() {
  local u rec
  u="$(jq -s -r '[ .[] | select(.type == "turn.completed") | .usage // empty ]
                 | last // empty' "$1" 2>/dev/null)" || u=""
  [[ -n "$u" && "$u" != "null" ]] || { printf 'null'; return 0; }
  rec="$(jq -c '{
    input_fresh: (((.input_tokens // 0) - (.cached_input_tokens // 0))),
    cache_read: (.cached_input_tokens // 0),
    cache_write_5m: 0,
    cache_write_1h: 0,
    cache_write_unsplit: (.cache_write_input_tokens // 0),
    output: (.output_tokens // 0),
    reasoning: (.reasoning_output_tokens // null),
    cost_usd: null,
    cost_source: null,
    price_table: null,
    billing: null,
    models: null,
    derived: ["input_fresh"]
  }' <<<"$u" 2>/dev/null)" || rec="null"
  [[ "$rec" != "null" ]] && rec="$(usage_with_total "$rec")"
  printf '%s' "${rec:-null}"
}

# Grok's own total_tokens reconciles with the parts, but reading it would
# trust a vendor field the identity replaces; summing reaches the same answer
# without the trust. Grok's modelUsage carries call counts rather than token
# totals, so per-model totals are unknown and left null.
usage_parse_grok() {
  local rec
  rec="$(jq -c '
    ((.modelUsage // {}) | keys) as $mk
    | if (.usage // null) == null then null
      else
        { input_fresh: (.usage.input_tokens // 0),
          cache_read: (.usage.cache_read_input_tokens // 0),
          cache_write_5m: 0,
          cache_write_1h: 0,
          cache_write_unsplit: (.usage.cache_creation_input_tokens // 0),
          output: (.usage.output_tokens // 0),
          reasoning: (.usage.reasoning_tokens // null),
          cost_usd: (if (.total_cost_usd | type) == "number"
                     then .total_cost_usd else null end),
          cost_source: (if (.total_cost_usd | type) == "number"
                        then "harness" else null end),
          price_table: null,
          billing: null,
          models: (if ($mk | length) == 0 then null
                   else [ $mk[] | {id: ., total: null} ] end),
          derived: []
        }
      end' "$1" 2>/dev/null)" || rec="null"
  [[ "$rec" != "null" ]] && rec="$(usage_with_total "$rec")"
  printf '%s' "${rec:-null}"
}

# Agy's vendor total_tokens excludes cache reads, which is the defect this
# change exists to fix; the parts are summed and the vendor total ignored.
usage_parse_agy() {
  local rec
  rec="$(jq -c '
    if (.usage // null) == null then null
    else {
      input_fresh: (.usage.input_tokens // 0),
      cache_read: (.usage.cache_read_tokens // 0),
      cache_write_5m: 0,
      cache_write_1h: 0,
      cache_write_unsplit: 0,
      output: (.usage.output_tokens // 0),
      reasoning: (.usage.thinking_tokens // null),
      cost_usd: null,
      cost_source: null,
      price_table: null,
      billing: null,
      models: null,
      derived: []
    } end' "$1" 2>/dev/null)" || rec="null"
  [[ "$rec" != "null" ]] && rec="$(usage_with_total "$rec")"
  printf '%s' "${rec:-null}"
}

usage_parse_opencode_export() {
  local src="" rec prog
  [[ $# -gt 0 && "$1" != "-" ]] && src="$1"
  prog='
    (.info.tokens // null) as $t
    | if $t == null then null
      else
        (.info.model.id // null) as $mid
        | {
            input_fresh: ($t.input // 0),
            cache_read: ($t.cache.read // 0),
            cache_write_5m: 0,
            cache_write_1h: 0,
            cache_write_unsplit: ($t.cache.write // 0),
            output: ($t.output // 0),
            reasoning: ($t.reasoning // null),
            cost_usd: null,
            cost_source: null,
            price_table: null,
            billing: null,
            models: (if $mid == null then null
                     else [ {id: $mid, total: null} ] end),
            derived: []
          }
      end'
  if [[ -n "$src" ]]; then
    rec="$(jq -c "$prog" "$src" 2>/dev/null)" || rec="null"
  else
    rec="$(jq -c "$prog" 2>/dev/null)" || rec="null"
  fi
  [[ "$rec" != "null" ]] && rec="$(usage_with_total "$rec")"
  printf '%s' "${rec:-null}"
}

# The session this invocation ran, read from its own event stream. Codex names
# the session in an event of its own before the first turn, and the rollout it
# writes carries the same identifier — in `session_meta` and in the rollout's
# filename. That identifier is the only thing tying a file under `sessions/` to
# the process that wrote it.
#
# Both spellings are read, at any depth. Codex has renamed the field once
# already — `session_configured.session_id` became `thread.started.thread_id` —
# and a rename this function does not know about costs a dash in the model
# column, never a wrong model name. That is the direction a miss should fail in.
usage_codex_session_id() {
  local f="${1:-}"
  [[ -n "$f" && -s "$f" ]] || return 0
  jq -sr '[ .[]? | objects | .. | objects
            | (.thread_id? // .session_id? // empty)
            | select(type == "string") ]
          | first // empty' "$f" 2>/dev/null
}

# Codex's event stream names neither model nor effort; its session rollout
# carries both. Two rules keep reading it safe: treat any failure as a miss,
# and never fail a leg on rollout trouble — the payload has been read by the
# time this runs, so the answer exists even when the telemetry does not.
#
# The home directory to search is the caller's argument, because the default
# path is harness knowledge and this file holds none. Reading CODEX_HOME here
# and stopping when it was empty made the function dead in local mode:
# cred_prepare exports that variable only when a staging secret is present,
# which is automated mode alone, so every local run missed a rollout that was
# on disk the whole time. The adapter now passes the fallback.
#
# The session id is required, and the rollout has to carry it. `~/.codex/sessions`
# is one directory shared by every Codex process on the machine, so the newest
# file in it belongs to whichever session wrote last — which, with a second
# Codex running alongside the leg, is not this one. Reading that file names
# another process's model, prices the leg at its rates, and fires the
# substitution warning on a run that never substituted anything. An
# uncorrelated rollout is therefore a miss, in line with the rule the billing
# derivation already follows: naming the wrong one is worse than naming none.
#
# Two ways to match, because one of them is free. A Codex rollout filename
# embeds the session id, so the common case never opens a file; where the name
# does not carry it, `session_meta` is read instead, and that read is capped so
# a sessions directory holding thousands of rollouts cannot turn a miss into a
# long scan.
#
# A rollout line is an envelope — {timestamp, type, payload} — and the fields
# live inside `payload`, on the `turn_context` record. Reading the envelope's
# own keys finds nothing on a real rollout, so `.payload // .` unwraps first
# and falls through for any line that carries no envelope. Effort is spelled
# `effort` there; `reasoning_effort` is read too, because that is the name the
# same value carries elsewhere in Codex's own output.
usage_read_codex_rollout() {
  local miss='{"model":null,"effort":null}'
  local home="${1:-${CODEX_HOME:-}}"
  local sid="${2:-}"
  [[ -n "$home" && -d "$home/sessions" ]] || { printf '%s' "$miss"; return 0; }
  [[ -n "$sid" ]] || { printf '%s' "$miss"; return 0; }
  local f="" cand rid opened=0
  while IFS= read -r cand; do
    [[ -n "$cand" ]] || continue
    case "${cand##*/}" in *"$sid"*) f="$cand"; break ;; esac
    (( opened >= 25 )) && continue
    opened=$(( opened + 1 ))
    rid="$(jq -sr '[ .[]? | objects | (.payload // .) | objects
                    | .id? | select(type == "string") ]
                   | first // empty' "$cand" 2>/dev/null)" || rid=""
    [[ "$rid" == "$sid" ]] && { f="$cand"; break; }
  done < <(find "$home/sessions" -type f 2>/dev/null | sort -r)
  [[ -n "$f" ]] || { printf '%s' "$miss"; return 0; }
  local model effort
  model="$(jq -sr '[ .[]? | objects | (.payload // .) | objects
                    | .model? | select(type == "string") ]
                   | first // empty' "$f" 2>/dev/null)" || model=""
  effort="$(jq -sr '[ .[]? | objects | (.payload // .) | objects
                     | (.effort? // .reasoning_effort?) | select(type == "string") ]
                    | first // empty' "$f" 2>/dev/null)" || effort=""
  jq -cn --arg m "$model" --arg e "$effort" \
    '{model: (if $m == "" then null else $m end),
      effort: (if $e == "" then null else $e end)}'
}

_usage_harness_file() {
  local root="${ROOT:-$_USAGE_LIB_ROOT}"
  printf '%s' "${CROSSREV_HARNESS_FILE:-$root/lib/harnesses.json}"
}

# Which harnesses keep a vendor API key alive is recorded per harness in the
# descriptor's env_keep array, not known here by name: billing is a function
# of the descriptor and the endpoint name, which is what keeps this file from
# hardcoding a harness.
_usage_keeps_api_key() {
  local harness="$1" hf
  hf="$(_usage_harness_file)"
  [[ -f "$hf" ]] || return 1
  jq -e --arg h "$harness" --arg k "ANTHROPIC_API_KEY" '
    any(.harnesses[];
        .name == $h and ((.credential.env_keep // []) | index($k)))' \
    "$hf" >/dev/null 2>&1
}

# What the harness's own credential bills as, recorded in its descriptor. Only
# subscription and api are claims; unknown and a missing field both print
# nothing, which is what a credential whose form CrossRev cannot tell apart
# deserves.
_usage_credential_billing() {
  local harness="$1" hf
  hf="$(_usage_harness_file)"
  [[ -f "$hf" ]] || return 0
  jq -r --arg h "$harness" '
    [ (.harnesses // [])[] | select(.name == $h) | .credential.billing? ]
    | first // ""
    | if . == "subscription" or . == "api" then . else "" end' \
    "$hf" 2>/dev/null
}

# Billing mode is derived from what the orchestrator already holds, never
# detected. A named endpoint wins first because it changes what a cost means;
# a descriptor that keeps the vendor API key wins over an oauth token because
# env_keep lets both survive into one run and the key is what the run was
# charged to.
#
# Everything else comes from the credential descriptor rather than from a
# subscription default. A harness whose stored credential can be either an
# oauth grant or a provider API key — opencode's `{type, key}` entry is one —
# records `unknown` and gets no billing claim at all, because naming the wrong
# one is worse than naming none.
usage_billing_for() {
  local harness="$1" endpoint="$2"
  if [[ -n "$endpoint" && "$endpoint" != "null" && "$endpoint" != "vendor" ]]; then
    printf 'endpoint'; return 0
  fi
  if _usage_keeps_api_key "$harness" && [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
    printf 'api'; return 0
  fi
  # `|| true` because this is the tail call and the caller runs under set -e:
  # an unreadable descriptor is a missing billing claim, never a dead leg.
  _usage_credential_billing "$harness" || true
}

# The listed key for a reported model id: lowercased, any [...] suffix
# stripped, exact match against a listed key or against a listed key's bare id
# without its provider/ prefix, else the longest bare id the report contains —
# so grok-4.6-build prices as xai/grok-4.6. Empty means unlisted, which prices
# as a refusal rather than a guess.
usage_price_key() {
  local reported
  reported="$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')"
  reported="${reported%%\[*}"
  [[ -n "$reported" ]] || return 0
  local pf hit
  pf="$(_usage_prices_file)"
  [[ -f "$pf" ]] || return 0
  # Every arm requires the value to be an object, because not every top-level
  # key in the price file is a model. `version` is one: it holds a string, and
  # matching it returned a key whose rates cannot be read. usage_price then
  # asked a string for .input_cost_per_token, which is a hard jq error rather
  # than a null — jq exited 5 having printed its own message where a record was
  # expected, and usage_attach handed that message on with status 0. A model
  # reported as `version` cost the caller the whole usage record. Any later
  # non-model key at the top level would do the same, so the shape is checked
  # rather than the name.
  hit="$(jq -r --arg k "$reported" 'if (.[$k] | type) == "object" then $k else empty end' "$pf" 2>/dev/null)"
  [[ -n "$hit" ]] && { printf '%s' "$hit"; return 0; }
  hit="$(jq -r --arg k "$reported" \
    'limit(1; to_entries[] | select((.value | type) == "object")
       | select((.key | split("/") | last) == $k) | .key)' \
    "$pf" 2>/dev/null)"
  [[ -n "$hit" ]] && { printf '%s' "$hit"; return 0; }
  # {key: .key, bare: $bare}, not {key, bare}. The shorthand reads .bare off
  # the to_entries object, which has no such field, so every element's bare was
  # null, `null | length` is 0 for all of them, and sort_by ranked nothing. The
  # rung then answered the LAST match in the file's order rather than the
  # longest bare id, which is what the comment above and the whole point of the
  # rung say it does. The file happens to list a general id before its
  # variants, so the two rules agree today; reordering it would have changed
  # what a model prices at, silently.
  jq -r --arg k "$reported" \
    '[ to_entries[] | select((.value | type) == "object")
       | (.key | split("/") | last) as $bare
       | select($k | contains($bare)) | {key: .key, bare: $bare} ]
     | sort_by(.bare | length) | last | .key // empty' "$pf" 2>/dev/null
}

# Table pricing. Rates are per-token dollars upstream; the arithmetic scales
# each rate to nano-dollars so the sum stays in integers well below float
# precision and one division rounds once. Three rules refuse to price rather
# than guess: a bucket with tokens in it whose rate the extract does not list,
# an unresolvable cache-write TTL whose rates differ, and a per-request
# long-context break a cumulative total cannot rule out.
usage_price() {
  local u="$1" model="$2"
  local key pf r version
  key="$(usage_price_key "$model")"
  pf="$(_usage_prices_file)"
  if [[ -z "$key" || ! -f "$pf" ]]; then
    _usage_clear_cost <<<"$u"; return 0
  fi
  r="$(jq -c --arg k "$key" '.[$k] // null' "$pf" 2>/dev/null)" || r="null"
  version="$(jq -r '.version // ""' "$pf" 2>/dev/null)"
  if [[ "$r" == "null" ]]; then
    _usage_clear_cost <<<"$u"; return 0
  fi
  jq -c --argjson r "$r" --arg v "$version" '
    # A bucket holding tokens whose rate the extract does not list is a
    # refusal, never a zero: an entry can omit a rate entirely — gpt-5.5 lists
    # no cache-write rate at all — and defaulting the missing one to zero
    # prices those tokens free and understates the leg without saying so. Only
    # a nonzero bucket counts, so an entry that omits a rate it never needs
    # still prices.
    ([ {b: (.input_fresh // 0), r: $r.input_cost_per_token},
       {b: (.output // 0), r: $r.output_cost_per_token},
       {b: (.cache_read // 0), r: $r.cache_read_input_token_cost},
       {b: (.cache_write_5m // 0), r: $r.cache_creation_input_token_cost},
       {b: (.cache_write_1h // 0),
        r: ($r.cache_creation_input_token_cost_above_1hr
            // $r.cache_creation_input_token_cost)},
       {b: (.cache_write_unsplit // 0), r: $r.cache_creation_input_token_cost} ]
     | any(.[]; .b > 0 and ((.r | type) != "number"))) as $refuse_unlisted
    | (if (.cache_write_unsplit // 0) > 0
         and ($r | has("cache_creation_input_token_cost_above_1hr"))
         and ($r.cache_creation_input_token_cost_above_1hr
              != $r.cache_creation_input_token_cost)
     then true else false end) as $refuse_unsplit
    | ([ $r | keys[]
         | select(test("^input_cost_per_token_above_[0-9]+k_tokens$"))
         | select(test("flex|priority|batches") | not) ] | first // "") as $bk
    | (if $bk == "" then null
       else ($bk | capture("^input_cost_per_token_above_(?<n>[0-9]+)k_tokens$").n
             | tonumber) end) as $bn
    | ((.input_fresh // 0) + (.cache_read // 0) + (.cache_write_5m // 0)
       + (.cache_write_1h // 0) + (.cache_write_unsplit // 0)) as $cum
    | (if $bn != null and $cum >= ($bn * 1000) then true else false end) as $refuse_break
    | if $refuse_unlisted or $refuse_unsplit or $refuse_break then
        .cost_usd = null | .cost_source = null | .price_table = null
      else
        # Every `// 0` below is reached only for a bucket that is already zero,
        # because a nonzero one with no listed rate refused above. It stays so
        # that a zero bucket meeting a missing rate is arithmetic rather than a
        # jq error that would take the whole record down.
        ((((.input_fresh // 0)
           * ((($r.input_cost_per_token // 0) * 1e9) | round))
          + ((.output // 0)
             * ((($r.output_cost_per_token // 0) * 1e9) | round))
          + ((.cache_read // 0)
             * ((($r.cache_read_input_token_cost // 0) * 1e9) | round))
          + ((.cache_write_5m // 0)
             * ((($r.cache_creation_input_token_cost // 0) * 1e9) | round))
          + ((.cache_write_1h // 0)
             * (((($r.cache_creation_input_token_cost_above_1hr
                   // $r.cache_creation_input_token_cost) // 0) * 1e9) | round))
          + ((.cache_write_unsplit // 0)
             * ((($r.cache_creation_input_token_cost // 0) * 1e9) | round)))
         / 1e9) as $cost
        | .cost_usd = $cost | .cost_source = "table" | .price_table = $v
      end' <<<"$u"
}

# Orchestrator-side merge. Billing always; the cost triple rewritten when the
# billing mode forbids one (a named endpoint discards whatever the adapter
# reported), kept and marked when the harness supplied it, table-priced when
# neither happened.
usage_attach() {
  local u="$1" harness="$2" endpoint="$3" model="$4"
  local billing isnum
  billing="$(usage_billing_for "$harness" "$endpoint")"
  if [[ "$billing" == "endpoint" ]]; then
    jq -c --arg b "$billing" \
      '.billing = $b | .cost_usd = null | .cost_source = null | .price_table = null' \
      <<<"$u"
    return 0
  fi
  isnum="$(jq -r '.cost_usd | if type == "number" then "y" else "n" end' <<<"$u")"
  if [[ "$isnum" == "y" ]]; then
    jq -c --arg b "$billing" \
      '.billing = (if $b == "" then null else $b end)
       | .cost_source = "harness" | .price_table = null' <<<"$u"
    return 0
  fi
  usage_price "$u" "$model" \
    | jq -c --arg b "$billing" '.billing = (if $b == "" then null else $b end)'
}

# Called inside run_invoke after the adapter returns and before cred_discard:
# ANTHROPIC_API_KEY is the billing signal and discard is what throws it away.
# A null usage is left alone — the caller still names billing on the marker.
usage_attach_envelope() {
  local f="$1" harness="$2" endpoint="$3"
  local u mr merged tk tmp
  u="$(jq -c '.usage // null' "$f")" || return 1
  [[ "$u" != "null" ]] || return 0
  mr="$(jq -r '.model_reported // ""' "$f")"
  merged="$(usage_attach "$u" "$harness" "$endpoint" "$mr")" || return 1
  tk="$(jq -r '.total // "null"' <<<"$merged")"
  tmp="$(mktemp)" || return 1
  if jq --argjson u "$merged" --argjson tk "$tk" \
      '.usage = $u | .tokens = $tk' "$f" >"$tmp"; then
    mv "$tmp" "$f"
  else
    rm -f "$tmp"; return 1
  fi
}

usage_cached() {
  jq -r '((.cache_read // 0) + (.cache_write_5m // 0)
          + (.cache_write_1h // 0) + (.cache_write_unsplit // 0))' <<<"$1"
}

usage_format_cost() {
  if [[ "${1:-}" =~ ^-?[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?$ ]]; then
    printf '~$%.2f' "$1"
  else
    printf '—'
  fi
}

# The footnote's inner sentences, composed from three clauses because no one
# sentence is true of every combination of cost_source and billing. No <sub>
# wrapper here; the caller wraps. When no cost clause applies, nothing prints
# and the caller still renders its own gap sentence.
usage_footnote() {
  local cs="${1:-}" billing="${2:-}" opening middle closing out
  [[ -n "$cs" && "$cs" != "null" ]] || return 0
  [[ "$billing" == "endpoint" ]] && return 0
  case "$cs" in
    harness)
      opening="Cost is the harness's own estimate, not an amount charged." ;;
    table)
      opening="Cost is an estimate, not an amount charged, calculated from published API rates for the nearest listed model, which may not be the exact variant that answered." ;;
    *) return 0 ;;
  esac
  middle="Cache reads bill at about a tenth of the input rate and cache writes above it, so the token columns alone do not indicate cost."
  closing=""
  case "$billing" in
    subscription)
      closing="A leg on a subscription inside its included usage is invoiced nothing." ;;
    api)
      closing="The provider's invoice remains authoritative." ;;
  esac
  out="$opening $middle"
  [[ -n "$closing" ]] && out="$out $closing"
  printf '%s' "$out"
}
