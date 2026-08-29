#!/usr/bin/env bash
#
# Termination and guard tests.
#
# A loop that stops one pass early looks exactly like a loop that converged, and
# a push guard that lets one case through is not a guard. The decisions are pure
# functions over state the orchestrator already holds; the guard cases at the
# bottom run the legs themselves against the stubbed boundary, because a
# predicate nothing calls is not a guard either. Neither needs a network.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=harness.sh
source "$HERE/harness.sh"
# shellcheck source=../lib/ui.sh
source "$HERE/../lib/ui.sh"
# shellcheck source=../lib/legs.sh
source "$HERE/../lib/legs.sh"

# verdict pass max stop blocked runs cap files maxfiles -> first word of decision
decides() {
  local want="$1" desc="$2"; shift 2
  local got; got="$(legs_should_continue "$@" | cut -d' ' -f1)"
  [[ "$got" == "$want" ]] && ok "$desc" || notok "$desc" "$want" "$got"
}

# [policy-table: legs_should_continue]
decides continue "issues remain below every cap"        issues-remain 1 3 false false 0 12 10 200
decides converged "converged stops the loop"            converged     1 3 false false 0 12 10 200
decides halt     "crossrev/stop outranks a healthy verdict" converged  1 3 true  false 0 12 10 200
decides halt     "the resolver reporting blocked halts"  issues-remain 1 3 false true  0 12 10 200

# The boundary is the thing worth testing: pass 3 of max 3 is the last pass, not
# the one after which a fourth begins.
decides continue "one below the pass cap continues"     issues-remain 2 3 false false 0 12 10 200
decides halt     "exactly at the pass cap halts"        issues-remain 3 3 false false 0 12 10 200
decides halt     "beyond the pass cap halts"            issues-remain 4 3 false false 0 12 10 200

decides continue "one below the daily cap continues"    issues-remain 1 3 false false 11 12 10 200
decides halt     "exactly at the daily cap halts"       issues-remain 1 3 false false 12 12 10 200
is "the daily halt names the other pull requests already reviewed" \
  "$(legs_should_continue issues-remain 1 3 false false 12 12 10 200)" \
  "halt reached max_prs_per_day (12) — 12 other pull requests were already reviewed in the last 24 hours"

decides continue "exactly at the file cap still runs"   issues-remain 1 3 false false 0 12 200 200
decides halt     "above the file cap halts"             issues-remain 1 3 false false 0 12 201 200
decides continue "a file cap of zero is no cap"         issues-remain 1 3 false false 0 12 9999 0
# [policy-table-end: legs_should_continue]

# --- the fixing threshold --------------------------------------------------
fixes() {
  local want="$1" desc="$2"; shift 2
  local got; if legs_should_fix "$@"; then got=yes; else got=no; fi
  [[ "$got" == "$want" ]] && ok "$desc" || notok "$desc" "$want" "$got"
}

fixes yes "at the threshold, the resolver may act"        medium medium false
fixes yes "above it too"                                  high   medium false
fixes no  "below it, the finding is reported and left"    low    medium false
fixes yes "min_fix_severity low takes everything"         low    low    false
fixes no  "min_fix_severity high takes only the top rung" medium high   false

# The one guardrail that is not configurable: provenance outranks severity, so a
# critical pre-existing hole is reported and filed rather than fixed here.
fixes no  "a pre-existing finding is never fixed"         high   low    true
fixes no  "even with the threshold at its lowest"         medium low    true

# Both unknown-value cases fail closed. Guessing here writes a commit nobody
# asked for, while refusing leaves a finding on the pull request for a human.
fixes no  "an unrecognised severity meets no threshold"   important medium false
fixes no  "an unrecognised threshold fixes nothing"       high      urgent false

# --- the label a leg waits behind ------------------------------------------
#
# The leg is the verb and the label is the noun, so this is a mapping rather
# than string concatenation. A mismatch here stalls the chain in silence: the
# label sits on the pull request with no workflow listening for it.
label() {
  local got; got="$(legs_awaiting_label "$1")"
  [[ "$got" == "$2" ]] && ok "$3" || notok "$3" "$2" "$got"
}
label review  crossrev/awaiting-review     "review waits behind awaiting-review"
label resolve crossrev/awaiting-resolution "resolve waits behind awaiting-resolution, not awaiting-resolve"

# --- push guard ------------------------------------------------------------
guard() {
  local want="$1" desc="$2"; shift 2
  if ( legs_assert_push_target "$@" ) >/dev/null 2>&1; then got=allow; else got=refuse; fi
  [[ "$got" == "$want" ]] && ok "$desc" || notok "$desc" "$want" "$got"
}
guard allow  "pushes when the tree is at the PR head revision"    sha1 sha1 feat/x main o/r o/r
guard refuse "refuses when the tree is at another revision"       sha2 sha1 feat/x main o/r o/r
guard refuse "refuses when the head branch is the default"        sha1 sha1 main   main o/r o/r
guard refuse "refuses when the head repo is not the origin"       sha1 sha1 feat/x main fork/r o/r

# --- the URL the guard is handed --------------------------------------------
#
# Everything above is only as good as the slug it compares. Testing for the
# substring "github.com" accepts a neighbouring host and a local path that
# happens to contain the name, and each of those is a way past the guard.
slug() {
  local want="$1" desc="$2" url="$3" got
  got="$(legs_github_slug "$url" 2>/dev/null)" || got=refused
  [[ "$got" == "$want" ]] && ok "$desc" || notok "$desc" "$want" "$got"
}
slug o/r     "an https remote"                        "https://github.com/o/r.git"
slug o/r     "an https remote with no .git suffix"    "https://github.com/o/r"
slug o/r     "an scp-style ssh remote"                "git@github.com:o/r.git"
slug o/r     "an ssh:// remote carrying a port"       "ssh://git@github.com:22/o/r.git"
slug o/r     "a git:// remote"                        "git://github.com/o/r.git"
slug o/r     "a host spelled with capitals"           "https://GitHub.com/o/r.git"
slug refused "a host that merely ends in the name"    "https://notgithub.com/o/r.git"
slug refused "a host that merely starts with it"      "https://github.com.example.net/o/r.git"
slug refused "a local path holding the host name"     "/tmp/w/github.com/o/r.git"
slug refused "an https remote on another host"        "https://git.example.com/o/r.git"
slug refused "a plain local path"                     "/tmp/w/origin.git"
slug refused "a path deeper than owner/repo"          "https://github.com/o/r/x.git"
# A host that is not github.com and folds into it. BSD tr under a UTF-8 locale
# folds multibyte letters, and U+0130 becomes ASCII i, so an unpinned compare
# accepted this and answered with a slug. The compare pins LC_ALL=C for the same
# reason state_finding_id does.
slug refused "a host spelled with a folding homoglyph" "$(printf 'https://G\xc4\xb0THUB.COM/o/r.git')"

