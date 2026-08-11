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
mode: single-run
max_passes: 3
reviewer:
  harness: agy
  model: reviewer-model
resolver:
  harness: claude
  model: resolver-model
persist:
  defects: none
EOF
}

# --- a review leg on the third harness --------------------------------------
fixture_repo "$(config_agy_reviews)"; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
REVLOOP_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export REVLOOP_REVIEW_PAYLOAD
out="$("$REVLOOP" review --pr 42 2>&1)"; rc=$?

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
# the adapter returns ok:false, and `revloop review` dies — so "a review leg runs
# on agy" is the real assertion, and it is already at the top of this file.
# What is left worth checking here is that the stub can still tell the
# difference, since a tripwire that has stopped tripping is worse than none.
#
# Read the prompt log first: the probes below invoke the stub directly and it
# overwrites that file.
has "the leg really did get the review prompt" \
  "$(cat "$PROMPT_LOG")" "You are the review leg"

( unset REVLOOP_REVIEW_PAYLOAD REVLOOP_HARNESS_PAYLOAD
  "$HERE/stub/agy" --print "prompt" --output-format json >/dev/null 2>&1 )
is  "the stub still refuses a flag placed after --print" "$?" "96"
# 1 rather than 0, and deliberately: the order is accepted, and it then fails for
# the ordinary reason — no canned payload was set for a bare invocation. What
# matters is that it is not 96.
( unset REVLOOP_REVIEW_PAYLOAD REVLOOP_HARNESS_PAYLOAD
  "$HERE/stub/agy" --output-format json --print "prompt" >/dev/null 2>&1 )
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
hasnt "and the comment does not pass the requested model off as the answering one" \
  "$(calls)" "answered by"

# The read-only rule, asserted at the boundary where it would break: a leg makes
# no attempt to write a secret, whatever the harness did to its borrowed copy.
# One writer is the whole reason the refresh chain survives.
is  "a leg writes no secret, ever"                "$(count 'secret set')" "0"

finish
