#!/usr/bin/env bash
#
# The Review Intelligence acceptance oracle, driven through the compiled
# binary with stubbed GitHub and real temporary git histories.
#
# The expected sets are literal data under tests/fixtures/intelligence/ and
# are never generated with production discovery or convergence functions:
# UnitIDs and digests below are hand-written from the frozen oracle, or
# computed with SHA-256 over the documented preimage. The shell suite proves
# the CLI path that writes labels and markers; internal/intel/acceptance_test.go,
# internal/prstate/ledger_acceptance_test.go and
# internal/policy/convergence_acceptance_test.go prove the same contract in Go.
#
# This suite fails on the pre-slice binary because it can converge with no
# coverage marker: the no-coverage converge case below asserts halted and a
# blocked verdict where the old binary reports converged.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

# --- helpers ---------------------------------------------------------------

# One review payload for the single required file app.ts at the fixture head.
# $1 verdict, $2 coverage JSON, $3 findings JSON (default []), $4 scope
# (default a fixed sentence), $5 limits JSON (default []).
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

# Ranged git evidence for app.ts at the fixture head, lines $1-$2.
evidence_range() {
  jq -cn --arg sha "$FIX_HEAD" --argjson s "$1" --argjson e "$2" \
    '[{path:"app.ts", revision:$sha, start_line:$s, end_line:$e,
       source:"git", note:null}]'
}

# Routes for one review run over the default single-file fixture with an
# empty comment list. Call after fixture_repo and stub_reset.
routes_review_empty() {
  routes_baseline "$(printf '[]' | payload)"
  route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
  route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
}

# The claim marker run 1 left, replayed as a static comment list with the
# trusted author, so a second binary invocation resumes from the same
# handle. The git objects persist in GH_STATE by themselves; only the
# marker needs replaying, and it is rebuilt from the ref run 1 left
# behind rather than re-serialized from a spool.
#
# $1 state (default started: the crash shape — run 1 published and died
# before the complete edit, so run 2 recovers the pass), $2 author
# (default the trusted user).
replay_claim() {
  replay_claim_as "${1:-started}" "${2:-$FIX_USER}"
}

replay_claim_as() {
  local state="$1" author="$2" sha gen
  read -r sha gen <<<"$(claim_handle)"
  local ref="refs/crossrev/pr/$FIX_PR/reviewer1/coverage"
  local marker
  marker="$(jq -cn --arg head "$FIX_HEAD" --arg sha "$sha" --argjson gen "$gen" \
    --arg state "$state" --argjson ts "$(date +%s)" --arg ref "$ref" \
    '{v:2, leg:"review", pass:1, state:$state, ts:$ts, head_sha:$head,
      harness:"claude", model:"reviewer-model",
      coverage_gen:$gen, coverage_ref:$ref, coverage_commit:$sha,
      coverage_degraded:false, findings:[]}')"
  route_first "api --paginate repos/*/issues/$FIX_PR/comments*" "$(jq -cn \
    --arg m "$marker" --arg a "$author" \
    '[{id:9001, body:("Reviewing.<!-- crossrev: " + $m + " -->"),
       user:{login:$a}, created_at:"2026-09-09T00:00:00Z"}]')"
}

# The blocked complete claim run 1 left, replayed so run 2 re-drives the
# pass: the writer gate rebuilds convergence from the current bytes rather
# than trusting the verdict run 1 recorded.
replay_blocked_claim() {
  local sha gen
  read -r sha gen <<<"$(claim_handle)"
  local ref="refs/crossrev/pr/$FIX_PR/reviewer1/coverage"
  local marker
  marker="$(jq -cn --arg head "$FIX_HEAD" --arg sha "$sha" --argjson gen "$gen" \
    --argjson ts "$(date +%s)" --arg ref "$ref" \
    '{v:2, leg:"review", pass:1, state:"complete", ts:$ts,
      head_sha:$head, harness:"claude", model:"reviewer-model",
      verdict:"blocked", blocked_reason:"coverage debt",
      coverage_gen:$gen, coverage_ref:$ref, coverage_commit:$sha,
      coverage_degraded:false, findings:[]}')"
  route_first "api --paginate repos/*/issues/$FIX_PR/comments*" "$(jq -cn \
    --arg m "$marker" --arg a "$FIX_USER" \
    '[{id:9001, body:("Reviewing.<!-- crossrev: " + $m + " -->"),
       user:{login:$a}, created_at:"2026-09-09T00:00:00Z"}]')"
}