# --- push-target URL resolution and rewrite warning ------------------------
err_file="$(mktemp)"
d="$(mktemp -d)"
(
  cd "$d" || exit 1
  git init -q
  git remote add origin "https://github.com/o/r.git"
)
orig_pwd="$PWD"
cd "$d" || exit 1
LEGS_PUSH_REPO=""
legs_resolve_push_repo origin 2>"$err_file"
cd "$orig_pwd" || exit 1
err="$(cat "$err_file")"
is "a remote with no rewrite does not warn" "$err" ""
is "and resolves the repository slug" "$LEGS_PUSH_REPO" "o/r"

d="$(mktemp -d)"
(
  cd "$d" || exit 1
  git init -q
  git remote add origin "https://github.com/o/r.git"
  git config "url./tmp/elsewhere.git.pushInsteadOf" "https://github.com/o/r"
)
cd "$d" || exit 1
LEGS_PUSH_REPO=""
legs_resolve_push_repo origin 2>"$err_file"
cd "$orig_pwd" || exit 1
err="$(cat "$err_file")"
rm -f "$err_file"
is "a remote with a pushInsteadOf rule still allows the push" "$LEGS_PUSH_REPO" "o/r"
has "a remote with a pushInsteadOf rule warns" "$err" "https://github.com/o/r.git"
has "and names the rewritten URL" "$err" "/tmp/elsewhere.git.git"

# --- divergence ------------------------------------------------------------
is_diff() {
  local want="$1" desc="$2"; shift 2
  local got; got="$(legs_configured_difference "$@")"
  [[ "$got" == "$want" ]] && ok "$desc" || notok "$desc" "$want" "$got"
}
is_diff different "a different harness counts as configured to differ" codex vendor null claude vendor null
is_diff different "a different model counts"    claude vendor claude-fable-5 claude vendor claude-opus-5
is_diff different "a different endpoint counts" claude kimi   k3             claude vendor claude-opus-5
is_diff same      "identical configuration is not a difference" claude vendor claude-opus-5 claude vendor claude-opus-5

# Layer two must not halt on a harness that reports no model — that would
# disqualify the codex adapter for a field Codex does not emit.
( legs_assert_models_diverged different "claude-opus-5" "" ) >/dev/null 2>&1 \
  && ok "an unreported answering model does not halt the loop" \
  || notok "an unreported answering model does not halt the loop" "allow" "refuse"
( legs_assert_models_diverged different "claude-opus-5" "claude-opus-5" ) >/dev/null 2>&1 \
  && notok "two legs configured to differ but answered by one model halts" "refuse" "allow" \
  || ok "two legs configured to differ but answered by one model halts"
( legs_assert_models_diverged same "claude-opus-5" "claude-opus-5" ) >/dev/null 2>&1 \
  && ok "one configured model answering both legs is fine" \
  || notok "one configured model answering both legs is fine" "allow" "refuse"

# --- endpoint leakage ------------------------------------------------------
( unset ANTHROPIC_BASE_URL ANTHROPIC_AUTH_TOKEN; legs_assert_env_clean ) >/dev/null 2>&1 \
  && ok "a clean environment passes the leakage check" \
  || notok "a clean environment passes the leakage check" "allow" "refuse"
