#!/usr/bin/env bash
#
# tests/eval/self-test.sh — the offline full-loop runner's own test.
#
# A full cycle of a one-case manifest against the suite's harness stubs with
# no model call (reviewer claude-stub, resolver claude-stub through the
# single-harness path; never codex, which is a tripwire that exits loudly
# instead of running). Two arms of the case run back to back, asserting no
# pushed commit, comment, label or marker carries over, plus one
# planted-findings resolve-only run.
#
# Not run by tests/run.sh (which globs only tests/test-*.sh) and never run
# by CI. Run it directly:
#
#   bash tests/eval/self-test.sh
#
# It builds the binary, builds a frozen fixture repository under a temp
# directory, generates a one-case manifest pointing at it, drives
# tests/eval/run-loop.sh, and asserts on the results directory.

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUNNER="$ROOT/tests/eval/run-loop.sh"
ADJUDICATE="$ROOT/tests/eval/adjudicate.sh"

if [[ ! -f "$RUNNER" ]]; then
  printf 'FAIL: the offline full-loop runner is missing: %s\n' "$RUNNER" >&2
  printf 'FAIL: write tests/eval/run-loop.sh first (this test defines its contract)\n' >&2
  exit 1
fi
if [[ ! -f "$ADJUDICATE" ]]; then
  printf 'FAIL: the adjudication-sheet generator is missing: %s\n' "$ADJUDICATE" >&2
  exit 1
fi

pass=0; fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" || notok "$1" "contains '$3'" "$2"; }
hasnt() { [[ "$2" != *"$3"* ]] && ok "$1" || notok "$1" "does not contain '$3'" "$2"; }

T="$(mktemp -d)"
# A green run cleans up; a red one keeps the tree so the failure can be read.
trap '(( fail == 0 )) && rm -rf "$T" || printf "kept tree: %s\n" "$T"' EXIT

printf '\nbuilding the binary\n'
BIN="$T/bin/crossrev"
bash "$ROOT/scripts/build-binary.sh" host "$BIN" >/dev/null 2>&1 || {
  printf 'FAIL: the binary did not build\n' >&2
  exit 1
}

# --- the frozen fixture repository ---------------------------------------
#
# A bare repository holding main and feature, built the way
# tests/harness.sh:184-217 builds its fixtures (throwaway checkout, real
# history, github.com remote rewritten to the bare copy). The runner copies
# this frozen original fresh for every case and arm, so no arm can observe
# another through it.

printf '\nfreezing a one-case fixture repository\n'
FIX_TEMPLATE="$(mktemp -d)"
FW="$T/frozen-work"
BARE="$T/frozen.git"
git init -q --bare --template="$FIX_TEMPLATE" "$BARE"
(
  cd "$FW" 2>/dev/null || { mkdir -p "$FW"; cd "$FW"; }
  git init -q -b main --template="$FIX_TEMPLATE" .
  git config user.email t@example.com
  git config user.name Test
  printf 'export const ok = 1\n' >app.ts
  mkdir -p .github
  cat >.github/crossrev.yml <<'EOF'
version: 2
mode: local
policy:
  min_fix_severity: medium
  max_passes_per_cycle: 3
  max_files_changed_per_pr: 200
  max_prs_per_day: 25
reviewer:
  harness: claude
  model: reviewer-model
resolver:
  harness: claude
  model: resolver-model
backlog:
  destination: none
EOF
  git add -A && git commit -q -m base
  git remote add origin "https://github.com/acme/widget.git"
  git config "url.$BARE.pushInsteadOf" "https://github.com/acme/widget.git"
  git checkout -q -b feature
  printf 'export const ok = 1\nexport function refresh() { fetch("/t") }\n' >app.ts
  git add -A && git commit -q -m feature
  git push -q origin main feature
)
BASE_SHA="$(git -C "$FW" rev-parse main)"
HEAD_SHA="$(git -C "$FW" rev-parse feature)"
is "the frozen fixture has a base and a head" "$([[ -n "$BASE_SHA" && -n "$HEAD_SHA" && "$BASE_SHA" != "$HEAD_SHA" ]] && echo yes || echo no)" "yes"

# --- the one-case manifest -------------------------------------------------
#
# review_payloads carry __EVAL_HEAD__ where the runner substitutes the head
# under review (the manifest head on pass 1, the repair head after a push).
# The two arms differ in their resolve payloads and edits, which is what
# makes carryover observable: arm-a's commit subject and comment text must
# never appear in arm-b's results, and vice versa.

