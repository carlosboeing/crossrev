#!/usr/bin/env bash
#
# The agy adapter, exercised through a real review leg rather than in isolation.
#
# Two things are worth proving and neither is visible from reading the adapter.
# The CLI's `--print` takes the prompt as its VALUE, so a flag written after it
# becomes the prompt — the stub refuses that order, so this suite fails loudly
# instead of silently reviewing the string "--output-format". And Antigravity
# constrains its own output: `--json-schema` returns the parsed object under
# `structured_output`, which is why it sits alongside claude and codex as
# schema-native and the retry path stays dead code for it.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

REVIEW_PAYLOAD='{"verdict":"issues-remain","blocked_reason":null,"prior":null,"findings":[
  {"path":"app.ts","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,
   "title":"Unchecked fetch response","why":"A failed request looks like a success","fix":"Check response.ok"}
]}'

config_agy_reviews() {
  cat <<'EOF'
version: 1
mode: local
policy:
  min_fix_severity: medium
  max_passes_per_cycle: 3
  max_files_changed_per_pr: 200
  max_prs_per_day: 25
reviewer:
  harness: agy
  model: reviewer-model
resolver:
  harness: claude
  model: resolver-model
backlog:
  destination: none
EOF
}

# --- a review leg on the third harness --------------------------------------
fixture_repo "$(config_agy_reviews)"; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
# Captured before the direct-stub probes below append to the same log.
agy_review_argv="$(cat "$ARGV_LOG")"

is  "a review leg runs on agy"                    "$rc" "0"
has "and names it in the run header"              "$out" "Reviewer: agy"
has "it reads the payload out of structured_output" "$out" "verdict: issues-remain"
is  "and posts the finding it carried"            "$(count 'method POST repos/acme/widget/pulls/42/comments')" "1"
has "the comment names the harness that produced it" "$(calls)" "agy"

# The flag order, asserted two ways, because the obvious way asserts nothing.
#
# Grepping the leg's output for the stub's complaint cannot fail: the stub writes
# it to stderr, the adapter captures stderr into a temp file, and that file only
# surfaces on the error path. The assertion passed whatever the adapter did.
#
# What actually covers it is the run above. Wrong order means the stub exits 96,
# the adapter returns ok:false, and `crossrev review` dies — so "a review leg runs
# on agy" is the real assertion, and it is already at the top of this file.
# What is left worth checking here is that the stub can still tell the
# difference, since a tripwire that has stopped tripping is worse than none.
#
# Read the prompt log first: the probes below invoke the stub directly and it
# overwrites that file.
has "the leg really did get the review prompt" \
  "$(cat "$PROMPT_LOG")" "You are the review leg"

( unset CROSSREV_REVIEW_PAYLOAD CROSSREV_HARNESS_PAYLOAD
  "$HERE/stub/agy" --print "prompt" --output-format json >/dev/null 2>&1 )
is  "the stub still refuses a flag placed after --print" "$?" "96"

# Antigravity keeps its own project root and ignores the shell's working
# directory. Without --add-dir it resolved a relative path against $HOME and the
# permission layer refused it, which read as a harness that cannot resolve at
# all. Asserted on the captured argv, because the leg only shows the stub's
# stderr on the error path.
has "the agy leg names the checkout as the workspace" "$agy_review_argv" "--add-dir"
( unset CROSSREV_REVIEW_PAYLOAD CROSSREV_HARNESS_PAYLOAD
  "$HERE/stub/agy" --output-format json --print "prompt" >/dev/null 2>&1 )
is  "and the stub refuses a leg that omits it"        "$?" "96"
# 1 rather than 0, and deliberately: the order is accepted, and it then fails for
# the ordinary reason — no canned payload was set for a bare invocation. What
# matters is that it is not 96.
( unset CROSSREV_REVIEW_PAYLOAD CROSSREV_HARNESS_PAYLOAD
  "$HERE/stub/agy" --output-format json --add-dir "$HERE" --print "prompt" >/dev/null 2>&1 )