( export ANTHROPIC_BASE_URL=http://leaked.example; legs_assert_env_clean ) >/dev/null 2>&1 \
  && notok "an exported ANTHROPIC_BASE_URL is refused" "refuse" "allow" \
  || ok "an exported ANTHROPIC_BASE_URL is refused"

# --- a completed resolve pass that settled nothing can be driven again ------
#
# Escalating or reporting blocked completes a pass rather than abandoning it,
# and the guard that refuses a second resolve run reads exactly that
# completion — so without a re-drive, the command status recommends for the
# halt is one that always declines.
redrivable() {
  local want="$1" desc="$2"
  local got; if legs_resolve_redrivable "$3"; then got=yes; else got=no; fi
  [[ "$got" == "$want" ]] && ok "$desc" || notok "$desc" "$want" "$got"
}

# [policy-table: legs_resolve_redrivable]
redrivable yes "a blocked pass re-drives once what stopped it is fixed" \
  '{"leg":"resolve","pass":1,"state":"complete","blocked":true,"resolutions":[]}'
redrivable yes "an escalated pass re-drives once a human has settled it" \
  '{"leg":"resolve","pass":1,"state":"complete","blocked":false,"resolutions":[{"resolution":"fixed"},{"resolution":"escalated"}]}'
redrivable no  "a settled pass stays refused" \
  '{"leg":"resolve","pass":1,"state":"complete","blocked":false,"commit_sha":"d81a3f2abc","resolutions":[{"resolution":"fixed"},{"resolution":"skipped"}]}'
# The leg writes no resolve marker for a pass that raised nothing, so a marker
# that reached `complete` answered at least one finding. One that records none
# of those answers settled nothing anybody can read, so it re-drives.
redrivable yes "a pass that recorded no resolutions re-drives" \
  '{"leg":"resolve","pass":1,"state":"complete","blocked":false,"resolutions":[]}'
redrivable no  "an all-disputed pass stays refused" \
  '{"leg":"resolve","pass":1,"state":"complete","blocked":false,"resolutions":[{"resolution":"disputed"}]}'
# A deferral whose record never landed left its thread open on purpose, so the
# pass is not settled: once the filing is fixed, driving it again is the remedy.
redrivable yes "an unpersisted deferral re-drives once the filing is fixed" \
  '{"leg":"resolve","pass":1,"state":"complete","blocked":false,"resolutions":[{"resolution":"deferred","crossrev_tracked":""}]}'
# A fix the resolver claimed and never committed decided nothing either: the
# thread is open, the code is unchanged, and re-running the leg is the remedy
# once whatever stopped the write is dealt with.
redrivable yes "a fix that reached no commit re-drives" \
  '{"leg":"resolve","pass":1,"state":"complete","blocked":false,"commit_sha":null,"resolutions":[{"resolution":"fixed"},{"resolution":"disputed"}]}'
redrivable yes "a legacy pass with no resolutions recorded re-drives" \
  '{"leg":"resolve","pass":1,"state":"complete","blocked":false,"commit_sha":null}'
# The same legacy marker with a commit is an ordinary finished pass: the head
# moved, the reviewer has something to see, and re-deciding it settles nothing.
redrivable no  "a legacy pass that pushed stays refused" \
  '{"leg":"resolve","pass":1,"state":"complete","blocked":false,"commit_sha":"d81a3f2abc"}'
# [policy-table-end: legs_resolve_redrivable]

# Completing a harness failure writes verdict blocked, which is what used to
# make the next review answer "already reviewed" on the same head.
review_redrivable() {
  local want="$1" desc="$2"
  local got; if legs_review_redrivable "$3"; then got=yes; else got=no; fi
  [[ "$got" == "$want" ]] && ok "$desc" || notok "$desc" "$want" "$got"
}

# [policy-table: legs_review_redrivable]
review_redrivable yes "a blocked review re-drives once the harness can run" \
  '{"leg":"review","pass":1,"state":"complete","verdict":"blocked","findings":[]}'
review_redrivable no  "a review that found issues stays refused" \
  '{"leg":"review","pass":1,"state":"complete","verdict":"issues-remain","findings":[{"severity":"high"}]}'
review_redrivable no  "a converged review stays refused" \
  '{"leg":"review","pass":1,"state":"complete","verdict":"converged","findings":[]}'
review_redrivable no  "an unfinished review is recovery, not this re-drive" \
  '{"leg":"review","pass":1,"state":"started","verdict":null,"findings":[]}'
review_redrivable no  "an empty marker is not redrivable" \
  ""
# [policy-table-end: legs_review_redrivable]

# --- the guard itself, end to end -------------------------------------------
#
# The predicate is only worth anything wired into the resolve leg, so these
# run the leg against the stubbed boundary: a pass that ended blocked with its
# findings escalated is driven again, and a settled one is still refused.
RD_ID_A="cccc000000000003"
RD_ID_B="dddd000000000004"

rd_review_marker() {
  jq -cn --arg sha "${1:-$FIX_HEAD}" --arg a "$RD_ID_A" --arg b "$RD_ID_B" '
    {v:1, leg:"review", pass:1, state:"complete", ts:100, done_ts:200, run_id:"1",
     head_sha:$sha, harness:"claude", model:"reviewer-model", effort:null, endpoint:null,
     model_reported:"reviewer-model", tokens:100, verdict:"issues-remain",
     findings:[
       {id:$a, path:"app.ts", line:2, side:"RIGHT", severity:"high", category:"correctness",
        pre_existing:false, title:"t a", why:"w", fix:"f", anchor:"", thread_id:"T_A",
        resolution:null, tracked_as:null},
       {id:$b, path:"app.ts", line:2, side:"RIGHT", severity:"medium", category:"correctness",
        pre_existing:false, title:"t b", why:"w", fix:"f", anchor:"", thread_id:"T_B",
        resolution:null, tracked_as:null}]}'
}

# $1 resolutions, $2 blocked flag, $3 the revision the pass ran against, $4 the
# commit it pushed.
#
# A pass that fixed something and pushed nothing is a distinct ending — the
# claim is not in the diff — so a fixture modelling a settled pass has to say
# which of the two it is rather than leaving the SHA off.
rd_resolve_marker() {
  jq -cn --arg sha "${3:-$FIX_HEAD}" --arg c "${4:-}" --argjson d "$1" --argjson b "$2" '
    {v:1, leg:"resolve", pass:1, state:"complete", ts:300, done_ts:400, run_id:"1",
     head_sha:$sha, harness:"claude", model:"resolver-model", effort:null, endpoint:null,
     model_reported:"resolver-model", tokens:100, blocked:$b,
     blocked_reason:(if $b then "no write access to the working tree" else null end),
     commit_sha:(if $c == "" then null else $c end), summary:"s", resolutions:$d}'
}

rd_resolutions() {
  jq -cn --arg a "$RD_ID_A" --arg b "$RD_ID_B" --arg disp "$1" '
    [{finding_id:$a, resolution:$disp, reply:"r", persist:null, duplicate_of:null},
     {finding_id:$b, resolution:$disp, reply:"r", persist:null, duplicate_of:null}]'
}

# The first attempt's escalation replies, already on the threads when the
# re-drive runs. Their finding markers are what the reply dedupe reads, so they
# are the difference between answering again and resolving a thread in silence.
rd_prior_replies() {
  jq -cn --arg a "$RD_ID_A" --arg b "$RD_ID_B" --arg u "$FIX_USER" '
    [ {body: ("needs a human <!-- crossrev:f " + ({id:$a, pass:1, leg:"resolve"} | tojson) + " -->"),
       user:{login:$u}},
      {body: ("needs a human <!-- crossrev:f " + ({id:$b, pass:1, leg:"resolve"} | tojson) + " -->"),
       user:{login:$u}} ]'
}

# The resolver changes code in the working tree; the orchestrator commits it.
rd_edit_script() {
  local f; f="$(mktemp)"
  printf 'printf "export const patched = 1\\n" >> app.ts\n' >"$f"
  printf '%s' "$f"
}

rd_routes() {
  route 'api --method POST repos/*/issues/42/comments*' '{"id":9003}'
  route '*reviewThreads*' "$(threads_response \
    "$(thread_node T_A app.ts 2 false "$RD_ID_A")" \
    "$(thread_node T_B app.ts 2 false "$RD_ID_B")")"
  route '*resolveReviewThread*' '{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}'
  route 'api --method POST repos/*/pulls/42/comments/*/replies*' '{"id":6001}'
}

rd_comments() {
  jq -cn --argjson a "$(marker_comment 9001 "$(rd_review_marker "${3:-}")")" \
         --argjson b "$(marker_comment 9002 "$(rd_resolve_marker "$1" "$2" "${3:-}" "${4:-}")")" '[$a, $b]'
}

RD_FIX_PAYLOAD='{"blocked":false,"blocked_reason":null,"summary":"Fixed both.","resolutions":[
  {"finding_number":1,"resolution":"fixed","reply":"Fixed a.","persist":null,"duplicate_of":null},
  {"finding_number":2,"resolution":"fixed","reply":"Fixed b.","persist":null,"duplicate_of":null}]}'

