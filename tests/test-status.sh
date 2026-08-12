#!/usr/bin/env bash
#
# `revloop status` — the five header words, every pass, and what to type next.
#
# Two properties carry the whole display, and both fail quietly if broken.
#
# **The header word is the label word.** One to one with INIT_FIXED_LABELS, so
# someone who learns the labels on GitHub already knows the terminal's words, and
# the header stops being computed independently of the label it duplicates. A
# sixth word would be a place for the two to disagree.
#
# **A leg that has not finished is described from evidence, never from its age.**
# The marker cannot tell a leg the usage limit killed after forty seconds from
# one working happily for forty seconds — it posts before the harness is invoked
# and survives the cleanup path — so neither "still running" nor "never finished"
# may be read off the clock. What can be read is the lock file a local run leaves
# in the git dir and the workflow status an automated one's run id buys, and the
# rows below only make a liveness claim where one of those answers. Where neither
# does, the row says how long ago the leg started and stops there.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

# A review marker. $1 pass, $2 verdict, $3 findings, $4 state, $5 age in seconds,
# $6 run_id.
#
# The run_id defaults to something no liveness check can read, which is the
# honest default for a fixture: it is what a status with no evidence either way
# renders from, and the cases that do have evidence say so by passing one.
review_m() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(( $(date +%s) - ${5:-60} ))" \
    --argjson p "$1" --arg v "$2" --argjson f "$3" --arg st "${4:-complete}" \
    --arg r "${6:-x}" '
    {v:1, leg:"review", pass:$p, state:$st, ts:$ts, done_ts:($ts + 30), run_id:$r,
     head_sha:$sha, harness:"claude", model:"m", effort:"high", endpoint:null,
     model_reported:"m", tokens:1000, verdict:$v, findings:$f}'
}

# A review marker whose head_sha is not the pull request's, so it is stale for
# the other reason: the branch moved underneath it.
moved_m() {
  review_m 1 null '[]' started 120 | jq -c '.head_sha = "0000000000000000000000000000000000000000"'
}

# A resolve marker. $1 pass, $2 dispositions, $3 commit sha, $4 blocked reason.
resolve_m() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(( $(date +%s) - 60 ))" \
    --argjson p "$1" --argjson d "$2" --arg c "${3:-}" --arg br "${4:-}" '
    {v:1, leg:"resolve", pass:$p, state:"complete", ts:$ts, done_ts:($ts + 30), run_id:"x",
     head_sha:$sha, harness:"claude", model:"m", effort:"high", endpoint:null,
     model_reported:"m", tokens:2000,
     blocked:($br != ""), blocked_reason:(if $br == "" then null else $br end),
     commit_sha:(if $c == "" then null else $c end),
     summary:"s", dispositions:$d}'
}

# A pass a cap refused to start: a comment and a label are not machine-readable,
# so the refusal writes a marker too, and that marker records a pass that did not
# happen.
declined_m() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(date +%s)" --argjson p "$1" --arg why "$2" '
    {v:1, leg:"review", pass:$p, state:"declined", ts:$ts, done_ts:$ts, run_id:"x",
     head_sha:$sha, harness:null, model:null, effort:null, endpoint:null,
     model_reported:null, tokens:null, verdict:"declined", reason:$why, findings:[]}'
}

HIGH_LOW='[{"severity":"high"},{"severity":"high"},{"severity":"low"}]'
ONE_MED='[{"severity":"medium"}]'
FIXED_SKIPPED='[{"disposition":"fixed"},{"disposition":"fixed"},{"disposition":"skipped"}]'
ONE_FIXED='[{"disposition":"fixed"}]'

# $1 names a function to run inside the fixture checkout after the routes are in
# place and before status runs — a lock file, an extra route — or is empty for
# the cases that need nothing. $2 is the labels array, then any number of marker
# JSON strings.
#
# The setup is an argument rather than a variable a case sets beforehand, because
# every call here happens inside `$( )`: a variable the function cleared at the
# end would be cleared in the subshell, and the next case would silently inherit
# the last one's evidence.
status_setup_with() {
  local setup="$1" labels="$2"; shift 2
  local comments="[]" id=9000 m
  for m in "$@"; do
    id=$(( id + 1 ))
    comments="$(jq -c --argjson c "$(marker_comment "$id" "$m")" '. + [$c]' <<<"$comments")"
  done
  fixture_repo; stub_reset
  routes_baseline "$(printf '%s' "$comments" | payload)" "$labels"
  [[ -n "$setup" ]] && "$setup"
  "$REVLOOP" status --pr 42 2>&1
}

