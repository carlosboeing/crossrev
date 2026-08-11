#!/usr/bin/env bash
#
# What revloop shows a human: the emoji, the one native alert per summary
# comment, the run-details table, and the wording correction underneath all of
# it.
#
# The correction is checked through a real run rather than by grepping the
# source, and that is the point of putting it here. Three occurrences of "a
# different model" in this repository are correct — a test name, a code comment
# and a note in the config template — so a repository-wide grep would either
# fail on all three or be weakened until it caught nothing. Running both legs
# reaches every emitted site instead: the inline comment, the review summary,
# and both prompts, which reproduce the two skill files verbatim.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

ID_SEC="aaaa000000000001"
ID_PRE="bbbb000000000002"

# One finding of each shape the rendering has to handle: above the threshold,
# below it, and pre-existing. The pipe in the third title is deliberate — a
# title carrying a table delimiter must not break the table it lands in.
REVIEW_PAYLOAD='{"verdict":"issues-remain","blocked_reason":null,"prior":null,"findings":[
  {"path":"app.ts","line":2,"side":"RIGHT","severity":"high","category":"security","pre_existing":false,
   "title":"Token compared with == instead of a constant-time check",
   "why":"A timing side channel leaks the token","fix":"Use timingSafeEqual"},
  {"path":"app.ts","line":1,"side":"RIGHT","severity":"low","category":"maintainability","pre_existing":false,
   "title":"Third copy of the same date helper","why":"Drift between copies","fix":"Extract one"},
  {"path":"app.ts","line":1,"side":"RIGHT","severity":"medium","category":"correctness","pre_existing":true,
   "title":"Off-by-one in the drain loop | second clause",
   "why":"Drops the last item","fix":"Use <= in the bound"}
]}'

CONVERGED_PAYLOAD='{"verdict":"converged","blocked_reason":null,"prior":null,"findings":[]}'
BLOCKED_PAYLOAD='{"verdict":"blocked","blocked_reason":"the diff would not fetch","prior":null,"findings":[]}'

no_threads() {
  route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
}

# One review leg against a canned payload. Leaves the call log and the prompt log
# in place for the caller to assert on.
run_review() {
  fixture_repo; stub_reset
  routes_baseline "$(printf '[]' | payload)"
  route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
  no_threads
  REVLOOP_REVIEW_PAYLOAD="$(printf '%s' "$1" | payload)"; export REVLOOP_REVIEW_PAYLOAD
  "$REVLOOP" review --pr 42 >/dev/null 2>&1
}

# A completed review marker carrying the same three findings, so the resolve leg
# has something to disposition.
review_marker() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(( $(date +%s) - 192 ))" \
    --arg s "$ID_SEC" --arg p "$ID_PRE" '
    {v:1, leg:"review", pass:1, state:"complete", ts:$ts, done_ts:($ts + 192), run_id:"1",
     head_sha:$sha, harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     effort:"high", endpoint:null, tokens:41205, verdict:"issues-remain",
     findings:[
       {id:$s, path:"app.ts", line:2, side:"RIGHT", severity:"high", category:"security",
        pre_existing:false, title:"Token compared with == instead of a constant-time check",
        why:"w", fix:"f", anchor:"", thread_id:"T_SEC", disposition:null, tracked_as:null},
       {id:$p, path:"app.ts", line:1, side:"RIGHT", severity:"medium", category:"correctness",
        pre_existing:true, title:"Off-by-one in the drain loop | second clause",
        why:"w", fix:"f", anchor:"", thread_id:"T_PRE", disposition:null, tracked_as:null}]}'
}

resolve_payload() {
  jq -cn --arg s "$ID_SEC" --arg p "$ID_PRE" '
    {blocked:false, blocked_reason:null,
     summary:"Replaced the comparison. The drain loop predates this branch.",
     dispositions:[
       {finding_id:$s, disposition:"fixed", reply:"Replaced with a constant-time compare.",
        persist:null, duplicate_of:null},
       {finding_id:$p, disposition:"skipped", reply:"Pre-existing, so it is reported not fixed.",
        persist:null, duplicate_of:null}]}'
}