# The handle run 1 published: the tip commit behind the slot's ref and the
# generation number its message carries, printed as "sha gen".
claim_handle() {
  local ref_file sha gen
  ref_file="$(ls "$GH_STATE"/ref-refs_crossrev_pr_"${FIX_PR}"_* 2>/dev/null | head -n 1)"
  sha="$(jq -r .object.sha "$ref_file")"
  gen="$(jq -r '.message' "$GH_STATE/commit-$sha" | sed -n 's/.*gen \([0-9][0-9]*\).*/\1/p')"
  printf '%s %s' "$sha" "${gen:-1}"
}

# Labels currently applied, one per line, from the stub call log.
applied_labels() { grep -o "labels\[\]=crossrev/[a-z-]*" "$GH_LOG" | sort -u; }

# Labels applied after line $1 of the call log: the per-run slice, so an
# earlier run's converged label cannot satisfy a later run's refusal.
labels_since() { tail -n +"$(( $1 + 1 ))" "$GH_LOG" | grep -o "labels\[\]=crossrev/[a-z-]*" | sort -u; }

# Re-point the pr-view route at the current FIX_HEAD and FIX_BASE after a
# repair commit: routes match in file order, so the stale baseline entry
# must be shadowed with route_first rather than appended behind it.
repoint_pr_view() {
  route_first "pr view $FIX_PR --repo * --json *" "$(jq -cn \
    --argjson n "$FIX_PR" --arg h "$FIX_HEAD" --arg b "$FIX_BASE" \
    '{number:$n, title:"Add refresh", body:"Adds a refresh helper.", url:"https://github.com/x",
      headRefName:"feature", headRefOid:$h, baseRefName:"main", baseRefOid:$b,
      changedFiles:1, labels:[], isCrossRepository:false, maintainerCanModify:false, isDraft:false,
      headRepositoryOwner:{login:"acme"}, headRepository:{name:"widget"}, state:"OPEN"}')"
}

# First-occurrence byte flip in a spooled comment (portable sed -i via temp
# file: BSD sed needs an argument to -i, so write aside and move back).
flip_first() { tmp="$(mktemp)"; sed "s/$1/$2/" "$3" >"$tmp" && mv "$tmp" "$3"; }

# --- semantic validation ---------------------------------------------------

# Omitted unit number: the answer names no coverage for the one required
# file, gets one retry quoting the missing number, then halts incomplete.
fixture_repo; stub_reset
routes_review_empty
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for issues-remain '[]' | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
hasnt "an omitted unit never converges" "$out" "verdict: converged"
has "an omitted unit names the missing number on retry" "$out" "missing unit number(s) 1"
is "a semantic retry invokes the harness once more" \
  "$(grep -c -- "-p --output-format json" "$ARGV_LOG" | tr -d ' ')" "2"

# Duplicate unit number: unit 1 twice, unit count still one required file.
fixture_repo; stub_reset
routes_review_empty
dup="$(jq -cs '.' <<<"$(unit1 no_issue '[]' "$(evidence_file)") $(unit1 no_issue '[]' "$(evidence_file)")")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for issues-remain "$dup" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
has "a duplicate unit names the number it repeated" "$out" "more than once"
hasnt "a duplicate unit never converges" "$out" "verdict: converged"

