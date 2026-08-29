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

# The permitted homes for a harness name are exactly three: lib/harnesses.json,
# lib/adapters/<name>.sh, and tests/ — test fixtures name harnesses deliberately,
# and tests/stub/codex is a tripwire that must keep naming one. Three files are
# outside the scanned set on purpose: templates/crossrev.yml and
# templates/operator-config.yml are example configuration whose job is to name a
# harness, and scripts/refresh-prices.sh holds LiteLLM price keys whose model
# ids contain harness names without driving any of them. A stale example there
# is a documentation bug rather than an allowlist. Any other exemption is a
# design change, not a lint tweak.
printf '\nharness names\n'
names="$(jq -r '.harnesses[].name, .not_driven[].name' lib/harnesses.json 2>/dev/null | paste -sd'|' -)"
if [[ -z "$names" ]]; then
  printf '  FAIL  lib/harnesses.json could not be read, so the harness-name check did not run\n'
  fail=1
else
  scan_scripts=()
  for f in scripts/*.sh; do
    [[ "$f" == "scripts/refresh-prices.sh" ]] && continue
    scan_scripts+=("$f")
  done
  hits="$(grep -nEH "\\b($names)\\b" \
      bin/crossrev lib/*.sh \
      ${scan_scripts[@]+"${scan_scripts[@]}"} \
      templates/crossrev-review.yml templates/crossrev-resolve.yml templates/crossrev-token-refresh.yml \
      2>/dev/null | grep -vE ':[0-9]+:[[:space:]]*#' || true)"
  if [[ -z "$hits" ]]; then
    printf '  ok    no harness name outside its adapter, the descriptor and tests/\n'
  else
    printf '  FAIL  a harness name appears outside its permitted homes\n'
    printf '%s\n' "$hits" | sed 's/^/        /'
    fail=1
  fi
fi

printf '\nrendered docs\n'
if bash scripts/render-harness-docs.sh --check >/dev/null 2>&1; then
  printf '  ok    generated harness tables in README.md and docs/ match lib/harnesses.json\n'
else
  printf '  FAIL  generated harness tables differ from lib/harnesses.json\n'
  printf '        Run: bash scripts/render-harness-docs.sh\n'
  fail=1
fi

# The Go packages embed the schemas, the skills and the harness descriptor, and
# `go:embed` cannot reach a
# file above its own package. The copies under internal/*/assets/ are generated,
# so a hand edit to one changes what a binary sends a harness while the file a
# contributor edits stays as it was.
printf '\nembedded assets\n'
if bash scripts/sync-embedded-assets.sh --check >/dev/null 2>&1; then
  printf '  ok    the embedded schemas, skills and descriptor match their canonical files\n'
else
  printf '  FAIL  an embedded copy differs from its canonical file\n'
  bash scripts/sync-embedded-assets.sh --check 2>&1 | sed 's/^/        /'
  printf '        Run: bash scripts/sync-embedded-assets.sh\n'
  fail=1
fi

printf '\ngo\n'
if command -v go >/dev/null 2>&1; then
  if GOTOOLCHAIN=go1.27.0 go mod verify >/dev/null 2>&1; then
    printf '  ok    go mod verified\n'
  else
    printf '  FAIL  go mod verify failed\n'
    GOTOOLCHAIN=go1.27.0 go mod verify 2>&1 | sed 's/^/        /'
    fail=1
  fi
  if GOTOOLCHAIN=go1.27.0 go vet ./... >/dev/null 2>&1; then
    printf '  ok    go vet clean\n'
  else
    printf '  FAIL  go vet found problems\n'
    GOTOOLCHAIN=go1.27.0 go vet ./... 2>&1 | sed 's/^/        /'
    fail=1
  fi
  # The generated Go parity vectors come from the Bash policy tables in
  # tests/test-state.sh and tests/test-legs.sh. Nothing else compares them, so an
  # edit to a table that nobody regenerated would leave the vectors disagreeing
  # with the oracle they were cut from, with every other check green.
  if GOTOOLCHAIN=go1.27.0 go run ./internal/testgen/policy -check >/dev/null 2>&1; then
    printf '  ok    generated parity vectors match the Bash policy tables\n'
  else
    printf '  FAIL  generated parity vectors differ from the Bash policy tables\n'
    GOTOOLCHAIN=go1.27.0 go run ./internal/testgen/policy -check 2>&1 | sed 's/^/        /'
    printf '        Run: go run ./internal/testgen/policy\n'
    fail=1
  fi
else
  printf '  FAIL  go is not installed\n'
  fail=1
fi

# Codex's hook-trust bypass flag is asserted absent by tests/test-sandbox.sh, not
# here. A grep for it in this file would match its own source and the test's, which
# is how the first version of that assertion failed on its own documentation.

printf '\n'
(( fail == 0 )) && printf 'lint clean\n' || printf 'lint found problems\n'
(( fail == 0 ))
