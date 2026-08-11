# shellcheck shell=bash
# lib/validate.sh — structural checks on what a harness returned.
#
# This is deliberately NOT general JSON Schema validation. Neither jq nor yq can
# do that, and pretending otherwise would be worse than saying so: required keys
# present, types right, enum values in range, and the schemas stay flat enough
# for that to be sufficient. A schema that outgrows this check is the signal to
# add a real validator, not to let the check quietly drift behind it.
#
# Both v1 harnesses constrain output natively, so on those paths this catches
# adapter bugs rather than model drift. On a fenced-JSON fallback path it is the
# only check there is, which is why it exists at all.

# Prints nothing and returns 0, or prints one line naming the first problem.
validate_findings() {
  local payload="$1" problem
  problem="$(jq -r '
    def bad(m): m;
    if type != "object" then bad("the payload is not a JSON object")
    elif (has("verdict") | not) then bad("no verdict key")
    elif (.verdict | IN("converged","issues-remain","blocked") | not)
      then bad("verdict is \"\(.verdict)\", which is not one of converged, issues-remain, blocked")
    elif (has("findings") | not) or (.findings | type != "array")
      then bad("findings is missing or not an array")
    else
      ( [ .findings[] | select(
            (.path? | type != "string") or (.path == "")
            or (.line? | type != "number")
            or ((.side? // "RIGHT") | IN("LEFT","RIGHT") | not)
            or ((.severity? // "") | IN("high","medium","low") | not)
            or ((.category? // "") | IN("correctness","security","performance","maintainability","testing","docs") | not)
            # Presence and type are checked separately, and neither through a
            # default. The only default available is false, which is the value
            # that says "this pull request introduced it" — so an absent or null
            # field would reach the resolve leg as something it may change code
            # for, defeating the one guardrail that is not configurable.
            or ((has("pre_existing")? // false) | not)
            or (.pre_existing? | type != "boolean")
            or (.title? | type != "string") or (.title == "")
          ) ] ) as $bad
      | if ($bad | length) > 0
        then bad("\($bad | length) finding(s) have a missing or out-of-range path, line, side, severity, category, pre_existing or title — first: \($bad[0] | tojson)")
        else empty end
    end' <<<"$payload" 2>/dev/null)" || problem="the payload is not parseable JSON"
  [[ -z "$problem" ]] && return 0
  printf '%s' "$problem"
  return 1
}

validate_resolve() {
  local payload="$1" problem
  problem="$(jq -r '
    def bad(m): m;
    if type != "object" then bad("the payload is not a JSON object")
    elif (has("dispositions") | not) or (.dispositions | type != "array")
      then bad("dispositions is missing or not an array")
    elif (has("wrap_up") | not) or (.wrap_up | type != "string") or (.wrap_up == "")
      then bad("wrap_up is missing or empty")
    elif ((.blocked // false) | type != "boolean")
      then bad("blocked is not a boolean")
    else
      ( [ .dispositions[] | select(
            (.finding_id? | type != "string") or (.finding_id == "")
            or ((.disposition? // "") | IN("fixed","skipped","deferred","rebutted","escalated") | not)
            or (.reply? | type != "string") or (.reply == "")
          ) ] ) as $bad
      | if ($bad | length) > 0
        then bad("\($bad | length) disposition(s) have a missing finding_id or reply, or a disposition outside the five allowed — first: \($bad[0] | tojson)")
        else empty end
    end' <<<"$payload" 2>/dev/null)" || problem="the payload is not parseable JSON"
  [[ -z "$problem" ]] && return 0
  printf '%s' "$problem"
  return 1
}

# Does this harness constrain its own output to the schema?
#
# All three shipped harnesses do, which is why the retry is not the normal path.
# It exists so that adding a harness without a schema flag does not also mean
# adding retry logic under time pressure. agy joined the list on evidence: its
# --json-schema was tested against a two-field schema and came back conforming,
# contradicting the amendment that asked for a fenced-JSON fallback.
validate_harness_is_schema_native() {
  case "$1" in
    claude|codex|agy) return 0 ;;
    *)                return 1 ;;
  esac
}