review_payload() {
  # The converged answer cites file-level evidence (a null range), the way
  # the suite's repair re-drive does: after the fix only line 2 is a
  # changed line, so a 1-2 range reads outside what was supplied.
  jq -cn --arg verdict "$1" --arg cov "$2" '{
    verdict:$verdict, blocked_reason:null, prior:null,
    findings:(if $verdict == "converged" then [] else [{
      path:"app.ts", line:2, side:"RIGHT", severity:"high",
      category:"correctness", pre_existing:false,
      title:"Unchecked fetch response", why:"A failed request looks like a success",
      fix:"Check response.ok"}] end),
    coverage:[{unit_number:1, verdict:$cov,
      finding_numbers:(if $cov == "finding" then [1] else [] end),
      evidence:[(if $cov == "finding"
        then {path:"app.ts", revision:"__EVAL_HEAD__",
          start_line:1, end_line:2, source:"git", note:null}
        else {path:"app.ts", revision:"__EVAL_HEAD__",
          start_line:null, end_line:null, source:"git", note:null} end)],
      reason:null}],
    examined_scope:"read app.ts at the head", known_limits:[]}'
}

resolve_payload() {
  jq -cn --arg subject "$1" --arg reply "$2" '{
    blocked:false, blocked_reason:null, commit_subject:$subject,
    summary:"Checked the fetch response before reading it.",
    resolutions:[{finding_number:1, resolution:"fixed",
      reply:$reply, persist:null, duplicate_of:null}]}'
}

resolve_edit_for() {
  printf 'printf "%%s\\n" "export const ok = 1" "export async function refresh() { const r = await fetch(\\"/t\\"); if (!r.ok) throw new Error(\\"bad\\") } // %s" > app.ts' "$1"
}

MANIFEST="$T/manifest.json"
jq -n \
  --arg bare "$BARE" \
  --arg base "$BASE_SHA" \
  --arg head "$HEAD_SHA" \
  --argjson rev1 "$(review_payload issues-remain finding)" \
  --argjson rev2 "$(review_payload converged no_issue)" \
  --argjson res_a "$(resolve_payload \
    "fix(api): check the response status before reading it (blue)" \
    "Added the ok check (blue).")" \
  --argjson res_b "$(resolve_payload \
    "fix(api): check the response status before reading it (green)" \
    "Added the ok check (green).")" \
  --arg edit_a "$(resolve_edit_for blue)" \
  --arg edit_b "$(resolve_edit_for green)" \
  --arg edit_p "$(resolve_edit_for amber)" \
  '{
    version:1, repo:"acme/widget", pr:42, frozen_repo:$bare,
    reviewer:{harness:"claude", model:"reviewer-model-eval"},
    resolver:{harness:"claude", model:"resolver-model-eval"},
    trusted_user:"eval-reviewer",
    cases:[{
      id:"refresh-helper", base:$base, head:$head,
      base_branch:"main", head_branch:"feature", mode:"full",
      arms:[
        {id:"arm-a", review_payloads:[$rev1,$rev2], resolve_payload:$res_a,
         resolve_edit:$edit_a},
        {id:"arm-b", review_payloads:[$rev1,$rev2], resolve_payload:$res_b,
         resolve_edit:$edit_b}
      ]
    },
    {
      id:"planted-check", base:$base, head:$head,
      base_branch:"main", head_branch:"feature", mode:"planted",
      arms:[{
        id:"arm-p",
        real_findings:[{id:"a1b2c3d4e5f60718", path:"app.ts", line:2,
          side:"RIGHT", severity:"high", category:"correctness",
          pre_existing:false, title:"Unchecked fetch response",
          why:"A failed request looks like a success", fix:"Check response.ok"}],
        planted_findings:[{id:"f1e2d3c4b5a69780", path:"app.ts", line:1,
          side:"RIGHT", severity:"medium", category:"maintainability",
          pre_existing:false, title:"Untyped legacy export",
          why:"The legacy export carries no type", fix:"Type the export"}],
        resolve_payload:{
          blocked:false, blocked_reason:null,
          commit_subject:"fix(api): answer the planted and real findings (amber)",
          summary:"Answered both findings.",
          resolutions:[
            {finding_number:1, resolution:"fixed",
             reply:"Added the ok check (amber).", persist:null, duplicate_of:null},
            {finding_number:2, resolution:"fixed",
             reply:"Typed the export (amber).", persist:null, duplicate_of:null}]},
        resolve_edit:$edit_p
      }]
    }]
  }' >"$MANIFEST"
