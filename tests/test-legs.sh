#!/usr/bin/env bash
#
# Termination and guard tests.
#
# A loop that stops one pass early looks exactly like a loop that converged, and
# a push guard that lets one case through is not a guard. Both are pure
# decisions over state the orchestrator already holds, so both are testable
# without a network.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/ui.sh
source "$HERE/../lib/ui.sh"
# shellcheck source=../lib/legs.sh
source "$HERE/../lib/legs.sh"

pass=0 fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }

# verdict pass max stop blocked runs cap files maxfiles -> first word of decision
decides() {
  local want="$1" desc="$2"; shift 2
  local got; got="$(legs_should_continue "$@" | cut -d' ' -f1)"
  [[ "$got" == "$want" ]] && ok "$desc" || notok "$desc" "$want" "$got"
}

decides continue "issues remain below every cap"        issues-remain 1 3 false false 0 12 10 200
decides converged "converged stops the loop"            converged     1 3 false false 0 12 10 200
decides halt     "revloop/stop outranks a healthy verdict" converged  1 3 true  false 0 12 10 200
decides halt     "the addresser reporting blocked halts" issues-remain 1 3 false true  0 12 10 200

# The boundary is the thing worth testing: pass 3 of max 3 is the last pass, not
# the one after which a fourth begins.
decides continue "one below the pass cap continues"     issues-remain 2 3 false false 0 12 10 200
decides halt     "exactly at the pass cap halts"        issues-remain 3 3 false false 0 12 10 200
decides halt     "beyond the pass cap halts"            issues-remain 4 3 false false 0 12 10 200

decides continue "one below the daily cap continues"    issues-remain 1 3 false false 11 12 10 200
decides halt     "exactly at the daily cap halts"       issues-remain 1 3 false false 12 12 10 200

decides continue "exactly at the file cap still runs"   issues-remain 1 3 false false 0 12 200 200
decides halt     "above the file cap halts"             issues-remain 1 3 false false 0 12 201 200
decides continue "a file cap of zero is no cap"         issues-remain 1 3 false false 0 12 9999 0

# --- nits ------------------------------------------------------------------
legs_should_fix_nits 1 1 && ok "nits are fixed up to skip_nits_after_pass" \
  || notok "nits are fixed up to skip_nits_after_pass" "yes" "no"
legs_should_fix_nits 2 1 && notok "nits stop being fixed past the cap" "no" "yes" \
  || ok "nits stop being fixed past the cap"
legs_should_fix_nits 1 0 && notok "skip_nits_after_pass 0 never fixes nits" "no" "yes" \
  || ok "skip_nits_after_pass 0 never fixes nits"
legs_should_fix_nits 3 3 && ok "skip_nits_after_pass 3 fixes them on the last pass" \
  || notok "skip_nits_after_pass 3 fixes them on the last pass" "yes" "no"

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

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