status_with() { status_setup_with "" "$@"; }

# The lock a local run leaves in the git dir while it works, in the format
# run_lock_acquire writes: `<pid> on <host> since <timestamp>`. $1 is the pid,
# $2 the host, defaulting to this machine's.
write_lock() {
  local dir; dir="$(git rev-parse --git-dir)/revloop"
  mkdir -p "$dir"
  printf '%s on %s since 2026-08-12T00:00:00Z\n' \
    "$1" "${2:-$(hostname 2>/dev/null || printf 'local')}" >"$dir/pr-42.lock"
}
lock_alive()     { write_lock "$$"; }                  # this test process, which is running
lock_dead()      { write_lock 999999; }                # a pid nothing is behind
lock_elsewhere() { write_lock "$$" buildbox; }         # readable here, testable only there

# What the Actions API says about the workflow run a marker names.
run_in_progress() { route_first 'run view 55501 --repo acme/widget --json status*' '{"status":"in_progress"}'; }
run_completed()   { route_first 'run view 55501 --repo acme/widget --json status*' '{"status":"completed"}'; }

lbl() { jq -cn --args '[$ARGS.positional[] | {name: .}]' "$@"; }

# --- a three-pass converged loop, which is the shape the design draws -------
out="$(status_with "$(lbl revloop/converged revloop/pass-3)" \
  "$(review_m 1 issues-remain "$HIGH_LOW")" \
  "$(resolve_m 1 "$FIXED_SKIPPED")" \
  "$(review_m 2 issues-remain "$ONE_MED")" \
  "$(resolve_m 2 "$ONE_FIXED" d81a3f2abc)" \
  "$(review_m 3 converged '[]')")"

has "the verdict is in the header, before anything to parse" "$out" "acme/widget#42 — converged"
has "the pull request is identifiable"          "$out" "title      Add refresh"
has "and reachable"                             "$out" "url        https://github.com/x"
has "the loop section says where it got to"     "$out" "passes     3 of 3"

# Every pass, not only the current one. An earlier draft had a PASS N detail block
# plus a separate HISTORY summary, which was two renderings of the same data.
has "pass 1 lists what the review found"        "$out" "1  ✓ review   2 high, 1 low"
has "and what the resolve leg did with it"      "$out" "✓ resolve  2 fixed, 1 skipped"
has "pass 2 as well"                            "$out" "2  ✓ review   1 medium"
has "including the commit it pushed"            "$out" "✓ resolve  1 fixed, pushed d81a3f2"
has "and pass 3, the one that converged"        "$out" "3  ✓ review   no findings — converged"
has "whose resolve leg was never owed"          "$out" "○ resolve  not needed, the review converged"

# The terminal and the pull request have to agree in wording, so a reader moving
# between them is not translating between two descriptions of one state.
has "NEXT says the loop finished, in the summary comment's own words" \
  "$out" "the loop converged on pass 3: nothing at or"
has "naming the threshold that let it"          "$out" "above min_fix_severity (medium) remains"

# --- awaiting review, with nothing run yet ----------------------------------
out="$(status_with '[]')"
has "an untouched pull request reads as awaiting review" "$out" "acme/widget#42 — awaiting review"
has "and says so on the passes line too"        "$out" "passes     none yet, up to 3"
# A heading with an empty body reads as a bug, and the line above already carries
# the information a `not started` header word would have duplicated.
hasnt "the PASSES section is omitted rather than printed empty" "$out" "PASSES"
has "NEXT is a command"                         "$out" "revloop review --pr 42"

# --- awaiting resolution ----------------------------------------------------
out="$(status_with "$(lbl revloop/awaiting-resolution revloop/pass-1)" \
  "$(review_m 1 issues-remain "$HIGH_LOW")")"
has "a landed review with findings owes the resolve leg" \
  "$out" "acme/widget#42 — awaiting resolution"
has "and NEXT is the command that runs it"      "$out" "revloop resolve --pr 42"
has "with the resolve leg shown as still owed"  "$out" "○ resolve  not run yet"

