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
     dispositions:[{finding_number:1, disposition:"fixed", reply:"r",
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

# ---------------------------------------------------------------------------
# The seam: what the orchestrator supplied, and what came back
# ---------------------------------------------------------------------------
#
# A finding is named by the number it was given in the prompt, so the harness's
# own schema enforcement rules out the mistyped identifier that corrupted three
# dispositions on PR 5. What the schema cannot express is a per-run range, a
# duplicate, or an omission, and those are checked here.
#
# The exit code is part of the contract. 1 is a shape problem, which on a
# schema-native harness means the adapter is broken and a retry reproduces it.
# 2 is the content contradicting what the orchestrator handed over, which is
# model drift and gets one more attempt. Conflating them either retries a bug or
# discards a pass over a typo.

# Three findings were supplied, and two issues were offered as candidates.
_expect='{"findings":3,"candidates":[19,31]}'

_dispositions() {
  jq -cn --argjson d "$1" \
    '{blocked:false, blocked_reason:null, summary:"What happened.", dispositions:$d}'
}
_d() {   # number, and an optional duplicate_of
  jq -cn --argjson n "$1" --argjson dup "${2:-null}" \
    '{finding_number:$n, disposition:"fixed", reply:"r", persist:null, duplicate_of:$dup}'
}
_all_three() { jq -cs . <<<"$(_d 1)
$(_d 2)
$(_d 3)"; }

# rc <what> <expected-code> <payload> [expect]
rc() {
  local what="$1" want="$2" payload="$3" expect="${4:-}" got
  validate_resolve "$payload" "$expect" >/dev/null 2>&1; got=$?
  if [[ "$got" == "$want" ]]; then ok "$what"
  else notok "$what" "exit $got, wanted $want"; fi
}

rc "three numbered dispositions for three findings pass" 0 \
  "$(_dispositions "$(_all_three)")" "$_expect"

# The failure this whole change exists for. A hash could be mistyped into
# another valid-looking hash; a number outside the range cannot hide.
rc "a finding number past the end is rejected" 2 \
  "$(_dispositions "$(jq -cs . <<<"$(_d 1)
$(_d 2)
$(_d 7)")")" "$_expect"
rc "and a number below one is rejected" 2 \
  "$(_dispositions "$(jq -cs . <<<"$(_d 1)
$(_d 2)
$(_d 0)")")" "$_expect"
rc "the same finding dispositioned twice is rejected" 2 \
  "$(_dispositions "$(jq -cs . <<<"$(_d 1)
$(_d 2)
$(_d 2)")")" "$_expect"
# An omission is silent today: that finding gets no reply, its thread is never
# resolved, and the marker records it as undispositioned.
rc "a finding with no disposition at all is rejected" 2 \
  "$(_dispositions "$(jq -cs . <<<"$(_d 1)
$(_d 2)")")" "$_expect"

# Shape, not drift. A harness that constrains output to the schema cannot emit
# either of these, so both mean something below the model is wrong.
rc "the old finding_id field is a shape failure, not a semantic one" 1 \
  "$(_dispositions '[{"finding_id":"9e4f9ee1cbe25125","disposition":"fixed","reply":"r","persist":null,"duplicate_of":null}]')" \
  "$_expect"
rc "and so is a finding number that is not a whole number" 1 \
  "$(_dispositions '[{"finding_number":1.5,"disposition":"fixed","reply":"r","persist":null,"duplicate_of":null}]')" \
  "$_expect"

# duplicate_of names an issue the orchestrator retrieved. Inventing one makes
# revloop comment on an unrelated issue and resolve the thread against it.
rc "a duplicate_of naming a supplied candidate passes" 0 \
  "$(_dispositions "$(jq -cs . <<<"$(_d 1 19)
$(_d 2)
$(_d 3)")")" "$_expect"
# Candidates are supplied per finding, but the check is against all of them.
# Per-finding would reject a model that correctly noticed one issue covers two
# findings, and inventing a number is the failure being designed out.
rc "as does one that names a candidate offered for a different finding" 0 \
  "$(_dispositions "$(jq -cs . <<<"$(_d 1)
$(_d 2 31)
$(_d 3)")")" "$_expect"
rc "a duplicate_of naming an issue nobody offered is rejected" 2 \
  "$(_dispositions "$(jq -cs . <<<"$(_d 1 404)
$(_d 2)
$(_d 3)")")" "$_expect"
# A file-sink repository is offered no candidates at all, and yet duplicate_of
# still becomes the "tracked as" line on a deferred finding's reply.
rc "and with no candidates offered, any duplicate_of is rejected" 2 \
  "$(_dispositions "$(_all_three)" | jq -c '.dispositions[0].duplicate_of = 19')" \
  '{"findings":3,"candidates":[]}'

# Called without expectations — as the fenced-JSON fallback path and the shape
# tests above do — it checks shape and stops. The semantic checks need something
# to compare against, and inventing one would be worse than not running them.
rc "with nothing to compare against, only shape is checked" 0 \
  "$(_dispositions "$(jq -cs . <<<"$(_d 9)")")"

# ---------------------------------------------------------------------------
# The review leg carries no hashes either
# ---------------------------------------------------------------------------
#
# `prior` is the review leg's classification of findings carried in from an
# earlier pass, and it named each one by its 16-character hash. One rule across
# both legs — the orchestrator numbers things, the model returns numbers — beats
# one rule per leg, especially where pass 2 is judging pass 1.
is "prior findings are named by number, not by hash" \
  "$(jq -r '.properties.prior.items.properties.finding_number.type // "absent"' "$F")" "integer"
is "and the old id field is gone rather than left beside it" \
  "$(jq -r '.properties.prior.items.properties.id.type // "absent"' "$F")" "absent"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
