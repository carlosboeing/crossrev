#!/usr/bin/env bash
#
# The address leg: dispositions, deferral, both dedupe tiers, persist-before-
# resolve, the commit, and the guards around all of it.
#
# A finding that is real but not fixed has to outlive the pull request, and an
# unresolved thread on a merged pull request is visible in no GitHub view. So the
# ordering here is the property under test: persist first, resolve second, and
# never resolve against a write that did not land.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

ID_FIX="aaaa000000000001"
ID_DEFER="bbbb000000000002"

config_with_issue_sink() {
  cat <<'EOF'
version: 1
mode: single-run
max_passes: 3
reviewer:
  harness: claude
  model: reviewer-model
addresser:
  harness: claude
  model: addresser-model
  skip_nits_after_pass: 1
sinks:
  issues:
    type: github_issue
    identity_label: revloop-review
    labels: [bug]
    create_labels: true
    comment_on_match: false
persist:
  defects: issues
  escalated: none
caps:
  runs_per_day: 12
  max_files_changed: 200
EOF
}

# A completed review marker carrying one important and one pre-existing finding.
# The pre-existing one is the interesting case: severity governs what happens
# after verification, never whether verification happens.
review_marker() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(date +%s)" \
    --arg f "$ID_FIX" --arg d "$ID_DEFER" '
    {v:1, leg:"review", pass:1, state:"complete", ts:$ts, run_id:"1", head_sha:$sha,
     harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     verdict:"issues-remain",
     findings:[
       {id:$f, path:"app.ts", line:2, side:"RIGHT", severity:"important",
        title:"Unchecked fetch response", why:"w", fix:"check it", anchor:"",
        thread_id:"T_FIX", disposition:null, tracked_as:null},
       {id:$d, path:"app.ts", line:1, side:"RIGHT", severity:"pre-existing",
        title:"Legacy export is untyped", why:"w", fix:"type it", anchor:"",
        thread_id:"T_DEFER", disposition:null, tracked_as:null}]}'
}

address_payload() {
  local dup="${1:-null}" persist="${2:-yes}"
  local p='{"title":"Legacy export is untyped","body":"Measured before filing."}'
  [[ "$persist" == "no" ]] && p='null'
  jq -cn --arg f "$ID_FIX" --arg d "$ID_DEFER" --argjson dup "$dup" --argjson p "$p" '
    {blocked:false, blocked_reason:null,
     wrap_up:"Fixed the unchecked response. The untyped legacy export is real but predates this branch.",
     dispositions:[
       {finding_id:$f, disposition:"fixed", reply:"Added the ok check.", persist:null, duplicate_of:null},
       {finding_id:$d, disposition:"deferred", reply:"Confirmed real, and it predates this branch.",
        persist:$p, duplicate_of:$dup}]}'
}

# The addresser changes code; the orchestrator commits it.
edit_script() {
  local f; f="$(mktemp)"
  printf 'printf "export const ok = 1\\nexport async function refresh() { const r = await fetch(\\"/t\\"); if (!r.ok) throw new Error(\\"bad\\") }\\n" > app.ts\n' >"$f"
  printf '%s' "$f"
}

routes_address() {
  local threads; threads="$(threads_response \
    "$(thread_node T_FIX app.ts 2 false "$ID_FIX")" \
    "$(thread_node T_DEFER app.ts 1 false "$ID_DEFER")")"
  route 'api --method POST repos/*/issues/42/comments*' '{"id":9002}'
  route '*reviewThreads*' "$threads"
  route '*resolveReviewThread*' '{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}'
  route 'api --method POST repos/*/pulls/42/comments/*/replies*' '{"id":6001}'
  route 'api -X GET search/issues*' '{"items":[]}'
  route 'api --paginate repos/*/issues?state=all*' '[]'
  route 'api --method POST repos/*/issues -f title=*' '{"number":77}'
}

# --- the happy path: one fix committed, one defect filed -------------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_address
REVLOOP_ADDRESS_PAYLOAD="$(address_payload | payload)"; export REVLOOP_ADDRESS_PAYLOAD
REVLOOP_ADDRESS_EDIT="$(edit_script)"; export REVLOOP_ADDRESS_EDIT
REVLOOP_ADDRESS_MODEL=addresser-model; export REVLOOP_ADDRESS_MODEL
out="$("$REVLOOP" address --pr 42 2>&1)"; rc=$?

