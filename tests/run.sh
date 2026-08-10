#!/usr/bin/env bash
#
# The whole suite, from one command.
#
# Everything here is offline: no network, no harness, no pull request. The parts
# that genuinely need a model or GitHub are verified by hand and recorded in the
# plan, because a test that needs a paid API call is a test nobody runs.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

failed=0
for t in "$HERE"/test-*.sh; do
  printf '\n%s\n' "$(basename "$t")"
  bash "$t" || failed=$((failed+1))
done

printf '\n'
if (( failed == 0 )); then
  printf 'all suites passed\n'
else
  printf '%d suite(s) failed\n' "$failed"
fi
(( failed == 0 ))
