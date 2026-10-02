#!/usr/bin/env bash
#
# The review-quality loop, driven through the compiled binary with stubbed
# GitHub and real temporary git histories: concern passes, the cross-model
# check and the required-check gate across a review leg and its resolve leg.
#
# Each case runs both legs against the stateful stand-in (tests/stub/gh):
# comment, thread, label and check-run reads are deliberately unrouted, so
# the resolve leg observes what the review leg wrote. A case fails if its
# mechanism is removed: without the check the rejected finding posts,
# without the coverage gate the settle converges, and without newest-wins
# an old success converges over a newer queued run.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

# --- helpers ---------------------------------------------------------------

# One review payload for the single required file app.ts at the fixture head.
# $1 verdict, $2 coverage JSON, $3 findings JSON (default []), $4 scope
# (default a fixed sentence), $5 limits JSON (default []).
# comments_since prints the stub comments created after comments_mark
# was taken, so an assertion reads one leg's own comments rather than
# every comment on the pull request.
comments_mark() { ls "$GH_STATE"/comment-[0-9]* 2>/dev/null; }
comments_since() {
  local f
  for f in "$GH_STATE"/comment-[0-9]*; do
    [[ -e "$f" ]] || continue
    grep -qxF "$f" <<<"$1" || cat "$f"
  done
}

review_payload_for() {
  local verdict="$1" coverage="$2" findings="${3:-[]}"
  local scope="${4:-read app.ts at the head}" limits="${5:-[]}"
  jq -cn --arg v "$verdict" --argjson c "$coverage" --argjson f "$findings" \
    --arg s "$scope" --argjson l "$limits" \
    '{verdict:$v, blocked_reason:null, findings:$f, coverage:$c,
      examined_scope:$s, known_limits:$l}'
}

# Coverage for unit 1 over app.ts at $FIX_HEAD. $1 verdict, $2 finding
# numbers JSON, $3 evidence JSON, $4 reason JSON (default null).
unit1() {
  local verdict="$1" numbers="$2" evidence="$3" reason="${4:-null}"
  jq -cn --arg d "$verdict" --argjson n "$numbers" --argjson e "$evidence" \
    --argjson r "$reason" \
    '{unit_number:1, verdict:$d, finding_numbers:$n, evidence:$e, reason:$r}'
}

# File-level git evidence for app.ts at the fixture head.
evidence_file() {
  jq -cn --arg sha "$FIX_HEAD" \
    '[{path:"app.ts", revision:$sha, start_line:null, end_line:null,
       source:"git", note:null}]'
}

# The correctness answer: one high finding on the added line, covered.
correctness_finding() {
  jq -cn --argjson cov "[$(unit1 finding '[1]' "$(evidence_file)")]" \
    --argjson f '[{"path":"app.ts", "line":2, "side":"RIGHT", "severity":"high",
      "category":"correctness", "pre_existing":false,
      "title":"Unchecked fetch response",
      "why":"fetch is used without checking ok",
      "fix":"check the response before reading it"}]' \
    '{verdict:"issues-remain", blocked_reason:null, findings:$f, coverage:$cov,
      examined_scope:"read app.ts at the head", known_limits:[]}'
}

# The consistency answer: the file could not be examined at all.
consistency_unexamined() {
  local reason='"submodule fetch failed, then LFS fetch failed"'
  jq -cn --argjson cov "[$(unit1 could_not_review '[]' '[]' "$reason")]" \
    '{verdict:"issues-remain", blocked_reason:null, findings:[], coverage:$cov,
      examined_scope:"tried app.ts at the head",
      known_limits:["could not read app.ts"]}'
}

# One check decision on the single candidate. $1 confirmed|rejected, $2 reason.
check_payload_for() {
  jq -cn --arg d "$1" --arg r "$2" \
    '{decisions:[{position:1, decision:$d, duplicate_of:null, reason:$r,
      severity:null, pre_existing:null}]}'
}

# A no-commit dispute of the single finding.
dispute_payload() {
  jq -cn '{blocked:false, blocked_reason:null,
    summary:"The fetch is guarded by the caller.",
    resolutions:[{finding_number:1, resolution:"disputed",
      reply:"the caller guards it", persist:null, duplicate_of:null}]}'
}

# Routes for one review run over the default single-file fixture with an
# empty comment list. Comment creates, edits, threads and labels stay
# unrouted so the stand-in's live state answers them across both legs.
routes_loop_empty() {
  routes_baseline "$(printf '[]' | payload)"
}

# The fixture config for the gate cases: one concern, so the review
# publishes and the settle judges under the same contract, and a
# required check the seeds below report on.
gate_config() {
  printf '%s\n' "$(fixture_default_config)" \
    'review:' \
    '  concerns: [correctness]' \
    'verification:' \
    '  required_checks: [build]' \
    '  wait_minutes: 0'
}