# The leg the loop is waiting on is the one most likely to be running while
# someone reads this, which is the case the issue was filed from: the row said
# the resolve leg had failed and the pull request said it was working. Both lines
# have to agree with the process now, not only the row.
resolve_started() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(( $(date +%s) - 420 ))" --arg r "local-$$" '
    {v:1, leg:"resolve", pass:1, state:"started", ts:$ts, done_ts:null, run_id:$r,
     head_sha:$sha, harness:"claude", model:"m", effort:"high", endpoint:null,
     model_reported:null, tokens:null, blocked:false, blocked_reason:null,
     commit_sha:null, summary:"", dispositions:[]}'
}
out="$(status_setup_with lock_alive "$(lbl revloop/awaiting-resolution revloop/pass-1)" \
  "$(review_m 1 issues-remain "$HIGH_LOW")" "$(resolve_started)")"
has   "a running resolve leg says so on its row"  "$out" "◐ resolve  running now — started 7 minute(s) ago"
has   "and NEXT stops reading as an invitation to start a second one" \
  "$out" "Pass 1 is running now, so wait for it"
has   "while still ending in the command that resumes it if that run dies" \
  "$out" "revloop resolve --pr 42"

# --- an unfinished leg, and what status can actually prove about it ---------
#
# The row reports liveness, and reports it only from evidence it can name: the
# lock file a local run leaves in the git dir, or the Actions API for a run that
# carries a real GITHUB_RUN_ID. The red cross belongs to the cases where the leg
# is provably over, and to no others — the whole point being that a leg still
# working is not a leg that failed.

# Nothing to go on: an unreadable run_id, no lock, inside the window. The row
# says how long ago it started and that no result has landed, which is the whole
# of what is known, and it carries neither verdict glyph.
out="$(status_with "$(lbl revloop/awaiting-review revloop/watchdog-retried revloop/pass-2)" \
  "$(review_m 1 issues-remain "$HIGH_LOW")" \
  "$(resolve_m 1 "$FIXED_SKIPPED")" \
  "$(review_m 2 null '[]' started 2820)")"

has   "an unfinished leg with no evidence either way shows its age" \
  "$out" "started 47 minute(s) ago, no result yet"
hasnt "and is not called a failure"             "$out" "✗ review"
hasnt "nor claimed to be running"               "$out" "running now"
has   "the retry is a qualifier on the header, read straight off its label" \
  "$out" "awaiting review (retried once)"
has   "and NEXT warns that a second failure halts" "$out" "a second failure"

# The word is "started", not "claimed". A leg posts a marker before it works so a
# second runner does not duplicate it, which is a claim in the code and a lease
# in the design — and neither is a distinction the reader of a status line has to
# make.
hasnt "no unfinished row talks about a claim" "$out" "claimed"
hasnt "and none of them says never finished"  "$out" "never finished"

# Alive: the marker names a pid and the lock file says that pid, on this host, is
# running against this pull request. Both have to agree before the row will say
# so — a pid alone is recycled, and every machine has its own.
out="$(status_setup_with lock_alive "$(lbl revloop/awaiting-review revloop/pass-1)" \
  "$(review_m 1 null '[]' started 420 "local-$$")")"
has   "a leg whose process answers is reported as running"  "$out" "◐ review   running now — started 7 minute(s) ago"
hasnt "and never as a failure"                              "$out" "✗ review"
has   "NEXT says to wait for it"                            "$out" "Pass 1 is running now, so wait for it"
hasnt "rather than inviting a second run over the same pull request" \
  "$out" "a re-run resumes pass 1"

# Dead, two minutes in. The lock is the same evidence read the other way, and it
# is definitive well inside the window: the process that took the lock is gone
# and the marker never completed.
out="$(status_setup_with lock_dead "$(lbl revloop/awaiting-review revloop/pass-1)" \
  "$(review_m 1 null '[]' started 120 "local-999999")")"
has "a leg whose process is gone is a failure, however recent" \
  "$out" "✗ review   started 2 minute(s) ago, abandoned — the process that started it is gone"

# On another machine. The lock is readable — a checkout on a shared filesystem —
# but the pid in it is not this machine's to test, so the row names where the run
# is instead of guessing whether it lives.
out="$(status_setup_with lock_elsewhere "$(lbl revloop/awaiting-review revloop/pass-1)" \
  "$(review_m 1 null '[]' started 420 "local-$$")")"