fixture_repo; stub_reset
routes_baseline "$(rd_comments "$(rd_resolutions escalated)" true | payload)"
rd_routes
route_first 'api --paginate repos/*/pulls/42/comments*' "$(rd_prior_replies)"
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$RD_FIX_PAYLOAD" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(rd_edit_script)"; export CROSSREV_RESOLVE_EDIT
CROSSREV_RESOLVE_MODEL=resolver-model; export CROSSREV_RESOLVE_MODEL
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the re-drive exits clean"                        "$rc" "0"
has "it says the pass is being driven again"          "$out" "driving pass 1 again"
hasnt "rather than declining as already resolved"     "$out" "already resolved"
has "the resolver actually runs"                      "$(cat "$PROMPT_LOG")" "You are the resolve leg"
has "and its fix is committed" "$(git --git-dir="$FIX_ORIGIN" log -1 refs/heads/feature --format=%s)" "fix: resolve crossrev review findings (pass 1)"
is  "both threads are answered again, over the first attempt's replies" \
  "$(count 'pulls/42/comments/5000/replies')" "2"
has "and resolved once fixed"                         "$out" "resolved 2 thread(s)"
has "the loop is handed back to the reviewer"         "$(calls)" "labels[]=crossrev/awaiting-review"

# The guard's other half: a pass that settled every finding is done, and
# re-running it would re-decide work that finished. Settled means fixed AND
# pushed — the SHA is what makes this marker a finished pass rather than a
# claim the diff does not carry.
fixture_repo; stub_reset
routes_baseline "$(rd_comments "$(rd_resolutions fixed)" false "" d81a3f2abc | payload)"
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the settled pass still declines cleanly"         "$rc" "0"
has "with the refusal the guard has always given"     "$out" "already resolved"
if [[ ! -s "$PROMPT_LOG" ]]; then
  ok "and the resolver is not run again"
else
  notok "and the resolver is not run again" "no prompt" "$(cat "$PROMPT_LOG")"
fi

# --- the label a finished resolve pass leaves behind -------------------------
#
# A pass that pushed hands back to the reviewer, because the head moved and
# there is something new to see. A pass that settled every finding without
# pushing — each disputed, skipped, or deferred and tracked — is over: the
# reviewer declines an unchanged head, so awaiting-review would park the loop
# on a command that refuses. Escalation still wins, and a deferral whose
# record never landed is not settled — its thread is open on purpose.
resolve_label() {
  local want="$1" desc="$2"
  local got; got="$(legs_resolve_pass_label "$3" "$4")"
  [[ "$got" == "$want" ]] && ok "$desc" || notok "$desc" "$want" "$got"
}

resolve_label converged "an all-disputed pass converges" \
  '{"state":"complete","blocked":false,"commit_sha":null,"resolutions":[{"resolution":"disputed"}]}' 0
resolve_label converged "an all-skipped pass converges" \
  '{"state":"complete","blocked":false,"commit_sha":null,"resolutions":[{"resolution":"skipped"}]}' 0
resolve_label converged "a deferral tracked elsewhere converges" \
  '{"state":"complete","blocked":false,"commit_sha":null,"resolutions":[{"resolution":"deferred","crossrev_tracked":"o/r#7"}]}' 0
resolve_label converged "a mix of the three settles" \
  '{"state":"complete","blocked":false,"commit_sha":null,"resolutions":[{"resolution":"disputed"},{"resolution":"skipped"},{"resolution":"deferred","crossrev_tracked":"o/r#7"}]}' 0
resolve_label converged "a legacy deferral without the tracking field reads as settled" \
  '{"state":"complete","blocked":false,"commit_sha":null,"resolutions":[{"resolution":"deferred"}]}' 0
resolve_label awaiting-review "a pass that pushed a fix hands back to the reviewer" \
  '{"state":"complete","blocked":false,"commit_sha":"d81a3f2abc","resolutions":[{"resolution":"fixed"},{"resolution":"disputed"}]}' 0
resolve_label awaiting-review "a deferral committed to the repository backlog moved the head" \
  '{"state":"complete","blocked":false,"commit_sha":"d81a3f2abc","resolutions":[{"resolution":"deferred","crossrev_tracked":".crossrev/backlog#1"}]}' 0
resolve_label halted "a dispute beside an escalation halts — the escalation wins" \
  '{"state":"complete","blocked":false,"commit_sha":null,"resolutions":[{"resolution":"disputed"},{"resolution":"escalated"}]}' 0
resolve_label halted "an escalation an earlier pass left standing halts the settle" \
  '{"state":"complete","blocked":false,"commit_sha":null,"resolutions":[{"resolution":"disputed"}]}' 1
resolve_label halted "a blocked pass halts" \
  '{"state":"complete","blocked":true,"commit_sha":null,"resolutions":[{"resolution":"disputed"}]}' 0
resolve_label halted "a deferral whose record never landed halts" \
  '{"state":"complete","blocked":false,"commit_sha":null,"resolutions":[{"resolution":"deferred","crossrev_tracked":""}]}' 0