is "the manifest is valid JSON" "$(jq -e '.version == 1 and (.cases | length) == 2' "$MANIFEST" >/dev/null 2>&1 && echo yes || echo no)" "yes"

# --- run the loop ----------------------------------------------------------

# A git wrapper that records every fetch invocation: the offline loop
# must fetch the repaired head from the arm's local bare copy, never from
# the github.com remote address. It sits on PATH only for the main runner
# invocation below and execs the real git for everything.
SHIMBIN="$T/shimbin"
FETCH_LOG="$T/fetch.log"
REAL_GIT="$(command -v git)"
mkdir -p "$SHIMBIN"
: >"$FETCH_LOG"
cat >"$SHIMBIN/git" <<EOF
#!/usr/bin/env bash
_want=0
for _a in "\$@"; do [[ "\$_a" == "fetch" ]] && _want=1; done
(( _want )) && printf '%s %s\n' "\$PWD" "\$*" >>"$FETCH_LOG"
exec "$REAL_GIT" "\$@"
EOF
chmod +x "$SHIMBIN/git"

printf '\nrunning the offline loop\n'
R="$T/results"
PATH="$SHIMBIN:$PATH" bash "$RUNNER" --manifest "$MANIFEST" --results-dir "$R" --bin "$BIN" \
  >"$T/runner-out.txt" 2>&1
runner_rc=$?
if (( runner_rc != 0 )); then
  printf '\n--- runner output ---\n'
  cat "$T/runner-out.txt"
  printf '%s\n' "--- end runner output (kept tree: $T) ---"
fi
is "the runner exits clean on the one-case manifest" "$runner_rc" "0"

# --- full-mode arms ----------------------------------------------------------
for arm in arm-a arm-b; do
  D="$R/refresh-helper/$arm"
  is "results exist for $arm" "$([[ -d "$D" ]] && echo yes || echo no)" "yes"
  is "$arm reaches the converged label" \
    "$(jq -r '.terminal // empty' "$D/result.json" 2>/dev/null)" "crossrev/converged"
  has "$arm ran review then resolve then review" \
    "$(jq -r '.legs | join(",")' "$D/result.json" 2>/dev/null)" "review,resolve,review"
  for f in markers.json findings.json resolutions.json reads.log usage.json \
    passes.jsonl labels.json result.json; do
    is "$arm wrote $f" "$([[ -f "$D/$f" ]] && echo yes || echo no)" "yes"
  done
  has "$arm recorded a reviewing verdict" "$(cat "$D"/run-*.log)" "verdict: issues-remain"
  has "$arm recorded the resolution" "$(cat "$D"/run-*.log)" "resolved pass 1"
  has "$arm usage names the pinned reviewer model" "$(cat "$D/usage.json")" "reviewer-model-eval"
done

# --- no carryover between the two back-to-back arms ---------------------------
A="$R/refresh-helper/arm-a"
B="$R/refresh-helper/arm-b"
log_subjects() { git --git-dir="$1/work/origin.git" log --format=%s refs/heads/feature; }
is "arm-a pushed its own fix" \
  "$(log_subjects "$A" | grep -c 'blue' || true)" "1"
hasnt "arm-a carries no arm-b commit" "$(log_subjects "$A")" "green"
is "arm-b pushed its own fix" \
  "$(log_subjects "$B" | grep -c 'green' || true)" "1"
hasnt "arm-b carries no arm-a commit" "$(log_subjects "$B")" "blue"
arm_text() { cat "$1"/markers.json "$1"/findings.json "$1"/resolutions.json "$1"/run-resolve-*.log; }
hasnt "arm-a carries no arm-b comment text" "$(arm_text "$A")" "green"
hasnt "arm-b carries no arm-a comment text" "$(arm_text "$B")" "blue"
is "arm-a labels converge" \
  "$(jq -r '.labels | map(select(. == "crossrev/converged")) | length' "$A/labels.json")" "1"
is "arm-b labels converge" \
  "$(jq -r '.labels | map(select(. == "crossrev/converged")) | length' "$B/labels.json")" "1"

