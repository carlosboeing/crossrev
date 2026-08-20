# shellcheck shell=bash
# lib/harnesses.sh — the harness descriptor, and the only thing that reads it.
#
# One JSON document rather than allowlists scattered across seven files. It is a
# trusted input: it names sourced paths, an install command written into a
# generated workflow, environment variable names, credential destinations, and
# quarantine paths handed to `mv` and `rm -rf`. A malformed entry therefore
# reaches a side effect, so validation runs at load and fails closed.
#
# `harnesses` and `not_driven` are ARRAYS of objects carrying a `name`, not
# objects keyed by name. That is the whole reason the uniqueness check works:
# `jq` keeps the last of two duplicate object keys and discards the first
# silently, so a name-keyed map cannot express "names are unique" at all — the
# duplicate is gone before any check sees it. Measured, not assumed: on
# `{"a":{"x":1},"a":{"x":2}}`, `jq -c .` prints `{"a":{"x":2}}`.

HARNESS_JSON=""

_harness_file() {
  local root="${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
  printf '%s' "${CROSSREV_HARNESS_FILE:-$root/lib/harnesses.json}"
}

# Ten checks in one pass: the design's six, plus a version guard, an array-shape
# guard, a duplicate-name guard and a not-driven-name guard.
# Prints the first problem, or nothing.
harness_validate() {
  jq -r '
    def bad(m): m;
    def dup($a): [ $a | group_by(.)[] | select(length > 1) | .[0] ];
    def relative($p): ($p | type == "string") and ($p != "")
                      and ($p | startswith("/") | not)
                      and (($p | split("/")) | index("..") | not);
    (.harnesses // []) as $h
    | (.not_driven // []) as $nd
    | [ $h[].name ] as $names
    | [ $nd[].name ] as $others
    | if (.version != 1)
        then bad("the descriptor'\''s version is \(.version | tojson), and this build reads version 1")
      elif ($h | type != "array") or ($nd | type != "array")
        then bad("harnesses and not_driven must be arrays of objects carrying a name")
      elif ($names | length) == 0
        then bad("the descriptor names no harnesses")
      elif (dup($names) | length) > 0
        then bad("harness name \(dup($names)[0]) appears more than once")
      elif ([ $names[] | select((type == "string") and test("^[a-z][a-z0-9-]*$") | not) ] | length) > 0
        then bad("harness name \([ $names[] | select((type == "string") and test("^[a-z][a-z0-9-]*$") | not) ][0] | tojson) is not [a-z][a-z0-9-]*")
      elif (dup($others) | length) > 0 or (($names + $others) | length) != (($names + $others) | unique | length)
        then bad("a not_driven name is duplicated, or is also a driven harness")
      elif ([ $h[] | .credential.env_names[]?, .credential.secret?, .credential.staging.env?
              | select(. != null)
              | select(test("^[A-Z_][A-Z0-9_]*$") | not) ] | length) > 0
        then bad("environment variable name \([ $h[] | .credential.env_names[]?, .credential.secret?, .credential.staging.env? | select(. != null) | select(test("^[A-Z_][A-Z0-9_]*$") | not) ][0]) is not [A-Z_][A-Z0-9_]*")
      elif ([ $h[]
              | select((.credential.archetype | IN("A","B","C") | not)
                    or (.credential.provenance | IN("measured","inferred","vendor-documented") | not)
                    or (.schema_style | IN("inline","path") | not)
                    or (.install.kind | IN("script","npm") | not)
                    or (.credential.staging.kind | IN("none","file","home","env") | not)) ] | length) > 0
        then bad("harness \([ $h[] | select((.credential.archetype | IN("A","B","C") | not) or (.credential.provenance | IN("measured","inferred","vendor-documented") | not) or (.schema_style | IN("inline","path") | not) or (.install.kind | IN("script","npm") | not) or (.credential.staging.kind | IN("none","file","home","env") | not)) | .name ][0]) carries an out-of-range archetype, provenance, schema_style, install kind or staging kind")
      elif ([ $h[] | .quarantine[]?, .credential.staging.path? | select(. != null) | select(relative(.) | not) ]
             + [ (.quarantine_shared // [])[] | select(relative(.) | not) ] | length) > 0
        then bad("quarantine or destination path \(([ $h[] | .quarantine[]?, .credential.staging.path? | select(. != null) | select(relative(.) | not) ] + [ (.quarantine_shared // [])[] | select(relative(.) | not) ])[0] | tojson) is absolute, empty, or contains a .. segment")
      elif ([ $h[]
              | select(.install as $i
                       | (($i.url // "") == "")
                      or (($i.command // "") == "")
                      or (($i.pinned_version != null)
                          and (($i.command | contains($i.pinned_version)) | not))) ] | length) > 0
        then bad("harness \([ $h[] | select(.install as $i | (($i.url // "") == "") or (($i.command // "") == "") or (($i.pinned_version != null) and (($i.command | contains($i.pinned_version)) | not))) | .name ][0]) has an installer with no url, no command, or a pinned version its command does not carry")
      else empty end' <<<"$1" 2>/dev/null \
    || printf 'the descriptor is not parseable JSON'
}

# Read and validate once. Idempotent, so every accessor may call it. The
# re-entrant call from the adapter loop below returns at the first line, because
# HARNESS_JSON is already set by then.
harness_load() {
  [[ -n "$HARNESS_JSON" ]] && return 0
  command -v jq >/dev/null 2>&1 || ui_die \
    "jq is not installed, and CrossRev reads its harness descriptor with it" \
    "Install it, then run this again. On macOS: brew install jq."
  local file problem
  file="$(_harness_file)"
  [[ -r "$file" ]] || ui_die \
    "the harness descriptor is missing at $file" \
    "It ships with CrossRev. A checkout missing it is incomplete — re-clone, or reinstall."
  HARNESS_JSON="$(jq -c . "$file" 2>/dev/null)" || ui_die \
    "the harness descriptor at $file is not valid JSON" \
    "Check it with: jq . $file"
  problem="$(harness_validate "$HARNESS_JSON")"
  [[ -z "$problem" ]] || { HARNESS_JSON=""; ui_die \
    "the harness descriptor is invalid: $problem" \
    "It drives sourced paths, install commands, environment names and quarantine paths, so CrossRev stops rather than acting on it. Fix $file."; }

  # Every driven harness needs an adapter, and every adapter needs an entry.
  # Checked here rather than in jq, because it is a fact about the filesystem.
  local root="${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
  local n
  while IFS= read -r n; do
    [[ -r "$root/lib/adapters/$n.sh" ]] || ui_die \
      "the descriptor names the harness '$n', and there is no lib/adapters/$n.sh" \
      "Add the adapter, or remove the entry."
  done < <(harness_names)
  local f
  for f in "$root"/lib/adapters/*.sh; do
    n="$(basename "$f" .sh)"
    harness_known "$n" || ui_die \
      "lib/adapters/$n.sh exists, and the descriptor has no entry for '$n'" \
      "Add the entry to lib/harnesses.json, or remove the adapter."
  done
  return 0
}

harness_names() {
  command -v jq >/dev/null 2>&1 || return 1
  harness_load
  jq -r '.harnesses[].name' <<<"$HARNESS_JSON"
}

harness_get()      { harness_load; jq -r --arg n "$1" ".harnesses[] | select(.name == \$n) | $2 // empty" <<<"$HARNESS_JSON"; }
harness_get_json() { harness_load; jq -c --arg n "$1" "(.harnesses[] | select(.name == \$n) | $2) // null" <<<"$HARNESS_JSON"; }
harness_field()    { harness_load; jq -r "$1 // empty" <<<"$HARNESS_JSON"; }
harness_known()    { harness_load; [[ "$(jq -r --arg n "$1" 'any(.harnesses[]; .name == $n)' <<<"$HARNESS_JSON")" == "true" ]]; }

harness_not_driven() {
  harness_load
  local reason
  reason="$(jq -r --arg n "$1" '.not_driven[] | select(.name == $n) | .reason // empty' <<<"$HARNESS_JSON")"
  [[ -n "$reason" ]] || return 1
  printf '%s' "$reason"
}

# "claude, codex and agy" — for message text that has to name the set.
harness_names_human() {
  harness_names | awk '{n[NR]=$0} END {
    for (i = 1; i <= NR; i++) {
      printf "%s", n[i]
      if (i == NR - 1) printf " and "; else if (i < NR) printf ", "
    }
  }'
}