# A fix nobody committed is the one settle that must never read as converged:
# the resolver's own answer says the defect is real, and the code is unchanged.
resolve_label halted "a fix the resolver claimed and never committed halts" \
  '{"state":"complete","blocked":false,"commit_sha":null,"resolutions":[{"resolution":"fixed"}]}' 0
resolve_label halted "and it outranks the disputes beside it" \
  '{"state":"complete","blocked":false,"commit_sha":null,"resolutions":[{"resolution":"fixed"},{"resolution":"disputed"}]}' 0
# Every settle above is read off the resolutions, so a marker carrying none
# cannot be shown to have settled anything. Converging there would print the
# green terminal on the strength of a missing record.
resolve_label halted "a pass that recorded no resolutions halts" \
  '{"state":"complete","blocked":false,"commit_sha":null,"resolutions":[]}' 0
resolve_label halted "and a legacy marker without the field at all halts too" \
  '{"state":"complete","blocked":false,"commit_sha":null}' 0
resolve_label awaiting-review "the same legacy marker with a commit hands back to the reviewer" \
  '{"state":"complete","blocked":false,"commit_sha":"d81a3f2abc"}' 0

# --- and the label the resolve leg actually writes, end to end ---------------
#
# Every finding disputed, nothing pushed: the loop is done, and the label has
# to say so rather than hand back to a reviewer that will decline.
RD_REBUT_PAYLOAD='{"blocked":false,"blocked_reason":null,"summary":"Both disputed.","resolutions":[
  {"finding_number":1,"resolution":"disputed","reply":"Not a defect.","persist":null,"duplicate_of":null},
  {"finding_number":2,"resolution":"disputed","reply":"Not this one either.","persist":null,"duplicate_of":null}]}'

fixture_repo; stub_reset
routes_baseline "$(jq -cn --argjson a "$(marker_comment 9001 "$(rd_review_marker)")" '[$a]' | payload)"
rd_routes
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$RD_REBUT_PAYLOAD" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_MODEL=resolver-model; export CROSSREV_RESOLVE_MODEL
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the all-disputed resolve exits clean"            "$rc" "0"
has "both threads are answered and resolved"          "$out" "resolved 2 thread(s)"
has "the pass is labelled converged"                  "$(calls)" "labels[]=crossrev/converged"
hasnt "not handed back to a reviewer that declines"   "$(calls)" "labels[]=crossrev/awaiting-review"
has "and the run says the loop is done"               "$out" "the loop is done"

# One dispute beside one escalation: the pass is halted, not converged — the
# escalation waits on a human and outranks every settle beside it.
RD_MIXED_PAYLOAD='{"blocked":false,"blocked_reason":null,"summary":"One disputed, one escalated.","resolutions":[
  {"finding_number":1,"resolution":"disputed","reply":"Not a defect.","persist":null,"duplicate_of":null},
  {"finding_number":2,"resolution":"escalated","reply":"This needs a human.","persist":null,"duplicate_of":null}]}'

fixture_repo; stub_reset
routes_baseline "$(jq -cn --argjson a "$(marker_comment 9001 "$(rd_review_marker)")" '[$a]' | payload)"
rd_routes
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$RD_MIXED_PAYLOAD" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_MODEL=resolver-model; export CROSSREV_RESOLVE_MODEL
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the mixed pass exits clean"                      "$rc" "0"
has "a dispute beside an escalation stays halted"    "$(calls)" "labels[]=crossrev/halted"
hasnt "and never converges"                           "$(calls)" "labels[]=crossrev/converged"
has "the stop the escalation applies still lands"     "$(calls)" "crossrev/stop"

# Both findings claimed fixed, and the resolver changed no files: with no edit
# script the tree is clean, so `gh_commit_and_push` produces no commit and the
# replies claim a change the diff does not carry. The pass is not converged —
# nothing was settled — and it is not awaiting review either, because the head
# never moved. It halts, and the threads stay open for the person who reads it.
fixture_repo; stub_reset
routes_baseline "$(jq -cn --argjson a "$(marker_comment 9001 "$(rd_review_marker)")" '[$a]' | payload)"
rd_routes
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$RD_FIX_PAYLOAD" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_MODEL=resolver-model; export CROSSREV_RESOLVE_MODEL
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the no-change fixed pass exits clean"            "$rc" "0"
has "the run says the fixes reached no files"         "$out" "changed no files"
is  "nothing is committed over an unchanged tree"     "$(git log -1 feature --format=%s)" "feature"
has "the pass is halted"                              "$(calls)" "labels[]=crossrev/halted"
hasnt "never converged over an unkept promise"        "$(calls)" "labels[]=crossrev/converged"
hasnt "nor handed to a reviewer with nothing to see"  "$(calls)" "labels[]=crossrev/awaiting-review"
is  "and no thread is resolved over it"               "$(count 'resolveReviewThread')" "0"
has "the run says why a person is needed"             "$out" "reached no commit"

# --- the label a finished review pass leaves behind --------------------------
#
# An empty pass after an escalation is not a convergence: the reviewer
# correctly declined to re-raise a settled finding, so nothing actionable
# means nothing NEW, while the escalated thread still waits on a human. halted
# is the honest label; converged would contradict the marker it sits beside.
pass_label() {
  local want="$1" desc="$2"; shift 2
  local got; got="$(legs_pass_label "$@")"
  [[ "$got" == "$want" ]] && ok "$desc" || notok "$desc" "$want" "$got"
}

pass_label awaiting-resolution "a converged verdict with actionable findings is overridden" converged 2 0
pass_label converged           "a converged verdict after an escalation still exits the halt" converged 0 2
pass_label halted              "a blocked review halts"                          blocked 0 0
pass_label awaiting-resolution "actionable findings owe the resolve leg"         issues-remain 2 0
pass_label converged           "an empty pass with nothing open converges"       issues-remain 0 0
pass_label halted              "an empty pass while an escalation stands halts"  issues-remain 0 1

