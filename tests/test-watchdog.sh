#!/usr/bin/env bash
#
# The watchdog.
#
# Event-driven mode's failure mode is silence: a dropped label event and a
# converged pull request look identical from outside, so something has to go
# looking. One retry, then halt — a dropped event is fixed by re-firing it, and a
# second failure is not a dropped event.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

CROSSREV_APP_SLUG=crossrev-acme; export CROSSREV_APP_SLUG

waiting_prs() {
  local labels="$1" draft="${2:-false}"
  jq -cn --argjson l "$labels" --arg h "$FIX_HEAD" --argjson d "$draft" \
    '[{number:42, labels:$l, head:{sha:$h}, draft:$d}]'
}

routes_watchdog() {
  local prs="$1" comments="$2"
  route 'repo view --json nameWithOwner*' "{\"nameWithOwner\":\"$FIX_REPO\"}"
  route 'api repos/*/pulls?state=open*' "$prs"
  route "api --paginate repos/*/issues/42/comments*" "@$comments"
  route 'api repos/*/labels/*' '{"name":"x"}'
}

# --- a leg still inside its timeout is left alone -------------------------
fixture_repo; stub_reset
fresh="$(jq -cn --argjson ts "$(date +%s)" --arg sha "$FIX_HEAD" '
  {v:1, leg:"review", pass:1, state:"started", ts:$ts, run_id:"1", head_sha:$sha,
   harness:"claude", model:"m", model_reported:"m", verdict:null, findings:[]}')"
routes_watchdog "$(waiting_prs '[{"name":"crossrev/awaiting-review"}]')" \
  "$(marker_comment 9001 "$fresh" "$FIX_APP" | jq -cs . | payload)"
out="$("$CROSSREV" watchdog 2>&1)"; rc=$?

is  "the watchdog exits clean"                    "$rc" "0"
has "a leg inside its timeout is reported, not touched" "$out" "inside the 30-minute timeout"
is  "and nothing is retried"                      "$(count 'method DELETE')" "0"
has "the summary counts what it looked at"        "$out" "checked 1 pull request(s) waiting on a leg — retried 0, halted 0"

# --- a leg past its timeout is retried exactly once ---------------------
#
# Re-applying a label GitHub already holds fires no event, so the retry has to
# remove it first. That is the whole mechanism.
fixture_repo; stub_reset
stuck="$(jq -c --argjson ts "$(( $(date +%s) - 3600 ))" '.ts = $ts' <<<"$fresh")"
routes_watchdog "$(waiting_prs '[{"name":"crossrev/awaiting-resolution"}]')" \
  "$(marker_comment 9001 "$stuck" "$FIX_APP" | jq -cs . | payload)"
out="$("$CROSSREV" watchdog 2>&1)"

has "a stuck leg is named with how long it has been stuck" "$out" "for 60 minutes, past the 30-minute timeout"
has "the retry removes the label first"          "$(calls)" "method DELETE repos/acme/widget/issues/42/labels/crossrev/awaiting-resolution"
has "and re-applies it to re-fire the event"     "$(calls)" "labels[]=crossrev/awaiting-resolution"
has "the retry is recorded on the pull request so it happens only once" \
  "$(calls)" "labels[]=crossrev/watchdog-retried"
has "the summary counts the retry"               "$out" "retried 1, halted 0"
is  "and nothing is halted yet"                  "$(count 'labels\[\]=crossrev/halted')" "0"

# --- a second failure halts and says why -------------------------------
fixture_repo; stub_reset
routes_watchdog "$(waiting_prs '[{"name":"crossrev/awaiting-resolution"},{"name":"crossrev/watchdog-retried"}]')" \
  "$(marker_comment 9001 "$stuck" "$FIX_APP" | jq -cs . | payload)"
out="$("$CROSSREV" watchdog 2>&1)"

has "an already-retried leg is halted"           "$out" "halted — it had already been retried once"
has "and the pull request is labelled halted"    "$(calls)" "labels[]=crossrev/halted"
has "and a comment says the loop stopped rather than converged" \
  "$(calls)" "it did not converge"
has "and names the command to look at it"        "$(calls)" "crossrev status --pr 42"
has "the summary counts the halt"                "$out" "retried 0, halted 1"

