#!/usr/bin/env bash
#
# Retry recovery, crash boundaries, stale claims and the local lock.
#
# The watchdog retries, so a leg must survive being run twice. GitHub's
# concurrency groups cancel older runs; they do not make external writes atomic.
# Every boundary the design enumerates gets a fixture here, because "it probably
# reconciles" is not a property.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

REVIEW_PAYLOAD='{"verdict":"issues-remain","blocked_reason":null,"prior":null,"findings":[
  {"path":"app.ts","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,
   "title":"Unchecked fetch response","why":"A failed request looks like a success","fix":"Check response.ok"},
  {"path":"app.ts","line":2,"side":"RIGHT","severity":"low","category":"maintainability","pre_existing":false,
   "title":"Missing return type","why":"The inferred type is wider than intended","fix":"Annotate it"}
]}'

# The two ids revloop will derive, so the fixtures can pretend one already landed.
id_of() {
  # shellcheck source=../lib/state.sh
  ( source "$HERE/../lib/ui.sh"; source "$HERE/../lib/state.sh"
    state_finding_id "$1" "$2" "$(cd "$3" && state_anchor app.ts 2)" )
}

# A claim against the CURRENT fixture's head.
#
# Rebuilt per case rather than once: each fixture_repo is a new repository with a
# new head SHA, and a claim carrying someone else's head is stale by definition —
# which would make every case below test staleness instead of what it names.
make_claim() {
  jq -cn --arg sha "${1:-$FIX_HEAD}" --arg id "$first_id" --argjson ts "${2:-$(date +%s)}" '
    {v:1, leg:"review", pass:1, state:"started", ts:$ts, run_id:"1", head_sha:$sha,
     harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     verdict:"issues-remain",
     findings:[{id:$id, path:"app.ts", line:2, side:"RIGHT", severity:"high", category:"correctness", pre_existing:false,
                title:"Unchecked fetch response", why:"w", fix:"f", anchor:"",
                thread_id:null, disposition:null, tracked_as:null},
               {id:"other000", path:"app.ts", line:2, side:"RIGHT", severity:"low", category:"maintainability", pre_existing:false,
                title:"Missing return type", why:"w", fix:"f", anchor:"",
                thread_id:null, disposition:null, tracked_as:null}]}'
}

# --- a clean first pass ----------------------------------------------------
fixture_repo; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
REVLOOP_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export REVLOOP_REVIEW_PAYLOAD
out="$("$REVLOOP" review --pr 42 2>&1)"; rc=$?

is  "a first pass exits clean"                        "$rc" "0"
has "it reports the verdict"                          "$out" "verdict: issues-remain"
has "it counts the severities it found"               "$out" "1 high, 0 medium, 1 low, of which 0 pre-existing"
is  "it posts one inline comment per finding"         "$(count 'method POST repos/acme/widget/pulls/42/comments')" "2"
is  "it posts exactly one overall comment"            "$(count 'method POST repos/acme/widget/issues/42/comments')" "1"
has "the claim is edited to complete, not re-posted"  "$(calls)" "PATCH repos/acme/widget/issues/comments/9001"
has "it applies the pass label"                       "$(calls)" "labels[]=revloop/pass-1"
has "it hands the loop to the resolve leg"            "$(calls)" "labels[]=revloop/awaiting-resolution"

# Every inline comment carries its own finding id, which is what makes recovery
# exact rather than approximate.
is  "each inline comment carries a finding marker" \
  "$(grep -c 'revloop:f' "$GH_LOG")" "2"

# --- crash boundary: between two inline comments ---------------------------
#
# One finding already landed. Recovery must post the other and only the other.
fixture_repo; stub_reset
first_id="$(id_of app.ts "Unchecked fetch response" "$FIX_DIR")"
claim="$(make_claim)"
routes_baseline "$(marker_comment 9001 "$claim" | jq -cs . | payload)"
landed="$(posted_comments "$first_id")"
route_first "api --paginate repos/*/pulls/$FIX_PR/comments*" "$landed"
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
out="$("$REVLOOP" review --pr 42 2>&1)"; rc=$?

is  "recovery exits clean"                            "$rc" "0"
has "recovery says what it is resuming"               "$out" "Resuming pass 1"
has "recovery does not re-run the model"              "$out" "already recorded its findings, so the review is not run again"
is  "the already-posted finding is not posted twice"  "$(count 'method POST repos/acme/widget/pulls/42/comments')" "1"
has "and it says so rather than silently skipping"    "$out" "already on the pull request from an earlier attempt"