# --- and the label the leg actually writes, end to end -----------------------
#
# The reviewer stub answers with no findings and verdict issues-remain — the
# correct answer over a pass whose open findings are all escalated, since a
# settled finding is not re-raised. What the label row says afterwards is
# the whole assertion.
EMPTY_REVIEW='{"verdict":"issues-remain","blocked_reason":null,"prior":null,"findings":[]}'
# Pass 1 ran against an older revision, so the review leg may start pass 2.
RD_OLD_SHA="1111111111111111111111111111111111111111"

fixture_repo; stub_reset
routes_baseline "$(rd_comments "$(rd_resolutions escalated)" false "$RD_OLD_SHA" | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9003}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$EMPTY_REVIEW" | payload)"; export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?

is  "the empty pass exits clean"                      "$rc" "0"
has "the pull request stays halted"                   "$(calls)" "labels[]=crossrev/halted"
hasnt "and is never labelled converged"               "$(calls)" "labels[]=crossrev/converged"

# With nothing escalated the same empty pass is a real convergence, so the
# green label still exists for the pass that earned it.
fixture_repo; stub_reset
routes_baseline "$(rd_comments "$(rd_resolutions fixed)" false "$RD_OLD_SHA" d81a3f2abc | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9003}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$EMPTY_REVIEW" | payload)"; export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?

is  "the settled pass's empty review exits clean"     "$rc" "0"
has "and converges, with nothing left open"           "$(calls)" "labels[]=crossrev/converged"

# A reviewer that returns converged alongside actionable findings is overridden:
# the count outranks the verdict, the pass is labelled awaiting-resolution, and
# the call site warns naming both.
CONVERGED_WITH_FINDINGS='{"verdict":"converged","blocked_reason":null,"prior":null,"findings":[{"path":"app.ts","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"t","why":"w","fix":"f"}]}'

fixture_repo; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$CONVERGED_WITH_FINDINGS" | payload)"; export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?

is  "a converged review with actionable findings exits clean" "$rc" "0"
has "the pull request is labelled awaiting-resolution"        "$(calls)" "labels[]=crossrev/awaiting-resolution"
hasnt "and is not labelled converged"                         "$(calls)" "labels[]=crossrev/converged"
has "it warns that the converged verdict was overridden"      "$out" "the reviewer returned verdict 'converged' alongside 1 actionable finding"
has "the warning names the awaiting-resolution label"         "$out" "awaiting-resolution"

# --- a finished leg is not rewritten when the next one dies ------------------
#
# The EXIT trap records a fatal error into whichever leg was open, and the last
# checkpoint runs BEFORE the complete edit. Left alone, the snapshot went on
# saying `started` for the rest of the process — so anything dying afterwards
# would PATCH a finished review to blocked and label the pull request halted over
# work that had succeeded. `cycle` is the real case: the review leg completes,
# and the resolve leg's whole start-up can still die before it takes a checkpoint
# of its own.
#
# kimi is on PATH as a stub and has no adapter, so the resolve leg dies in
# run_leg_settings. That sits after the review leg's complete edit and before
# resolve's first checkpoint, which is exactly the window.
# Driven at the function boundary rather than through a leg. The window is
# between one leg's complete edit and the next leg's first checkpoint, and the
# offline `gh` stub answers the comment list from a fixed payload — so a cycle
# here never reads back the marker its own review leg just posted, stops at pass
# 0, and asserts nothing about the case. The probe below is the case.
fatal_probe() {
  local state="$1" rc="$2" settled="${3:-no}"
  ROOT="$HERE/.." P_STATE="$state" P_RC="$rc" P_SETTLED="$settled" bash -c '
    set -uo pipefail
    source "$ROOT/lib/ui.sh"
    source "$ROOT/lib/state.sh"
    source "$ROOT/lib/legs.sh"
    source "$ROOT/lib/github.sh"
    source "$ROOT/lib/run.sh"
    CTX_REPO="acme/widget"; CTX_PR=42; CTX_LABELS=""
    comment_id=9001
    marker="$(jq -cn --arg s "$P_STATE" "{v:1, leg:\"review\", pass:1, state:\$s,
      verdict:\"issues-remain\", findings:[], comment_id:9001}")"
    run_checkpoint
    [[ "$P_SETTLED" == "yes" ]] && run_leg_settled
    CROSSREV_DIE_REASON="the harness could not be driven"
    _run_report_fatal "$P_RC"
  ' 2>/dev/null
}

reports() {
  local want="$1" desc="$2"; shift 2
  stub_reset >/dev/null
  fatal_probe "$@"
  local got=no; grep -q "method PATCH" "$GH_LOG" 2>/dev/null && got=yes
  [[ "$got" == "$want" ]] && ok "$desc" || notok "$desc" "$want" "$got"
}

reports yes "an open leg that dies is recorded"              started  1
reports no  "a clean exit records nothing"                   started  0
reports no  "an interrupt stays the resumable claim it is"   started  130
reports no  "a marker already complete is never rewritten"   complete 1

# The regression this exists for. run_checkpoint snapshots before the complete
# edit, so without run_leg_settled the snapshot goes on saying `started` after
# the leg has finished — and everything that runs afterwards can still die.
# Under `cycle` that is run_pass_labels and the whole start-up of the next leg:
# worktree creation, the push guard, the adapter lookup, the claim comment. Any
# of them would have PATCHed a finished review to blocked and halted the pull
# request over work that succeeded.
reports no  "a settled leg is not rewritten when a later step dies" started 1 yes

# --- the way out of the halt, driven by hand ---------------------------------
#
# A human settles the escalated thread and the next review returns converged,
# which is the escape the label rule exempts. Running the resolve leg over that
# pass by hand finds nothing to resolve, and reading only the escalation count
# there would take the green label straight back off: the escalation is history
# the marker keeps, and the verdict is the answer about it.
rd_converged_review_marker() {
  jq -cn --arg sha "$FIX_HEAD" '
    {v:1, leg:"review", pass:2, state:"complete", ts:500, done_ts:600, run_id:"1",
     head_sha:$sha, harness:"claude", model:"reviewer-model", effort:null, endpoint:null,
     model_reported:"reviewer-model", tokens:100, verdict:"converged", findings:[]}'
}

