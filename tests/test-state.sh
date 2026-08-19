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
# shellcheck source=../lib/github.sh
source "$HERE/../lib/github.sh"
# shellcheck source=../lib/state.sh
source "$HERE/../lib/state.sh"

pass=0 fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" || notok "$1" "contains '$3'" "$2"; }

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

# --- old vocabulary on a marker already on a pull request ------------------
#
# `dispositions` became `resolutions` and `rebutted` became `disputed`. A pull
# request mid-loop when the rename shipped carries the old keys, and every read
# of them is a `jq` that returns empty rather than failing — so an unmigrated
# marker would report a pass as having settled nothing, and the loop would drive
# a finished pass again. Migrated at the decode, which is the one place every
# read goes through.
old_marker='{"v":1,"leg":"resolve","pass":1,"state":"complete","dispositions":[
  {"finding_id":"aaaa000000000001","disposition":"fixed"},
  {"finding_id":"aaaa000000000002","disposition":"rebutted"}]}'
old_body="Pass summary.$(state_marker_encode "$old_marker")"
migrated="$(state_marker_of "$old_body")"

is "an old marker's dispositions are read as resolutions" \
  "$(jq -r '.resolutions | length' <<<"$migrated")" "2"
is "and the old key is gone rather than carried alongside" \
  "$(jq -r 'has("dispositions")' <<<"$migrated")" "false"
is "a per-finding disposition is read as its resolution" \
  "$(jq -r '.resolutions[0].resolution' <<<"$migrated")" "fixed"
is "and rebutted is read as disputed" \
  "$(jq -r '.resolutions[1].resolution' <<<"$migrated")" "disputed"

# A review marker carries the same word on each finding.
old_review='{"v":1,"leg":"review","pass":1,"findings":[
  {"id":"aaaa000000000001","disposition":"rebutted"}]}'
migrated_review="$(state_marker_of "Findings.$(state_marker_encode "$old_review")")"
is "a finding's old disposition is migrated too" \
  "$(jq -r '.findings[0].resolution' <<<"$migrated_review")" "disputed"

# A marker already written in the new vocabulary passes through untouched.
new_marker='{"v":1,"leg":"resolve","pass":2,"resolutions":[
  {"finding_id":"aaaa000000000003","resolution":"deferred"}]}'
is "a marker already using the new keys is left alone" \
  "$(state_marker_of "Pass.$(state_marker_encode "$new_marker")" | jq -c .)" \
  "$(jq -c . <<<"$new_marker")"

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
is "the resolve leg does not advance the pass number" \
  "$(state_pass '[{"leg":"review","pass":1,"state":"complete"},{"leg":"resolve","pass":1,"state":"complete"}]')" "2"

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

# --- repository-wide daily cap --------------------------------------------
now="$(date +%s)"
cutoff="$(( now - 86400 ))"
review_body="review$(state_marker_encode "$(jq -cn --argjson ts "$now" '{leg:"review",state:"complete",ts:$ts}')")"
resolve_body="resolve$(state_marker_encode "$(jq -cn --argjson ts "$now" '{leg:"resolve",state:"complete",ts:$ts}')")"

gh_repo_issue_comments_page() {
  jq -cn --arg a trusted --arg rb "$review_body" --arg xb "$resolve_body" '[
    {user:{login:$a},body:$rb,issue_url:"https://api.github.test/repos/acme/widget/issues/7"},
    {user:{login:$a},body:$xb,issue_url:"https://api.github.test/repos/acme/widget/issues/7"},
    {user:{login:$a},body:$rb,issue_url:"https://api.github.test/repos/acme/widget/issues/8"}
  ]'
}
is "one review-and-resolve cycle counts once, not once per leg" \
  "$(state_prs_reviewed_today acme/widget trusted "$cutoff" 25 42 '[]')" "2"

current_markers="$(jq -cn --argjson ts "$now" '[{leg:"review",state:"complete",ts:$ts}]')"
is "a pull request already reviewed in the window adds no new unit" \
  "$(state_prs_reviewed_today acme/widget trusted "$cutoff" 25 42 "$current_markers")" "0"

gh_repo_issue_comments_page() {
  local page="$3"
  jq -cn --arg a trusted --arg b "$review_body" --argjson p "$page" '
    [range(0;100) | {user:{login:$a},body:$b,
      issue_url:("https://api.github.test/repos/acme/widget/issues/" + (($p * 1000 + .) | tostring))}]'
}
truncated="$(state_prs_reviewed_today acme/widget trusted "$cutoff" 2000 42 '[]' 2>&1)"
has "a daily count truncated after ten pages announces itself" "$truncated" "first 10 pages"
is "and the bounded read returns the distinct count it saw" "${truncated##*$'\n'}" "1000"

# The cap is a stopping point rather than a tally. A whole page is folded in at
# once, so the count can pass the cap inside one page and what gets reported must
# still be the cap itself.
is "a count that trips the cap reports the cap, not where it stopped" \
  "$(state_prs_reviewed_today acme/widget trusted "$cutoff" 5 42 '[]')" "5"

# --- finding markers -------------------------------------------------------
fm="$(state_finding_marker "a1b2c3d4" 2 review)"
is "a per-write finding marker carries its id" \
  "$(sed -n 's/.*<!-- crossrev:f \(.*\) -->.*/\1/p' <<<"$fm" | jq -r .id)" "a1b2c3d4"
is "a per-write finding marker carries its pass" \
  "$(sed -n 's/.*<!-- crossrev:f \(.*\) -->.*/\1/p' <<<"$fm" | jq -r .pass)" "2"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
