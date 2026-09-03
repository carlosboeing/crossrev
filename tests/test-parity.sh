#!/usr/bin/env bash
#
# Parity vectors: the Bash implementation must still produce exactly what
# tests/fixtures/parity records, on every platform that runs this suite.
#
# The fixtures were captured once by tests/capture-parity.sh, from the real
# library functions, with each file naming the platform, tr implementation and
# locale it was captured under. A native port will assert against the same
# files; this suite holds the Bash side of that contract until then. Any
# failure here is either an unintended behaviour change or a recapture made
# without reading its own diff.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tmproot.sh
source "$HERE/tmproot.sh"
# shellcheck source=../lib/ui.sh
source "$HERE/../lib/ui.sh"
# shellcheck source=../lib/diff.sh
source "$HERE/../lib/diff.sh"
# shellcheck source=../lib/sandbox.sh
source "$HERE/../lib/sandbox.sh"
# shellcheck source=../lib/prompt.sh
source "$HERE/../lib/prompt.sh"
# shellcheck source=../lib/state.sh
source "$HERE/../lib/state.sh"

PARITY="$HERE/fixtures/parity"

pass=0 fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"; _tmproot_cleanup' EXIT

# Every fixture names where it was captured, or a silent recapture has nothing
# to be compared against later.
provenance_is_recorded() {
  local f name
  for f in "$PARITY"/*.json; do
    name="$(basename "$f")"
    [[ -n "$(jq -r '.captured.platform // empty' "$f")" ]] \
      && [[ -n "$(jq -r '.captured.tr_implementation // empty' "$f")" ]] \
      && [[ -n "$(jq -r '.captured.locale // empty' "$f")" ]] \
      && ok "$name records platform, tr implementation and locale" \
      || notok "$name records platform, tr implementation and locale" "all three present" "$(jq -c .captured "$f")"
  done
}

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  is "finding id: $name" \
    "$(state_finding_id "$(jq -r .path <<<"$c")" "$(jq -jr .title <<<"$c")" "$(jq -r .anchor <<<"$c")")" \
    "$(jq -r .id <<<"$c")"
done < <(jq -c '.cases[]' "$PARITY/state_finding_id.json")

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  if [[ "$(jq -r .exists <<<"$c")" == "true" ]]; then
    cf="$workdir/content.ts"
    jq -jr .content <<<"$c" >"$cf"
    is "anchor: $name" "$(state_anchor "$cf" "$(jq -r .line <<<"$c")")" "$(jq -r .anchor <<<"$c")"
  else
    is "anchor: $name" "$(state_anchor "$workdir/does-not-exist.ts" "$(jq -r .line <<<"$c")")" ""
  fi
done < <(jq -c '.cases[]' "$PARITY/state_anchor.json")

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  is "marker codec: $name" \
    "$(state_marker_of "$(jq -jr .body <<<"$c")")" \
    "$(jq -r .decoded <<<"$c")"
done < <(jq -c '.cases[]' "$PARITY/marker_codec.json")

rebuild_prompt() {
  local fixture="$1" leg="$2" out="$3" skill diff meta threads
  skill="$workdir/skill.md"; diff="$workdir/diff.patch"
  jq -j '.inputs.skill' "$fixture" >"$skill"
  jq -j '.inputs.diff' "$fixture" >"$diff"
  meta="$(jq -c '.inputs.meta' "$fixture")"
  threads="$(jq -c '.inputs.threads' "$fixture")"
  if [[ "$leg" == "review" ]]; then
    prompt_review "$out" "$skill" "$diff" "$meta" \
      "$(jq -c '.inputs.prior' "$fixture")" "$threads"
  else
    prompt_resolve "$out" "$skill" "$diff" "$meta" \
      "$(jq -c '.inputs.findings' "$fixture")" "$threads" \
      "$(jq -c '.inputs.candidates' "$fixture")"
  fi
}

for leg in review resolve; do
  fixture="$PARITY/prompt_$leg.json"
  out="$workdir/prompt.txt"; expected="$workdir/expected.txt"
  rebuild_prompt "$fixture" "$leg" "$out"
  jq -j '.prompt' "$fixture" >"$expected"
  if cmp -s "$out" "$expected"; then
    ok "prompt_$leg assembles byte for byte ($(wc -c <"$out" | tr -d ' ') bytes)"
  else
    notok "prompt_$leg assembles byte for byte" "$(wc -c <"$expected" | tr -d ' ') bytes" \
      "$(wc -c <"$out" | tr -d ' ') bytes, first difference: $(cmp "$out" "$expected" 2>&1 | head -1)"
  fi
done

provenance_is_recorded

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
