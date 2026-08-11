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

# ---------------------------------------------------------------------------
# The three severity fields
# ---------------------------------------------------------------------------
#
# One field answering three questions is what the redesign took apart, so the
# split is asserted rather than assumed: how bad it is, what kind it is, and
# whether this pull request caused it.

F="$SCHEMAS/findings.schema.json"
props='.properties.findings.items.properties'

is() { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "expected '$3', got '$2'"; }

is "severity is the three-rung ordinal scale" \
  "$(jq -r "$props.severity.enum | join(\",\")" "$F")" "high,medium,low"
is "category is the closed six" \
  "$(jq -r "$props.category.enum | join(\",\")" "$F")" \
  "correctness,security,performance,maintainability,testing,docs"
is "pre_existing is a boolean, not an enum" \
  "$(jq -r "$props.pre_existing.type" "$F")" "boolean"
is "and provenance is not smuggled back into severity" \
  "$(jq -r "$props.severity.enum | index(\"pre-existing\") // \"absent\"" "$F")" "absent"

# ---------------------------------------------------------------------------
# What the validator does with a payload the harness did not constrain
# ---------------------------------------------------------------------------
#
# On a fenced-JSON fallback path this check is the only one there is, so each of
# the three fields gets a rejection case. A validator that waves an out-of-range
# value through posts a comment with a severity nothing downstream can rank.

# shellcheck source=../lib/validate.sh
source "$HERE/../lib/validate.sh"

_finding() {
  jq -cn --arg s "${1:-high}" --arg c "${2:-correctness}" --argjson p "${3:-false}" \
    '{verdict:"issues-remain", prior:null, blocked_reason:null,
      findings:[{path:"a.ts", line:1, side:"RIGHT", severity:$s, category:$c,
                 pre_existing:$p, title:"t", why:"w", fix:"f"}]}'
}

rejects() {
  local what="$1" payload="$2"
  if validate_findings "$payload" >/dev/null 2>&1; then
    notok "$what" "the validator accepted it"
  else
    ok "$what"
  fi
}

if validate_findings "$(_finding high security true)" >/dev/null; then
  ok "a well-formed finding passes"
else
  notok "a well-formed finding passes" "the validator rejected it"
fi
if validate_findings "$(_finding high security false)" >/dev/null; then
  ok "and an explicit false is not mistaken for an absent field"
else
  notok "and an explicit false is not mistaken for an absent field" "the validator rejected it"
fi
rejects "an old severity word is rejected"    "$(_finding important correctness false)"
rejects "an invented category is rejected"    "$(_finding high refactoring false)"
rejects "a non-boolean pre_existing is rejected" "$(_finding high correctness '"yes"')"

# Provenance has no safe default. Absent or null must fail rather than fall to
# false, which is the value that authorises the resolve leg to change code.
_finding_without_provenance() {
  jq -cn '{verdict:"issues-remain", prior:null, blocked_reason:null,
           findings:[{path:"a.ts", line:1, side:"RIGHT", severity:"high",
                      category:"correctness", title:"t", why:"w", fix:"f"}]}'
}
rejects "a missing pre_existing is rejected"  "$(_finding_without_provenance)"
rejects "a null pre_existing is rejected"     "$(_finding high correctness null)"

# One concept, one name. The summary comment was `wrap_up` in the schema and
# "the overall comment body" in its own description, and a field the model fills
# at run time is the cheapest kind to rename — no live pull request carries one.
_resolve_payload() {
  jq -cn --arg field "${1:-summary}" '
    {blocked:false, blocked_reason:null,
     dispositions:[{finding_id:"a1", disposition:"fixed", reply:"r",
                    persist:null, duplicate_of:null}]}
    + {($field): "What happened this pass."}'
}
if validate_resolve "$(_resolve_payload summary)" >/dev/null; then
  ok "a resolve payload carrying summary passes"
else
  notok "a resolve payload carrying summary passes" "the validator rejected it"
fi
if validate_resolve "$(_resolve_payload wrap_up)" >/dev/null 2>&1; then
  notok "the old wrap_up field is rejected" "the validator accepted it"
else
  ok "the old wrap_up field is rejected"
fi

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
