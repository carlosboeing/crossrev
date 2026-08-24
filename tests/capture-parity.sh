#!/usr/bin/env bash
#
# capture-parity.sh — regenerate tests/fixtures/parity/.
#
# The native migration needs a fixed corpus a Go implementation can assert
# against, captured from the Bash implementation while it is the only one.
# Each fixture records the platform, the tr implementation and the locale it
# was captured under; every vector's expected value comes from calling the
# real library functions, never from a hand-written answer.
#
# Recapture deliberately: run this script when and only when the underlying
# behaviour changes on purpose, and read the diff before committing it. A
# recapture that changes an id or a prompt silently is exactly what
# tests/test-parity.sh exists to make loud.

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$HERE/.."
FIXDIR="$REPO_ROOT/tests/fixtures/parity"
mkdir -p "$FIXDIR"

# shellcheck source=../lib/ui.sh
source "$REPO_ROOT/lib/ui.sh"
# shellcheck source=../lib/diff.sh
source "$REPO_ROOT/lib/diff.sh"
# shellcheck source=../lib/sandbox.sh
source "$REPO_ROOT/lib/sandbox.sh"
# shellcheck source=../lib/prompt.sh
source "$REPO_ROOT/lib/prompt.sh"
# shellcheck source=../lib/state.sh
source "$REPO_ROOT/lib/state.sh"

platform="$(uname -s -r -m)"
tr_path="$(command -v tr)"
if tr --version </dev/null 2>/dev/null | grep -q GNU; then tr_flavor="GNU coreutils tr"; else tr_flavor="BSD tr"; fi
locale="${LC_ALL:-${LC_CTYPE:-${LANG:-unset}}}"

captured_json() {
  jq -n --arg p "$platform" --arg t "$tr_path ($tr_flavor)" --arg l "$locale" '{
    platform: $p,
    tr_implementation: $t,
    locale: $l,
    note: "state_finding_id and state_anchor pin LC_ALL=C internally, so ids and anchors are byte-oriented whatever locale this file was captured under."
  }'
}

# --- state_finding_id over (path, title, anchor) triples --------------------

fid_case() { # name path title anchor
  local id
  id="$(state_finding_id "$2" "$3" "$4")"
  jq -cn --arg n "$1" --arg p "$2" --arg t "$3" --arg a "$4" --arg i "$id" \
    '{name:$n, path:$p, title:$t, anchor:$a, id:$i}'
}

fid_cases() {
  fid_case "ascii-basic" "lib/auth.ts" "Token refresh races with logout" "abcd1234"
  fid_case "ascii-mixed-case-and-padding" "lib/auth.ts" "  token   REFRESH races with logout " "abcd1234"
  fid_case "ascii-empty-anchor" "lib/auth.ts" "Token refresh races with logout" ""
  fid_case "tab-in-title" "lib/x.ts" $'Token\trefresh\traces' "abcd1234"
  fid_case "newline-in-title" "lib/x.ts" $'Token\nrefresh\nraces' "abcd1234"
  fid_case "vertical-tab-in-title" "lib/x.ts" $'Token\vrefresh\vraces' "abcd1234"
  fid_case "form-feed-in-title" "lib/x.ts" $'Token\fraces\fagain' "abcd1234"
  fid_case "carriage-return-in-title" "lib/x.ts" $'Token\rraces\rhere' "abcd1234"
  fid_case "non-ascii-upper-case-title" "greek/x.ts" "ΣIGMA refresh races" ""
  fid_case "non-breaking-space-in-title" "w/x.ts" "$(printf 'token\xc2\xa0refresh')" ""
  fid_case "leading-tab-title" "lib/x.ts" $'\tLeading tab title' "abcd1234"
}

jq -n --argjson captured "$(captured_json)" \
  --argjson cases "$(fid_cases | jq -s .)" \
  '{captured:$captured, function:"state_finding_id", cases:$cases}' \
  >"$FIXDIR/state_finding_id.json"