fixture_repo; stub_reset
routes_baseline "$(jq -cn \
  --argjson a "$(marker_comment 9001 "$(rd_review_marker "$RD_OLD_SHA")")" \
  --argjson b "$(marker_comment 9002 "$(rd_resolve_marker "$(rd_resolutions escalated)" false "$RD_OLD_SHA")")" \
  --argjson c "$(marker_comment 9004 "$(rd_converged_review_marker)")" '[$a, $b, $c]' | payload)" \
  '[{"name":"crossrev/converged"},{"name":"crossrev/pass-2"}]'
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "resolving a converged pass exits clean"          "$rc" "0"
has "it says there is nothing to resolve"             "$out" "found nothing to resolve"
has "the converged label stands"                      "$(calls)" "labels[]=crossrev/converged"
hasnt "it is never taken off the pull request"        "$(calls)" "labels/crossrev/converged"
hasnt "and the settled pass is not halted again"      "$(calls)" "labels[]=crossrev/halted"
if [[ ! -s "$PROMPT_LOG" ]]; then
  ok "with no resolver run over a pass that has no findings"
else
  notok "with no resolver run over a pass that has no findings" "no prompt" "$(cat "$PROMPT_LOG")"
fi

# --- the part of a harness's stderr worth showing ---------------------------
#
# The fixture is a real capture, not an invention: `codex exec` run against an
# empty CODEX_HOME, which is exactly what a leg does when the secret did not
# arrive. Its shape is the whole problem — a banner, the prompt echoed back, then
# the same 401 nine times, with the only line carrying a reason phrase at the very
# end. An invented fixture would have put the error first and proved nothing.
cxerr="$(mktemp)"
cat >"$cxerr" <<'STDERR'
Reading additional input from stdin...
OpenAI Codex v0.147.0
--------
workdir: /private/tmp
model: gpt-5.6-sol
provider: openai
approval: never
sandbox: read-only
reasoning effort: none
reasoning summaries: none
session id: 01a00fbb-b6b4-76d0-9880-dd8ffb257a31
--------
user
say hi
2026-08-17T12:39:16.323309Z ERROR codex_api::endpoint::responses_websocket: failed to connect to websocket: HTTP error: 401 Unauthorized, url: wss://api.openai.com/v1/responses
ERROR: Reconnecting... 5/5
warning: Falling back from WebSockets to HTTPS transport. unexpected status 401 Unauthorized: Missing bearer or basic authentication in header, url: wss://api.openai.com/v1/responses
ERROR: unexpected status 401 Unauthorized: Missing bearer or basic authentication in header, url: https://api.openai.com/v1/responses, request id: req_9274b2b4de5546c9afd06f74c789a44d
STDERR

picked="$(legs_harness_error "$cxerr")"
has "the harness error names the status code that explains it" "$picked" "401"
has "and the reason phrase behind it"          "$picked" "Missing bearer or basic authentication"
hasnt "not the banner the harness opens with"  "$picked" "OpenAI Codex v0.147.0"
hasnt "nor the prompt echoed back at us"       "$picked" "say hi"
hasnt "nor a session id nobody can act on"     "$picked" "session id"

# The regression this replaced, asserted rather than described. head -c 400 on
# this fixture stops two bytes short of the first "401" — measured, not estimated
# — and a real review prompt pushes it thousands of bytes further out.
hasnt "the old head-of-stream read missed all of that" "$(head -c 400 "$cxerr")" "401"

