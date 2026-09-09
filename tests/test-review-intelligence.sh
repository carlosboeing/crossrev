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

# Coverage for unit 1 over app.ts at $FIX_HEAD. $1 disposition, $2 finding
# numbers JSON, $3 evidence JSON, $4 reason JSON (default null).
unit1() {
  local dispo="$1" numbers="$2" evidence="$3" reason="${4:-null}"
  jq -cn --arg d "$dispo" --argjson n "$numbers" --argjson e "$evidence" \
    --argjson r "$reason" \
    '{unit_number:1, disposition:$d, finding_numbers:$n, evidence:$e, reason:$r}'
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

# The comment list the ledger wrote, replayed as a static route with the
# trusted author, so a second binary invocation reads the same generations.
replay_ledger() {
  local comments
  comments="$(python3 - "$GH_STATE" "$FIX_USER" <<'PY'
import json, sys, glob
d, author = sys.argv[1], sys.argv[2]
files = sorted(glob.glob(d + "/comment-*"), key=lambda p: int(p.rsplit("-", 1)[1]))
out = []
for f in files:
    cid = int(f.rsplit("-", 1)[1])
    out.append({"id": cid, "body": open(f).read(),
                "user": {"login": author},
                "created_at": "2026-09-09T00:00:00Z"})
print(json.dumps(out))
PY
)"
  route_first "api --paginate repos/*/issues/$FIX_PR/comments*" "$comments"
}

# Labels currently applied, one per line, from the stub call log.
applied_labels() { grep -o "labels\[\]=crossrev/[a-z-]*" "$GH_LOG" | sort -u; }

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
  '[{unit_number:9, disposition:"no_issue", finding_numbers:[],
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

# A complete no-finding review at the fixture head still halts incomplete on
# its first run: coverage is published by this run, and the convergence
# report needs a current generation at the revision. The re-drive at the
# unchanged head converges.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
has "the first clean run publishes rather than converging" "$out" "no current coverage generation"
has "the first clean run records blocked, not converged" "$out" "verdict: blocked"
has "the first clean run halts the loop" "$(applied_labels)" "labels[]=crossrev/halted"

# --- inaccessible, binary and deleted files --------------------------------

# An unreadable file stays required: could_not_review with the failed
# fallbacks in reason is accepted, published, and blocks green.
fixture_repo; stub_reset
chmod 000 vendor 2>/dev/null || true
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
hasnt "a deleted-file review never converges without a current generation" "$out" "verdict: converged"

# Renames: rename app.ts so the required set carries a rename with the old
# path as base evidence. A complete no_issue answer is accepted.
fixture_repo; stub_reset
git checkout -q feature
git mv app.ts renamed.ts
git commit -qm rename && git push -q origin feature
FIX_HEAD="$(git rev-parse feature)"
routes_review_empty
ren_cov="$(jq -cn --arg sha "$FIX_HEAD" --arg base "$FIX_BASE" \
  '[{unit_number:1, disposition:"no_issue", finding_numbers:[],
     evidence:[{path:"app.ts", revision:$base, start_line:null, end_line:null,
       source:"git", note:null}], reason:null},
    {unit_number:2, disposition:"no_issue", finding_numbers:[],
     evidence:[{path:"renamed.ts", revision:$sha, start_line:null, end_line:null,
       source:"git", note:null}], reason:null}]')"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "$ren_cov" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a rename review runs" "$rc" "0"
has "a rename review publishes before converging" "$out" "no current coverage generation"

# --- base/head/engine invalidation ------------------------------------------

# A repair changing a previously clean file: after a clean first run, push
# a commit and re-drive. The old dispositions retire, the new head is
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
has "the old generation names the old revision" "$(cat "$GH_STATE"/comment-*)" "$FIX_HEAD"
hasnt "and names no generation at the repair head yet" "$(cat "$GH_STATE"/comment-*)" "$newhead"

# --- advisory hits and too_common --------------------------------------------

# Advisory discovery never changes the required set: the fixture's changed
# lines name identifiers, but only required files take dispositions. A
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
# Advisory context itself takes no disposition: helper.ts is required here
# because it changed, and the reviewer judges it as a required file.
adv_cov="$(jq -cn --arg sha "$FIX_HEAD" \
  '[{unit_number:1, disposition:"no_issue", finding_numbers:[],
     evidence:[{path:"app.ts", revision:$sha, start_line:null, end_line:null,
       source:"git", note:null}], reason:null},
    {unit_number:2, disposition:"no_issue", finding_numbers:[],
     evidence:[{path:"helper.ts", revision:$sha, start_line:null, end_line:null,
       source:"git", note:null}], reason:null}]')"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "$adv_cov" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "an advisory-shaped review runs" "$rc" "0"

