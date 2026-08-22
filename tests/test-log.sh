#!/usr/bin/env bash
#
# tests/test-log.sh — the per-run record: the run log, the transcripts, the
# redaction they pass through, and the sweep that bounds the directory.
#
# Half of this file drives lib/log.sh directly; the other half runs real
# (stubbed) legs end to end, because the properties that matter are on the
# boundary between the two: a failed leg keeps its transcript, a successful
# one does not, and nothing on either path carries a credential shape.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"
# shellcheck source=../lib/log.sh
source "$HERE/../lib/log.sh"

RUNS_BASE="$XDG_STATE_HOME/crossrev/runs"

# --- the run directory -------------------------------------------------------
# pr 99, so the unit record can never be mistaken for a fixture leg's pr-42.
log_init "acme/widget" 99
is "log_init creates the run directory" \
  "$([[ -d "$RUNS_BASE/acme-widget/pr-99/$(log_run_id)" ]] && echo yes)" "yes"
is "and names it after the run id the markers carry" "$CROSSREV_RUN_DIR" \
  "$RUNS_BASE/acme-widget/pr-99/local-$$"
is "the run log opens with the run event" "$(wc -l <"$CROSSREV_RUN_DIR/run.log" | tr -d ' ')" "1"
has "and the line names the repo and pull request" \
  "$(cat "$CROSSREV_RUN_DIR/run.log")" "run start repo=acme/widget pr=99"

log_init "acme/widget" 99
is "a second log_init does not restart the record" \
  "$(wc -l <"$CROSSREV_RUN_DIR/run.log" | tr -d ' ')" "1"

log_event invoke "harness=claude attempt=1 exit=0 duration=3s"
is "log_event appends one line" "$(wc -l <"$CROSSREV_RUN_DIR/run.log" | tr -d ' ')" "2"
has "and the line carries the phase and detail" \
  "$(tail -1 "$CROSSREV_RUN_DIR/run.log")" "invoke harness=claude attempt=1 exit=0 duration=3s"
has "and opens with a UTC timestamp" \
  "$(tail -1 "$CROSSREV_RUN_DIR/run.log" | cut -d' ' -f1)" "T"

CROSSREV_RUN_DIR_SAVED="$CROSSREV_RUN_DIR"
CROSSREV_RUN_DIR=""
log_event phase "should not land anywhere"
is "an uninitialised log_event is a no-op" "$?" "0"
is "and writes nothing" \
  "$([[ -f "$CROSSREV_RUN_DIR_SAVED/run.log" ]] && wc -l <"$CROSSREV_RUN_DIR_SAVED/run.log" | tr -d ' ')" "2"
CROSSREV_RUN_DIR="$CROSSREV_RUN_DIR_SAVED"

# --- redaction ---------------------------------------------------------------
#
# The constraint from ADR 0001 applied to files: the harness holds no GitHub
# credential, and this filter is the backstop for everything else a CLI might
# echo on its way to a failure. Each shape keeps its prefix, so a redacted
# line still names the kind of token it held.
leak="bearer ghp_abc123def456ghi789TOKEN and sk-ant-api03-aBcDeFgHiJkLmNoPqRsTuVwXyZ1234 and github_pat_11ABCDEFG0_aBcDeFgHiJkLmNoPqRsT and xai-aBcDeFgHiJkLmNoPqRsTuVw and gho_aBcDeFgHiJkLmNoP end"
redacted="$(printf '%s\n' "$leak" | log_redact)"
hasnt "a GitHub token body does not survive"      "$redacted" "ghi789TOKEN"
hasnt "an Anthropic key body does not survive"    "$redacted" "LmNoPqRsTuVwXyZ1234"
hasnt "a fine-grained PAT body does not survive"  "$redacted" "aBcDeFgHiJkLmNoPqRsT"
hasnt "an xAI key body does not survive"          "$redacted" "xai-aBcDeFgHiJkLmNoPqRsTuVw"
hasnt "an OAuth token body does not survive"      "$redacted" "gho_aBcDeFgHiJkLmNoP end"
has   "the prefix survives to name the kind"      "$redacted" "ghp_abc123…[redacted]"
has   "and the sk-ant prefix survives"            "$redacted" "sk-ant-api03-…[redacted]"
plain="commit 9f8e7d6 pushed to feature, all checks passed"
is  "ordinary text passes through unchanged" "$(printf '%s\n' "$plain" | log_redact)" "$plain"
is  "a short lookalike is left alone" \
  "$(printf 'ghp_abc is too short\n' | log_redact)" "ghp_abc is too short"

