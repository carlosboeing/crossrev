#!/usr/bin/env bash
#
# Inspect upstream Linguist's generated.rb for additions to crossrev's
# built-in generated rules in internal/intel/generated.go.
#
# A maintainer tool, never on a runtime path: the binary only reads the
# committed Go tables, so running reviews needs no network and builds
# remain deterministic.
#
# Linguist's Ruby regexes do not convert mechanically to Go regexes,
# so this script only prints additions the lists lack for a maintainer
# to review and apply.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_FILE="$ROOT/internal/intel/generated.go"

UPSTREAM_URL="${CROSSREV_LINGUIST_URL:-https://raw.githubusercontent.com/github-linguist/linguist/main/lib/linguist/generated.rb}"
COMMITS_URL="${CROSSREV_LINGUIST_COMMITS_URL:-https://api.github.com/repos/github-linguist/linguist/commits?path=lib/linguist/generated.rb&per_page=1}"

need() {
  command -v "$1" >/dev/null 2>&1 || { printf '%s is required\n' "$1" >&2; exit 1; }
}
need curl
need jq

# Upstream commit SHA
sha="${CROSSREV_LINGUIST_COMMIT:-}"
if [[ -z "$sha" ]]; then
  sha="$(curl -fsSL "$COMMITS_URL" | jq -r '.[0].sha // empty')"
  [[ -n "$sha" ]] || { printf 'could not resolve the upstream commit\n' >&2; exit 1; }
fi

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

curl -fsSL "$UPSTREAM_URL" -o "$tmp"

printf 'Linguist upstream commit: %s\n\n' "$sha"

# Extract name matching lines and their regex patterns
# Regexes in generated.rb are between / and /, with escaped slashes \/
# We replace \/ with a placeholder, extract /.../, then restore /
patterns=()
while IFS= read -r pat; do
  [[ -n "$pat" ]] && patterns+=("$pat")
done < <(grep -E 'name(\.match|\s*=~)' "$tmp" \
  | sed 's|\\/|__SLASH__|g' \
  | grep -oE '/[^/]+/' \
  | sed -e 's|__SLASH__|/|g' -e 's|^/||' -e 's|/$||' \
  | sort -u)

absent_lockfiles=()
absent_patterns=()

clean_lockfile() {
  local p="$1"
  p="${p#*(?:\^|/)}"
  p="${p#*(\^|/)}"
  p="${p#\^}"
  p="${p%\$}"
  p="${p//\\./.}"
  printf '%s' "$p"
}

for pat in "${patterns[@]}"; do
  # Determine if this pattern specifies a lockfile
  if [[ "$pat" =~ (lock|resolved|shrinkwrap) ]]; then
    name="$(clean_lockfile "$pat")"
    # Check if the lockfile name is in internal/intel/generated.go
    if ! grep -qF "\"$name\"" "$GO_FILE"; then
      absent_lockfiles+=("$name")
    fi
  else
    # Filename pattern: check if pattern is in internal/intel/generated.go
    if ! grep -qF "$pat" "$GO_FILE"; then
      absent_patterns+=("$pat")
    fi
  fi
done

if (( ${#absent_lockfiles[@]} > 0 )); then
  printf 'lockfile names absent from internal/intel/generated.go:\n'
  for l in "${absent_lockfiles[@]}"; do
    printf '  %s\n' "$l"
  done
  printf '\n'
else
  printf 'all upstream lockfiles present in internal/intel/generated.go\n\n'
fi

if (( ${#absent_patterns[@]} > 0 )); then
  printf 'filename patterns absent from internal/intel/generated.go:\n'
  for p in "${absent_patterns[@]}"; do
    printf '  %s\n' "$p"
  done
  printf '\n'
else
  printf 'all upstream filename patterns present in internal/intel/generated.go\n\n'
fi