# --- state_anchor over (file content, line) pairs ----------------------------

anchor_case() { # name line exists content
  local anchor=""
  if [[ "$3" == "true" ]]; then
    local f; f="$(mktemp)"
    printf '%s' "$4" >"$f"
    anchor="$(state_anchor "$f" "$2")"
    rm -f "$f"
  fi
  jq -cn --arg n "$1" --argjson l "$2" --arg e "$3" --arg c "${4-}" --arg a "$anchor" \
    '{name:$n, line:$l, exists:$e, content:(if $e == "true" then $c else null end), anchor:$a}'
}

anchor_cases() {
  anchor_case "six-line-file-line-1" 1 true \
    $'alpha beta\ngamma delta\nepsilon zeta\neta theta\niota kappa\nlambda mu\n'
  anchor_case "six-line-file-line-2" 2 true \
    $'alpha beta\ngamma delta\nepsilon zeta\neta theta\niota kappa\nlambda mu\n'
  anchor_case "missing-file" 3 false ""
  anchor_case "short-file-past-window" 9 true $'only\n'
}

jq -n --argjson captured "$(captured_json)" \
  --argjson cases "$(anchor_cases | jq -s .)" \
  '{captured:$captured, function:"state_anchor", cases:$cases}' \
  >"$FIXDIR/state_anchor.json"

# --- the marker codec --------------------------------------------------------

codec_case() { # name body
  local decoded
  decoded="$(state_marker_of "$2")"
  jq -cn --arg n "$1" --arg b "$2" --arg d "$decoded" \
    '{name:$n, body:$b, decoded:$d}'
}

codec_cases() {
  local one two
  one="$(state_marker_encode '{"v":1,"leg":"review","pass":1,"state":"complete","head_sha":"abc1234"}')"
  codec_case "one-marker" "Summary.${one}"
  two="$(state_marker_encode '{"v":1,"leg":"resolve","pass":1,"state":"complete"}')"
  codec_case "two-markers-one-body" "A.${one}${two}"
  codec_case "marker-split-across-lines" \
    "$(printf 'Summary.\n<!-- crossrev: {"v":1,"leg":"rev\niew","pass":1} -->')"

  # Each of the three vocabulary migrations, in isolation.
  codec_case "migration-top-level-dispositions" \
    "Pass.$(state_marker_encode '{"v":1,"leg":"resolve","pass":1,"state":"complete",
      "dispositions":[{"finding_id":"aaaa000000000001","resolution":"fixed"}]}')"
  codec_case "migration-per-finding-disposition" \
    "Pass.$(state_marker_encode '{"v":1,"leg":"resolve","pass":1,"state":"complete",
      "resolutions":[{"finding_id":"aaaa000000000001","disposition":"rebutted"}]}')"
  codec_case "migration-finding-disposition" \
    "Findings.$(state_marker_encode '{"v":1,"leg":"review","pass":1,
      "findings":[{"id":"aaaa000000000001","disposition":"rebutted"}]}')"
}

jq -n --argjson captured "$(captured_json)" \
  --argjson cases "$(codec_cases | jq -s .)" \
  '{captured:$captured, function:"state_marker_of", cases:$cases}' \
  >"$FIXDIR/marker_codec.json"

# --- both assembled prompts, byte for byte -----------------------------------

workdir="$(mktemp -d)"

diff_text='diff --git a/app.ts b/app.ts
--- a/app.ts
+++ b/app.ts
@@ -1,2 +1,3 @@
 export const ok = 1
+export function refresh() { fetch("/t") }
 export const stale = 2'
printf '%s' "$diff_text" >"$workdir/diff.patch"

review_meta='{"repo":"acme/widget","pr":42,"pass":2,"head_sha":"9f3c1ab4d2e5",
  "title":"Add refresh helper","min_fix_severity":"medium",
  "body":"Adds a refresh helper.\n\nFixes the timeout."}'
