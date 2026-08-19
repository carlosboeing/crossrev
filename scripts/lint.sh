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
# `.worktrees/` holds other branches' checkouts, which are not this branch's code
# to lint. Without the prune a shellcheck warning on an in-flight branch fails
# lint on main, and only from the repository root — a session working inside a
# worktree sees a clean run and never learns why CI disagreed.
done < <(find . -path './.worktrees' -prune -o \
  -type f \( -name '*.sh' -o -name crossrev -path '*/bin/*' -o -path './tests/stub/*' \) -print | sort)

printf '\nshellcheck -S warning\n'
if command -v shellcheck >/dev/null 2>&1; then
  # bootstrap.sh is here for the reason it cannot be anywhere else: it is fetched
  # and piped into bash straight from the internet, and it is deliberately
  # self-contained — it cannot source lib/ui.sh, because the reason it is running
  # is that lib/ is not on the machine yet. Nothing else would catch a mistake in
  # it.
  if shellcheck -S warning -x bin/crossrev lib/*.sh lib/adapters/*.sh \
       tests/*.sh tests/stub/* bootstrap.sh install.sh scripts/*.sh \
       scripts/githooks/pre-commit; then
    printf '  ok    clean\n'
  else
    fail=1
  fi
else
  printf '  ○     shellcheck is not installed — install it with: brew install shellcheck\n'
fi

# The version lives in two files because npm needs it in the manifest and
# `crossrev --version` reads the VERSION file at runtime. Nothing enforces that
# duplication but this, and the failure it prevents is silent: a published
# package whose own --version disagrees with the registry it came from.
printf '\nversion\n'
pkg_version="$(jq -r '.version // empty' package.json 2>/dev/null)"
file_version="$(tr -d '[:space:]' <VERSION 2>/dev/null)"
if [[ -z "$pkg_version" ]]; then
  printf '  FAIL  package.json has no version, or is not readable\n'
  fail=1
elif [[ "$pkg_version" == "$file_version" ]]; then
  printf '  ok    package.json and VERSION agree on %s\n' "$file_version"
else
  printf '  FAIL  package.json says %s, VERSION says %s\n' "$pkg_version" "$file_version"
  printf '        Both, or neither. A release bumps them together.\n'
  fail=1
fi

# Codex's hook-trust bypass flag is asserted absent by tests/test-sandbox.sh, not
# here. A grep for it in this file would match its own source and the test's, which
# is how the first version of that assertion failed on its own documentation.

printf '\n'
(( fail == 0 )) && printf 'lint clean\n' || printf 'lint found problems\n'
(( fail == 0 ))
