#!/usr/bin/env bash
#
# State-layer tests: marker round-trip, pass numbering, revision detection,
# finding identity, termination boundaries.
#
# All of it against fixtures rather than GitHub — the logic is deterministic and
# its failure is silent, which is exactly the combination worth testing offline.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/ui.sh
source "$HERE/../lib/ui.sh"
# shellcheck source=../lib/auth.sh
source "$HERE/../lib/auth.sh"
# shellcheck source=../lib/state.sh
source "$HERE/../lib/state.sh"

pass=0 fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }

# --- marker round-trip -----------------------------------------------------
m='{"v":1,"leg":"review","pass":2,"state":"complete","verdict":"issues-remain","head_sha":"9f3c1ab"}'
body="Some human-readable summary.$(state_marker_encode "$m")"
is "a marker survives encode then decode" "$(state_marker_of "$body" | jq -c .)" "$(jq -c . <<<"$m")"

# A body with other HTML comments around it must not confuse the parse.
body2="<!-- unrelated -->
Text.$(state_marker_encode "$m")
<!-- trailing -->"
is "a marker parses beside adjacent HTML comments" "$(state_marker_of "$body2" | jq -r .pass)" "2"

is "a body with no marker yields nothing" "$(state_marker_of "just a comment")" ""

# --- finding identity ------------------------------------------------------
a="$(state_finding_id "lib/auth.ts" "Token refresh races with logout" "abcd1234")"
b="$(state_finding_id "lib/auth.ts" "  token   REFRESH races with logout " "abcd1234")"
is "the id is stable across whitespace and case in the title" "$a" "$b"
c="$(state_finding_id "lib/other.ts" "Token refresh races with logout" "abcd1234")"
[[ "$a" != "$c" ]] && ok "a different path is a different finding" \
  || notok "a different path is a different finding" "different ids" "both $a"

# --- pass numbering --------------------------------------------------------
is "no trusted marker means pass 1" "$(state_pass '[]')" "1"
is "one completed review means pass 2" \
  "$(state_pass '[{"leg":"review","pass":1,"state":"complete"}]')" "2"
is "the address leg does not advance the pass number" \
  "$(state_pass '[{"leg":"review","pass":1,"state":"complete"},{"leg":"address","pass":1,"state":"complete"}]')" "2"

# --- recovery --------------------------------------------------------------
claim='[{"leg":"review","pass":2,"state":"started","run_id":"7"}]'
is "an unfinished claim is found for recovery" \
  "$(state_open_claim "$claim" 2 review | jq -r .run_id)" "7"
done_='[{"leg":"review","pass":2,"state":"started"},{"leg":"review","pass":2,"state":"complete"}]'
is "a completed leg leaves no open claim" "$(state_open_claim "$done_" 2 review)" ""

# --- revision detection ----------------------------------------------------
markers='[{"leg":"review","pass":1,"state":"complete","head_sha":"aaa111"}]'
state_is_new_revision "$markers" "aaa111" \
  && notok "the same head SHA is not a new revision" "false" "true" \
  || ok "the same head SHA is not a new revision"
state_is_new_revision "$markers" "bbb222" \
  && ok "a different head SHA is a new revision" \
  || notok "a different head SHA is a new revision" "true" "false"
state_is_new_revision '[]' "aaa111" \
  && ok "a PR with no review marker is always a new revision" \
  || notok "a PR with no review marker is always a new revision" "true" "false"

# --- daily cap -------------------------------------------------------------
now="$(date +%s)"
recent="$(jq -cn --argjson n "$now" '[{ts:($n-100)},{ts:($n-200)},{ts:($n-90000)}]')"
is "runs are counted only within the last 24 hours" "$(state_runs_today "$recent")" "2"

# --- finding markers -------------------------------------------------------
fm="$(state_finding_marker "a1b2c3d4" 2 review)"
is "a per-write finding marker carries its id" \
  "$(sed -n 's/.*<!-- revloop:f \(.*\) -->.*/\1/p' <<<"$fm" | jq -r .id)" "a1b2c3d4"
is "a per-write finding marker carries its pass" \
  "$(sed -n 's/.*<!-- revloop:f \(.*\) -->.*/\1/p' <<<"$fm" | jq -r .pass)" "2"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
