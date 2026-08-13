#!/usr/bin/env bash
#
# Everything the test suite cannot catch: shell that parses but is wrong.
#
# Run this before a commit. `tests/run.sh` proves behaviour; this proves the shell
# itself — unquoted expansions, the empty-array-expands-to-one-empty-word trap,
# assignments in a single `local` that read each other and silently do not.
#
# -x follows `source` directives, which is the only way it can see the lib files
# from bin/crossrev.

set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1

fail=0

printf '\nsyntax\n'
while IFS= read -r f; do
  if bash -n "$f" 2>/dev/null; then
    printf '  ok    %s\n' "${f#./}"
  else
    printf '  FAIL  %s\n' "${f#./}"
    bash -n "$f" 2>&1 | sed 's/^/        /'
    fail=1
  fi
done < <(find . -name '*.sh' -o -name crossrev -path '*/bin/*' | sort)

printf '\nshellcheck -S warning\n'
if command -v shellcheck >/dev/null 2>&1; then
  if shellcheck -S warning -x bin/crossrev lib/*.sh lib/adapters/*.sh \
       tests/*.sh tests/stub/* install.sh scripts/*.sh; then
    printf '  ok    clean\n'
  else
    fail=1
  fi
else
  printf '  ○     shellcheck is not installed — install it with: brew install shellcheck\n'
fi

# Codex's hook-trust bypass flag is asserted absent by tests/test-sandbox.sh, not
# here. A grep for it in this file would match its own source and the test's, which
# is how the first version of that assertion failed on its own documentation.

printf '\n'
(( fail == 0 )) && printf 'lint clean\n' || printf 'lint found problems\n'
(( fail == 0 ))
