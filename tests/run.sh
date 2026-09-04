#!/usr/bin/env bash
#
# The whole suite, from one command.
#
# Everything here is offline: no network, no harness, no pull request. The parts
# that genuinely need a model or GitHub are verified by hand and recorded in the
# plan, because a test that needs a paid API call is a test nobody runs.
#
# The suites drive the native Go binary, built once up front and handed to every
# suite through CROSSREV_TEST_BIN (see tests/harness.sh). There is no shell
# implementation left to run them against.
#
# Suites run in parallel by default, because nothing is shared between them:
# tests/harness.sh gives each suite its own XDG_CONFIG_HOME and XDG_STATE_HOME,
# stub_reset gives each case its own gh route table and call log, and
# fixture_repo builds a fresh checkout and a fresh bare origin per case. The
# order the glob returns them in has never meant anything.
#
# Output keeps that glob order whatever the job count, so a parallel run and a
# sequential one print the same bytes. A suite's block appears once every suite
# ahead of it has finished, while the rest keep running behind it.
#
# The time goes on spawning processes rather than on computing: fixture_repo
# alone starts about thirteen git processes. That is why more workers help and
# why the cap is modest.
#
# Usage:
#   bash tests/run.sh              # parallel, one job per core, capped at 8
#   bash tests/run.sh -j 1         # sequential, streaming as each suite runs
#   CROSSREV_TEST_JOBS=4 bash tests/run.sh
#
# Every suite runs with stdin closed. That is how CI runs them, and a suite that
# asks a question is meant to die rather than hang — the binary reads
# /dev/tty first, the way the shell it replaced did. A run at a terminal now
# matches the runner instead of diverging from it.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# One job per core, never more than 8. The work is process spawning and disk,
# not arithmetic, so past that the workers compete rather than add.
_default_jobs() {
  local n=""
  if   command -v nproc  >/dev/null 2>&1; then n="$(nproc 2>/dev/null)"
  elif command -v sysctl >/dev/null 2>&1; then n="$(sysctl -n hw.ncpu 2>/dev/null)"
  fi
  [[ "$n" =~ ^[0-9]+$ ]] && (( n > 0 )) || n=4
  (( n > 8 )) && n=8
  printf '%s' "$n"
}

jobs="${CROSSREV_TEST_JOBS:-$(_default_jobs)}"
while (( $# )); do
  case "$1" in
    -j|--jobs)
      [[ $# -ge 2 ]] || { printf '%s needs a number\n' "$1" >&2; exit 2; }
      jobs="$2"; shift 2 ;;
    -j*)       jobs="${1#-j}"; shift ;;
    -h|--help) printf 'Usage: bash tests/run.sh [-j JOBS]\n'; exit 0 ;;
    *) printf 'unknown option: %s\n' "$1" >&2; exit 2 ;;
  esac
done
if ! [[ "$jobs" =~ ^[0-9]+$ ]] || (( jobs < 1 )); then
  printf 'jobs must be a whole number above zero, got "%s"\n' "$jobs" >&2
  exit 2
fi

# One binary for the whole run, built before the first suite starts. Absolute,
# because every fixture cds into a throwaway checkout.
BIN="$HERE/../.tmp/crossrev-native"
printf '\nbuilding %s\n' "$BIN"
bash "$HERE/../scripts/build-native.sh" "$BIN" || {
  printf '\nthe native binary did not build; nothing was run\n' >&2
  exit 1
}
export CROSSREV_TEST_BIN="$BIN"

suites=("$HERE"/test-*.sh)
(( ${#suites[@]} > 0 )) || { printf 'no suites found under %s\n' "$HERE" >&2; exit 2; }

failed=0

run_sequential() {
  local t
  for t in "${suites[@]}"; do
    printf '\n%s\n' "$(basename "$t")"
    bash "$t" </dev/null || failed=$((failed+1))
  done
}

run_parallel() {
  local out queue t b rc
  out="$(mktemp -d)"
  # shellcheck disable=SC2064  # the path is wanted now, not when the trap fires
  trap "rm -rf '$out'" EXIT

  printf '\n%d suites, %d at a time\n' "${#suites[@]}" "$jobs"

  # xargs owns the queue rather than a batch-and-wait loop. bash 3.2 has no
  # `wait -n`, and waiting on a whole batch idles every worker that finished
  # early until the slowest one in that batch is done — which cost about half
  # the available speed-up when measured.
  printf '%s\n' "${suites[@]}" \
    | CROSSREV_SUITE_OUT="$out" xargs -P "$jobs" -I{} bash -c '
        b="$(basename "$1" .sh)"
        bash "$1" >"$CROSSREV_SUITE_OUT/$b.out" 2>&1 </dev/null
        # Written after the redirect above has closed, so a readable rc file
        # means the out file beside it is complete rather than half-flushed.
        printf "%s" "$?" >"$CROSSREV_SUITE_OUT/$b.rc"
      ' _ {} &
  queue=$!

  for t in "${suites[@]}"; do
    b="$(basename "$t" .sh)"
    while [[ ! -f "$out/$b.rc" ]]; do
      # A dead queue means no further rc file is coming. Stop waiting for one
      # rather than hang; the missing rc is counted as a failure below.
      kill -0 "$queue" 2>/dev/null || break
      sleep 0.1
    done
    printf '\n%s\n' "$(basename "$t")"
    [[ -f "$out/$b.out" ]] && cat "$out/$b.out"
    rc="$(cat "$out/$b.rc" 2>/dev/null)"
    [[ "$rc" == "0" ]] || failed=$((failed+1))
  done

  wait "$queue" 2>/dev/null
}

if (( jobs == 1 )); then run_sequential; else run_parallel; fi

printf '\n'
if (( failed == 0 )); then
  printf 'all suites passed\n'
else
  printf '%d suite(s) failed\n' "$failed"
fi
(( failed == 0 ))