# One resolve leg over that marker.
run_resolve() {
  fixture_repo; stub_reset
  routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
  route 'api --method POST repos/*/issues/42/comments*' '{"id":9002}'
  route '*reviewThreads*' "$(threads_response \
    "$(thread_node T_SEC app.ts 2 false "$ID_SEC")" \
    "$(thread_node T_PRE app.ts 1 false "$ID_PRE")")"
  route '*resolveReviewThread*' '{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}'
  route 'api --method POST repos/*/pulls/42/comments/*/replies*' '{"id":6001}'
  REVLOOP_RESOLVE_PAYLOAD="$(printf '%s' "$1" | payload)"; export REVLOOP_RESOLVE_PAYLOAD
  REVLOOP_RESOLVE_MODEL=resolver-model; export REVLOOP_RESOLVE_MODEL
  "$REVLOOP" resolve --pr 42 >/dev/null 2>&1
}

# ---------------------------------------------------------------------------
# The correction: nothing emitted claims a second MODEL
# ---------------------------------------------------------------------------
#
# `legs_assert_models_diverged` short-circuits when both legs were configured
# with the same model, so configuring them identically is permitted and the loop
# runs normally. Every emitted claim of a *different model* is therefore a claim
# revloop cannot back. Agent-shaped wording is true in every configuration and
# still carries the independent-second-look property.

run_review "$REVIEW_PAYLOAD"
review_calls="$(calls)"
review_prompt="$(cat "$PROMPT_LOG")"

hasnt "the inline comment claims no second model"     "$review_calls" "second model"
hasnt "nor a different one"                           "$review_calls" "different model"
has   "it claims a second agent instead"              "$review_calls" "A second agent now verifies"
has   "and the summary hands over to one"             "$review_calls" "a second agent verifies every finding"
hasnt "the review prompt makes no model claim either" "$review_prompt" "two-model loop"
has   "the review skill says two-agent"               "$review_prompt" "two-agent loop"

run_resolve "$(resolve_payload)"
resolve_calls="$(calls)"
resolve_prompt="$(cat "$PROMPT_LOG")"

hasnt "the resolve prompt claims no different model"  "$resolve_prompt" "different model"
has   "it names the review leg as a separate agent"   "$resolve_prompt" "a separate agent"
has   "and the resolve skill agrees"                  "$resolve_prompt" "a second agent that reviewed it"

# ---------------------------------------------------------------------------
# Decision 1 — emoji for severity and category
# ---------------------------------------------------------------------------

# Variant A: severity dot, category word, title on the same line. It has to fit a
# narrow diff column without wrapping, which is why the category stays a word and
# only the severity carries a glyph.
has "the inline heading leads with the severity dot"  "$review_calls" "🔴 **High · Security** — Token compared"
hasnt "and does not put the category emoji there too" "$review_calls" "🔴 🔒"
hasnt "nor bracket it like machine output"            "$review_calls" "[High · Security]"

# Provenance is not a fourth severity. It belongs where there is room to say what
# it means, which is the table's *what* cell and the note under the finding.
hasnt "the heading carries no provenance"             "$review_calls" "· Security · pre-existing"
has   "the table's what cell does, muted"             "$review_calls" "<sub>· pre-existing</sub>"

has "the summary table header names both columns"     "$review_calls" "| Severity | Category | Where | What |"
has "a high security finding carries both glyphs"     "$review_calls" "| 🔴 High | 🔒 Security |"
has "a low maintainability one carries its own"       "$review_calls" "| 🔵 Low | 🧹 Maintainability |"
has "and a medium correctness one"                    "$review_calls" "| 🟠 Medium | 🐛 Correctness |"

# A model-written title carrying a pipe splits the row into extra columns, and the
# surrounding rows still render — so the table is quietly wrong rather than
# obviously broken. Escaped at the cell, not hoped away in the prompt.
has "a pipe in a title is escaped rather than splitting the row" \
  "$review_calls" 'Off-by-one in the drain loop \| second clause'

has "the resolve table names findings, not ids alone" \
  "$resolve_calls" "| 🔴 Token compared with == instead of a constant-time check"
has "and keeps the id small for matching a row to a thread" \
  "$resolve_calls" "<sub>\`$ID_SEC\`</sub> | fixed |"

finish
