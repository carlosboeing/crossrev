# shellcheck shell=bash
# lib/legs.sh — termination, guards, and the shared shape of both legs.
#
# The decisions here are pure functions over state the orchestrator already
# holds, so they are testable without a network, a harness or a pull request.
# That matters: their failure mode is silence — a loop that stops one pass early
# looks exactly like a loop that converged.

# Should the loop continue? Prints "<decision> <reason>".
#
# Terminates on the first of: the reviewer returns converged; the pass count
# reaches max_passes; a human applies revloop/stop; the resolver returns
# blocked; the daily run cap is exceeded; the PR is larger than the file cap.
#
# $1 verdict, $2 pass, $3 max_passes, $4 has_stop_label, $5 blocked,
# $6 runs_today, $7 runs_per_day, $8 files_changed, $9 max_files_changed
legs_should_continue() {
  local verdict="$1" pass="$2" max_passes="$3" stop="$4" blocked="$5"
  local runs_today="$6" runs_per_day="$7" files="$8" max_files="$9"

  # A human's request outranks everything, including a healthy verdict.
  # Deliberately checked first: revloop/stop is an instruction, not a state.
  [[ "$stop" == "true" ]] && { printf 'halt a human applied revloop/stop'; return 1; }

  [[ "$blocked" == "true" ]] && { printf 'halt the resolver reported blocked'; return 1; }

  # Only findings at or above fix_at keep the loop alive. Anything below it, and
  # anything pre-existing, is reported and cannot prevent convergence.
  [[ "$verdict" == "converged" ]] && { printf 'converged nothing at or above fix_at remains'; return 1; }

  # At exactly the cap, stop. Pass 3 of max_passes 3 is the last pass, not the
  # one after which a fourth begins.
  if (( max_passes > 0 && pass >= max_passes )); then
    printf 'halt reached max_passes (%s)' "$max_passes"; return 1
  fi

  if (( runs_per_day > 0 && runs_today >= runs_per_day )); then
    printf 'halt reached runs_per_day (%s) in the last 24 hours' "$runs_per_day"; return 1
  fi

  if (( max_files > 0 && files > max_files )); then
    printf 'halt %s files changed, above max_files_changed (%s)' "$files" "$max_files"; return 1
  fi

  printf 'continue issues remain and no cap is reached'
  return 0
}

# ---------------------------------------------------------------------------
# The fixing threshold
# ---------------------------------------------------------------------------
#
# `fix_at` names the lowest severity the resolve leg may change code for while
# nobody is watching. Everything below it is still reported, still tabled, and
# still replied to — the threshold governs the commit, never the comment.

# An ordinal rank for a severity word. 0 for anything unrecognised, which cannot
# meet any real threshold.
legs_severity_rank() {
  case "$1" in
    high)   printf '3' ;;
    medium) printf '2' ;;
    low)    printf '1' ;;
    *)      printf '0' ;;
  esac
}

# May the resolve leg change code for this finding?
#
# $1 severity, $2 fix_at, $3 pre_existing ("true" or anything else)
#
# Two rules, in order. A pre-existing defect is never fixed here whatever its
# severity — that is the one guardrail deliberately not configurable, because a
# pull request that also fixes old bugs is one nobody can review. Otherwise the
# severity has to reach the threshold.
#
# An unrecognised threshold fixes nothing rather than guessing. The two ways to
# be wrong are not symmetrical: refusing to fix leaves a finding reported and a
# human to read it, while guessing leaves an unattended commit nobody asked for.
legs_should_fix() {
  local severity="$1" fix_at="$2" pre_existing="${3:-false}"
  [[ "$pre_existing" == "true" ]] && return 1
  local bar; bar="$(legs_severity_rank "$fix_at")"
  (( bar == 0 )) && return 1
  (( $(legs_severity_rank "$severity") >= bar ))
}

# The label a leg waits behind.
#
# Not `revloop/awaiting-$leg`: the leg is named for the verb and the label for
# the noun, so `resolve` waits behind `revloop/awaiting-resolution`. Derived in
# one place because the workflows key off these strings and a mismatch stalls the
# chain silently — the label sits on the pull request with nothing listening.
legs_awaiting_label() {
  case "$1" in
    review)  printf 'revloop/awaiting-review' ;;
    resolve) printf 'revloop/awaiting-resolution' ;;
    *)       printf 'revloop/awaiting-%s' "$1" ;;
  esac
}

# The colour a loop label is minted with.
#
# Six hues, no two adjacent on the wheel, so the label row on a pull request
# carries state at a glance rather than a row of identical purple pills. Red is
# reserved for `revloop/stop`, the one label a human applies — a red pill in a
# pull request list always means somebody pulled the brake, never that the loop
# had trouble.
#
# Dark enough that GitHub renders the label text white. It picks by brightness
# and cannot be told otherwise, so the background is the only lever — and the
# lighter first palette left `revloop/pass-N` at 4.42:1 on a pull request chip,
# under the 4.5:1 the accessibility standard asks for. Every colour here clears
# that in all three renderings GitHub uses: the solid pill on the labels page,
# and the tinted chip in light and dark themes.
#
# One Primer step darker again reads better on the labels page and fails in dark
# mode, where GitHub derives the chip's text from this same hex. That is the wall
# the design hit when it rejected near-black for `stop`, one step further along.
#
# One map rather than a constant per call site. The watchdog mints
# `revloop/halted` itself when it gives up, and a second hex there is how the
# same label ends up two colours depending on which code path created it.
legs_label_colour() {
  case "$1" in
    revloop/awaiting-review)     printf '0969da' ;;   # blue, in progress
    revloop/awaiting-resolution) printf '8250df' ;;   # purple, a leg owes an answer
    revloop/converged)           printf '1a7f37' ;;   # green, finished on its own
    revloop/halted)              printf 'bc4c00' ;;   # orange, a human is needed
    revloop/stop)                printf 'cf222e' ;;   # red, a human applied it
    revloop/pass-*)              printf '57606a' ;;   # grey, informational
    # Not a loop state and not one of the six: the watchdog's own bookkeeping,
    # which reads as a qualifier on whatever state it sits beside.
    revloop/watchdog-retried)    printf 'fbca04' ;;
    *)                           printf 'ededed' ;;
  esac
}