is  "and accepts the order the adapter uses"            "$?" "1"

# --- no model, and that is not a failure ------------------------------------
#
# Antigravity reports no answering model at all. Layer two of the divergence
# guard must stay quiet rather than halting on a field this vendor does not emit,
# exactly as for codex. The marker records the absence rather than implying a
# check that never ran, and the comment claims no answering model rather than
# echoing back the one that was asked for.
has "the marker records that no answering model was reported" \
  "$(calls)" '"model_reported":null'
# The run-details table has one cell for which agent ran, so a harness that
# reports no answering model has to say so rather than let the requested one
# stand in for it unannounced. Named once under the table, not per row: the gap
# is the same every pass.
has "the run details name the gap rather than passing the requested model off as the answering one" \
  "$(calls)" "agy does not report which model answered"
has "and the cell still says which model was asked for" \
  "$(calls)" '`agy` · `reviewer-model`'

# Antigravity does report usage, which is the one number it can contribute.
has "the token count reaches the run-details table" "$(calls)" "| review |"
has "and the marker records it for a re-render"     "$(calls)" '"tokens":2'

# The read-only rule, asserted at the boundary where it would break: a leg makes
# no attempt to write a secret, whatever the harness did to its borrowed copy.
# One writer is the whole reason the refresh chain survives.
is  "a leg writes no secret, ever"                "$(count 'secret set')" "0"

# --- a review leg on the fourth harness -------------------------------------
config_grok_reviews() {
  cat <<'EOF'
version: 1
mode: local
policy:
  min_fix_severity: medium
  max_passes_per_cycle: 3
  max_files_changed_per_pr: 200
  max_prs_per_day: 25
reviewer:
  harness: grok
  model: reviewer-model
resolver:
  harness: claude
  model: resolver-model
backlog:
  destination: none
EOF
}

config_grok_resolves() {
  cat <<'EOF'
version: 1
mode: local
policy:
  min_fix_severity: medium
  max_passes_per_cycle: 3
  max_files_changed_per_pr: 200
  max_prs_per_day: 25
reviewer:
  harness: claude
  model: reviewer-model
resolver:
  harness: grok
  model: resolver-model
backlog:
  destination: none
EOF
}

fixture_repo "$(config_grok_reviews)"; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
# Capture before the probes below invoke the stub and append to the same log.
grok_review_argv="$(cat "$ARGV_LOG")"

is  "a review leg runs on grok"                   "$rc" "0"
has "and names it in the run header"              "$out" "Reviewer: grok"
has "it reads the payload out of structuredOutput" "$out" "verdict: issues-remain"
is  "and posts the finding it carried"            "$(count 'method POST repos/acme/widget/pulls/42/comments')" "1"
has "the comment names the harness that produced it" "$(calls)" "grok"

has "the grok review leg really did get the review prompt" \
  "$(cat "$PROMPT_LOG")" "You are the review leg"

( unset CROSSREV_REVIEW_PAYLOAD CROSSREV_HARNESS_PAYLOAD
  "$HERE/stub/grok" -p "prompt" --output-format json >/dev/null 2>&1 )
is  "the stub still refuses -p, which would swallow the next flag as the prompt" "$?" "96"

tmp_prompt="$(mktemp)"
printf 'You are the review leg\n' >"$tmp_prompt"
( unset CROSSREV_REVIEW_PAYLOAD CROSSREV_HARNESS_PAYLOAD
  "$HERE/stub/grok" --output-format json --permission-mode dontAsk \
    --sandbox read-only --deny Edit --deny Write --prompt-file "$tmp_prompt" \
    >/dev/null 2>&1 )
is  "and accepts the flags the adapter uses" "$?" "1"
rm -f "$tmp_prompt"

has "the marker records the answering model from modelUsage" \
  "$(calls)" '"model_reported":"reviewer-model"'
has "the cell names grok and the answering model" \
  "$(calls)" '`grok` · `reviewer-model`'
