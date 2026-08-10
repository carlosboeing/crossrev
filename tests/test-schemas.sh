#!/usr/bin/env bash
#
# Schema shape tests.
#
# Both harness-forced constraints are silent failures if broken: a schema with a
# $schema key makes Claude Code refuse before the model is called, and a property
# missing from `required` makes Codex return HTTP 400. Neither shows up until a
# leg runs against a real harness, which is the expensive place to find out.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCHEMAS="$HERE/../schemas"

pass=0 fail=0
ok()   { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok(){ printf '  FAIL  %s\n    %s\n' "$1" "$2"; fail=$((fail+1)); }

for f in "$SCHEMAS"/*.json; do
  name="$(basename "$f")"

  if out="$(jq -e . "$f" 2>&1 >/dev/null)"; then ok "$name is valid JSON"
  else notok "$name is valid JSON" "$out"; fi

  # Claude Code: --json-schema rejects a schema naming the 2020-12 meta-schema
  # with "no schema with key or ref", before the model is ever called.
  if [[ "$(jq -r 'has("$schema") or has("$id")' "$f")" == "false" ]]; then
    ok "$name carries no \$schema or \$id key"
  else
    notok "$name carries no \$schema or \$id key" "Claude Code cannot resolve the meta-schema ref and fails closed"
  fi

  # Codex: OpenAI strict mode requires every property to appear in `required`.
  missing="$(jq -r '
    [ .. | objects | select(has("properties"))
      | (.properties | keys) - ((.required // []))
      | select(length > 0) ] | flatten | unique | join(", ")' "$f")"
  if [[ -z "$missing" ]]; then
    ok "$name lists every property in required"
  else
    notok "$name lists every property in required" "Codex returns HTTP 400 for: $missing"
  fi
done

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
