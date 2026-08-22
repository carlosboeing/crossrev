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

# --- a review leg on the fifth harness --------------------------------------
#
# opencode constrains nothing: there is no schema flag, so the schema travels
# inside the prompt and the adapter extracts JSON from the NDJSON event stream
# itself. The stub emits what the real CLI measured at 1.18.21 emits, and its
# tripwires carry the isolation story — this harness grants `edit` and `bash`
# out of the box, so a leg that ran without the deny config would be the bug.
config_opencode_reviews() {
  cat <<'EOF'
version: 1
mode: local
policy:
  min_fix_severity: medium
  max_passes_per_cycle: 3
  max_files_changed_per_pr: 200
  max_prs_per_day: 25
reviewer:
  harness: opencode
  model: opencode/reviewer-model
resolver:
  harness: claude
  model: resolver-model
backlog:
  destination: none
EOF
}

# The five recorded stdout shapes feed through extraction: bare JSON, a fence,
# prose around the JSON, an empty stream, and an error event alone. The first
# three must yield the same payload; the last two must fail, saying different
# things.

fixture_repo "$(config_opencode_reviews)"; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
# Captured before the direct-stub probes below append to the same log.
opencode_review_argv="$(cat "$ARGV_LOG")"

is  "a review leg runs on opencode"               "$rc" "0"
has "and names it in the run header"              "$out" "Reviewer: opencode"
has "it reads the payload out of the concatenated text events" "$out" "verdict: issues-remain"
is  "and posts the finding it carried"            "$(count 'method POST repos/acme/widget/pulls/42/comments')" "1"
has "the comment names the harness that produced it" "$(calls)" "opencode"

# The answering model and the whole-run token total come from one `export`
# call against the sessionID the events carry: input 10 + output 2 +
# reasoning 1 + cache.read 4 = 17, the arithmetic the CLI itself uses.
has "the marker records the answering model from the session record" \
  "$(calls)" '"model_reported":"stub-model"'
has "the token count sums the whole-run figure"   "$(calls)" '"tokens":17'
has "the cell names opencode and the answering model" \
  "$(calls)" '`opencode` · `stub-model`'

has "the leg really did get the review prompt" \
  "$(cat "$PROMPT_LOG")" "You are the review leg"

# The argument vector, asserted on what the adapter built rather than on the
# stub's complaints: --format json selects the event stream, --dir names the
# checkout, and --auto is a blanket bypass that is never passed.
has "the leg asks for the json event stream"      "$opencode_review_argv" "--format json"
has "the leg names the checkout as the workspace" "$opencode_review_argv" "--dir"
has "the leg passes the configured model through" "$opencode_review_argv" "--model opencode/reviewer-model"
hasnt "a leg is granted no blanket bypass"        "$opencode_review_argv" "--auto"

( unset OPENCODE_CONFIG OPENCODE_CONFIG_DIR
  "$HERE/stub/opencode" run --format json --dir "$HERE" "prompt" >/dev/null 2>&1 )
is  "the stub refuses a run with no isolation config" "$?" "96"
( unset OPENCODE_CONFIG OPENCODE_CONFIG_DIR
  "$HERE/stub/opencode" run --format json --auto --dir "$HERE" "prompt" >/dev/null 2>&1 )
is  "the stub still refuses --auto, which is a blanket bypass" "$?" "96"

deny_cfg="$(mktemp)"
printf '{"permission":{"edit":"deny","bash":"deny"}}' >"$deny_cfg"
( export OPENCODE_CONFIG="$deny_cfg" OPENCODE_CONFIG_DIR="$HERE"
  unset CROSSREV_REVIEW_PAYLOAD
  "$HERE/stub/opencode" run --format json --dir "$HERE" "prompt" >/dev/null 2>&1 )
is  "and accepts the flags and config the adapter uses" "$?" "1"
rm -f "$deny_cfg"

# A fence around the JSON must change nothing.
fixture_repo "$(config_opencode_reviews)"; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
CROSSREV_OPENCODE_MODE=fenced; export CROSSREV_OPENCODE_MODE
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
unset CROSSREV_OPENCODE_MODE
is  "a fenced answer still reviews cleanly"       "$rc" "0"
is  "and posts the same finding"                  "$(count 'method POST repos/acme/widget/pulls/42/comments')" "1"

# Prose wrapped around the JSON must change nothing either — ladder step three.
fixture_repo "$(config_opencode_reviews)"; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
CROSSREV_OPENCODE_MODE=prose; export CROSSREV_OPENCODE_MODE
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
unset CROSSREV_OPENCODE_MODE
is  "prose around the JSON still reviews cleanly" "$rc" "0"
is  "and posts the same finding"                  "$(count 'method POST repos/acme/widget/pulls/42/comments')" "1"

# An empty stream is not a malformed answer — there is no answer at all — and
# the two are diagnosed differently. This is the shape the real CLI produces
# when it answers nothing, and it must not read as a schema mismatch.
fixture_repo "$(config_opencode_reviews)"; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
CROSSREV_OPENCODE_MODE=empty; export CROSSREV_OPENCODE_MODE
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
unset CROSSREV_OPENCODE_MODE
is  "a run that answered nothing fails"           "$rc" "1"
has "and says there was no answer, distinctly"    "$out" "produced no answer"
hasnt "and never calls it a schema mismatch"      "$out" "does not match the schema"

# An error event alone is how an authentication rejection reaches stdout, with
# AI_APICallError naming Unauthorized on stderr behind it. It is classified as
# a credential failure naming opencode, not a generic harness error.
fixture_repo "$(config_opencode_reviews)"; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
CROSSREV_OPENCODE_MODE=error; export CROSSREV_OPENCODE_MODE
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
unset CROSSREV_OPENCODE_MODE
is  "an unauthenticated opencode run fails"       "$rc" "1"
has "and CrossRev names it a credential failure"  "$out" "credential failure"
has "naming opencode in the diagnosis"            "$out" "rejected its credential"

# The session record is telemetry, not the answer: if the export call fails,
# both fields fall back to null and the review itself stands.
fixture_repo "$(config_opencode_reviews)"; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
CROSSREV_OPENCODE_NO_EXPORT=1; export CROSSREV_OPENCODE_NO_EXPORT
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
unset CROSSREV_OPENCODE_NO_EXPORT
is  "a failed session export costs the leg nothing" "$rc" "0"
has "and the marker records no answering model"   "$(calls)" '"model_reported":null'

finish