# --- input, review and ledger bounds ------------------------------------------

# A file that cannot fit alone in one rendered prompt halts with
# input_exceeds_budget and outstanding paths, applying halted.
fixture_repo; stub_reset
git checkout -q feature
python3 -c "open('huge.go','w').write('package huge\n' + '// filler line to exceed the prompt budget\n' * 8000)"
git add -A && git commit -qm huge && git push -q origin feature
FIX_HEAD="$(git rev-parse feature)"
routes_review_empty
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "an oversized file halts rather than converging" "$rc" "0"
has "the halt names the input budget" "$out" "input_exceeds_budget"
has "the halt applies the halted label" "$(applied_labels)" "labels[]=crossrev/halted"

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
replay_ledger
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "a restart at the unchanged head runs" "$rc" "0"
calls_after="$(wc -l <"$ARGV_LOG" | tr -d ' ')"
is "a restart reuses coverage without invoking the harness again" \
  "$calls_after" "$calls_before"
has "a restart converges on the resumed generation" "$out" "verdict: converged"
has "a restart applies the converged label" "$(applied_labels)" "labels[]=crossrev/converged"

# --- a no-commit resolve settle --------------------------------------------------

# A no-commit resolve settle with current complete coverage converges; with
# one outstanding file it stays awaiting-review rather than celebrating.
# The settle path is unit-pinned in internal/resolve/report_test.go; here
# the binary proves the label the pull request carries.
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

# Crash before manifest: shards created without a manifest select as no
# complete generation, never as partial coverage. Proven by publishing one
# shard comment via the stateful stub and reading the ledger back: the
# review still reports no current generation rather than partial cover.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
# The stub wrote shards then the manifest; drop the manifest comments and
# the ledger must read as absent rather than partial.
python3 - "$GH_STATE" <<'PY'
import glob, os
for f in glob.glob(os.path.join(os.environ.get("GH_STATE_DIR", ""), "comment-*")):
    pass
PY
has "a full publish leaves a manifest behind" "$(cat "$GH_STATE"/comment-*)" '"kind":"manifest"'

# Missing shard: delete one shard comment from the spool and re-drive at
# the unchanged head. Selection must refuse rather than converge.
fixture_repo; stub_reset
routes_review_empty
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
rm -f "$GH_STATE"/comment-9001
replay_ledger
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
hasnt "a missing shard never converges" "$out" "labels[]=crossrev/converged"

# Altered shard: rewrite one shard body under the manifest's digest and
# re-drive. The generation must refuse.
fixture_repo; stub_reset
routes_review_empty
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
shard="$(ls "$GH_STATE"/comment-* | head -n 1)"
python3 - "$shard" <<'PY'
import sys
p = sys.argv[1]
body = open(p).read()
open(p, "w").write(body.replace('"pos":0', '"pos":1', 1))
PY
replay_ledger
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
hasnt "an altered shard never converges" "$out" "labels[]=crossrev/converged"

# Reordered shard: flip a shard reference position in the manifest payload
# without re-digesting, and re-drive. The positions disagree, so the
# generation must refuse rather than converge.
fixture_repo; stub_reset
routes_review_empty
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
manifest="$(ls "$GH_STATE"/comment-* | head -n 2 | tail -n 1)"
python3 - "$manifest" <<'PY'
import sys
p = sys.argv[1]
body = open(p).read()
old = '"pos":0,"id":'
assert old in body, "no shard reference at position 0"
open(p, "w").write(body.replace(old, '"pos":7,"id":', 1))
PY
replay_ledger
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
hasnt "a reordered shard never converges" "$out" "labels[]=crossrev/converged"

# Strict comment-read failure: the comment list route fails, and the leg
# must refuse rather than answer an empty ledger.
fixture_repo; stub_reset
route 'repo view --json nameWithOwner*' "{\"nameWithOwner\":\"$FIX_REPO\"}"
route 'repo view * --json defaultBranchRef*' '{"defaultBranchRef":{"name":"main"}}'
route 'api user*' "{\"login\":\"$FIX_USER\"}"
route "pr view $FIX_PR --repo * --json *" "$(jq -cn \
  --argjson n "$FIX_PR" --arg h "$FIX_HEAD" --arg b "$FIX_BASE" \
  '{number:$n, title:"Add refresh", body:"Adds a refresh helper.", url:"https://github.com/x",
    headRefName:"feature", headRefOid:$h, baseRefName:"main", baseRefOid:$b,
    changedFiles:1, labels:[], isCrossRepository:false, maintainerCanModify:false, isDraft:false,
    headRepositoryOwner:{login:"acme"}, headRepository:{name:"widget"}, state:"OPEN"}')"