hasnt "a reported model is not described as a gap" \
  "$(calls)" "grok does not report which model answered"

has "the token count reaches the run-details table" "$(calls)" "| review |"
has "and the marker records grok's usage total"     "$(calls)" '"tokens":7'

is  "a grok review leg writes no secret, ever"    "$(count 'secret set')" "0"

has  "the grok review leg is pinned read-only"    "$grok_review_argv" "--sandbox read-only"
has  "and denies Edit"                            "$grok_review_argv" "--deny Edit"
has  "and denies Write"                           "$grok_review_argv" "--deny Write"
has  "and runs dontAsk, not a promptable default" "$grok_review_argv" "--permission-mode dontAsk"
hasnt "a grok review leg is granted no Edit"      "$grok_review_argv" "--allow Edit"
hasnt "nor Write"                                 "$grok_review_argv" "--allow Write"
hasnt "nor a blanket bypass"                      "$grok_review_argv" "bypassPermissions"
hasnt "nor --always-approve"                      "$grok_review_argv" "--always-approve"
hasnt "nor --yolo"                                "$grok_review_argv" "--yolo"
hasnt "nor anything --dangerously"                "$grok_review_argv" "--dangerously"

# --- grok resolve leg gets the write grant the review leg was denied --------
ID_A="a1b2c3d4"

review_marker_for_grok() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(( $(date +%s) - 192 ))" --arg a "$ID_A" '
    {v:1, leg:"review", pass:1, state:"complete", ts:$ts, done_ts:($ts + 192), run_id:"1",
     head_sha:$sha, harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     effort:null, endpoint:null, tokens:41205, verdict:"issues-remain",
     findings:[
       {id:$a, path:"app.ts", line:2, side:"RIGHT", severity:"high", category:"correctness",
        pre_existing:false, title:"Unchecked fetch response", why:"w", fix:"f",
        anchor:"", thread_id:"T_A", resolution:null, tracked_as:null}]}'
}

RESOLVE_PAYLOAD='{"blocked":false,"blocked_reason":null,"summary":"Checked the response.",
  "resolutions":[{"finding_number":1,"resolution":"fixed","reply":"Checked response.ok.",
                   "persist":null,"duplicate_of":null}]}'

fixture_repo "$(config_grok_resolves)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker_for_grok)" | jq -cs . | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9002}'
route '*reviewThreads*' "$(threads_response "$(thread_node T_A app.ts 2 false "$ID_A")")"
route '*resolveReviewThread*' '{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}'
route 'api --method POST repos/*/pulls/42/comments/*/replies*' '{"id":6001}'
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$RESOLVE_PAYLOAD" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(mktemp)"
printf 'printf "export const ok = 2\\n" >app.ts\n' >"$CROSSREV_RESOLVE_EDIT"
export CROSSREV_RESOLVE_EDIT
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "a resolve leg runs on grok"                  "$rc" "0"
grok_resolve_argv="$(cat "$ARGV_LOG")"
has  "the grok resolve leg gets a workspace sandbox" "$grok_resolve_argv" "--sandbox workspace"
has  "and may Edit"                               "$grok_resolve_argv" "--allow Edit"
has  "and may Write"                              "$grok_resolve_argv" "--allow Write"
has  "and still runs dontAsk"                     "$grok_resolve_argv" "--permission-mode dontAsk"
hasnt "a grok resolve leg is not pinned read-only" "$grok_resolve_argv" "--sandbox read-only"
hasnt "nor given a blanket bypass"                "$grok_resolve_argv" "bypassPermissions"

# --- grok authentication rejection is a credential failure ------------------
fixture_repo "$(config_grok_reviews)"; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
CROSSREV_GROK_UNAUTH=1; export CROSSREV_GROK_UNAUTH
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
unset CROSSREV_GROK_UNAUTH

is  "an unauthenticated grok run fails"           "$rc" "1"
has "and CrossRev names it a credential failure"  "$out" "credential failure"
has "naming Grok in the diagnosis"                "$out" "Grok"

finish
