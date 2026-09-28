#!/usr/bin/env bash
#
# Pin the third-party actions the generated workflows use to full SHAs.
#
# A tag alone looks immutable and is not one: `git tag -f` plus a force push
# moves it. So every third-party `uses:` in templates/ names a 40-character
# SHA with the tag riding in a trailing comment (ADR 0009), the same
# `SHA # vX.Y.Z` form .github/workflows/ uses — and
# internal/initcmd/pins_test.go refuses any other form.
#
# A maintainer tool, never on a runtime path: it resolves each tag to its SHA
# through the GitHub API and rewrites the templates preserving the version
# comments. The delivery pin (carlosboeing/crossrev) is not listed here:
# `crossrev init` renders that one from the source checkout at install time.
# Re-run this to move a pin forward; the SHAs are resolved at run time, so
# the versions below are the whole input.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1

# The third-party actions the generated workflows use, each as "repo tag".
PINS=(
  "actions/checkout v7.0.1"
  "actions/upload-artifact v7.0.1"
  "actions/create-github-app-token v2"
)

need() {
  command -v "$1" >/dev/null 2>&1 || { printf '%s is required\n' "$1" >&2; exit 1; }
}
need curl
need jq

# resolve <repo> <tag>: print the commit SHA the tag points at. An annotated
# tag points at a tag object first, so it is dereferenced once; a lightweight
# tag points straight at the commit.
resolve() {
  local repo="$1" tag="$2" ref type sha url
  ref="$(curl -fsSL "https://api.github.com/repos/${repo}/git/ref/tags/${tag}")"
  type="$(jq -r '.object.type // empty' <<<"$ref")"
  sha="$(jq -r '.object.sha // empty' <<<"$ref")"
  if [[ "$type" == "tag" ]]; then
    url="$(jq -r '.object.url // empty' <<<"$ref")"
    [[ -n "$url" ]] || { printf 'no tag object url for %s@%s\n' "$repo" "$tag" >&2; return 1; }
    sha="$(curl -fsSL "$url" | jq -r '.object.sha // empty')"
  fi
  [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || { printf 'could not resolve %s@%s to a commit SHA\n' "$repo" "$tag" >&2; return 1; }
  printf '%s' "$sha"
}

fail=0
for pin in "${PINS[@]}"; do
  repo="${pin%% *}"
  tag="${pin#* }"
  if ! sha="$(resolve "$repo" "$tag")"; then
    fail=1
    continue
  fi
  printf '%s@%s resolves to %s\n' "$repo" "$tag" "$sha"
  for f in templates/*.yml; do
    if ! grep -q "uses: ${repo}@" "$f"; then
      continue
    fi
    tmp="$(mktemp)"
    sed -E "s|(uses: ${repo}@)[^[:space:]#]+([[:space:]]*#.*)?|\1${sha} # ${tag}|" "$f" >"$tmp"
    if cmp -s "$f" "$tmp"; then
      rm -f "$tmp"
    else
      mv "$tmp" "$f"
      printf '  pinned in %s\n' "$f"
    fi
  done
done

(( fail == 0 )) || exit 1
printf 'action pins refreshed\n'