prior='[
  {"id":"aaaa000000000001","path":"app.ts","line":2,"severity":"high",
   "category":"security","pre_existing":false,
   "title":"Fetch without a timeout hangs the request"},
  {"id":"bbbb000000000002","path":"app.ts","line":3,"severity":"low",
   "pre_existing":true,"title":"stale is unused"}
]'
threads='[
  {"id":"t1","isResolved":false,"isOutdated":false,"path":"app.ts","line":2,
   "comments":[
    {"databaseId":5001,"author":{"login":"alice"},
     "body":"No timeout here. <!-- crossrev:f {\"id\":\"aaaa000000000001\",\"pass\":1,\"leg\":\"review\"} -->"},
    {"databaseId":5002,"author":{"login":"bob"},"body":"Agreed."}]},
  {"id":"t2","isResolved":true,"isOutdated":false,"path":"app.ts","line":3,
   "comments":[{"databaseId":5003,"author":{"login":"alice"},"body":"Pre-existing."}]}
]'

# Copied, not printed through a substitution: a command substitution strips the
# final newline, and the vector pins the skill's exact bytes.
cp "$REPO_ROOT/skills/pr-review/SKILL.md" "$workdir/review-skill.md"
prompt_review "$workdir/review-prompt.txt" "$workdir/review-skill.md" \
  "$workdir/diff.patch" "$review_meta" "$prior" "$threads"

resolve_meta='{"repo":"acme/widget","pr":42,"pass":2,"head_sha":"9f3c1ab4d2e5",
  "title":"Add refresh helper","min_fix_severity":"medium",
  "backlog":"backlog/tasks","base_sha":"","crossrev_email":""}'
findings='[
  {"number":1,"id":"aaaa000000000001","severity":"high","category":"security",
   "pre_existing":false,"path":"app.ts","line":2,
   "title":"Fetch without a timeout hangs the request",
   "why":"A hung request blocks the worker.","fix":"Pass a timeout.","may_fix":true},
  {"number":2,"id":"cccc000000000003","severity":"low","category":"style",
   "pre_existing":true,"path":"app.ts","line":3,"title":"stale is unused",
   "why":"Dead code invites drift.","may_fix":false}
]'
candidates='{
  "aaaa000000000001":[{"number":17,"state":"OPEN","title":"Requests can hang forever"}]
}'

cp "$REPO_ROOT/skills/pr-resolve/SKILL.md" "$workdir/resolve-skill.md"
prompt_resolve "$workdir/resolve-prompt.txt" "$workdir/resolve-skill.md" \
  "$workdir/diff.patch" "$resolve_meta" "$findings" "$threads" "$candidates"

for leg in review resolve; do
  if [[ $leg == review ]]; then inputs_meta="$review_meta"; else inputs_meta="$resolve_meta"; fi
  if [[ $leg == review ]]; then prior_or_findings="$prior"; else prior_or_findings="$findings"; fi

  # --rawfile keeps every byte exact, including the trailing newline.
  jq -n --argjson captured "$(captured_json)" \
    --rawfile skill "$workdir/$leg-skill.md" \
    --rawfile diff "$workdir/diff.patch" \
    --rawfile prompt "$workdir/$leg-prompt.txt" \
    --argjson meta "$inputs_meta" \
    --argjson prior_or_findings "$prior_or_findings" \
    --argjson threads "$threads" \
    --argjson candidates "$candidates" \
    --arg leg "$leg" '
    {
      captured: $captured,
      function: ("prompt_" + $leg),
      inputs: ({
        skill: $skill,
        diff: $diff,
        meta: $meta,
        threads: $threads
      } + (if $leg == "review"
           then {prior: $prior_or_findings}
           else {findings: $prior_or_findings, candidates: $candidates} end)),
      prompt: $prompt
    }' >"$FIXDIR/prompt_$leg.json"
done

rm -rf "$workdir"
printf 'parity vectors written to %s\n' "$FIXDIR"
