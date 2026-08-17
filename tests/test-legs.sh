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
guard allow  "pushes to the PR head branch"                     feat/x feat/x main o/r o/r
guard refuse "refuses when the checkout is on another branch"   main   feat/x main o/r o/r
guard refuse "refuses when the head branch is the default"      main   main   main o/r o/r
guard refuse "refuses when the head repo is not the origin"     feat/x feat/x main fork/r o/r

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

redrivable yes "a blocked pass re-drives once what stopped it is fixed" \
  '{"leg":"resolve","pass":1,"state":"complete","blocked":true,"dispositions":[]}'
redrivable yes "an escalated pass re-drives once a human has settled it" \
  '{"leg":"resolve","pass":1,"state":"complete","blocked":false,"dispositions":[{"disposition":"fixed"},{"disposition":"escalated"}]}'
redrivable no  "a settled pass stays refused" \
  '{"leg":"resolve","pass":1,"state":"complete","blocked":false,"dispositions":[{"disposition":"fixed"},{"disposition":"skipped"}]}'
redrivable no  "a pass that dispositioned nothing stays refused" \
  '{"leg":"resolve","pass":1,"state":"complete","blocked":false,"dispositions":[]}'

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
        disposition:null, tracked_as:null},
       {id:$b, path:"app.ts", line:2, side:"RIGHT", severity:"medium", category:"correctness",
        pre_existing:false, title:"t b", why:"w", fix:"f", anchor:"", thread_id:"T_B",
        disposition:null, tracked_as:null}]}'
}

# $1 dispositions, $2 blocked flag, $3 the revision the pass ran against.
rd_resolve_marker() {
  jq -cn --arg sha "${3:-$FIX_HEAD}" --argjson d "$1" --argjson b "$2" '
    {v:1, leg:"resolve", pass:1, state:"complete", ts:300, done_ts:400, run_id:"1",
     head_sha:$sha, harness:"claude", model:"resolver-model", effort:null, endpoint:null,
     model_reported:"resolver-model", tokens:100, blocked:$b,
     blocked_reason:(if $b then "no write access to the working tree" else null end),
     commit_sha:null, summary:"s", dispositions:$d}'
}

rd_dispositions() {
  jq -cn --arg a "$RD_ID_A" --arg b "$RD_ID_B" --arg disp "$1" '
    [{finding_id:$a, disposition:$disp, reply:"r", persist:null, duplicate_of:null},
     {finding_id:$b, disposition:$disp, reply:"r", persist:null, duplicate_of:null}]'
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
         --argjson b "$(marker_comment 9002 "$(rd_resolve_marker "$1" "$2" "${3:-}")")" '[$a, $b]'
}

RD_FIX_PAYLOAD='{"blocked":false,"blocked_reason":null,"summary":"Fixed both.","dispositions":[
  {"finding_number":1,"disposition":"fixed","reply":"Fixed a.","persist":null,"duplicate_of":null},
  {"finding_number":2,"disposition":"fixed","reply":"Fixed b.","persist":null,"duplicate_of":null}]}'

fixture_repo; stub_reset
routes_baseline "$(rd_comments "$(rd_dispositions escalated)" true | payload)"
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
has "and its fix is committed" "$(git log -1 --format=%s)" "fix: resolve crossrev review findings (pass 1)"
is  "both threads are answered again, over the first attempt's replies" \
  "$(count 'pulls/42/comments/5000/replies')" "2"
has "and resolved once fixed"                         "$out" "resolved 2 thread(s)"
has "the loop is handed back to the reviewer"         "$(calls)" "labels[]=crossrev/awaiting-review"

# The guard's other half: a pass that settled every finding is done, and
# re-running it would re-decide work that finished.
fixture_repo; stub_reset
routes_baseline "$(rd_comments "$(rd_dispositions fixed)" false | payload)"
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the settled pass still declines cleanly"         "$rc" "0"
has "with the refusal the guard has always given"     "$out" "already resolved"
if [[ ! -s "$PROMPT_LOG" ]]; then
  ok "and the resolver is not run again"
else
  notok "and the resolver is not run again" "no prompt" "$(cat "$PROMPT_LOG")"
fi

# --- the label a finished review pass leaves behind --------------------------
#
# An empty pass after an escalation is not a convergence: the reviewer
# correctly declined to re-raise a dispositioned finding, so nothing actionable
# means nothing NEW, while the escalated thread still waits on a human. halted
# is the honest label; converged would contradict the marker it sits beside.
pass_label() {
  local want="$1" desc="$2"; shift 2
  local got; got="$(legs_pass_label "$@")"
  [[ "$got" == "$want" ]] && ok "$desc" || notok "$desc" "$want" "$got"
}

pass_label converged           "a converged verdict converges, whatever is open" converged 0 2
pass_label halted              "a blocked review halts"                          blocked 0 0
pass_label awaiting-resolution "actionable findings owe the resolve leg"         issues-remain 2 0
pass_label converged           "an empty pass with nothing open converges"       issues-remain 0 0
pass_label halted              "an empty pass while an escalation stands halts"  issues-remain 0 1

# --- and the label the leg actually writes, end to end -----------------------
#
# The reviewer stub answers with no findings and verdict issues-remain — the
# correct answer over a pass whose open findings are all escalated, since a
# dispositioned finding is not re-raised. What the label row says afterwards is
# the whole assertion.
EMPTY_REVIEW='{"verdict":"issues-remain","blocked_reason":null,"prior":null,"findings":[]}'
# Pass 1 ran against an older revision, so the review leg may start pass 2.
RD_OLD_SHA="1111111111111111111111111111111111111111"

fixture_repo; stub_reset
routes_baseline "$(rd_comments "$(rd_dispositions escalated)" false "$RD_OLD_SHA" | payload)"
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
routes_baseline "$(rd_comments "$(rd_dispositions fixed)" false "$RD_OLD_SHA" | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9003}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$EMPTY_REVIEW" | payload)"; export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?

is  "the settled pass's empty review exits clean"     "$rc" "0"
has "and converges, with nothing left open"           "$(calls)" "labels[]=crossrev/converged"

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
  --argjson b "$(marker_comment 9002 "$(rd_resolve_marker "$(rd_dispositions escalated)" false "$RD_OLD_SHA")")" \
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

finish