# --- the repair reached the file, and labels stay live ----------------------
for arm in arm-a arm-b; do
  tag="blue"; [[ "$arm" == "arm-b" ]] && tag="green"
  arm_file="$(git --git-dir="$R/refresh-helper/$arm/work/origin.git" show refs/heads/feature:app.ts 2>/dev/null || true)"
  has "$arm wrote the repaired refresh helper to app.ts" "$arm_file" "if (!r.ok)"
  has "$arm kept the untouched export beside it" "$arm_file" "export const ok = 1"
  has "$arm tagged its own repair" "$arm_file" "$tag"
  checkout_file="$(cat "$R/refresh-helper/$arm/work/checkout/app.ts" 2>/dev/null || true)"
  has "$arm fast-forwarded its checkout to the repaired file" "$checkout_file" "if (!r.ok)"
  has "$arm checkout carries its own repair tag" "$checkout_file" "$tag"
done
hasnt "arm-a dropped its stale pass label" "$(cat "$A/labels.json")" "crossrev/pass-1"
hasnt "arm-b dropped its stale pass label" "$(cat "$B/labels.json")" "crossrev/pass-1"
# pr view must agree with the live label list: the stand-in overlays the
# live labels onto the seeded pull request object.
pr_view_labels() {
  (
    export CROSSREV_GH_STATE="$1/state" CROSSREV_GH_ROUTES="$1/routes"
    export CROSSREV_GH_LOG=/dev/null CROSSREV_GH_AUTHOR=eval-reviewer
    "$ROOT/tests/stub/gh" pr view 42 --repo acme/widget --json number,labels,headRefOid \
      --jq '.labels | map(.name) | join(",")'
  )
}
is "pr view agrees with the live labels for arm-a" \
  "$(pr_view_labels "$A")" "$(jq -r '.labels | join(",")' "$A/labels.json")"
is "pr view agrees with the live labels for arm-b" \
  "$(pr_view_labels "$B")" "$(jq -r '.labels | join(",")' "$B/labels.json")"
hasnt "repoint fetches the local copy, never the network remote" "$(cat "$FETCH_LOG")" " origin"

# --- the synthetic base revision ----------------------------------------------
is "the results record the base to base-prime mapping" \
  "$(jq -r '.["refresh-helper"].base' "$R/base-map.json" 2>/dev/null)" "$BASE_SHA"
BASE_PRIME="$(jq -r '.["refresh-helper"].base_prime // empty' "$R/base-map.json" 2>/dev/null)"
is "base-prime differs from the case base" \
  "$([[ -n "$BASE_PRIME" && "$BASE_PRIME" != "$BASE_SHA" ]] && echo yes || echo no)" "yes"
eval_config_on_base_prime="$(git --git-dir="$A/work/origin.git" show "$BASE_PRIME:.github/crossrev.yml" 2>/dev/null || true)"
for key in "store: marker" "max_passes_per_cycle: 6" "destination: none" \
  "min_fix_severity: medium" "reviewer-model-eval" "resolver-model-eval"; do
  has "base-prime carries the uniform eval config ($key)" "$eval_config_on_base_prime" "$key"
done

# --- planted-findings resolve-only run -----------------------------------------
P="$R/planted-check/arm-p"
is "results exist for the planted arm" "$([[ -d "$P" ]] && echo yes || echo no)" "yes"
is "the planted run is resolve-only" \
  "$(jq -r '.legs | join(",")' "$P/result.json" 2>/dev/null)" "resolve"
has "the planted marker held the real finding" "$(cat "$P/markers.json")" "Unchecked fetch response"
has "and the planted finding" "$(cat "$P/markers.json")" "Untyped legacy export"
has "the planted fix reached the branch" \
  "$(git --git-dir="$P/work/origin.git" log --format=%s refs/heads/feature)" \
  "fix(api): answer the planted and real findings (amber)"
planted_file="$(git --git-dir="$P/work/origin.git" show refs/heads/feature:app.ts 2>/dev/null || true)"
has "the planted repair reached app.ts" "$planted_file" "if (!r.ok)"
is "the planted run resolved every thread it posted" \
  "$(jq -r '[.[] | select(.isResolved != true)] | length' "$P/state/threads.json" 2>/dev/null)" "0"