has   "a lock on another host names the host"   "$out" "○ review   started 7 minute(s) ago on buildbox"
hasnt "and claims nothing about the process"    "$out" "running now"
hasnt "least of all that it failed"             "$out" "✗ review"

# An automated leg answers from anywhere, because the marker carries the real
# workflow run id. The run_id's shape is what picks the check, not the configured
# mode: a marker written on a runner reads the same from a laptop.
out="$(status_setup_with run_in_progress "$(lbl revloop/awaiting-review revloop/pass-1)" \
  "$(review_m 1 null '[]' started 420 55501)")"
has "a workflow run still in progress is reported as running" \
  "$out" "◐ review   running now — started 7 minute(s) ago"

out="$(status_setup_with run_completed "$(lbl revloop/awaiting-review revloop/pass-1)" \
  "$(review_m 1 null '[]' started 120 55501)")"
has "a workflow that ended without completing the leg is a failure" \
  "$out" "✗ review   started 2 minute(s) ago, abandoned — the workflow run finished without it"

# The API not answering is not evidence of death. A run in another repository, a
# token without actions:read and an aeroplane all look the same from here, and
# none of them says the leg failed.
out="$(status_with "$(lbl revloop/awaiting-review revloop/pass-1)" \
  "$(review_m 1 null '[]' started 120 55501)")"
has   "an unanswerable workflow read falls back to what is known" \
  "$out" "started 2 minute(s) ago, no result yet"
hasnt "and never turns an unreachable API into a dead leg" "$out" "✗ review"

# Past the window with nothing to check, the age is the evidence and the claim is
# written off. The reason says so in its own words rather than the row repeating
# the age it already carries.
out="$(status_with "$(lbl revloop/awaiting-review revloop/pass-1)" \
  "$(review_m 1 null '[]' started 7200)")"
has "an unfinished leg past the window is abandoned" \
  "$out" "✗ review   abandoned — it was made 120 minutes ago, past the 60-minute window"
has "and NEXT says a re-run starts the pass again" "$out" "so a re-run abandons it"

# Stale for the other reason: the branch moved under it, so resuming would
# reconcile against a revision the findings no longer describe.
out="$(status_with "$(lbl revloop/awaiting-review revloop/pass-1)" "$(moved_m)")"
has   "a claim against a revision that moved is abandoned too" \
  "$out" "✗ review   abandoned — it started against 0000000 and the pull request is now at"
hasnt "and says it started against that revision rather than claimed it" \
  "$out" "it claimed 0000000"

# Nothing to check, inside the window: a re-run resumes rather than restarts, and
# that is still what NEXT says.
out="$(status_with "$(lbl revloop/awaiting-review revloop/pass-1)" \
  "$(review_m 1 null '[]' started 120)")"
has "NEXT says a re-run resumes a pass nothing contradicts" "$out" "a re-run resumes pass 1"

# --- the cap is reached, and no pass has been refused yet -------------------
#
# The state between "the last pass finished" and "a refused pass recorded a
# declined marker". A review is genuinely owed — the loop hands back to the
# reviewer after every resolve leg — and the bound stops the loop starting one
# by itself, not a person asking for one. So NEXT has to describe the state
# without telling the reader their own command would be refused: `revloop review
# --pr N` typed by hand runs a pass past the bound, and only an automatic
# trigger or a cycle's generated pass meets it.
out="$(status_with "$(lbl revloop/awaiting-review revloop/pass-3)" \
  "$(review_m 1 issues-remain "$HIGH_LOW")" \
  "$(resolve_m 1 "$FIXED_SKIPPED")" \
  "$(review_m 2 issues-remain "$ONE_MED")" \
  "$(resolve_m 2 "$ONE_FIXED" d81a3f2abc)" \
  "$(review_m 3 issues-remain "$ONE_MED")" \
  "$(resolve_m 3 "$ONE_FIXED" c02b418def)")"

has "the pass at the bound says the loop stops there on its own" \
  "$out" "pass 3 reached max_passes_per_cycle (3)"
has "and that the bound stops the loop, not the reader" \
  "$out" "Asking for one pass"