# Labels currently applied, one per line, from the stub call log. Adds use
# -f labels[]=, removals use a DELETE path, so this names only adds.
applied_labels() { grep -o "labels\[\]=crossrev/[a-z0-9-]*" "$GH_LOG" | sort -u; }

# Labels applied after line $1 of the call log: the per-leg slice, so one
# leg's converged label cannot satisfy the other leg's refusal.
labels_since() { tail -n +"$(( $1 + 1 ))" "$GH_LOG" | grep -o "labels\[\]=crossrev/[a-z0-9-]*" | sort -u; }

# Inline finding comments posted so far. Replies travel under
# .../comments/<id>/replies, so the trailing " -f" keeps them out.
# grep -c prints 0 AND exits 1 on no match, so the fallback assigns
# rather than printing a second zero (the tests/harness.sh count trap).
inline_posts() {
  local n
  n="$(grep -c "pulls/$FIX_PR/comments -f" "$GH_LOG" 2>/dev/null)" || n=0
  printf '%s' "${n:-0}"
}

# A rejected finding with an unexamined file blocks the pass: correctness
# raises one issue, consistency answers could_not_review, the check
# rejects the issue. Nothing posts, nothing converges, and the resolve
# leg refuses the blocked review rather than settling it.
fixture_repo; stub_reset
routes_loop_empty
CROSSREV_REVIEW_PAYLOAD="$(correctness_finding | payload)"; export CROSSREV_REVIEW_PAYLOAD
CROSSREV_REVIEW_PAYLOAD_2="$(consistency_unexamined | payload)"; export CROSSREV_REVIEW_PAYLOAD_2
CROSSREV_STUB_COUNT="$(mktemp)"; export CROSSREV_STUB_COUNT
CROSSREV_HARNESS_PAYLOAD="$(check_payload_for rejected 'the fetch is guarded one line above' | payload)"
export CROSSREV_HARNESS_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a rejected finding with an unexamined file runs" "$rc" "0"
has "the check names its rejection" "$out" "rejected 1 of 1 candidates"
has "the pass records blocked on the unexamined file" "$out" "verdict: blocked — 1 required file(s) could not be examined"
is "a rejected finding posts no inline comment" "$(inline_posts)" "0"
has "a blocked pass halts" "$(applied_labels)" "labels[]=crossrev/halted"
hasnt "a blocked pass never converges" "$(applied_labels)" "labels[]=crossrev/converged"
unset CROSSREV_REVIEW_PAYLOAD CROSSREV_REVIEW_PAYLOAD_2 CROSSREV_STUB_COUNT CROSSREV_HARNESS_PAYLOAD
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?
is "resolving a blocked review refuses" "$rc" "1"
has "the refusal names the blocked review" "$out" "was blocked"
hasnt "a refused resolve never converges" "$(applied_labels)" "labels[]=crossrev/converged"

# A confirmed finding with an unexamined file cannot settle: the same
# pass with the issue confirmed posts it, and the resolve leg's
# no-commit dispute hands back to the reviewer instead of converging,
# because the generation still carries the unexamined record.
fixture_repo; stub_reset
routes_loop_empty
CROSSREV_REVIEW_PAYLOAD="$(correctness_finding | payload)"; export CROSSREV_REVIEW_PAYLOAD
CROSSREV_REVIEW_PAYLOAD_2="$(consistency_unexamined | payload)"; export CROSSREV_REVIEW_PAYLOAD_2
CROSSREV_STUB_COUNT="$(mktemp)"; export CROSSREV_STUB_COUNT
CROSSREV_HARNESS_PAYLOAD="$(check_payload_for confirmed 'the fetch result is read with no guard' | payload)"
export CROSSREV_HARNESS_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a confirmed finding with an unexamined file runs" "$rc" "0"
is "a confirmed finding posts one inline comment" "$(inline_posts)" "1"
has "the review owes the resolve leg" "$(applied_labels)" "labels[]=crossrev/awaiting-resolution"
unset CROSSREV_REVIEW_PAYLOAD CROSSREV_REVIEW_PAYLOAD_2 CROSSREV_STUB_COUNT CROSSREV_HARNESS_PAYLOAD
CROSSREV_RESOLVE_PAYLOAD="$(dispute_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
mark="$(wc -l <"$GH_LOG" | tr -d ' ')"
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?
is "a no-commit dispute runs" "$rc" "0"
has "a dispute over unexamined coverage hands back to the reviewer" "$(labels_since "$mark")" "labels[]=crossrev/awaiting-review"
hasnt "a dispute over unexamined coverage never converges" "$(labels_since "$mark")" "labels[]=crossrev/converged"
unset CROSSREV_RESOLVE_PAYLOAD

# A failed required check holds the settle off converged: the review
# posts its confirmed finding while the gate reports failure, and the
# resolve leg's no-commit dispute lands awaiting-review with the
# failed evidence on its marker.
fixture_repo "$(gate_config)"; stub_reset
routes_loop_empty
printf '%s' '[{"id":7,"name":"build","status":"completed","conclusion":"failure",
  "html_url":"https://github.com/acme/widget/runs/7",
  "app":{"slug":"github-actions"}}]' >"$GH_STATE/check-runs.json"