# --- blind adjudication ----------------------------------------------------------
is "the adjudication sheet exists" "$([[ -f "$R/adjudication.tsv" ]] && echo yes || echo no)" "yes"
sheet_body="$(tail -n +2 "$R/adjudication.tsv")"
is "the sheet blinds all three arm runs" "$(printf '%s' "$sheet_body" | grep -c . || true)" "3"
for leaked in arm-a arm-b arm-p; do
  hasnt "the sheet names no arm ($leaked)" "$sheet_body" "$leaked"
done
is "the key maps every blind entry back" \
  "$(jq -r 'keys | length' "$R/adjudication-key.json" 2>/dev/null)" "3"

# --- guards -----------------------------------------------------------------------
printf '\nprobing the guards\n'
BAD_MANIFEST="$T/manifest-bad-head.json"
jq --arg head "0000000000000000000000000000000000000000" \
  '.cases[0].head = $head' "$MANIFEST" >"$BAD_MANIFEST"
bash "$RUNNER" --manifest "$BAD_MANIFEST" --results-dir "$T/results-bad" --bin "$BIN" \
  >"$T/runner-bad-out.txt" 2>&1
bad_rc=$?
is "a manifest head that matches nothing refuses the run" "$(( bad_rc != 0 ? 1 : 0 ))" "1"
has "and the refusal names the head" "$(cat "$T/runner-bad-out.txt")" "head"

bash "$RUNNER" --live --manifest "$MANIFEST" --results-dir "$T/results-live" --bin "$BIN" \
  >"$T/runner-live-out.txt" 2>&1
live_rc=$?
is "a live run without assignments is refused" "$(( live_rc != 0 ? 1 : 0 ))" "1"
has "and the refusal names the assignments file" "$(cat "$T/runner-live-out.txt")" "assignments"

PRIV_MANIFEST="$T/manifest-priv.json"
jq '.repo = "acme/workbench-evil"' "$MANIFEST" >"$PRIV_MANIFEST"
bash "$RUNNER" --manifest "$PRIV_MANIFEST" --results-dir "$T/results-priv" --bin "$BIN" \
  >"$T/runner-priv-out.txt" 2>&1
priv_rc=$?
is "a manifest naming a private record is refused" "$(( priv_rc != 0 ? 1 : 0 ))" "1"
has "and the refusal says why" "$(cat "$T/runner-priv-out.txt")" "private"

NOPAY_MANIFEST="$T/manifest-no-payloads.json"
jq '.cases[0].arms[1] |= del(.review_payloads)' "$MANIFEST" >"$NOPAY_MANIFEST"
bash "$RUNNER" --manifest "$NOPAY_MANIFEST" --results-dir "$T/results-no-payloads" --bin "$BIN" \
  >"$T/runner-no-payloads-out.txt" 2>&1
nopay_rc=$?
is "a full arm with no review_payloads is refused" "$(( nopay_rc != 0 ? 1 : 0 ))" "1"
has "and the refusal names the field" "$(cat "$T/runner-no-payloads-out.txt")" "review_payloads"

printf 'not json' >"$T/stamp-probe.json"
# Color-forced end to end, the way a user shell with color forcing runs
# it: the stamp name must read clean even then.
CLICOLOR=1 CLICOLOR_FORCE=1 XDG_STATE_HOME="$T/xdg-state" bash "$RUNNER" --manifest "$T/stamp-probe.json" --bin "$BIN" \
  >"$T/stamp-probe-out.txt" 2>&1 || true
# A glob and basename, never ls output: under forced color ls wraps the
# name in ANSI codes and the stamp check fails on a correct directory.
stamp_dir=""
for stamp_path in "$T"/xdg-state/crossrev-eval/*/; do
  [[ -d "$stamp_path" ]] && stamp_dir="$(basename "$stamp_path")"
done
is "the default results stamp carries the day" \
  "$([[ $stamp_dir =~ ^[0-9]{8}-[0-9]{6}$ ]] && echo yes || echo no)" "yes"

SPACE_R="$T/results with space"
bash "$RUNNER" --manifest "$MANIFEST" --results-dir "$SPACE_R" --bin "$BIN" \
  >"$T/runner-space-out.txt" 2>&1
space_rc=$?
is "the runner exits clean with a space in the results path" "$space_rc" "0"
is "the base map is written with a space in the results path" \
  "$(jq -e '.["refresh-helper"] | has("base_prime")' "$SPACE_R/base-map.json" >/dev/null 2>&1 && echo yes || echo no)" "yes"

printf '\n  %d passed, %d failed\n\n' "$pass" "$fail"
(( fail == 0 ))