is  "the address leg exits clean"                     "$rc" "0"
has "it reports the pass it addressed"                "$out" "addressed pass 1"
has "it commits and pushes the fix"                   "$out" "to feature"
is  "the fix is actually in the branch" \
  "$(git log -1 --format=%s)" "fix: address revloop review findings (pass 1)"
# Checked against the bare repo rather than `git ls-remote origin`: the fixture's
# fetch URL is a real github.com address it must never contact, so ls-remote there
# would fail and prove nothing either way.
is  "and it is pushed, not just committed" \
  "$(git rev-parse HEAD)" "$(git -C "$FIX_ORIGIN" rev-parse refs/heads/feature)"

has "it files the deferred defect"                    "$out" "filed 1 issue(s) for deferred work"
has "the issue carries the identity label"            "$(calls)" "labels[]=revloop-review"
has "and the repository's own taxonomy alongside it"  "$(calls)" "labels[]=bug"
is  "it replies in each thread rather than at top level" \
  "$(count 'pulls/42/comments/5000/replies')" "2"
has "it resolves the threads it settled"              "$out" "resolved 2 thread(s)"
has "the wrap-up lists what was deferred and where"   "$(calls)" "Deferred work filed"
has "the loop is handed back to the reviewer"         "$(calls)" "labels[]=revloop/awaiting-review"

# Severity governs what happens after verification, never whether it happens.
has "a pre-existing finding still reached the addresser" \
  "$(cat "$PROMPT_LOG")" "Legacy export is untyped"
has "and the prompt tells it not to fix that one here" \
  "$(cat "$PROMPT_LOG")" "verify, then stop"

# --- persist: none files nothing and leaves the thread open ---------------
fixture_repo; stub_reset      # the default config has persist.defects: none
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_address
REVLOOP_ADDRESS_PAYLOAD="$(address_payload null no | payload)"; export REVLOOP_ADDRESS_PAYLOAD
out="$("$REVLOOP" address --pr 42 2>&1)"; rc=$?

is  "with no sink the leg still completes"            "$rc" "0"
is  "nothing is filed anywhere"                       "$(count 'method POST repos/acme/widget/issues -f title=')" "0"
has "and the wrap-up says the thread stays open"      "$(calls)" "not persisted anywhere"
is  "only the settled thread is resolved, not the deferred one" \
  "$(count 'resolveReviewThread')" "1"

# --- dedupe tier 1: revloop already filed this exact finding --------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_address
existing="$(jq -cn --arg d "$ID_DEFER" '
  [{number:55, pull_request:null,
    body:("Filed earlier. <!-- revloop:f " + ({id:$d, pass:1, leg:"address"} | tojson) + " -->")}]')"
route_first 'api --paginate repos/*/issues?state=all*' "$existing"
REVLOOP_ADDRESS_PAYLOAD="$(address_payload | payload)"; export REVLOOP_ADDRESS_PAYLOAD
out="$("$REVLOOP" address --pr 42 2>&1)"; rc=$?

is  "an already-filed finding files nothing"          "$(count 'method POST repos/acme/widget/issues -f title=')" "0"
has "and it says the finding was already tracked"     "$(calls)" "already tracked as #55"
has "the thread still resolves, because it is tracked" "$out" "resolved 2 thread(s)"

# --- dedupe tier 2: the addresser matched a human-filed issue ------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_address
route_first 'api -X GET search/issues*' \
  '{"items":[{"number":31,"title":"app.ts exports are untyped","state":"open","body":"Noticed a while ago."}]}'
REVLOOP_ADDRESS_PAYLOAD="$(address_payload 31 | payload)"; export REVLOOP_ADDRESS_PAYLOAD
out="$("$REVLOOP" address --pr 42 2>&1)"; rc=$?

is  "a matched human-filed issue files nothing"       "$(count 'method POST repos/acme/widget/issues -f title=')" "0"
has "and the wrap-up names the issue it matched"      "$(calls)" "matches the existing issue #31"
has "the candidates were handed to the model to judge" "$(cat "$PROMPT_LOG")" "**#31** (open)"
hasnt "and revloop did not comment on the human's issue by default" \
  "$(calls)" "repos/acme/widget/issues/31/comments"

# A closed candidate counts the same: re-filing something explicitly closed is the
# most irritating duplicate available.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_address
route_first 'api -X GET search/issues*' \
  '{"items":[{"number":19,"title":"untyped exports","state":"closed","body":"Decided against."}]}'