# What the label means, in the words the design uses for it.
#
# Per label rather than one string for all of them, because a label description
# is the only place GitHub shows a reader what a label means without them going
# looking — it is the hover text on the pill and the second column on the labels
# page. "revloop loop state" on all six answered nothing, and the colours are
# the only other signal a reader has.
#
# One map rather than a constant per call site, for the same reason the colours
# are one map: the watchdog mints `revloop/halted` itself when it gives up, and a
# second string there is how the same label ends up described two ways depending
# on which code path created it.
legs_label_description() {
  case "$1" in
    revloop/awaiting-review)     printf 'revloop: a review is owed on this pull request' ;;
    revloop/awaiting-resolution) printf 'revloop: the review landed, the resolve leg is owed' ;;
    revloop/converged)           printf 'revloop: the loop finished on its own' ;;
    revloop/halted)              printf 'revloop: stopped short, a human is needed' ;;
    revloop/stop)                printf 'revloop: apply this to stop the loop' ;;
    revloop/pass-*)              printf 'revloop: reached pass %s' "${1##*-}" ;;
    revloop/watchdog-retried)    printf 'revloop: the watchdog retried this leg once' ;;
    *)                           printf 'revloop loop state' ;;
  esac
}

# Refuse to push anywhere except the PR's own head branch.
#
# Branch protection is a backstop, not a control: it fires after a bad push is
# attempted and says nothing about which branch was targeted. This asserts the
# target before anything leaves the machine.
legs_assert_push_target() {
  local current_branch="$1" head_branch="$2" default_branch="$3" head_repo="$4" origin_repo="$5"

  [[ "$current_branch" == "$head_branch" ]] || ui_die \
    "the checkout is on '$current_branch' but the pull request's head branch is '$head_branch'" \
    "revloop pushes only to the branch under review. Check out $head_branch first."

  [[ "$head_branch" != "$default_branch" ]] || ui_die \
    "the pull request's head branch is the repository default branch ('$default_branch')" \
    "revloop refuses to push to a default branch. Re-open the pull request from a feature branch."

  [[ "$head_repo" == "$origin_repo" ]] || ui_die \
    "the pull request's head is in '$head_repo' but this checkout pushes to '$origin_repo'" \
    "revloop pushes only to the head repository of the pull request under review."

  return 0
}

# Assert the two legs will actually run differently.
#
# Layer one, always available: the orchestrator knows exactly what it invoked,
# so it asserts the legs differ in binary, resolved base URL or model — and that
# the endpoint variables are unset in the inherited environment, which is the
# specific failure this exists for. Leakage from one leg into the next is then
# caught exactly, with no cooperation needed from any vendor.
legs_assert_env_clean() {
  local leaked=()
  [[ -n "${ANTHROPIC_BASE_URL:-}" ]] && leaked+=("ANTHROPIC_BASE_URL")
  [[ -n "${ANTHROPIC_AUTH_TOKEN:-}" ]] && leaked+=("ANTHROPIC_AUTH_TOKEN")
  (( ${#leaked[@]} == 0 )) && return 0
  ui_die "these endpoint variables are set in the environment revloop inherited: ${leaked[*]}" \
    "They redirect the harness process-wide, so a leg would silently run on the wrong model and the loop would complete normally with no error anywhere. Unset them; revloop sets them per invocation."
}

# Layer one's other half: did the two legs differ in anything the orchestrator
# controls? Prints "same" or "different".
legs_configured_difference() {
  local r_harness="$1" r_endpoint="$2" r_model="$3"
  local s_harness="$4" s_endpoint="$5" s_model="$6"
  if [[ "$r_harness" != "$s_harness" || "$r_endpoint" != "$s_endpoint" || "$r_model" != "$s_model" ]]; then
    printf 'different'
  else
    printf 'same'
  fi
}

# Layer two, where the harness reports it: compare answering models.
#
# Do not halt merely because a harness reports none — that would disqualify the
# codex adapter for a field Codex does not emit. What this adds is detection of
# *server-side* substitution; where it is unavailable the marker records its
# absence rather than implying a check that never ran.
legs_assert_models_diverged() {
  local configured="$1" reviewer_model="$2" resolver_model="$3"
  [[ "$configured" == "different" ]] || return 0          # one model was asked for
  [[ -n "$reviewer_model" && "$reviewer_model" != "null" ]] || return 0
  [[ -n "$resolver_model" && "$resolver_model" != "null" ]] || return 0
  [[ "$reviewer_model" != "$resolver_model" ]] || ui_die \
    "both legs were configured to differ but the same model answered each: $reviewer_model" \
    "This is the failure the cross-model design exists to prevent, and it completes normally when unchecked. Check the endpoint block and that no endpoint variable is exported."
}