# Unknown unit number: unit 9 where only unit 1 was supplied.
fixture_repo; stub_reset
routes_review_empty
unknown="$(jq -cn --argjson e "$(evidence_file)" \
  '[{unit_number:9, verdict:"no_issue", finding_numbers:[],
     evidence:$e, reason:null}]')"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for issues-remain "$unknown" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
has "an unknown unit names the invented number" "$out" "unknown unit number(s) 9"
hasnt "an unknown unit never converges" "$out" "verdict: converged"

# Empty output is a shape error, never clean coverage.
fixture_repo; stub_reset
routes_review_empty
CROSSREV_REVIEW_PAYLOAD="$(printf '' | payload)"; export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
has "empty output is refused as never clean" "$out" "never clean coverage"
hasnt "empty output never converges" "$out" "verdict: converged"

# Malformed output is exit 1, not a silent loss.
fixture_repo; stub_reset
routes_review_empty
CROSSREV_REVIEW_PAYLOAD="$(printf 'not json' | payload)"; export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "malformed output fails the leg" "$rc" "1"
has "malformed output is refused as a schema mismatch" "$out" "does not match the schema"

# --- complete no-finding review --------------------------------------------

# A complete no-finding review at the fixture head converges on its first
# run: the run publishes its generations as git objects and the writer gate
# reads the current bytes back, the way production sees objects the run
# just wrote. The stub keeps every blob, tree, commit and ref it is handed
# for exactly this reason.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a complete clean review runs" "$rc" "0"
has "a complete clean review converges on its first run" "$out" "verdict: converged"
has "a complete clean review applies the converged label" "$(applied_labels)" "labels[]=crossrev/converged"
is "a pass creates no coverage comment" \
  "$(grep -c 'body=.*crossrev:c' "$CROSSREV_GH_LOG" || true)" "0"

# --- inaccessible, binary and deleted files --------------------------------

# An unreadable file stays required: could_not_review with the failed
# fallbacks in reason is accepted, published, and blocks green.
fixture_repo; stub_reset
routes_review_empty
# The default fixture has no vendor file; use a binary-shaped claim instead:
# evidence with a null span is file-level, which is the only span an access
# limit carries.
limit_evidence="$(jq -cn --arg sha "$FIX_HEAD" \
  '[{path:"app.ts", revision:$sha, start_line:null, end_line:null,
     source:"git", note:null}]')"
limit_cov="$(unit1 could_not_review '[]' "$limit_evidence" '"binary content could not be read, fallback search found nothing"')"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$limit_cov]" '[]' 'read app.ts at the head' '["app.ts is binary"]' | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
has "an unexaminable file records blocked, not converged" "$out" "verdict: blocked"
hasnt "an unexaminable file never applies the converged label" "$(applied_labels)" "labels[]=crossrev/converged"

# Deleted file: remove app.ts on the branch so the required set is a
# deletion read at the base. The reviewer answers not_affected with
# evidence and a reason, which is accepted but cannot converge without a
# current generation on the first run.
fixture_repo; stub_reset
git checkout -q feature
git rm -q app.ts
git commit -qm delete && git push -q origin feature
FIX_HEAD="$(git rev-parse feature)"
routes_review_empty
del_evidence="$(jq -cn --arg sha "$FIX_BASE" \
  '[{path:"app.ts", revision:$sha, start_line:null, end_line:null,
     source:"git", note:null}]')"
del_cov="$(unit1 not_affected '[]' "$del_evidence" '"deleted file needs no change"')"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$del_cov]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a deleted-file review runs" "$rc" "0"
has "a deleted-file review converges with its base evidence covered" "$out" "verdict: converged"

