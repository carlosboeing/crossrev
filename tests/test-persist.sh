#!/usr/bin/env bash
#
# The resolve leg: dispositions, deferral, both dedupe tiers, persist-before-
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
resolver:
  harness: claude
  model: resolver-model
  fix_at: medium
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

# A completed review marker carrying one fixable finding and one pre-existing
# one. The pre-existing finding is the interesting case: it is verified like any
# other, and provenance rather than severity is what keeps it out of the diff.
review_marker() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(date +%s)" \
    --arg f "$ID_FIX" --arg d "$ID_DEFER" '
    {v:1, leg:"review", pass:1, state:"complete", ts:$ts, run_id:"1", head_sha:$sha,
     harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     verdict:"issues-remain",
     findings:[
       {id:$f, path:"app.ts", line:2, side:"RIGHT", severity:"high", category:"correctness", pre_existing:false,
        title:"Unchecked fetch response", why:"w", fix:"check it", anchor:"",
        thread_id:"T_FIX", disposition:null, tracked_as:null},
       {id:$d, path:"app.ts", line:1, side:"RIGHT", severity:"high", category:"maintainability", pre_existing:true,
        title:"Legacy export is untyped", why:"w", fix:"type it", anchor:"",
        thread_id:"T_DEFER", disposition:null, tracked_as:null}]}'
}

resolve_payload() {
  local dup="${1:-null}" persist="${2:-yes}"
  local p='{"title":"Legacy export is untyped","body":"Measured before filing."}'
  [[ "$persist" == "no" ]] && p='null'
  jq -cn --arg f "$ID_FIX" --arg d "$ID_DEFER" --argjson dup "$dup" --argjson p "$p" '
    {blocked:false, blocked_reason:null,
     summary:"Fixed the unchecked response. The untyped legacy export is real but predates this branch.",
     dispositions:[
       {finding_id:$f, disposition:"fixed", reply:"Added the ok check.", persist:null, duplicate_of:null},
       {finding_id:$d, disposition:"deferred", reply:"Confirmed real, and it predates this branch.",
        persist:$p, duplicate_of:$dup}]}'
}

config_with_file_sink() {
  cat <<'EOF'
version: 1
mode: single-run
max_passes: 3
reviewer:
  harness: claude
  model: reviewer-model
resolver:
  harness: claude
  model: resolver-model
  fix_at: medium
sinks:
  backlog:
    type: file
    path: .revloop/backlog
persist:
  defects: backlog
  escalated: none
caps:
  runs_per_day: 12
  max_files_changed: 200
EOF
}

# Only a deferral, nothing fixed. The case where a commit has to happen for a
# reason other than a code change.
defer_only_payload() {
  jq -cn --arg d "$ID_DEFER" '
    {blocked:false, blocked_reason:null,
     summary:"Nothing was fixed here. The untyped legacy export is real but predates this branch.",
     dispositions:[
       {finding_id:$d, disposition:"deferred", reply:"Confirmed real, and it predates this branch.",
        persist:{title:"Legacy export is untyped", body:"Measured before filing."},
        duplicate_of:null}]}'
}

# The resolver changes code; the orchestrator commits it.
edit_script() {
  local f; f="$(mktemp)"
  printf 'printf "export const ok = 1\\nexport async function refresh() { const r = await fetch(\\"/t\\"); if (!r.ok) throw new Error(\\"bad\\") }\\n" > app.ts\n' >"$f"
  printf '%s' "$f"
}

