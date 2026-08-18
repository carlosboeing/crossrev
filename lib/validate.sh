# shellcheck shell=bash
# lib/validate.sh — checks on what a harness returned.
#
# Two kinds of check live here, and the exit code says which one failed.
#
#   0  fine
#   1  SHAPE — a key is missing, a type is wrong, an enum value is out of range.
#      This is deliberately NOT general JSON Schema validation. Neither jq nor yq
#      can do that, and pretending otherwise would be worse than saying so:
#      required keys present, types right, enum values in range, and the schemas
#      stay flat enough for that to be sufficient. A schema that outgrows this
#      check is the signal to add a real validator, not to let the check quietly
#      drift behind it.
#   2  SEMANTIC — the shape is perfect and the content contradicts what the
#      orchestrator itself supplied: a finding number nothing was numbered with,
#      the same finding answered twice, one left out, an issue number nobody
#      offered.
#
# The split exists because the two mean opposite things about who is at fault.
# Every shipped harness constrains output to the schema natively, so a shape
# failure there is an adapter or harness bug and a retry reproduces it. A
# semantic failure is model drift by definition — no adapter is involved — so it
# earns one more attempt rather than costing the whole pass. `run_invoke` reads
# the code and decides. On a fenced-JSON fallback path the shape half is the only
# check there is, which is why it exists at all.
#
# The semantic checks need to know what was handed over, which arrives as one
# optional JSON argument: {"findings": 3, "candidates": [19, 31]}. Without it
# they are skipped, because comparing against an invented expectation would be
# worse than not comparing.

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

# validate_resolve <payload> [expectations]
#
# Returns 1 for a shape problem and 2 for a semantic one. See the header.
validate_resolve() {
  local payload="$1" expect="${2:-}" problem
  problem="$(jq -r '
    def bad(m): m;
    if type != "object" then bad("the payload is not a JSON object")
    elif (has("resolutions") | not) or (.resolutions | type != "array")
      then bad("resolutions is missing or not an array")
    elif (has("summary") | not) or (.summary | type != "string") or (.summary == "")
      then bad("summary is missing or empty")
    elif ((.blocked // false) | type != "boolean")
      then bad("blocked is not a boolean")
    # Typed rather than required, which is the same call `blocked_reason` above
    # gets: the schema lists both under "required" because one harness enforces
    # strict mode, not because a payload without them says anything false. An
    # absent subject is a resolver that wrote none, and the commit carries the
    # generic one — a fix is not worth failing over a missing message. A subject
    # of the wrong type is different, because `jq -r` turns a number, a boolean
    # or an empty array into a plausible-looking string and commits it.
    elif (has("commit_subject")) and (.commit_subject != null)
         and (.commit_subject | type != "string")
      then bad("commit_subject is \(.commit_subject | type), and a commit subject is a string or null")
    else
      ( [ .resolutions[] | select(
            # jq has one number type, so whole-ness is checked rather than
            # assumed. A harness that constrains output cannot return 1.5 here,
            # and the fenced-JSON path has no such guarantee.
            (.finding_number? | type != "number")
            or ((.finding_number | floor) != .finding_number)
            or ((.resolution? // "") | IN("fixed","skipped","deferred","disputed","escalated") | not)
            or (.reply? | type != "string") or (.reply == "")
            or ((.duplicate_of? // 0) | type != "number")
          ) ] ) as $bad
      | if ($bad | length) > 0
        then bad("\($bad | length) resolution(s) have a missing or non-whole finding_number, a missing reply, a non-numeric duplicate_of, or a resolution outside the five allowed — first: \($bad[0] | tojson)")
        else empty end
    end' <<<"$payload" 2>/dev/null)" || problem="the payload is not parseable JSON"
  if [[ -n "$problem" ]]; then printf '%s' "$problem"; return 1; fi

  # Nothing to compare against: the shape half is the whole check.
  [[ -n "$expect" ]] || return 0

  problem="$(jq -r --argjson e "$expect" '
    ($e.findings // 0) as $n
    | [ .resolutions[].finding_number ] as $got
    | ([ $got[] | select(. < 1 or . > $n) ] | unique) as $range
    | ([ $got | group_by(.)[] | select(length > 1) | .[0] ] | unique) as $twice
    | (([range(1; $n + 1)] - $got) | unique) as $missing
    | ([ .resolutions[] | select(.duplicate_of != null) | .duplicate_of ]
       - ($e.candidates // []) | unique) as $invented
    | if ($range | length) > 0
      then "finding number(s) \($range | join(", ")) do not exist — \($n) finding(s) were supplied, numbered 1 to \($n)"
      elif ($twice | length) > 0
      then "finding(s) \($twice | join(", ")) were settled more than once"
      elif ($missing | length) > 0
      then "finding(s) \($missing | join(", ")) got no resolution at all, and a finding left out gets no reply and no thread resolution"
      elif ($invented | length) > 0
      then "duplicate_of names issue(s) \($invented | join(", ")), which were not among the candidates supplied in the prompt"
      else empty end' <<<"$payload" 2>/dev/null)" \
    || problem="the payload stopped being parseable between the two checks"
  if [[ -n "$problem" ]]; then printf '%s' "$problem"; return 2; fi
  return 0
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