# Renames: rename app.ts so the required set carries a rename with the old
# path as base evidence. A complete no_issue answer over both units is
# accepted and converges, with the deletion read at the base.
fixture_repo; stub_reset
git checkout -q feature
git mv app.ts renamed.ts
git commit -qm rename && git push -q origin feature
FIX_HEAD="$(git rev-parse feature)"
routes_review_empty
ren_cov="$(jq -cn --arg sha "$FIX_HEAD" --arg base "$FIX_BASE" \
  '[{unit_number:1, verdict:"no_issue", finding_numbers:[],
     evidence:[{path:"app.ts", revision:$base, start_line:null, end_line:null,
       source:"git", note:null}], reason:null},
    {unit_number:2, verdict:"no_issue", finding_numbers:[],
     evidence:[{path:"renamed.ts", revision:$sha, start_line:null, end_line:null,
       source:"git", note:null}], reason:null}]')"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "$ren_cov" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a rename review runs" "$rc" "0"
has "a rename review converges with both units covered" "$out" "verdict: converged"

# --- base/head/engine invalidation ------------------------------------------

# A repair changing a previously clean file: after a clean first run, push
# a commit and re-drive. The old verdicts retire, the new head is
# reviewed, and the ledger holds a generation at the new revision.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
git checkout -q feature
printf 'export const ok = 1\nexport function more() { fetch("/m") }\n' >app.ts
git add -A && git commit -qm repair && git push -q origin feature
newhead="$(git rev-parse feature)"
[[ "$newhead" != "$FIX_HEAD" ]] && ok "a repair moves the head" "moved" "moved" \
  || notok "a repair moves the head" "a new head" "$newhead"
# Old generations name the old head, so the new head starts uncovered.
# Re-drive at the repair head: the retired verdicts mean the new head
# is reviewed from zero accepted units, and the ledger gains a generation
# at the new revision rather than converging on stale ones.
has "the old generation names the old revision" "$(cat "$GH_STATE"/blob-*)" "$FIX_HEAD"
hasnt "and names no generation at the repair head yet" "$(cat "$GH_STATE"/blob-*)" "$newhead"
FIX_HEAD="$newhead"
routes_review_empty
repoint_pr_view
repair_cov="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$repair_cov]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a repair re-drive runs" "$rc" "0"
has "a repair re-drive publishes at the new head" "$(cat "$GH_STATE"/blob-*)" "$newhead"

# --- advisory hits and too_common --------------------------------------------

# Advisory discovery never changes the required set: the fixture's changed
# lines name identifiers, but only required files take verdicts. A
# complete no_issue answer over the one required file is accepted.
fixture_repo; stub_reset
git checkout -q feature
printf 'export function SharedThing() {}\nexport const ok = 1\n' >app.ts
printf 'export function SharedThing() {}\n' >helper.ts
git add -A && git commit -qm advisory && git push -q origin feature
FIX_HEAD="$(git rev-parse feature)"
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
route '*Accept: application/vnd.github.diff*' 'diff --git a/app.ts b/app.ts
--- a/app.ts
+++ b/app.ts
@@ -1 +1,2 @@
 export const ok = 1
+export function SharedThing() {}'
# Two required files now (app.ts and helper.ts): answer both, one per unit.
# Advisory context itself takes no verdict: helper.ts is required here
# because it changed, and the reviewer judges it as a required file.
adv_cov="$(jq -cn --arg sha "$FIX_HEAD" \
  '[{unit_number:1, verdict:"no_issue", finding_numbers:[],
     evidence:[{path:"app.ts", revision:$sha, start_line:null, end_line:null,
       source:"git", note:null}], reason:null},
    {unit_number:2, verdict:"no_issue", finding_numbers:[],
     evidence:[{path:"helper.ts", revision:$sha, start_line:null, end_line:null,
       source:"git", note:null}], reason:null}]')"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "$adv_cov" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "an advisory-shaped review runs" "$rc" "0"
has "an advisory-shaped review converges on both required files" "$(applied_labels)" "labels[]=crossrev/converged"
has "advisory context adds no required unit" "$(cat "$GH_STATE"/blob-*)" '"required_count":2'

# --- input, review and ledger bounds ------------------------------------------