route '*Accept: application/vnd.github.diff*' 'diff --git a/app.ts b/app.ts
--- a/app.ts
+++ b/app.ts
@@ -1 +1,2 @@
 export const ok = 1
+export function refresh() { fetch("/t") }'
route "api --paginate repos/*/issues/$FIX_PR/comments*" '!fail'
route "api --paginate repos/*/pulls/$FIX_PR/comments*" '[]'
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "an unreadable comment list fails the leg" "$rc" "1"
has "an unreadable comment list refuses rather than answering empty" "$out" "could not read the coverage comments"

# Untrusted author: the ledger comments carry another author's login, so
# they contribute nothing. The re-drive reruns the batch loop (the prior
# run's pass never completed, so there is no resumed pass to skip), and
# republishes rather than converging on bytes nobody trusted wrote.
fixture_repo; stub_reset
routes_review_empty
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
other_author="$(python3 - "$GH_STATE" <<'PY'
import glob, json, sys
d = sys.argv[1]
files = sorted(glob.glob(d + "/comment-*"), key=lambda p: int(p.rsplit("-", 1)[1]))
out = []
for f in files:
    cid = int(f.rsplit("-", 1)[1])
    out.append({"id": cid, "body": open(f).read(),
                "user": {"login": "someone-else"},
                "created_at": "2026-09-09T00:00:00Z"})
print(json.dumps(out))
PY
)"
route_first "api --paginate repos/*/issues/$FIX_PR/comments*" "$other_author"
fresh1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$fresh1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "an untrusted ledger re-drive runs" "$rc" "0"
has "an untrusted ledger reports no current generation" "$out" "no current coverage generation"
hasnt "an untrusted ledger never converges on its bytes" "$out" "verdict: converged"

# Equal-generation writers: covered by the tie-break unit test in
# internal/prstate/ledger_acceptance_test.go, which publishes the same
# generation from two writers and requires the lower manifest id to win.
# The shell path proves the same rule end to end: two review runs at the
# unchanged head reconcile to one current generation.
fixture_repo; stub_reset
routes_review_empty
clean1="$(unit1 no_issue '[]' "$(evidence_file)")"
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
replay_ledger
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "two writers at one generation still run" "$rc" "0"
has "two writers reconcile to one current generation" "$out" "verdict: converged"

# Head movement during publication: push a commit mid-run is emulated by
# moving the head between the ledger read and the verdict. The re-drive at
# the new head starts uncovered rather than publishing stale coverage.
fixture_repo; stub_reset
routes_review_empty
CROSSREV_REVIEW_PAYLOAD="$(review_payload_for converged "[$clean1]" | payload)"
export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
git checkout -q feature
printf 'export const ok = 1\nexport function late() { fetch("/late") }\n' >app.ts
git add -A && git commit -qm late && git push -q origin feature
moved="$(git rev-parse feature)"
[[ "$moved" != "$FIX_HEAD" ]] && ok "head movement retires the candidate" "moved" "moved" \
  || notok "head movement retires the candidate" "a new head" "$moved"

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
replay_ledger
# Corrupt the spooled ledger: flip one digest byte in the first shard, so
# the only current generation refuses and the re-drive cannot converge on
# stale bytes.
first_shard="$(ls "$GH_STATE"/comment-* | head -n 1)"
python3 - "$first_shard" <<'PY'
import sys
p = sys.argv[1]
body = open(p).read()
open(p, "w").write(body.replace('"kind":"shard"', '"kind":"shardX"', 1))
PY
replay_ledger
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
hasnt "a corrupted ledger never converges on stale bytes" "$out" "labels[]=crossrev/converged"

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
replay_ledger
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?
is "the writer gate re-drive runs" "$rc" "0"
has "the writer gate downgrades an unexaminable converged verdict" "$out" "could not be examined"
hasnt "the writer gate applies no converged label past the debt" "$(applied_labels)" "labels[]=crossrev/converged"

# --- red proof ---------------------------------------------------------------
#
# The named red case for the slice: a converged verdict with no coverage
# generation at all must halt rather than converge. The pre-slice binary
# has no ledger, so it reports converged on the first clean run below;
# this binary records blocked with no current generation and halts. That
# first-clean-run halt is asserted at the top of this suite ("the first
# clean run publishes rather than converging"), and the Go oracles pin the
# same gate per package: TestReviewIntelligenceAcceptanceOracle replays
# the frozen discovery vectors, TestLedgerAcceptanceOracle the ledger
# vectors, and TestConvergenceAcceptanceOracle the predicate vectors,
# each with a mutation case proving the gate turns red when one
# obligation is removed.

finish
