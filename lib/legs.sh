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
# reaches max_passes; a human applies revloop/stop; the addresser returns
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

  [[ "$blocked" == "true" ]] && { printf 'halt the addresser reported blocked'; return 1; }

  # Only Important findings keep the loop alive. Nits and pre-existing findings
  # are reported and cannot prevent convergence.
  [[ "$verdict" == "converged" ]] && { printf 'converged no important findings remain'; return 1; }

  # At exactly the cap, stop. Pass 3 of max_passes 3 is the last pass, not the
  # one after which a fourth begins.
  if (( pass >= max_passes )); then
    printf 'halt reached max_passes (%s)' "$max_passes"; return 1
  fi

  if (( runs_today >= runs_per_day )); then
    printf 'halt reached runs_per_day (%s) in the last 24 hours' "$runs_per_day"; return 1
  fi

  if (( max_files > 0 && files > max_files )); then
    printf 'halt %s files changed, above max_files_changed (%s)' "$files" "$max_files"; return 1
  fi

  printf 'continue issues remain and no cap is reached'
  return 0
}

# Should the addresser act on a nit?
#
# It still replies and resolves either way — nothing is silently dropped — but
# past the configured pass it stops changing code for them.
legs_should_fix_nits() {
  local pass="$1" skip_after="$2"
  (( skip_after <= 0 )) && return 1        # 0 = never fix nits
  (( pass <= skip_after ))
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
  local a_harness="$4" a_endpoint="$5" a_model="$6"
  if [[ "$r_harness" != "$a_harness" || "$r_endpoint" != "$a_endpoint" || "$r_model" != "$a_model" ]]; then
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
  local configured="$1" reviewer_model="$2" addresser_model="$3"
  [[ "$configured" == "different" ]] || return 0          # one model was asked for
  [[ -n "$reviewer_model" && "$reviewer_model" != "null" ]] || return 0
  [[ -n "$addresser_model" && "$addresser_model" != "null" ]] || return 0
  [[ "$reviewer_model" != "$addresser_model" ]] || ui_die \
    "both legs were configured to differ but the same model answered each: $reviewer_model" \
    "This is the failure the cross-model design exists to prevent, and it completes normally when unchecked. Check the endpoint block and that no endpoint variable is exported."
}