# A file that cannot fit alone in one rendered prompt halts with
# input_exceeds_budget only after the schedulable batches have run: the
# accepted batch (app.ts) persists first, so a re-drive resumes with just
# the oversized file outstanding rather than repeating work.
fixture_repo; stub_reset
git checkout -q feature
{ printf 'package huge\n'; yes '// filler line to exceed the prompt budget' | head -n 8000; } >huge.go
git add -A && git commit -qm huge && git push -q origin feature
FIX_HEAD="$(git rev-parse feature)"
routes_review_empty
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$(unit1 no_issue '[]' "$(evidence_file)")]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "an oversized file halts rather than converging" "$rc" "0"
has "the halt names the input budget" "$out" "input_exceeds_budget"
has "the halt applies the halted label" "$(applied_labels)" "labels[]=crossrev/halted"
has "the schedulable batch persisted before the halt" "$(cat "$GH_STATE"/blob-*)" '"gen":2'
has "the accepted batch left only the oversized file outstanding" "$(cat "$GH_STATE"/blob-*)" '"outstanding_count":1'

# --- unchanged-head restart ----------------------------------------------------

# An unchanged-head restart reuses the prior generation: the second run at
# the same revision invokes no batch and still reports invoked. Measured on
# ARGV_LOG, which the claude stub appends one line per harness invocation.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
calls_before="$(wc -l <"$ARGV_LOG" | tr -d ' ')"
replay_claim
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a restart at the unchanged head runs" "$rc" "0"
calls_after="$(wc -l <"$ARGV_LOG" | tr -d ' ')"
is "a restart reuses coverage without invoking the harness again" \
  "$calls_after" "$calls_before"
has "a restart converges on the resumed generation" "$out" "verdict: converged"
has "a restart applies the converged label" "$(applied_labels)" "labels[]=crossrev/converged"

# --- a no-commit resolve settle --------------------------------------------------

# A no-commit resolve settle with no coverage history keeps its legacy
# converged label: with no generation at this head no coverage pass ran
# here, so the frozen-path settle is unchanged. The outstanding-settle
# refusal is unit-pinned in internal/resolve/report_test.go
# (TestSettleWithOutstandingCoverageStaysAwaitingReview); here the binary
# proves the legacy label the pull request carries.
ID_D1="a1b2c3d4"
review_marker_v2() {
  jq -cn --arg sha "$FIX_HEAD" --arg a "$ID_D1" '
    {v:2, leg:"review", pass:1, state:"complete", ts:100, done_ts:200, run_id:"1",
     head_sha:$sha, harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     effort:null, endpoint:null, tokens:100, verdict:"issues-remain",
     findings:[
       {id:$a, path:"app.ts", line:2, side:"RIGHT", severity:"high", category:"correctness",
        pre_existing:false, title:"Unchecked fetch response", why:"w", fix:"check it",
        anchor:"", thread_id:"T_D1", resolution:null, tracked_as:null}]}'
}
settle_payload() {
  jq -cn '{blocked:false, blocked_reason:null, summary:"Not a bug on this pass.",
    resolutions:[{finding_number:1, resolution:"skipped", reply:"no",
      persist:null, duplicate_of:null}]}'
}
fixture_repo; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker_v2)" | jq -cs . | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9002}'
route '*reviewThreads*' "$(threads_response "$(thread_node T_D1 app.ts 2 false "$ID_D1")")"
route '*resolveReviewThread*' '{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}'
route 'api --method POST repos/*/pulls/42/comments/*/replies*' '{"id":6001}'
CROSSREV_RESOLVE_PAYLOAD="$(settle_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?
is "a no-commit settle runs" "$rc" "0"
# With no coverage generation at this head no coverage pass ran here, so
# the frozen-path settle keeps its legacy converged label.
has "a settle with no coverage history keeps its legacy label" "$(applied_labels)" "labels[]=crossrev/converged"

# --- persistence ---------------------------------------------------------------