CROSSREV_REVIEW_PAYLOAD="$(correctness_finding | payload)"; export CROSSREV_REVIEW_PAYLOAD
CROSSREV_HARNESS_PAYLOAD="$(check_payload_for confirmed 'the fetch result is read with no guard' | payload)"
export CROSSREV_HARNESS_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a review under a failed gate runs" "$rc" "0"
has "a review under a failed gate still owes the resolve leg" "$(applied_labels)" "labels[]=crossrev/awaiting-resolution"
unset CROSSREV_REVIEW_PAYLOAD CROSSREV_HARNESS_PAYLOAD
CROSSREV_RESOLVE_PAYLOAD="$(dispute_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
before="$(comments_mark)"
mark="$(wc -l <"$GH_LOG" | tr -d ' ')"
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?
is "a dispute under a failed gate runs" "$rc" "0"
has "a dispute under a failed gate hands back to the reviewer" "$(labels_since "$mark")" "labels[]=crossrev/awaiting-review"
hasnt "a dispute under a failed gate never converges" "$(labels_since "$mark")" "labels[]=crossrev/converged"
has "the settle records the failed gate" "$(comments_since "$before")" '"verification":{"state":"failed"'
unset CROSSREV_RESOLVE_PAYLOAD

# Checked-out findings under a failed gate block the review: the check
# rejects the only finding, so nothing posts and the pass records
# required_check_failed with the checks named, and the resolve leg
# refuses the blocked review.
fixture_repo "$(gate_config)"; stub_reset
routes_loop_empty
printf '%s' '[{"id":7,"name":"build","status":"completed","conclusion":"failure",
  "html_url":"https://github.com/acme/widget/runs/7",
  "app":{"slug":"github-actions"}}]' >"$GH_STATE/check-runs.json"
CROSSREV_REVIEW_PAYLOAD="$(correctness_finding | payload)"; export CROSSREV_REVIEW_PAYLOAD
CROSSREV_HARNESS_PAYLOAD="$(check_payload_for rejected 'the fetch is guarded one line above' | payload)"
export CROSSREV_HARNESS_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a checked-out review under a failed gate runs" "$rc" "0"
is "a checked-out review posts no inline comment" "$(inline_posts)" "0"
has "a checked-out review under a failed gate halts on the gate" "$out" "required_check_failed"
has "a checked-out review under a failed gate halts" "$(applied_labels)" "labels[]=crossrev/halted"
hasnt "a checked-out review under a failed gate never converges" "$(applied_labels)" "labels[]=crossrev/converged"
unset CROSSREV_REVIEW_PAYLOAD CROSSREV_HARNESS_PAYLOAD
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?
is "resolving a gate-blocked review refuses" "$rc" "1"
has "the refusal names the blocked review" "$out" "was blocked"

# A newer queued run supersedes an older success: the gate judges the
# run with the greatest id, so the settle reads pending and hands
# back to the reviewer instead of converging on the old success.
fixture_repo "$(gate_config)"; stub_reset
routes_loop_empty
printf '%s' '[{"id":7,"name":"build","status":"completed","conclusion":"success",
  "html_url":"https://github.com/acme/widget/runs/7",
  "app":{"slug":"github-actions"}},
  {"id":9,"name":"build","status":"queued","conclusion":null,
  "html_url":"https://github.com/acme/widget/runs/9",
  "app":{"slug":"github-actions"}}]' >"$GH_STATE/check-runs.json"
CROSSREV_REVIEW_PAYLOAD="$(correctness_finding | payload)"; export CROSSREV_REVIEW_PAYLOAD
CROSSREV_HARNESS_PAYLOAD="$(check_payload_for confirmed 'the fetch result is read with no guard' | payload)"
export CROSSREV_HARNESS_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a review under a superseded success runs" "$rc" "0"
has "a review under a superseded success still owes the resolve leg" "$(applied_labels)" "labels[]=crossrev/awaiting-resolution"
unset CROSSREV_REVIEW_PAYLOAD CROSSREV_HARNESS_PAYLOAD
CROSSREV_RESOLVE_PAYLOAD="$(dispute_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
before="$(comments_mark)"
mark="$(wc -l <"$GH_LOG" | tr -d ' ')"
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?
is "a dispute under a superseded success runs" "$rc" "0"
has "a dispute under a superseded success hands back to the reviewer" "$(labels_since "$mark")" "labels[]=crossrev/awaiting-review"
hasnt "a dispute under a superseded success never converges" "$(labels_since "$mark")" "labels[]=crossrev/converged"
has "the settle records the newer queued run" "$(comments_since "$before")" '"verification":{"state":"pending"'
unset CROSSREV_RESOLVE_PAYLOAD

finish
