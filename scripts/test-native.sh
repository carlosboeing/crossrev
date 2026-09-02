#!/usr/bin/env bash
#
# The CLI-driven shell suites, run against the native Go binary.
#
# `bash tests/run.sh` runs every suite against bin/crossrev, which is the shipped
# tool. This runs the subset that actually invokes a crossrev binary against the
# Go one instead, by building it once and exporting CROSSREV_TEST_BIN. Nothing
# in production reads that variable: it exists so the same assertions can be
# pointed at either implementation.
#
# The suites that source lib/*.sh and call shell functions directly stay out.
# They test Bash internals that have no counterpart in a binary, so running them
# here would report the shell passing and say nothing about the port.
#
# Usage:
#   scripts/test-native.sh                 # build, then run every suite below
#   scripts/test-native.sh test-status.sh  # the named suites only

set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1

# The CLI-driven suites, written out rather than globbed.
#
# A glob would sweep in every direct-source suite, and a suite added later would
# join silently either way. The list is what a grep answers today:
#
#   for f in tests/test-*.sh; do grep -q '"\$CROSSREV"' "$f" && echo "$f"; done
#
# plus tests/test-config.sh, which spells it `$CROSSREV` unquoted. Re-run that
# grep when a suite is added, and add the file here.
SUITES=(
  test-action.sh
  test-adapters.sh
  test-config.sh
  test-init.sh
  test-legs.sh
  test-log.sh
  test-permissions.sh
  test-persist.sh
  test-policy.sh
  test-presentation.sh
  test-recovery.sh
  test-runner.sh
  test-status.sh
  test-watchdog.sh
  test-worktree.sh
)

if (( $# )); then
  SUITES=("$@")
fi

# The list is checked against the tree rather than trusted. A suite renamed or
# removed would otherwise be skipped in silence, and the script would report
# every remaining one passing.
missing=0
for suite in "${SUITES[@]}"; do
  [[ -f "tests/$suite" ]] || { printf 'no such suite: tests/%s\n' "$suite" >&2; missing=1; }
done
(( missing )) && exit 2

BIN="$ROOT/.tmp/crossrev-native"
printf 'building %s\n' "$BIN"
bash "$ROOT/scripts/build-native.sh" "$BIN" || {
  printf '\nthe native binary did not build; nothing was run\n' >&2
  exit 1
}
printf '\n'

# Absolute, because every fixture cds into a throwaway checkout.
export CROSSREV_TEST_BIN="$BIN"

failed=()
for suite in "${SUITES[@]}"; do
  printf '%s\n' "$suite"
  # stdin closed, the way CI runs them: a suite that asks a question is meant
  # to die rather than hang (_ui_input_source, lib/ui.sh).
  output="$(bash "tests/$suite" 2>&1 </dev/null)"
  status=$?
  printf '%s\n' "$output"

  # A suite that died before printing its count is a failure even when its exit
  # status says otherwise, and one that printed a count with failures in it is a
  # failure even when it exits 0. Both are checked, because either alone lets a
  # broken suite through.
  if ! grep -qE '^  [0-9]+ passed, [0-9]+ failed$' <<<"$output"; then
    printf '  FAIL  %s ended without printing a count\n' "$suite"
    failed+=("$suite")
  elif (( status != 0 )); then
    failed+=("$suite")
  fi
  printf '\n'
done

if (( ${#failed[@]} )); then
  printf 'FAILED against the native binary: %s\n' "${failed[*]}"
  exit 1
fi

printf 'all CLI-driven suites passed against the native binary\n'