REVLOOP_ADDRESS_PAYLOAD="$(address_payload 19 | payload)"; export REVLOOP_ADDRESS_PAYLOAD
out="$("$REVLOOP" address --pr 42 2>&1)"
is  "a closed matching issue files nothing"           "$(count 'method POST repos/acme/widget/issues -f title=')" "0"
has "and the closed candidate was offered for judgement" "$(cat "$PROMPT_LOG")" "(closed)"

# --- persist before resolve ---------------------------------------------
#
# A sink write that fails must leave the thread open and the disposition
# unrecorded, rather than resolving against a write that did not land.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_address
route_first 'api --method POST repos/*/issues -f title=*' '!fail'
REVLOOP_ADDRESS_PAYLOAD="$(address_payload | payload)"; export REVLOOP_ADDRESS_PAYLOAD
out="$("$REVLOOP" address --pr 42 2>&1)"; rc=$?

is  "a failed filing does not fail the whole leg"     "$rc" "0"
has "it says the write did not land"                  "$out" "could not file an issue"
has "and the wrap-up records that nothing was persisted" "$(calls)" "not persisted anywhere"
is  "the deferred thread is left open"                "$(count 'resolveReviewThread')" "1"

# --- commit idempotency -------------------------------------------------
#
# Pushed code cannot dedupe the way comments can, so the address leg records the
# SHA it produced. A recovery that finds one skips the fix step entirely.
fixture_repo "$(config_with_issue_sink)"; stub_reset
addr_claim="$(jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(date +%s)" --arg f "$ID_FIX" --arg d "$ID_DEFER" '
  {v:1, leg:"address", pass:1, state:"started", ts:$ts, run_id:"1", head_sha:$sha,
   harness:"claude", model:"addresser-model", model_reported:"addresser-model",
   blocked:false, blocked_reason:null, commit_sha:"cafe0000cafe0000cafe0000cafe0000cafe0000",
   wrap_up:"Recovered.",
   dispositions:[{finding_id:$f, disposition:"fixed", reply:"done", persist:null, duplicate_of:null},
                 {finding_id:$d, disposition:"rebutted", reply:"not real", persist:null, duplicate_of:null}]}')"
comments="$( { marker_comment 9001 "$(review_marker)"; marker_comment 9002 "$addr_claim"; } | jq -cs . | payload)"
routes_baseline "$comments"
routes_address
out="$("$REVLOOP" address --pr 42 2>&1)"; rc=$?

is  "recovery with a recorded SHA exits clean"        "$rc" "0"
has "it does not run the addresser again"             "$out" "already recorded its dispositions"
has "and it does not re-run the fix step"             "$out" "already pushed cafe000"
is  "so nothing new is committed"                     "$(git log -1 --format=%s)" "feature"

# --- escalation halts the loop and summons a human ---------------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_address
escalating="$(jq -cn --arg f "$ID_FIX" --arg d "$ID_DEFER" '
  {blocked:false, blocked_reason:null, wrap_up:"One point needs you.",
   dispositions:[{finding_id:$f, disposition:"escalated", reply:"We disagree twice over.", persist:null, duplicate_of:null},
                 {finding_id:$d, disposition:"rebutted", reply:"Not real here.", persist:null, duplicate_of:null}]}')"
REVLOOP_ADDRESS_PAYLOAD="$(printf '%s' "$escalating" | payload)"; export REVLOOP_ADDRESS_PAYLOAD
out="$("$REVLOOP" address --pr 42 2>&1)"

has "an escalated finding applies revloop/stop"       "$(calls)" "labels[]=revloop/stop"
has "and halts rather than handing back to the reviewer" "$(calls)" "labels[]=revloop/halted"
has "and says a human is needed"                      "$out" "need a human decision"
is  "the escalated thread is left open"               "$(count 'resolveReviewThread')" "1"

# --- the divergence guard, layer two ----------------------------------
#
# Two legs configured to differ but answered by the same model is the failure the
# whole cross-model design exists to prevent, and it completes normally unchecked.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_address
REVLOOP_ADDRESS_PAYLOAD="$(address_payload | payload)"; export REVLOOP_ADDRESS_PAYLOAD
REVLOOP_ADDRESS_MODEL=reviewer-model; export REVLOOP_ADDRESS_MODEL
err="$("$REVLOOP" address --pr 42 2>&1 >/dev/null)"; rc=$?
is  "the same answering model on both legs halts"     "$rc" "1"
has "and the error names the model that answered both" "$err" "the same model answered each: reviewer-model"
export REVLOOP_ADDRESS_MODEL=addresser-model

finish