hasnt "so nothing claims the command below would be refused"     "$out" "refused rather than run"
has "the condition that has to change comes before the command"  "$out" "Raise policy.max_passes_per_cycle in"
has "and NEXT still ends in something you can type"              "$out" "revloop review --pr 42"
hasnt "nothing invites a pass beyond the cap automatically"      "$out" "so pass 4 reviews"

# Below the cap the invitation is correct and must survive the guard above.
out="$(status_with "$(lbl revloop/awaiting-review revloop/pass-1)" \
  "$(review_m 1 issues-remain "$HIGH_LOW")" \
  "$(resolve_m 1 "$FIXED_SKIPPED" d81a3f2abc)")"
has "a pass below the cap still points at the next review" "$out" "revloop review --pr 42"
hasnt "and says nothing about max_passes_per_cycle"        "$out" "max_passes_per_cycle"

# --- halted: a cap stopped the next pass before it began --------------------
#
# Caps are evaluated when a review leg decides whether the NEXT pass may begin, so
# the halt attaches to that review leg — which is literally what happened. A cap
# therefore never halts at a resolve leg; it cannot, given where the check sits.
out="$(status_with "$(lbl revloop/halted revloop/pass-2)" \
  "$(review_m 1 issues-remain "$HIGH_LOW")" \
  "$(resolve_m 1 "$FIXED_SKIPPED")" \
  "$(review_m 2 issues-remain "$ONE_MED")" \
  "$(resolve_m 2 "$ONE_FIXED" d81a3f2abc)" \
  "$(declined_m 3 'reached max_prs_per_day (25) — 25 other pull requests were already reviewed in the last 24 hours')")"

has "a cap halt reads as halted"                "$out" "acme/widget#42 — halted"
has "attached to the review leg that refused to start" \
  "$out" "3  ✗ review   never started — reached max_prs_per_day"
has "with its resolve leg shown as never run"   "$out" "○ resolve  not run"
has "NEXT says what the cap cost"               "$out" "So anything pass 2 changed is unverified"
has "and gives the lever"                       "$out" "Raise the cap in"
has "then the command that follows it"          "$out" "revloop review --pr 42"

# The refused pass records a pass that did not happen, so it must not count as
# one. Counting it would report pass 3 as current, and raising the cap and
# re-running would then answer "already reviewed at this revision".
has "the refused pass does not become the current one" "$out" "passes     2 of 3"

# --- halted: a cap refused the very first pass ------------------------------
#
# The same exclusion that keeps a refused pass out of the numbering leaves this
# case with no preceding pass at all, so the current pass is 0 — and the wording
# above would then warn that "anything pass 0 changed is unverified". No such
# pass exists, nothing was reviewed, and nothing was changed.
out="$(status_with "$(lbl revloop/halted revloop/pass-1)" \
  "$(declined_m 1 'reached max_files_changed_per_pr (200)')")"

has   "a cap refusing the first pass still reads as halted" "$out" "acme/widget#42 — halted"
has   "with the refusal on the leg that made it"  "$out" "1  ✗ review   never started — reached max_files_changed_per_pr"
has   "the loop section says nothing has run"     "$out" "passes     none yet, up to 3"
has   "NEXT names the pass that never began"      "$out" "pass 1 never began"
has   "and says no review ran rather than warning about a pass that never existed" \
  "$out" "No review has run on this pull request at all"
hasnt "so nothing refers to pass zero"            "$out" "pass 0"
has   "the lever is still the cap"                "$out" "Raise the cap in"

# --- halted: a leg reported blocked -----------------------------------------
out="$(status_with "$(lbl revloop/halted revloop/pass-2)" \
  "$(review_m 1 issues-remain "$HIGH_LOW")" \
  "$(resolve_m 1 "$FIXED_SKIPPED")" \
  "$(review_m 2 issues-remain "$ONE_MED")" \
  "$(resolve_m 2 "$ONE_FIXED" '' 'one fix needs a schema migration nobody has approved')")"

has "a blocked leg reads as halted, the same word and the same label" \
  "$out" "acme/widget#42 — halted"
has "attached to the leg that reported it"      "$out" "✗ resolve  blocked — one fix needs a schema migration"
has "and NEXT names the remedy"                 "$out" "Once that is settled"
has "with the command"                          "$out" "revloop resolve --pr 42"