# Lost objects: the commit the marker names no longer reads — a mirror
# push deleted the ref and the objects were collected. The re-drive must
# not converge on the missing generation; it re-reviews from zero, does
# the work again, and converges on its own fresh coverage.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
# The run published its generations as git objects; drop the commits and
# the ref behind the marker's handle, so nothing it names reads anymore.
has "a full publish leaves a manifest behind" "$(cat "$GH_STATE"/blob-*)" '"kind":"manifest"'
replay_claim
rm -f "$GH_STATE"/commit-* "$GH_STATE"/ref-*
calls_before="$(wc -l <"$ARGV_LOG" | tr -d ' ')"
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a lost-ledger re-drive runs" "$rc" "0"
has "a lost ledger re-reviews and converges on fresh coverage" "$out" "verdict: converged"
calls_after="$(wc -l <"$ARGV_LOG" | tr -d ' ')"
is "a lost ledger does the work again rather than trusting the missing bytes" \
  "$(( calls_after > calls_before ))" "1"
has "a lost-ledger re-drive republishes the ref" "$(ls "$GH_STATE"/ref-* 2>/dev/null)" "ref-"

# Missing object: delete the tip generation's manifest blob and re-drive at
# the unchanged head. The generation no longer reads, so the re-drive
# re-reviews from zero and converges on fresh coverage.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
replay_claim
rm -f "$(grep -l '"gen":2' "$GH_STATE"/blob-* | head -n 1)"
calls_before="$(wc -l <"$ARGV_LOG" | tr -d ' ')"
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a missing-object re-drive runs" "$rc" "0"
has "a missing object re-reviews and converges on fresh coverage" "$out" "verdict: converged"
calls_after="$(wc -l <"$ARGV_LOG" | tr -d ' ')"
is "a missing object does the work again rather than trusting the missing bytes" \
  "$(( calls_after > calls_before ))" "1"

# Altered records: rewrite one records blob under the manifest's digest and
# re-drive. The generation must refuse rather than converge.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
for records in $(grep -l '"kind":"records"' "$GH_STATE"/blob-*); do
  flip_first '"kind":"records"' '"kind":"recordsX"' "$records"
done
replay_claim
mark="$(wc -l <"$GH_LOG" | tr -d ' ')"
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
hasnt "altered records never converge" "$(labels_since "$mark")" "labels[]=crossrev/converged"

# Altered manifest: flip the generation number in the manifest blob without
# re-digesting, and re-drive. The digests disagree, so the generation must
# refuse rather than converge.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
manifest="$(grep -l '"gen":2' "$GH_STATE"/blob-* | head -n 1)"
flip_first '"gen":2' '"gen":7' "$manifest"
replay_claim
mark="$(wc -l <"$GH_LOG" | tr -d ' ')"
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
hasnt "an altered manifest never converges" "$(labels_since "$mark")" "labels[]=crossrev/converged"

# Strict store-read failure: the generation read fails, and the leg must
# refuse rather than answer an empty ledger. A transient failure is not
# absence: answering "no coverage" to an API error would let a network
# blip retire real work.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
replay_claim
route_first 'api repos/*/git/commits/*' '!fail'
mark="$(wc -l <"$GH_LOG" | tr -d ' ')"
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "an unreadable store fails the leg" "$rc" "1"
hasnt "an unreadable store never converges" "$(labels_since "$mark")" "labels[]=crossrev/converged"

# Untrusted author: the claim marker carries another author's login, so its
# handle contributes nothing. The re-drive reviews from zero — the git
# objects are unread without a trusted marker naming them — and converges
# on its own fresh coverage.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
replay_claim_as started "someone-else"
fresh1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$fresh1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
calls_before="$(wc -l <"$ARGV_LOG" | tr -d ' ')"
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "an untrusted-marker re-drive runs" "$rc" "0"
has "an untrusted marker re-reviews and converges on fresh coverage" "$out" "verdict: converged"
calls_after="$(wc -l <"$ARGV_LOG" | tr -d ' ')"
is "an untrusted marker does the work again rather than trusting its bytes" \
  "$(( calls_after > calls_before ))" "1"