routes_resolve() {
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
routes_resolve
REVLOOP_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export REVLOOP_RESOLVE_PAYLOAD
REVLOOP_RESOLVE_EDIT="$(edit_script)"; export REVLOOP_RESOLVE_EDIT
REVLOOP_RESOLVE_MODEL=resolver-model; export REVLOOP_RESOLVE_MODEL
out="$("$REVLOOP" resolve --pr 42 2>&1)"; rc=$?

is  "the resolve leg exits clean"                     "$rc" "0"
has "it reports the pass it resolved"                "$out" "resolved pass 1"
has "it commits and pushes the fix"                   "$out" "to feature"
is  "the fix is actually in the branch" \
  "$(git log -1 --format=%s)" "fix: resolve revloop review findings (pass 1)"
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
has "the summary lists what was deferred and where"   "$(calls)" "Deferred work filed"
has "the loop is handed back to the reviewer"         "$(calls)" "labels[]=revloop/awaiting-review"

# Provenance governs what happens after verification, never whether it happens.
# The fixture's pre-existing finding is `high` deliberately: under the severity
# split it would otherwise be the one thing above the threshold, and the point of
# the boolean is that provenance outranks the scale.
has "a pre-existing finding still reached the resolver" \
  "$(cat "$PROMPT_LOG")" "Legacy export is untyped"
has "and the prompt tells it not to fix that one here" \
  "$(cat "$PROMPT_LOG")" "verify, then stop"
has "the prompt marks it as one the resolver may not fix" \
  "$(cat "$PROMPT_LOG")" "May fix: no"
hasnt "a high-severity pre-existing finding is not in the commit" \
  "$(git log -1 --format=%B)" "$ID_DEFER"
has  "it is filed to the sink instead"                "$(calls)" "Legacy export is untyped"

# The resolver is blind to the quarantined paths while the diff still carries
# their changes, so the reviewer can raise findings it cannot act on. The rule
# is only useful if the list is concrete: "instruction files" is not a path.
has "the prompt says which paths are not in the checkout" \
  "$(cat "$PROMPT_LOG")" "deliberately not in the checkout"
has "and names them rather than describing them" \
  "$(cat "$PROMPT_LOG")" "CLAUDE.md, AGENTS.md, GEMINI.md"
has "and says what to return for one"                 "$(cat "$PROMPT_LOG")" "quarantined and the finding was reported rather than verified"

# --- a file sink's write has to be inside the commit ----------------------
#
# The issue sink writes to GitHub, so ordering cannot hurt it. A file sink writes
# into the working tree, and a commit that ran first left that write behind: on
# an ephemeral runner it died with the container while its thread resolved,
# because `tracked` was non-empty. That is the "work disappears" failure
# persist-before-resolve exists to prevent, one step further along.
fixture_repo "$(config_with_file_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
REVLOOP_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export REVLOOP_RESOLVE_PAYLOAD
REVLOOP_RESOLVE_EDIT="$(edit_script)"; export REVLOOP_RESOLVE_EDIT
out="$("$REVLOOP" resolve --pr 42 2>&1)"; rc=$?

is  "the file-sink leg exits clean"                   "$rc" "0"
has "the deferral is recorded to the file sink"       "$out" "filed 1 issue(s) for deferred work"
is  "the sink file is in the commit, not just the tree" \
  "$(git show --name-only --format= HEAD | grep -c '^\.revloop/backlog/')" "1"
is  "so nothing of it is left behind in the tree" \
  "$(git status --porcelain -- .revloop | wc -l | tr -d ' ')" "0"
is  "and the code fix rides in the same commit" \
  "$(git show --name-only --format= HEAD | grep -c '^app\.ts$')" "1"

# The other half: a pass that defers and fixes nothing still has a tree write to
# carry, and a commit guarded only on the fix count skipped exactly that case.
fixture_repo "$(config_with_file_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
REVLOOP_RESOLVE_PAYLOAD="$(defer_only_payload | payload)"; export REVLOOP_RESOLVE_PAYLOAD
out="$("$REVLOOP" resolve --pr 42 2>&1)"; rc=$?

is  "a deferral-only pass exits clean"                "$rc" "0"
is  "it still commits, because the sink wrote to the tree" \
  "$(git show --name-only --format= HEAD | grep -c '^\.revloop/backlog/')" "1"
has "and says what the commit is for, not 'fix'"      "$(git log -1 --format=%s)" "chore: record deferred revloop findings"
hasnt "no fix was claimed, so nothing warns about one" \
  "$out" "changed no files"

# --- the review leg's own comments must not silence the resolver ---------
#
# Every inline comment the review leg posts carries a finding marker, so a posted
# set read without filtering on the leg finds every id already there. The reply
# is then skipped as a duplicate while the thread is resolved anyway, and the
# collaborator gets a resolved thread with no explanation in it. The baseline
# stubs pulls/42/comments as [], which is exactly why nothing caught this: the
# one condition that triggers it is the one the fixture never had.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
route_first "api --paginate repos/*/pulls/42/comments*" \
  "@$(POSTED_LEG=review posted_comments "$ID_FIX" "$ID_DEFER" | payload)"
REVLOOP_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export REVLOOP_RESOLVE_PAYLOAD
REVLOOP_RESOLVE_EDIT="$(edit_script)"; export REVLOOP_RESOLVE_EDIT
out="$("$REVLOOP" resolve --pr 42 2>&1)"; rc=$?

is  "the leg completes with the review leg's comments present" "$rc" "0"
is  "it still replies in both threads" \
  "$(count 'pulls/42/comments/5000/replies')" "2"
has "and still resolves what it settled"              "$out" "resolved 2 thread(s)"

# The other half of the same rule: a resolve-leg marker DOES mean "replied
# already", so a retry after a crash must not reply twice.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
route_first "api --paginate repos/*/pulls/42/comments*" \
  "@$(POSTED_LEG=resolve posted_comments "$ID_FIX" | payload)"
REVLOOP_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export REVLOOP_RESOLVE_PAYLOAD
REVLOOP_RESOLVE_EDIT="$(edit_script)"; export REVLOOP_RESOLVE_EDIT
out="$("$REVLOOP" resolve --pr 42 2>&1)"; rc=$?

is  "a finding already replied to is not replied to twice" \
  "$(count 'pulls/42/comments/5000/replies')" "1"

# --- persist: none files nothing and leaves the thread open ---------------
fixture_repo; stub_reset      # the default config has persist.defects: none
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
REVLOOP_RESOLVE_PAYLOAD="$(resolve_payload null no | payload)"; export REVLOOP_RESOLVE_PAYLOAD
out="$("$REVLOOP" resolve --pr 42 2>&1)"; rc=$?

is  "with no sink the leg still completes"            "$rc" "0"
is  "nothing is filed anywhere"                       "$(count 'method POST repos/acme/widget/issues -f title=')" "0"
has "and the summary says the thread stays open"      "$(calls)" "not persisted anywhere"
is  "only the settled thread is resolved, not the deferred one" \
  "$(count 'resolveReviewThread')" "1"

# --- dedupe tier 1: revloop already filed this exact finding --------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
existing="$(jq -cn --arg d "$ID_DEFER" '
  [{number:55, pull_request:null,
    body:("Filed earlier. <!-- revloop:f " + ({id:$d, pass:1, leg:"resolve"} | tojson) + " -->")}]')"
route_first 'api --paginate repos/*/issues?state=all*' "$existing"
REVLOOP_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export REVLOOP_RESOLVE_PAYLOAD
out="$("$REVLOOP" resolve --pr 42 2>&1)"; rc=$?

is  "an already-filed finding files nothing"          "$(count 'method POST repos/acme/widget/issues -f title=')" "0"
has "and it says the finding was already tracked"     "$(calls)" "already tracked as #55"
has "the thread still resolves, because it is tracked" "$out" "resolved 2 thread(s)"

# --- dedupe tier 2: the resolver matched a human-filed issue ------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
route_first 'api -X GET search/issues*' \
  '{"items":[{"number":31,"title":"app.ts exports are untyped","state":"open","body":"Noticed a while ago."}]}'
REVLOOP_RESOLVE_PAYLOAD="$(resolve_payload 31 | payload)"; export REVLOOP_RESOLVE_PAYLOAD
out="$("$REVLOOP" resolve --pr 42 2>&1)"; rc=$?

is  "a matched human-filed issue files nothing"       "$(count 'method POST repos/acme/widget/issues -f title=')" "0"
has "and the summary names the issue it matched"      "$(calls)" "matches the existing issue #31"
has "the candidates were handed to the model to judge" "$(cat "$PROMPT_LOG")" "**#31** (open)"
hasnt "and revloop did not comment on the human's issue by default" \
  "$(calls)" "repos/acme/widget/issues/31/comments"

# A closed candidate counts the same: re-filing something explicitly closed is the
# most irritating duplicate available.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
route_first 'api -X GET search/issues*' \
  '{"items":[{"number":19,"title":"untyped exports","state":"closed","body":"Decided against."}]}'
REVLOOP_RESOLVE_PAYLOAD="$(resolve_payload 19 | payload)"; export REVLOOP_RESOLVE_PAYLOAD
out="$("$REVLOOP" resolve --pr 42 2>&1)"
is  "a closed matching issue files nothing"           "$(count 'method POST repos/acme/widget/issues -f title=')" "0"
has "and the closed candidate was offered for judgement" "$(cat "$PROMPT_LOG")" "(closed)"

# --- persist before resolve ---------------------------------------------
#
# A sink write that fails must leave the thread open and the disposition
# unrecorded, rather than resolving against a write that did not land.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
route_first 'api --method POST repos/*/issues -f title=*' '!fail'
REVLOOP_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export REVLOOP_RESOLVE_PAYLOAD
out="$("$REVLOOP" resolve --pr 42 2>&1)"; rc=$?

is  "a failed filing does not fail the whole leg"     "$rc" "0"
has "it says the write did not land"                  "$out" "could not file an issue"
has "and the summary records that nothing was persisted" "$(calls)" "not persisted anywhere"
is  "the deferred thread is left open"                "$(count 'resolveReviewThread')" "1"

# --- commit idempotency -------------------------------------------------
#
# Pushed code cannot dedupe the way comments can, so the resolve leg records the
# SHA it produced. A recovery that finds one skips the fix step entirely.
fixture_repo "$(config_with_issue_sink)"; stub_reset
addr_claim="$(jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(date +%s)" --arg f "$ID_FIX" --arg d "$ID_DEFER" '
  {v:1, leg:"resolve", pass:1, state:"started", ts:$ts, run_id:"1", head_sha:$sha,
   harness:"claude", model:"resolver-model", model_reported:"resolver-model",
   blocked:false, blocked_reason:null, commit_sha:"cafe0000cafe0000cafe0000cafe0000cafe0000",
   summary:"Recovered.",
   dispositions:[{finding_id:$f, disposition:"fixed", reply:"done", persist:null, duplicate_of:null},
                 {finding_id:$d, disposition:"rebutted", reply:"not real", persist:null, duplicate_of:null}]}')"
comments="$( { marker_comment 9001 "$(review_marker)"; marker_comment 9002 "$addr_claim"; } | jq -cs . | payload)"
routes_baseline "$comments"
routes_resolve
out="$("$REVLOOP" resolve --pr 42 2>&1)"; rc=$?

is  "recovery with a recorded SHA exits clean"        "$rc" "0"
has "it does not run the resolver again"             "$out" "already recorded its dispositions"
has "and it does not re-run the fix step"             "$out" "already pushed cafe000"
is  "so nothing new is committed"                     "$(git log -1 --format=%s)" "feature"

# --- escalation halts the loop and summons a human ---------------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
escalating="$(jq -cn --arg f "$ID_FIX" --arg d "$ID_DEFER" '
  {blocked:false, blocked_reason:null, summary:"One point needs you.",
   dispositions:[{finding_id:$f, disposition:"escalated", reply:"We disagree twice over.", persist:null, duplicate_of:null},
                 {finding_id:$d, disposition:"rebutted", reply:"Not real here.", persist:null, duplicate_of:null}]}')"
REVLOOP_RESOLVE_PAYLOAD="$(printf '%s' "$escalating" | payload)"; export REVLOOP_RESOLVE_PAYLOAD
out="$("$REVLOOP" resolve --pr 42 2>&1)"

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
routes_resolve
REVLOOP_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export REVLOOP_RESOLVE_PAYLOAD
REVLOOP_RESOLVE_MODEL=reviewer-model; export REVLOOP_RESOLVE_MODEL
err="$("$REVLOOP" resolve --pr 42 2>&1 >/dev/null)"; rc=$?
is  "the same answering model on both legs halts"     "$rc" "1"
has "and the error names the model that answered both" "$err" "the same model answered each: reviewer-model"
export REVLOOP_RESOLVE_MODEL=resolver-model

finish