# --- a leg that never started at all -----------------------------------
fixture_repo; stub_reset
routes_watchdog "$(waiting_prs '[{"name":"crossrev/awaiting-review"}]')" "$(printf '[]' | payload)"
out="$("$CROSSREV" watchdog 2>&1)"
has "a label with no marker behind it means the leg never started" \
  "$out" "with no marker at all, so it never started"
has "and it is retried"                          "$out" "retried 1, halted 0"

# --- crossrev/stop is honoured here too ---------------------------------
fixture_repo; stub_reset
routes_watchdog "$(waiting_prs '[{"name":"crossrev/awaiting-resolution"},{"name":"crossrev/stop"}]')" \
  "$(marker_comment 9001 "$stuck" "$FIX_APP" | jq -cs . | payload)"
out="$("$CROSSREV" watchdog 2>&1)"
is  "a pull request a human stopped is not retried" "$(count 'method DELETE')" "0"
has "and the summary says nothing was acted on"     "$out" "retried 0, halted 0"

# --- a draft is waiting on its author, not on a leg --------------------
#
# The review workflow's job condition skips a draft, and ctx_load refuses one on
# any automatic invocation. So the label sits there and no event will move it.
# Retrying re-fires the label into the same refusal, and the sweep after that
# reports "already retried once and is still not finishing" about a leg that
# never started. The watchdog says the true reason instead (#122).
fixture_repo; stub_reset
routes_watchdog "$(waiting_prs '[{"name":"crossrev/awaiting-review"}]' true)" \
  "$(marker_comment 9001 "$stuck" "$FIX_APP" | jq -cs . | payload)"
out="$("$CROSSREV" watchdog 2>&1)"; rc=$?

is  "the watchdog exits clean over a draft"        "$rc" "0"
has "a draft is named as the reason no leg runs"   "$out" "a draft pull request, so no automatic review leg runs on it"
has "and the recovery is spelled out"              "$out" "mark it ready for review"
is  "and nothing is retried"                       "$(count 'method DELETE')" "0"
is  "and nothing is halted"                        "$(count 'labels\[\]=crossrev/halted')" "0"
has "the summary counts it apart from the rest"    "$out" "retried 0, halted 0, 1 in draft"

# The same for the resolve leg, which is the half that pushes: a draft reviewed
# by hand leaves crossrev/awaiting-resolution behind, and no workflow clears it.
fixture_repo; stub_reset
routes_watchdog "$(waiting_prs '[{"name":"crossrev/awaiting-resolution"}]' true)" \
  "$(marker_comment 9001 "$stuck" "$FIX_APP" | jq -cs . | payload)"
out="$("$CROSSREV" watchdog 2>&1)"
has "a draft awaiting resolution is named the same way" "$out" "no automatic resolve leg runs on it"
is  "and it is not retried either"                      "$(count 'method DELETE')" "0"

# An already-retried draft is still not halted: the halt comment would say the
# leg is "still not finishing", and the leg has not started.
fixture_repo; stub_reset
routes_watchdog "$(waiting_prs '[{"name":"crossrev/awaiting-review"},{"name":"crossrev/watchdog-retried"}]' true)" \
  "$(marker_comment 9001 "$stuck" "$FIX_APP" | jq -cs . | payload)"
out="$("$CROSSREV" watchdog 2>&1)"
is  "a draft retried by an older version is not then halted" "$(count 'labels\[\]=crossrev/halted')" "0"
hasnt "and nothing claims the leg is still not finishing"    "$(calls)" "still not finishing"

# --- a forged marker does not fool the watchdog -----------------------
#
# The watchdog is the component a forged "state":"complete" would most like to
# mislead, so it reads markers under the same trusted-author rule as everything
# else.
fixture_repo; stub_reset
routes_watchdog "$(waiting_prs '[{"name":"crossrev/awaiting-resolution"}]')" \
  "$(marker_comment 9001 "$(jq -c '.state = "complete"' <<<"$stuck")" "$FIX_USER" | jq -cs . | payload)"
out="$("$CROSSREV" watchdog 2>&1)"
has "a marker authored by anyone but the App is treated as absent" \
  "$out" "with no marker at all, so it never started"

finish