# Two runs at the unchanged head reconcile to one current generation: the
# second run resumes the first run's checkpoint instead of reviewing again.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
replay_claim
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "two writers at one generation still run" "$rc" "0"
has "two writers reconcile to one current generation" "$out" "verdict: converged"

# Head movement during publication: push a commit mid-run is emulated by
# moving the head between the ledger read and the verdict. The re-drive at
# the new head starts uncovered rather than publishing stale coverage.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
git checkout -q feature
printf 'export const ok = 1\nexport function late() { fetch("/late") }\n' >app.ts
git add -A && git commit -qm late && git push -q origin feature
moved="$(git rev-parse feature)"
[[ "$moved" != "$FIX_HEAD" ]] && ok "head movement retires the candidate" "moved" "moved" \
  || notok "head movement retires the candidate" "a new head" "$moved"
# Re-drive at the moved head: retired verdicts mean the new head is
# reviewed from zero accepted units, and the ledger gains a generation
# there instead of converging on the old one.
FIX_HEAD="$moved"
routes_review_empty
repoint_pr_view
late_cov="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$late_cov]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a moved-head re-drive runs" "$rc" "0"
has "a moved-head re-drive publishes at the new head" "$(cat "$GH_STATE"/blob-*)" "$moved"

# --- mutation red proof ----------------------------------------------------------

# Removing one obligation turns the suite red: flip one expected byte in a
# copy of the convergence fixture and assert the predicate disagrees. The
# literal all-clear input converges; the same input with ledger_current
# false must not. A production predicate that converged on a stale ledger
# would fail this case.
fixture_repo; stub_reset
routes_review_empty
stale_cov="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$stale_cov]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
# Corrupt the stored ledger: flip one digest byte in the tip generation's
# records blob, so the only current generation refuses and the re-drive
# cannot converge on stale bytes.
for records in $(grep -l '"kind":"records"' "$GH_STATE"/blob-*); do
  flip_first '"kind":"records"' '"kind":"recordsX"' "$records"
done
replay_claim
mark="$(wc -l <"$GH_LOG" | tr -d ' ')"
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
hasnt "a corrupted ledger never converges on stale bytes" "$(labels_since "$mark")" "labels[]=crossrev/converged"

# Bypassing a convergence route turns the suite red (route 1, the review
# writer gate): a converged verdict with one file unexaminable must record
# blocked with the debt named and apply no converged label. The first run
# publishes the could_not_review generation; the restart at the unchanged
# head rebuilds convergence from the current bytes and reports the debt.
fixture_repo; stub_reset
routes_review_empty
gate_evidence="$(evidence_file)"
unexamined="$(unit1 could_not_review '[]' "$gate_evidence" '"binary content could not be read, fallback search found nothing"')"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$unexamined]" '[]' 'read app.ts at the head' '["app.ts is binary"]' | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
replay_blocked_claim
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "the writer gate re-drive runs" "$rc" "0"
has "the writer gate downgrades an unexaminable converged verdict" "$out" "could not be examined"
hasnt "the writer gate applies no converged label past the debt" "$(applied_labels)" "labels[]=crossrev/converged"

# --- red proof ---------------------------------------------------------------
#
# The CLI always publishes coverage, so no CLI path reaches "converged with
# no generation": the red proof is the writer gate above (an unexaminable
# file records blocked with the debt named), the corrupted ledger below
# (the only generation refuses, so the re-drive blocks), and the Go
# oracles, which pin the same gate per package:
# TestReviewIntelligenceAcceptanceOracle replays the frozen discovery
# vectors, TestLedgerAcceptanceOracle the ledger vectors, and
# TestConvergenceAcceptanceOracle the predicate vectors, each with a
# mutation case proving the gate turns red when one obligation is removed.
# The pre-slice binary converges with no coverage marker anywhere; this
# binary cannot produce that shape, which is the point of the slice.

finish