f="$(mktemp)"
printf 'header\ntoken ghp_abc123def456ghi789TOKEN\nfooter\n' >"$f"
log_redact_file "$f"
hasnt "log_redact_file redacts in place" "$(cat "$f")" "ghi789TOKEN"
has   "and keeps the surrounding lines"    "$(cat "$f")" "footer"
rm -f "$f"

# --- the sweep ---------------------------------------------------------------
old_run="$RUNS_BASE/acme-widget/pr-7/local-99999"
mkdir -p "$old_run"
printf 'old\n' >"$old_run/run.log"
touch -t 202001010000 "$old_run/run.log" "$old_run"
fresh_run="$RUNS_BASE/acme-widget/pr-7/local-88888"
mkdir -p "$fresh_run"
printf 'fresh\n' >"$fresh_run/run.log"
# shellcheck disable=SC2034  # read by log_sweep in lib/log.sh
CROSSREV_LOG_RETENTION_DAYS=14
log_sweep
is "the sweep removes a run directory older than the window" \
  "$([[ -d "$old_run" ]] && echo present || echo gone)" "gone"
is "and leaves a recent one alone" \
  "$([[ -f "$fresh_run/run.log" ]] && echo present || echo gone)" "present"
# shellcheck disable=SC2034  # read by log_sweep in lib/log.sh
CROSSREV_LOG_RETENTION_DAYS=not-a-number
log_sweep
is "a non-numeric window falls back to the default rather than failing" "$?" "0"
is "and still leaves the recent directory alone" \
  "$([[ -f "$fresh_run/run.log" ]] && echo present || echo gone)" "present"

# --- a successful leg leaves a run log and no transcript ---------------------
fixture_repo; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
REVIEW_PAYLOAD='{"verdict":"issues-remain","blocked_reason":null,"prior":null,"findings":[
  {"path":"app.ts","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,
   "title":"Unchecked fetch response","why":"A failed request looks like a success","fix":"Check response.ok"}
]}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?

is  "a review leg still passes with logging on" "$rc" "0"
rd="$(find "$RUNS_BASE/acme-widget/pr-42" -mindepth 1 -maxdepth 1 -type d | head -1)"
is  "the run directory exists" "$([[ -d "$rd" ]] && echo yes)" "yes"
runlog="$(cat "$rd/run.log")"
has "the log records the leg"     "$runlog" "leg review trigger=human mode=local"
has "the invoke is bracketed"     "$runlog" "invoke harness=claude attempt=1 start"
has "with its exit code"          "$runlog" "invoke harness=claude attempt=1 exit=0"
has "and a duration"              "$runlog" "duration="
has "and the run's own exit"      "$runlog" "exit code=0"
is  "and the transcripts are deleted after a successful leg" \
  "$(find "$rd" -name 'review.attempt-*' | wc -l | tr -d ' ')" "0"

# --- --keep-transcripts keeps them -------------------------------------------
rm -rf "$RUNS_BASE"
fixture_repo; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 --keep-transcripts 2>&1)"; rc=$?

is  "the flag is accepted and the leg passes" "$rc" "0"
rd="$(find "$RUNS_BASE/acme-widget/pr-42" -mindepth 1 -maxdepth 1 -type d | head -1)"
is  "the transcript survives a successful leg under the flag" \
  "$([[ -s "$rd/review.attempt-1.stdout" ]] && echo yes)" "yes"
has "and it holds what the harness printed" "$(cat "$rd/review.attempt-1.stdout")" "modelUsage"