# --- crash boundary: between the last inline comment and the overall comment ---
fixture_repo; stub_reset
claim="$(make_claim)"
routes_baseline "$(marker_comment 9001 "$claim" | jq -cs . | payload)"
landed="$(posted_comments "$first_id" other000)"
route_first "api --paginate repos/*/pulls/$FIX_PR/comments*" "$landed"
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
out="$("$REVLOOP" review --pr 42 2>&1)"; rc=$?

is  "with every inline comment landed, recovery posts none again" \
  "$(count 'method POST repos/acme/widget/pulls/42/comments')" "0"
has "and it still writes the overall comment"         "$(calls)" "PATCH repos/acme/widget/issues/comments/9001"
is  "and it exits clean"                              "$rc" "0"

# --- crash boundary: between the overall comment and the completion edit ---
#
# The two are separate writes on purpose, so this boundary exists to be crossed.
# Recovery flips the state and nothing else.
is  "the overall comment and the completion edit are two writes" \
  "$(count 'PATCH repos/acme/widget/issues/comments/9001')" "2"

# --- a stale claim is abandoned, not resumed -------------------------------
fixture_repo; stub_reset
old="$(make_claim "$FIX_HEAD" "$(( $(date +%s) - 7200 ))")"
routes_baseline "$(marker_comment 9001 "$old" | jq -cs . | payload)"
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
REVLOOP_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export REVLOOP_REVIEW_PAYLOAD
out="$("$REVLOOP" review --pr 42 2>&1)"; rc=$?
has "a claim past its window is abandoned"            "$out" "abandoning the unfinished pass-1 review claim"
has "and the reason names the window"                 "$out" "past the 60-minute window"
is  "abandoning still exits clean"                    "$rc" "0"

# A claim whose head_sha no longer matches is stale for a different reason: the
# pull request moved on underneath it.
fixture_repo; stub_reset
moved="$(make_claim 0000000000000000000000000000000000000000)"
routes_baseline "$(marker_comment 9001 "$moved" | jq -cs . | payload)"
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
out="$("$REVLOOP" review --pr 42 2>&1)"; rc=$?
has "a claim against a moved head is abandoned"       "$out" "and the pull request is now at"

# --- status reports interruption, not just position ------------------------
fixture_repo; stub_reset
claim="$(make_claim)"
routes_baseline "$(marker_comment 9001 "$claim" | jq -cs . | payload)"
out="$("$REVLOOP" status --pr 42 2>&1)"
has "status names the interrupted leg"                "$out" "review — interrupted mid-flight"
has "status names the command that resumes it"        "$out" "revloop review --pr 42"
has "status says which side is not resumable work"    "$out" "resolve — has not run this pass"

# --- the local lock -------------------------------------------------------
fixture_repo; stub_reset
mkdir -p "$(git rev-parse --git-dir)/revloop"
printf '%s on other since now\n' "$$" >"$(git rev-parse --git-dir)/revloop/pr-42.lock"
routes_baseline "$(printf '[]' | payload)"
err="$("$REVLOOP" review --pr 42 2>&1 >/dev/null)"; rc=$?
is  "a second run against the same PR refuses"        "$rc" "1"
has "and it names the process holding the lock"       "$err" "already holds pull request 42 — $$"

# A lock left by a process that is gone is taken over rather than obeyed.
printf '999999 on other since then\n' >"$(git rev-parse --git-dir)/revloop/pr-42.lock"
stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
REVLOOP_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export REVLOOP_REVIEW_PAYLOAD
out="$("$REVLOOP" review --pr 42 2>&1)"; rc=$?
is  "a dead holder's lock is taken over"              "$rc" "0"
has "and taking it over is announced"                 "$out" "no longer running"

# --- the same revision is not reviewed twice ------------------------------
fixture_repo; stub_reset
done_marker="$(make_claim | jq -c '.state = "complete"')"
routes_baseline "$(marker_comment 9001 "$done_marker" | jq -cs . | payload)"
out="$("$REVLOOP" review --pr 42 2>&1)"; rc=$?
is  "a reviewed revision is a no-op"                  "$rc" "0"
has "and it says why rather than looking busy"        "$out" "nothing has changed since"
is  "a no-op writes nothing"                          "$(count 'method POST')" "0"

finish