# Today's output prints a green tick for any leg that reached `complete` whatever
# its verdict, so a review that came back blocked looked identical to one that
# converged — green for "the reviewer gave up".
out="$(status_with "$(lbl revloop/halted revloop/pass-1)" \
  "$(review_m 1 blocked '[]')")"
has "a blocked review gets the red glyph, not the green one" "$out" "✗ review   blocked"

# --- halted: a finding was escalated, with no labels to read it off ----------
#
# Escalation is the one halt the markers have to answer on their own. Locally a
# label that will not apply is a warning rather than a fatal, so a repository
# that never ran `revloop init` runs the loop with no labels on it at all — and
# there the header comes from the markers. A resolve pass that escalated is
# complete and not blocked, so reading only `blocked` would answer "awaiting
# review" and send the reader to start a pass that settles nothing.
ESCALATED='[{"disposition":"fixed"},{"disposition":"escalated"}]'
out="$(status_with '[]' \
  "$(review_m 1 issues-remain "$HIGH_LOW")" \
  "$(resolve_m 1 "$ESCALATED")")"

has "an escalated disposition halts the loop even with no labels to read" \
  "$out" "acme/widget#42 — halted"
hasnt "and does not hand the loop back to the reviewer as though it were owed" \
  "$out" "— awaiting review"
has "NEXT names the pending decision"           "$out" "1 finding need"
has "and where the reasoning is"                "$out" "left the"
has "then the command that follows settling it" "$out" "revloop review --pr 42"

# The row has to agree with the header above it. A completed resolve marker gets
# the tick for reaching `complete`, which is the wrong question: this one halted
# the loop for a human, and a green leg line under a halted header reads as a
# healthy pass.
has   "the escalated leg carries the failure glyph" "$out" "✗ resolve  1 fixed, 1 escalated"
hasnt "and never the tick a settled pass gets"      "$out" "✓ resolve  1 fixed, 1 escalated"

# With the labels applied, the stop the resolve leg put on outranks the halt
# beside it — and the two paths have to agree that this is not a review owed.
out="$(status_with "$(lbl revloop/halted revloop/stop revloop/pass-1)" \
  "$(review_m 1 issues-remain "$HIGH_LOW")" \
  "$(resolve_m 1 "$ESCALATED")")"
has "the labelled path reads the stop the escalation applied" "$out" "acme/widget#42 — stopped"
hasnt "and still does not read as a review owed"              "$out" "— awaiting review"

# --- stopped by a human -----------------------------------------------------
#
# Its own word rather than folding into halted, because the remedy differs.
# Halted means raise a cap or take over. Stopped means remove a label somebody
# applied on purpose.
out="$(status_with "$(lbl revloop/stop revloop/awaiting-resolution revloop/pass-1)" \
  "$(review_m 1 issues-remain "$HIGH_LOW")")"

has "a stop request outranks the awaiting label beside it" "$out" "acme/widget#42 — stopped"
has "and NEXT starts with removing the label"   "$out" "gh pr edit 42 --remove-label revloop/stop"
has "then the leg that was owed when the brake went on" "$out" "revloop resolve --pr 42"
has "the resolve leg says why it did not run"   "$out" "○ resolve  not run — revloop/stop is applied"

# --- the header word is one of exactly five ---------------------------------
#
# A sixth word would be a place for the terminal and the label to disagree, which
# is the thing reading the header off the label was meant to stop.
for pair in \
  "revloop/awaiting-review:awaiting review" \
  "revloop/awaiting-resolution:awaiting resolution" \
  "revloop/converged:converged" \
  "revloop/halted:halted" \
  "revloop/stop:stopped"; do
  label="${pair%%:*}"; word="${pair#*:}"
  out="$(status_with "$(lbl "$label")" "$(review_m 1 issues-remain "$ONE_MED")")"
  has "$label reads as '$word'" "$out" "acme/widget#42 — $word"
done

# `interrupted` was dropped entirely: an interrupted review still leaves a review
# owed, and NEXT still prints the same command, so it was a sixth word that
# changed nothing about what to do.
out="$(status_with '[]' "$(review_m 1 null '[]' started 120)")"
hasnt "there is no interrupted header word"     "$out" "— interrupted"
has "the state is what is owed, not how it came to be owed" "$out" "— awaiting review"

finish
