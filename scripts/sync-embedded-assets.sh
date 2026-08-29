#!/usr/bin/env bash
#
# Copy the canonical schemas, skills and harness descriptor into the Go packages
# that embed them.
#
# `go:embed` patterns are package-relative and cannot contain `..`, so a Go
# package cannot embed a file from the repository root. The canonical files stay
# where every other reader already finds them — `schemas/`, `skills/` and
# `lib/harnesses.json` — and this script keeps a byte-identical copy beside the
# package that embeds it.
#
# The copies are generated. Editing one by hand is the failure this exists to
# catch: the skill text is reproduced into every prompt (ADR 0007), so a
# package-local copy that has drifted changes what the model reads while the file
# a contributor edits stays as it was.
#
# `cp` is the whole mechanism. It reads and writes bytes, so a trailing newline,
# a CRLF and a non-ASCII character all survive; `cmp` compares the same bytes
# back. Nothing here parses JSON or Markdown, because a formatter in the middle
# is a second thing that can rewrite the file.
#
#   sync-embedded-assets.sh            copy, and say what changed
#   sync-embedded-assets.sh --check    compare only, and fail on drift

set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1

check=0
if (( $# > 1 )); then
  printf 'usage: %s [--check]\n' "${0##*/}" >&2; exit 2
fi
case "${1:-}" in
  --check) check=1 ;;
  "")      ;;
  *)       printf 'usage: %s [--check]\n' "${0##*/}" >&2; exit 2 ;;
esac

# source destination, in pairs.
ASSETS=(
  "schemas/findings.schema.json"  "internal/validate/assets/findings.schema.json"
  "schemas/resolve.schema.json"   "internal/validate/assets/resolve.schema.json"
  "skills/pr-review/SKILL.md"     "internal/prompt/assets/pr-review.SKILL.md"
  "skills/pr-resolve/SKILL.md"    "internal/prompt/assets/pr-resolve.SKILL.md"
  "lib/harnesses.json"            "internal/cred/assets/harnesses.json"
)

# The list above is what gets copied. It is not what decides the list is
# complete: a further `//go:embed` with no entry here would be copied by nobody
# and checked by nobody, and `--check` would still print `embedded assets
# match`. So the directives themselves are read back out of the Go source and
# every destination they name has to appear above.
#
# A directive's pattern is package-relative, so the destination is the
# directory of the file carrying it joined to the pattern. Patterns here are
# plain paths; a glob is expanded so that one cannot hide a file either.
fail=0
missing_source=()

declare -a embedded=()
while IFS= read -r line; do
  gofile="${line%%:*}"
  pattern="${line#*://go:embed }"
  [[ -n "$pattern" && "$pattern" != "$line" ]] || continue
  pkgdir="$(dirname "$gofile")"
  # A directive can name several patterns on one line.
  for pat in $pattern; do
    pat="${pat%\"}"; pat="${pat#\"}"
    for match in "$pkgdir/"$pat; do
      [[ -f "$match" ]] || continue
      embedded+=("${match#./}")
    done
  done
done < <(find . -name '*.go' -not -path './.git/*' -print0 \
           | xargs -0 grep -n '^//go:embed ' 2>/dev/null \
           | sed 's/:[0-9][0-9]*:/:/')

for dst in "${embedded[@]}"; do
  known=0
  j=1
  while (( j < ${#ASSETS[@]} )); do
    [[ "${ASSETS[$j]}" == "$dst" ]] && known=1
    j=$(( j + 2 ))
  done
  if (( ! known )); then
    printf '  FAIL  %s is embedded and has no canonical source in this script\n' "$dst"
    missing_source+=("$dst")
    fail=1
  fi
done

drift=()
i=0
while (( i < ${#ASSETS[@]} )); do
  src="${ASSETS[$i]}"; dst="${ASSETS[$((i + 1))]}"
  i=$(( i + 2 ))

  if [[ ! -f "$src" ]]; then
    printf '  FAIL  %s is missing, so %s cannot be generated from it\n' "$src" "$dst"
    fail=1
    continue
  fi

  if (( check )); then
    if cmp -s "$src" "$dst"; then
      printf '  ok    %s\n' "$dst"
    else
      printf '  FAIL  %s differs from %s\n' "$dst" "$src"
      drift+=("$dst")
      fail=1
    fi
    continue
  fi

  mkdir -p "$(dirname "$dst")" || { fail=1; continue; }
  if cmp -s "$src" "$dst"; then
    printf '  ok    %s\n' "$dst"
  elif cp "$src" "$dst"; then
    printf '  →     %s copied from %s\n' "$dst" "$src"
  else
    printf '  FAIL  could not copy %s to %s\n' "$src" "$dst"
    fail=1
  fi
done

if (( fail )); then
  if (( ${#drift[@]} > 0 )); then
    printf '\n        The copies are generated. Edit the canonical file and run:\n'
    printf '        bash scripts/sync-embedded-assets.sh\n'
  fi
  if (( ${#missing_source[@]} > 0 )); then
    printf '\n        Every //go:embed needs a canonical file at the repository root and a\n'
    printf '        pair in ASSETS above, or nothing keeps the copy honest.\n'
  fi
  exit 1
fi

if (( check )); then
  printf '\nembedded assets match\n'
else
  printf '\nembedded assets synced\n'
fi
exit 0