# --- logs.keep_transcripts: true keeps them without the flag ------------------
rm -rf "$RUNS_BASE"
fixture_repo "$(fixture_config local medium; printf 'logs:\n  keep_transcripts: true\n')"
stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?

is  "the config key keeps the leg passing" "$rc" "0"
rd="$(find "$RUNS_BASE/acme-widget/pr-42" -mindepth 1 -maxdepth 1 -type d | head -1)"
is  "and keeps the transcript without the flag" \
  "$([[ -s "$rd/review.attempt-1.stdout" ]] && echo yes)" "yes"

# --- a failed leg keeps the transcript, and says where -----------------------
rm -rf "$RUNS_BASE"
fixture_repo; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
# No CROSSREV_REVIEW_PAYLOAD: the stub exits 1, the way an unauthenticated
# harness does.
out="$("$CROSSREV" review --pr 42 2>&1)"; rc=$?

is  "a harness failure still fails the leg" "$rc" "1"
has "and the failure names where the record is" "$out" "Run log and any kept transcripts:"
rd="$(find "$RUNS_BASE/acme-widget/pr-42" -mindepth 1 -maxdepth 1 -type d | head -1)"
is  "the failed leg's stdout transcript is kept" \
  "$([[ -f "$rd/review.attempt-1.stdout" ]] && echo yes)" "yes"
has "and holds the harness's own error" "$(cat "$rd/review.attempt-1.stdout")" "no canned payload"
runlog="$(cat "$rd/run.log")"
has "the log records the failing exit code" "$runlog" "invoke harness=claude attempt=1 exit=1"
has "and the run's fatal exit"              "$runlog" "exit code=1"

# --- a resolve leg logs the commit and the push --------------------------------
rm -rf "$RUNS_BASE"
fixture_repo; stub_reset
review_marker_done() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(( $(date +%s) - 192 ))" --arg a "a1b2c3d4" '
    {v:1, leg:"review", pass:1, state:"complete", ts:$ts, done_ts:($ts + 192), run_id:"1",
     head_sha:$sha, harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     effort:null, endpoint:null, tokens:41205, verdict:"issues-remain",
     findings:[
       {id:$a, path:"app.ts", line:2, side:"RIGHT", severity:"high", category:"correctness",
        pre_existing:false, title:"Unchecked fetch response", why:"w", fix:"f",
        anchor:"", thread_id:"T_A", resolution:null, tracked_as:null}]}'
}
routes_baseline "$(marker_comment 9001 "$(review_marker_done)" | jq -cs . | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9002}'
route '*reviewThreads*' "$(threads_response "$(thread_node T_A app.ts 2 false "a1b2c3d4")")"
route '*resolveReviewThread*' '{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}'
route 'api --method POST repos/*/pulls/42/comments/*/replies*' '{"id":6001}'
RESOLVE_PAYLOAD='{"blocked":false,"blocked_reason":null,"summary":"Checked the response.",
  "resolutions":[{"finding_number":1,"resolution":"fixed","reply":"Checked response.ok.",
                   "persist":null,"duplicate_of":null}]}'
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$RESOLVE_PAYLOAD" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(mktemp)"
printf 'printf "export const ok = 2\\n" >app.ts\n' >"$CROSSREV_RESOLVE_EDIT"
export CROSSREV_RESOLVE_EDIT
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "a resolve leg still passes with logging on" "$rc" "0"
rd="$(find "$RUNS_BASE/acme-widget/pr-42" -mindepth 1 -maxdepth 1 -type d | head -1)"
runlog="$(cat "$rd/run.log")"
has "the worktree creation is logged"  "$runlog" "worktree created"
has "the commit is bracketed"          "$runlog" "commit start branch=feature"
has "and exits clean"                  "$runlog" "commit exit=0"
has "the push is bracketed"            "$runlog" "push start branch=feature"
has "and exits clean"                  "$runlog" "push exit=0"
has "and the worktree removal closes the record" "$runlog" "worktree removed"
is  "and a successful resolve keeps no transcript" \
  "$(find "$rd" -name 'resolve.attempt-*' | wc -l | tr -d ' ')" "0"

finish