# A budget spent on repeated retries is a budget spent on nothing. Codex reports
# the identical failure nine times and only the last line says why.
(( ${#picked} <= 400 )) && ok "and it stays inside the 400-byte budget" \
  || notok "and it stays inside the 400-byte budget" "<= 400" "${#picked}"
[[ "$picked" != $'\n'* && "$picked" != " "* ]] \
  && ok "and never opens halfway through a truncated line" \
  || notok "and never opens halfway through a truncated line" "a whole line" "$picked"

# No keyword anywhere is the case the tail fallback exists for: the banner is
# always at the top, so the end of the stream is the better guess either way.
quiet="$(mktemp)"
printf 'banner line\nversion 1.2.3\nworkdir: /tmp\nsession: abc\nmiddle\nthe last thing it said\n' >"$quiet"
has "a stream with no diagnosis falls back to its end" \
  "$(legs_harness_error "$quiet")" "the last thing it said"
hasnt "rather than to the banner at its start" \
  "$(legs_harness_error "$quiet")" "banner line"

empty="$(mktemp)"
legs_harness_error "$empty" >/dev/null 2>&1 \
  && notok "an empty stderr reports nothing rather than an empty message" "non-zero" "0" \
  || ok "an empty stderr reports nothing rather than an empty message"

rm -f "$cxerr" "$quiet" "$empty"

# --- a cycle may take --harness ----------------------------------------------
#
# --harness on cycle is forwarded into both legs, so the override puts one
# harness on both sides of the loop. That is a pairing CrossRev serves rather
# than refuses, and refusing it would contradict the tool twice over: the same
# pairing named through reviewer.harness and resolver.harness has always run,
# because legs_assert_models_diverged returns early when one model was asked
# for; and run_leg_settings already puts both legs on one harness by itself
# when only one is installed. An operator with a single harness has no other
# pairing available.
#
# So the override is checked for the one thing the configured names are checked
# for: whether that harness serves the leg.
fixture_repo; stub_reset
routes_baseline "$(printf '[]' | payload)"
route '*reviewThreads*' "$(threads_response)"
out="$("$CROSSREV" cycle --pr 42 --harness opencode 2>&1)"; rc=$?

has   "a cycle --harness naming a both-legs harness starts" "$out" "Reviewing acme/widget#42"
has   "with the override on the leg it named"                 "$out" "Reviewer: opencode"
hasnt "and nothing refuses it for using one harness twice"    "$out" "both legs"

# The start assertion never reaches the resolve leg: the stub answers the
# comment list from a fixed payload, so cycle cannot read back the marker
# its own review just posted. Pre-seeding a complete review at this SHA
# lets the review decline as already-reviewed and the resolve leg run —
# which is where the divergence guard used to die, because it still read
# the config and a two-harness file prints "different" while both legs
# report stub-model.
two_harness_cfg="$(fixture_default_config | awk '
  $1 == "resolver:" { r = 1 }
  r && $1 == "harness:" { sub(/claude/, "codex"); r = 0 }
  { print }
')"
fixture_repo "$two_harness_cfg"; stub_reset
cycle_override_review="$(rd_review_marker | jq -c \
  '.harness = "opencode" | .model = null | .model_reported = "stub-model"')"
routes_baseline "$(marker_comment 9001 "$cycle_override_review" | jq -cs . | payload)"
rd_routes
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$RD_FIX_PAYLOAD" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
out="$("$CROSSREV" cycle --pr 42 --harness opencode 2>&1)"; rc=$?

is    "a cycle --harness over a two-harness config completes" "$rc" "0"
has   "having run the resolve leg"                            "$out" "Resolving acme/widget#42"
hasnt "and the divergence guard does not fire"                "$out" "the same model answered each"

# A harness that serves only one leg is still refused, and before the pass loop
# rather than when that leg starts. No shipped harness is review-only now, so
# the case needs a descriptor of its own: the real one with grok cut back.
review_only_desc="$(mktemp)"
jq '(.harnesses[] | select(.name == "grok")).legs = ["review"]' \
  "$HERE/../lib/harnesses.json" >"$review_only_desc"

fixture_repo; stub_reset
routes_baseline "$(printf '[]' | payload)"
route '*reviewThreads*' "$(threads_response)"
out="$(CROSSREV_HARNESS_FILE="$review_only_desc" "$CROSSREV" cycle --pr 42 --harness grok 2>&1)"; rc=$?

is  "a cycle --harness naming a review-only harness is refused" "$rc" "1"
has "naming the leg it cannot serve"                            "$out" "cannot serve the resolve leg"
is  "no harness ran on the way out"                             "$(cat "$PROMPT_LOG")" ""
is  "and nothing was posted"                                    "$(count 'method POST repos/acme/widget/pulls/42/comments')" "0"

# The per-leg gate in run_leg_settings is the backstop for a bare
# `crossrev resolve --harness` that cycle's pairing check never sees.
fixture_repo; stub_reset
routes_baseline "$(marker_comment 9001 "$(rd_review_marker)" | jq -cs . | payload)"
route '*reviewThreads*' "$(threads_response "$(thread_node T_A app.ts 2 false "$RD_ID_A")")"
out="$(CROSSREV_HARNESS_FILE="$review_only_desc" "$CROSSREV" resolve --pr 42 --harness grok 2>&1)"; rc=$?

is  "a bare resolve --harness naming a review-only harness is refused" "$rc" "1"
has "naming the leg it cannot serve"                                  "$out" "cannot serve the resolve leg"
is  "no harness ran on that path either"                              "$(cat "$PROMPT_LOG")" ""
is  "and stages no credential"                                        "$(count 'secret set')" "0"

# The fallback iterates only harnesses that serve the leg. On a machine
# where the configured resolver is absent and the only installed harness
# is review-only, substituting it would stage a credential for a leg it
# can never run.
path_only_grok() {
  local keep src
  keep="$(mktemp -d)"
  ln -s "$HERE/stub/grok" "$keep/grok"
  ln -s "$HERE/stub/gh" "$keep/gh"
  for src in jq yq; do
    ln -s "$(command -v "$src")" "$keep/$src"
  done
  PATH="$keep:/usr/bin:/bin"
  export PATH
}

fixture_repo; stub_reset
routes_baseline "$(marker_comment 9001 "$(rd_review_marker)" | jq -cs . | payload)"
route '*reviewThreads*' "$(threads_response "$(thread_node T_A app.ts 2 false "$RD_ID_A")")"
# The helper rewrites PATH for everything after it, so it is bracketed: the
# fallback blocks are not the last statements in this file, and a leaked PATH
# would quietly change what every later command resolves.
_path_before_fallback="$PATH"
path_only_grok
out="$(CROSSREV_HARNESS_FILE="$review_only_desc" "$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the resolve fallback refuses when the only installed harness cannot serve the leg" "$rc" "1"
has "and says no other resolve-capable harness is installed"                             "$out" "no other harness that can serve the resolve leg"
is  "no harness ran on the fallback path either"                                         "$(cat "$PROMPT_LOG")" ""
is  "and still stages no credential"                                                     "$(count 'secret set')" "0"

PATH="$_path_before_fallback"; export PATH

rm -f "$review_only_desc"

# The single-harness fallback now reaches opencode too. With the descriptor
# naming both legs, a machine where only opencode is installed substitutes it
# for the resolve leg under the standing warning, instead of refusing a
# resolver that can now actually serve.
path_only_opencode() {
  local keep src
  keep="$(mktemp -d)"
  ln -s "$HERE/stub/opencode" "$keep/opencode"
  ln -s "$HERE/stub/gh" "$keep/gh"
  for src in jq yq; do
    ln -s "$(command -v "$src")" "$keep/$src"
  done
  PATH="$keep:/usr/bin:/bin"
  export PATH
}

fixture_repo; stub_reset
routes_baseline "$(rd_comments "$(rd_resolutions escalated)" true | payload)"
rd_routes
route_first 'api --paginate repos/*/pulls/42/comments*' "$(rd_prior_replies)"
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$RD_FIX_PAYLOAD" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(rd_edit_script)"; export CROSSREV_RESOLVE_EDIT
_path_before_fallback="$PATH"
path_only_opencode
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the resolve fallback substitutes opencode when nothing else is installed" "$rc" "0"
has "under the standing substitution warning"    "$out" "runs on 'opencode' instead"
has "and the resolver really runs"               "$(cat "$PROMPT_LOG")" "You are the resolve leg"
has "and its fix lands" \
  "$(git --git-dir="$FIX_ORIGIN" log -1 refs/heads/feature --format=%s)" "fix: resolve crossrev review findings (pass 1)"

PATH="$_path_before_fallback"; export PATH

finish
