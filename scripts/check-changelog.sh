#!/usr/bin/env bash
#
# Fails when a change alters what ships without recording it under
# `## [Unreleased]` in CHANGELOG.md.
#
# Versions are cut deliberately rather than per merge, so a feature branch does
# not bump VERSION — that happens once, at release time, from the commit types
# accumulated since the last tag. What a branch must not do is change the
# published artifact and leave no trace of it, because at release time nobody
# reconstructs a missing entry from a diff.
#
# The shipped set is read from `npm pack`, not restated here. The `files`
# allowlist in package.json is the single source of truth for what ships, and a
# second list in this file would drift from it silently — which is the whole
# failure this script exists to prevent, one level up.
#
# Needs npm, which the publish route already requires. Takes a base ref,
# defaulting to origin/main.

set -uo pipefail

BASE="${1:-origin/main}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1

if ! command -v npm >/dev/null 2>&1; then
  echo "error  npm is required to determine which files ship." >&2
  echo "       This check is enforced in CI, where npm is always present." >&2
  exit 1
fi

if ! git rev-parse --verify --quiet "$BASE" >/dev/null; then
  echo "error  base ref '$BASE' does not exist here." >&2
  echo "       Pass one explicitly: scripts/check-changelog.sh <ref>" >&2
  exit 1
fi

# Everything the published tarball contains, one path per line.
shipped="$(npm pack --dry-run --json 2>/dev/null | jq -r '.[0].files[].path')"
if [[ -z "$shipped" ]]; then
  echo "error  could not read the packed file list from npm." >&2
  exit 1
fi

# A failed diff must not read as an empty diff. On a shallow clone this fails
# with "no merge base", and treating that as "nothing changed" makes the whole
# check pass without ever having run — the exact failure it is here to catch.
if ! changed="$(git diff --name-only "$BASE...HEAD" 2>&1)"; then
  echo "error  cannot diff against $BASE:" >&2
  sed 's/^/       /' >&2 <<<"$changed"
  echo "       A shallow clone has no merge base. In CI, check out with" >&2
  echo "       fetch-depth: 0 so the base and the head share history." >&2
  exit 1
fi

[[ -z "$changed" ]] && { printf '\nchangelog\n  ○     nothing changed against %s\n' "$BASE"; exit 0; }

# The intersection: changed files that a user would actually receive.
shipped_changed="$(comm -12 <(sort <<<"$shipped") <(sort <<<"$changed"))"

# Go test files travel inside shipped directories (cmd/, internal/) but do not
# ship: the binary carries no test code, and goldens under testdata/ steer the
# tests rather than the tool. Without this a test-only change would demand an
# entry, which ends the rule that tests and CI changes are exempt.
shipped_changed="$(grep -vE '_test\.go$|/testdata/' <<<"$shipped_changed" || true)"

printf '\nchangelog\n'

if [[ -z "$shipped_changed" ]]; then
  printf '  ok    nothing that ships changed — no entry required\n'
  exit 0
fi

# The section between `## [Unreleased]` and the next `## [` heading.
_unreleased() { awk '/^## \[Unreleased\]/ {f=1; next} /^## \[/ {f=0} f'; }

before="$(git show "$BASE:CHANGELOG.md" 2>/dev/null | _unreleased)"
after="$(_unreleased <CHANGELOG.md)"

if [[ "$before" == "$after" ]]; then
  printf '  FAIL  these ship, and nothing was added under [Unreleased]:\n'
  sed 's/^/          /' <<<"$shipped_changed"
  printf '\n        A release reads CHANGELOG.md, not the diff. An entry written now\n'
  printf '        explains why; one reconstructed later explains what.\n'
  exit 1
fi

count="$(wc -l <<<"$shipped_changed" | tr -d ' ')"
printf '  ok    %s shipped file(s) changed, and [Unreleased] records it\n' "$count"
