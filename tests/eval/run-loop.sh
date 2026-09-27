#!/usr/bin/env bash
#
# tests/eval/run-loop.sh — the offline full-loop eval runner.
#
# Takes a case manifest by path (never naming a private repository, record
# or path) and, per case and arm: copies the frozen bare repository fresh,
# sets the remote's configured URL to a github.com address with
# pushInsteadOf to the copy (the tests/harness.sh:171-203 pattern, which is
# how the push guard's github.com-only rule is satisfied with no code
# change), commits one synthetic base' on the case base carrying the uniform
# eval config and reviews base'...head, then runs the real binary and the
# real harness CLIs in local mode to a terminal label.
#
# Why base': policy reads only from the base revision (ADR 0003) and the CLI
# offers no config override, so the eval config has to live on a commit the
# pull request's base points at. The synthetic commit sits on the original
# base, so three-dot enumeration still resolves to the original base and the
# stand-in serves the original diff: base' is invisible to review. The
# runner records the base to base-prime mapping in the results.
#
# Per case and arm the runner writes markers, findings, resolutions, run
# logs, read logs and usage to a private results directory (outside the
# checkout, so it can never be committed), plus a blind adjudication sheet
# with the arm labels removed (tests/eval/adjudicate.sh).
#
# A planted-findings mode writes a review marker holding real and planted
# findings into the stand-in, then runs crossrev resolve alone.
#
# Offline (the default) drives the suite's stubs: tests/stub/gh as the
# GitHub stand-in with a freshly seeded empty state directory per arm, and
# the harness stubs answering from the manifest's payloads, so no model is
# ever called. A live run (--live) uses the ambient gh and harness CLIs
# against the current checkout instead, and is refused without an
# --assignments file recording the four (case, arm, reviewer, resolver)
# assignments; that file is copied into the results. Planted cases are
# refused live: they need the stand-in to hold the marker.
#
# tests/run.sh never runs this directory (it globs only tests/test-*.sh)
# and CI never runs it either.
#
# Usage:
#   bash tests/eval/run-loop.sh --manifest <path> --bin <path>
#     [--results-dir <dir>] [--live] [--assignments <file>]
#
# Prior art: the fixture layout and the stub-env snapshot below follow
# tests/harness.sh (the pushInsteadOf origin, the per-case route table and
# call log, the CROSSREV_ snapshot the native binary's allowlist would
# otherwise drop). The stateful stand-in it drives is tests/stub/gh, proven
# by tests/eval/test-stateful-gh.sh. No existing offline full-loop runner
# fitted: the suites drive one leg per case, never a loop to a terminal
# label across arms.

set -uo pipefail

EVAL_HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EVAL_ROOT="$(cd "$EVAL_HERE/../.." && pwd)"
EVAL_STUB_DIR="$EVAL_ROOT/tests/stub"

MANIFEST=""; RESULTS_DIR=""; BIN=""; LIVE=0; ASSIGNMENTS=""
while (( $# )); do
  case "$1" in
    --manifest) [[ $# -ge 2 ]] || { printf 'run-loop: --manifest needs a value\n' >&2; exit 2; }
      MANIFEST="$2"; shift 2 ;;
    --results-dir) [[ $# -ge 2 ]] || { printf 'run-loop: --results-dir needs a value\n' >&2; exit 2; }
      RESULTS_DIR="$2"; shift 2 ;;
    --bin) [[ $# -ge 2 ]] || { printf 'run-loop: --bin needs a value\n' >&2; exit 2; }
      BIN="$2"; shift 2 ;;
    --live) LIVE=1; shift ;;
    --assignments) [[ $# -ge 2 ]] || { printf 'run-loop: --assignments needs a value\n' >&2; exit 2; }
      ASSIGNMENTS="$2"; shift 2 ;;
    -h|--help)
      printf 'Usage: run-loop.sh --manifest <path> --bin <path> [--results-dir <dir>] [--live] [--assignments <file>]\n'
      exit 0 ;;
    *) printf 'run-loop: unknown option: %s\n' "$1" >&2; exit 2 ;;
  esac
done

[[ -n "$MANIFEST" ]] || { printf 'run-loop: --manifest is required\n' >&2; exit 2; }
[[ -f "$MANIFEST" ]] || { printf 'run-loop: manifest not found: %s\n' "$MANIFEST" >&2; exit 2; }
[[ -n "$BIN" ]] || { printf 'run-loop: --bin is required\n' >&2; exit 2; }
[[ "$BIN" = /* ]] || { printf 'run-loop: --bin must be an absolute path: %s\n' "$BIN" >&2; exit 2; }
[[ -x "$BIN" ]] || { printf 'run-loop: --bin is not executable: %s\n' "$BIN" >&2; exit 2; }

if (( LIVE )) && [[ -z "$ASSIGNMENTS" ]]; then
  printf 'run-loop: a live run needs --assignments <file> recording the case, arm, reviewer and resolver assignments; refusing\n' >&2
  exit 2
fi
if [[ -n "$ASSIGNMENTS" ]]; then
  [[ -f "$ASSIGNMENTS" ]] || { printf 'run-loop: assignments file not found: %s\n' "$ASSIGNMENTS" >&2; exit 2; }
fi

if [[ -z "$RESULTS_DIR" ]]; then
  stamp="$(date +%Y%mdb-%H%M%S 2>/dev/null || date +%s)"
  RESULTS_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/crossrev-eval/$stamp"
fi
mkdir -p "$RESULTS_DIR" || { printf 'run-loop: cannot create %s\n' "$RESULTS_DIR" >&2; exit 2; }
# Resolve to an absolute path: the legs run in throwaway checkouts.
RESULTS_DIR="$(cd "$RESULTS_DIR" && pwd)"

# --- manifest validation ------------------------------------------------------
#
# The schema lives in tests/eval/manifest.schema.json; this is its
# enforcement, in jq, because the eval allows no runtime beyond bash, gh,
# jq and yq. Every failure names the field.

eval_fail() { printf 'run-loop: manifest %s: %s\n' "$MANIFEST" "$1" >&2; exit 2; }

jq -e . "$MANIFEST" >/dev/null 2>&1 || eval_fail "is not valid JSON"
[[ "$(jq -r '.version // empty' "$MANIFEST")" == "1" ]] || eval_fail "needs version 1"
for key in repo pr frozen_repo reviewer resolver trusted_user cases; do
  [[ "$(jq -r --arg k "$key" '.[$k] // empty | tostring | length' "$MANIFEST")" != "0" ]] \
    || eval_fail "needs .$key"
done
[[ "$(jq -r '.repo' "$MANIFEST")" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] \
  || eval_fail ".repo must be an owner/name slug, never a path or URL"
[[ "$(jq -r '.pr' "$MANIFEST")" =~ ^[0-9]+$ ]] || eval_fail ".pr must be a number"
[[ -d "$(jq -r '.frozen_repo' "$MANIFEST")" ]] || eval_fail ".frozen_repo is not a directory"
for role in reviewer resolver; do
  jq -e --arg r "$role" '.[$r].harness | IN("claude","agy","grok","kimi","opencode")' "$MANIFEST" >/dev/null \
    || eval_fail " alleged .$role.harness is not a stubbed harness (never codex, which is a tripwire)"
  [[ -n "$(jq -r --arg r "$role" '.[$r].model // empty' "$MANIFEST")" ]] \
    || eval_fail ".$role.model must pin an explicit model"
done
[[ "$(jq -r '.trusted_user | length' "$MANIFEST")" != "0" ]] || eval_fail ".trusted_user must not be empty"
[[ "$(jq -r '.cases | length' "$MANIFEST")" != "0" ]] || eval_fail ".cases must not be empty"
# The manifest never names a private repository, record or path. Spelled
# without the dot so this line itself stays out of the pre-commit privacy
# gate; any dotted occurrence still contains the bare word.
if grep -qE 'workbench|/Users/|/home/' "$MANIFEST"; then
  eval_fail "names a private repository, record or local path"
fi

ncases="$(jq -r '.cases | length' "$MANIFEST")"
for (( ci=0; ci<ncases; ci++ )); do
  for key in id base head base_branch head_branch mode arms; do
    [[ "$(jq -r --argjson i "$ci" --arg k "$key" '.cases[$i][$k] // empty | tostring | length' "$MANIFEST")" != "0" ]] \
      || eval_fail ".cases[$ci] needs .$key"
  done
  for key in base head; do
    [[ "$(jq -r --argjson i "$ci" --arg k "$key" '.cases[$i][$k]' "$MANIFEST")" =~ ^[0-9a-f]{40}$ ]] \
      || eval_fail ".cases[$ci].$key must be a full SHA"
  done
  [[ "$(jq -r --argjson i "$ci" '.cases[$i].mode' "$MANIFEST")" =~ ^(full|planted)$ ]] \
    || eval_fail ".cases[$ci].mode must be full or planted"
  if (( LIVE )) && [[ "$(jq -r --argjson i "$ci" '.cases[$i].mode' "$MANIFEST")" == "planted" ]]; then
    printf 'run-loop: live run refused: case %s is planted-findings mode, which needs the stand-in\n' \
      "$(jq -r --argjson i "$ci" '.cases[$i].id' "$MANIFEST")" >&2
    exit 2
  fi
  narms="$(jq -r --argjson i "$ci" '.cases[$i].arms | length' "$MANIFEST")"
  [[ "$narms" != "0" ]] || eval_fail ".cases[$ci].arms must not be empty"
  for (( ai=0; ai<narms; ai++ )); do
    [[ -n "$(jq -r --argjson i "$ci" --argjson j "$ai" '.cases[$i].arms[$j].id // empty' "$MANIFEST")" ]] \
      || eval_fail ".cases[$ci].arms[$ai] needs .id"
  done
done

REPO="$(jq -r '.repo' "$MANIFEST")"
PR="$(jq -r '.pr' "$MANIFEST")"
FROZEN="$(jq -r '.frozen_repo' "$MANIFEST")"
TRUSTED="$(jq -r '.trusted_user' "$MANIFEST")"
REVIEWER_HARNESS="$(jq -r '.reviewer.harness' "$MANIFEST")"
REVIEWER_MODEL="$(jq -r '.reviewer.model' "$MANIFEST")"
RESOLVER_HARNESS="$(jq -r '.resolver.harness' "$MANIFEST")"
RESOLVER_MODEL="$(jq -r '.resolver.model' "$MANIFEST")"

cp "$MANIFEST" "$RESULTS_DIR/manifest.json"
if [[ -n "$ASSIGNMENTS" ]]; then
  cp "$ASSIGNMENTS" "$RESULTS_DIR/assignments.json"
fi

printf 'run-loop: %d case(s), repo %s, pr %s, results %s\n' "$ncases" "$REPO" "$PR" "$RESULTS_DIR"

# Leg names for the arm in progress, the terminal label it ended on, whether
# it stalled, and the head under review. Set by the loop runners.
LEGS=()
EVAL_TERMINAL="none"
EVAL_STALLED="false"
EVAL_HEAD=""

# --- shared helpers ------------------------------------------------------------

# The wrapper a native run is invoked through. Same shape as
# tests/harness.sh: the native binary hands each child an exact allowlist,
# so a test-only CROSSREV_ name must not be widened onto it. The wrapper
# snapshots the names beside the arm's own XDG_CONFIG_HOME before every
# invocation (payloads change per pass) and removes the snapshot after,
# and drops every _AUTH name so no credential is ever written down.
eval_wrapper_for() {
  local target="$1" dir="$2"
  mkdir -p "$dir"
  cat >"$dir/crossrev" <<WRAP
#!/usr/bin/env bash
set -uo pipefail
_base="$dir/xdg-config"
mkdir -p "\$_base" 2>/dev/null
_umask=\$(umask)
umask 077
: >"\$_base/crossrev-stub.env"
umask "\$_umask"
while IFS= read -r _name; do
  printf 'export %s=%q\\n' "\$_name" "\${!_name}" >>"\$_base/crossrev-stub.env"
done < <(compgen -e | grep '^CROSSREV_' | grep -v '_AUTH\$' || true)
"$target" "\$@"
_rc=\$?
rm -f "\$_base/crossrev-stub.env"
exit "\$_rc"
WRAP
  chmod +x "$dir/crossrev"
  printf '%s' "$dir/crossrev"
}

# The uniform eval config committed on every synthetic base revision:
# explicit model pins, marker coverage, six passes, no backlog sink, and a
# medium floor. Six rather than the usual three so a slow convergence still
# terminates inside the loop instead of at the policy bound.
eval_uniform_config() {
  cat <<EOF
version: 2
mode: local
policy:
  min_fix_severity: medium
  max_passes_per_cycle: 6
  max_files_changed_per_pr: 200
  max_prs_per_day: 25
coverage:
  store: marker
reviewer:
  harness: $REVIEWER_HARNESS
  model: $REVIEWER_MODEL
resolver:
  harness: $RESOLVER_HARNESS
  model: $RESOLVER_MODEL
backlog:
  destination: none
EOF
}

# Labels currently on the pull request, one per line. Offline this reads the
# stand-in's live state; live it asks GitHub.
eval_labels() {
  "$GH" api "repos/$REPO/issues/$PR/labels" --jq '.[].name' 2>/dev/null || true
}

eval_terminal_label() {
  local labels="$1"
  while IFS= read -r label; do
    case "$label" in
      crossrev/converged|crossrev/halted|crossrev/stop) printf '%s' "$label"; return 0 ;;
    esac
  done <<<"$labels"
  return 1
}

# The stand-in's route table: repository and author reads, the pull request
# metadata with the synthetic base, and the original diff. Comment, thread
# and label reads are deliberately unrouted so the stand-in's live state
# answers them — that is what makes legs observe each other.
eval_write_routes() {
  local head="$1" base_prime="$2" diff_file="$3"
  local owner="${REPO%%/*}" name="${REPO##*/}"
  local pr_json
  pr_json="$(jq -cn --argjson n "$PR" --arg h "$head" --arg b "$base_prime" \
    --arg o "$owner" --arg r "$name" \
    --arg hb "${ARM_HEAD_BRANCH:-feature}" --arg bb "${ARM_BASE_BRANCH:-main}" \
    '{number:$n, title:"Eval case", body:"Offline eval fixture.", url:"https://github.com/x",
      headRefName:$hb, headRefOid:$h, baseRefName:$bb, baseRefOid:$b,
      changedFiles:1, labels:[], isCrossRepository:false, maintainerCanModify:false, isDraft:false,
      headRepositoryOwner:{login:$o}, headRepository:{name:$r}, state:"OPEN"}')"
  {
    printf '%s\t%s\n' 'repo view --json nameWithOwner*' "{\"nameWithOwner\":\"$REPO\"}"
    printf '%s\t%s\n' 'repo view * --json defaultBranchRef*' '{"defaultBranchRef":{"name":"main"}}'
    printf '%s\t%s\n' 'api user*' "{\"login\":\"$TRUSTED\"}"
    printf '%s\t%s\n' "pr view $PR --repo * --json *" "$pr_json"
    printf '%s\t@%s\n' '*Accept: application/vnd.github.diff*' "$diff_file"
  } >"$ARM_ROUTES"
}

# --- offline arm setup -----------------------------------------------------------
#
# Fresh bare copy, a working clone rewired to the github.com address with
# pushInsteadOf to the copy, the head check against the manifest, and one
# synthetic base' carrying the uniform eval config. Sets ARM_CHECKOUT,
# ARM_STATE, ARM_ROUTES, ARM_GH_LOG and ARM_BASE_PRIME for the caller.
# The base' commit uses a fixed timestamp so every arm of a case lands on
# the same SHA: the base to base-prime mapping stays one to one.

eval_offline_setup() {
  local case_id="$1" base="$2" head="$3" base_branch="$4" head_branch="$5"
  local arm_dir="$6"
  local bare_copy="$arm_dir/work/origin.git"
  local checkout="$arm_dir/work/checkout"

  mkdir -p "$arm_dir/work"
  cp -R "$FROZEN" "$bare_copy"
  git clone -q -b "$base_branch" "$bare_copy" "$checkout" \
    || { printf 'run-loop: cannot clone the frozen copy for %s\n' "$case_id" >&2; return 1; }
  (
    cd "$checkout" || exit 1
    git config user.email "crossrev-eval@example.com"
    git config user.name "crossrev-eval"
    git remote set-url origin "https://github.com/$REPO.git"
    git config "url.$bare_copy.pushInsteadOf" "https://github.com/$REPO.git"
    git checkout -q "$head_branch"
  ) || return 1

  local actual_head
  actual_head="$(git -C "$checkout" rev-parse "$head_branch")"
  if [[ "$actual_head" != "$head" ]]; then
    printf 'run-loop: case %s: head %s is %s, the manifest wants %s; refusing\n' \
      "$case_id" "$head_branch" "$actual_head" "$head" >&2
    return 1
  fi

  # The original diff, served by the stand-in for every pass of this arm.
  git -C "$checkout" diff "$base" "$head" >"$arm_dir/diff.txt"

  local base_prime
  (
    cd "$checkout" || exit 1
    git checkout -q "$base_branch"
    mkdir -p .github
    eval_uniform_config >.github/crossrev.yml
    git add .github/crossrev.yml
    GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z" \
      git commit -q -m "eval: uniform policy for the offline loop"
    git push -q origin "$base_branch"
    git checkout -q "$head_branch"
  ) || return 1
  base_prime="$(git -C "$checkout" rev-parse "$base_branch")"
  printf '%s' "$base" >"$arm_dir/base.txt"
  printf '%s' "$base_prime" >"$arm_dir/base-prime.txt"

  mkdir -p "$arm_dir/state"
  ARM_CHECKOUT="$checkout"
  ARM_STATE="$arm_dir/state"
  ARM_ROUTES="$arm_dir/routes"
  ARM_GH_LOG="$arm_dir/gh.log"
  ARM_BASE_PRIME="$base_prime"
  ARM_BASE="$base"
  ARM_HEAD_BRANCH="$head_branch"
  ARM_BASE_BRANCH="$base_branch"
  : >"$ARM_GH_LOG"
  eval_write_routes "$head" "$base_prime" "$arm_dir/diff.txt"
}

# After a resolve push moved the head: fetch it into the checkout and
# re-point the stand-in at the repair head with the current diff, the way
# a repointed pull request would read. Records the new head for the caller.
eval_repoint() {
  local arm_dir="$1"
  (( LIVE )) && return 1
  local new_head
  new_head="$(git --git-dir="$arm_dir/work/origin.git" rev-parse "$ARM_HEAD_BRANCH")"
  [[ "$new_head" == "$EVAL_HEAD" ]] && return 1
  git -C "$ARM_CHECKOUT" fetch -q origin "$ARM_HEAD_BRANCH" 2>/dev/null || true
  git -C "$ARM_CHECKOUT" diff "$ARM_BASE" "$new_head" >"$arm_dir/diff.txt"
  eval_write_routes "$new_head" "$ARM_BASE_PRIME" "$arm_dir/diff.txt"
  EVAL_HEAD="$new_head"
  return 0
}

# --- leg execution ---------------------------------------------------------------
#
# One leg in the arm's checkout, stdin closed the way the suite runs it. The
# wrapper snapshots the exported CROSSREV_ names (payloads change per pass)
# so they survive the binary's environment allowlist. Harness overrides come
# from the arm: --harness is the CLI's own per-leg override.

eval_run_leg() {
  local leg="$1" log="$2"
  shift 2
  ( cd "$ARM_CHECKOUT" && "$WRAPPER" "$leg" --pr "$PR" "$@" </dev/null >"$log" 2>&1 )
}

eval_record_pass() {
  local arm_dir="$1" pass="$2" leg="$3" rc="$4"
  jq -cn --argjson p "$pass" --arg l "$leg" --argjson r "$rc" \
    --arg h "$EVAL_HEAD" '{pass:$p, leg:$l, rc:$r, head:$h}' >>"$arm_dir/passes.jsonl"
}

# The full loop for one arm: review and resolve legs alternate until a
# terminal label (converged, halted, or the human stop) or six passes.
# Appends leg names to LEGS and sets EVAL_TERMINAL (possibly "none") and
# EVAL_STALLED (possibly "true").
eval_full_loop() {
  local arm_dir="$1" arm_json="$2"
  local npayloads pass payload_idx
  npayloads="$(jq -r '.review_payloads | length' <<<"$arm_json")"
  [[ "$npayloads" != "0" ]] || { printf 'run-loop: arm needs review_payloads in full mode\n' >&2; return 1; }
  local reviewer_override resolver_override
  reviewer_override="$(jq -r '.reviewer_harness // empty' <<<"$arm_json")"
  resolver_override="$(jq -r '.resolver_harness // empty' <<<"$arm_json")"

  local resolve_payload_file="$arm_dir/payload-resolve.json"
  jq -c '.resolve_payload' <<<"$arm_json" >"$resolve_payload_file"
  export CROSSREV_RESOLVE_PAYLOAD="$resolve_payload_file"
  local resolve_edit
  resolve_edit="$(jq -r '.resolve_edit // empty' <<<"$arm_json")"
  if [[ -n "$resolve_edit" ]]; then
    printf '%s\n' "$resolve_edit" >"$arm_dir/resolve-edit.sh"
    export CROSSREV_RESOLVE_EDIT="$arm_dir/resolve-edit.sh"
  else
    unset CROSSREV_RESOLVE_EDIT
  fi

  LEGS=()
  EVAL_TERMINAL="none"
  EVAL_STALLED="false"
  local labels terminal
  pass=1
  while (( pass <= 6 )); do
    payload_idx=$(( pass - 1 ))
    (( payload_idx >= npayloads )) && payload_idx=$(( npayloads - 1 ))
    jq -c --argjson i "$payload_idx" '.review_payloads[$i]' <<<"$arm_json" \
      | sed "s|__EVAL_HEAD__|$EVAL_HEAD|g; s|__EVAL_BASE__|$ARM_BASE_PRIME|g" \
      >"$arm_dir/payload-review-$pass.json"
    export CROSSREV_REVIEW_PAYLOAD="$arm_dir/payload-review-$pass.json"
    local rc=0
    if [[ -n "$reviewer_override" ]]; then
      eval_run_leg review "$arm_dir/run-review-$pass.log" --harness "$reviewer_override" || rc=$?
    else
      eval_run_leg review "$arm_dir/run-review-$pass.log" || rc=$?
    fi
    out="$(cat "$arm_dir/run-review-$pass.log")"
    eval_record_pass "$arm_dir" "$pass" review "$rc"
    LEGS+=("review")
    if grep -q 'already reviewed' "$arm_dir/run-review-$pass.log"; then
      EVAL_STALLED="true"
      break
    fi
    if (( rc != 0 )); then
      break
    fi
    labels="$(eval_labels)"
    if terminal="$(eval_terminal_label "$labels")"; then
      EVAL_TERMINAL="$terminal"
      break
    fi
    if [[ -n "$resolver_override" ]]; then
      eval_run_leg resolve "$arm_dir/run-resolve-$pass.log" --harness "$resolver_override" || rc=$?
    else
      eval_run_leg resolve "$arm_dir/run-resolve-$pass.log" || rc=$?
    fi
    eval_record_pass "$arm_dir" "$pass" resolve "$rc"
    LEGS+=("resolve")
    if (( rc != 0 )); then
      break
    fi
    labels="$(eval_labels)"
    if terminal="$(eval_terminal_label "$labels")"; then
      EVAL_TERMINAL="$terminal"
      break
    fi
    eval_repoint "$arm_dir" || true
    pass=$((pass + 1))
  done
  labels="$(eval_labels)"
  if terminal="$(eval_terminal_label "$labels")"; then
    EVAL_TERMINAL="$terminal"
  fi
  printf '%s' "$labels" >"$arm_dir/labels.txt"
}

# --- planted-findings mode ---------------------------------------------------------
#
# No review leg runs. Each real and planted finding is posted as a review
# comment through the stand-in (which mints a real thread per comment), the
# thread ids are read back, one v1 review marker holding all of them is
# written as the pass summary, and crossrev resolve runs alone against it.

eval_planted_arm() {
  local arm_dir="$1" arm_json="$2"
  local findings_json="$arm_dir/planted-findings.json"
  jq -c '[.real_findings // [], .planted_findings // []] | add' <<<"$arm_json" >"$findings_json"
  [[ "$(jq -r 'length' "$findings_json")" != "0" ]] \
    || { printf 'run-loop: planted arm needs real_findings or planted_findings\n' >&2; return 1; }

  local ids_json="$arm_dir/planted-comment-ids.json"
  printf '[]' >"$ids_json"
  local n i finding fid path line side title why fix body cid
  n="$(jq -r 'length' "$findings_json")"
  for (( i=0; i<n; i++ )); do
    finding="$(jq -c --argjson i "$i" '.[$i]' "$findings_json")"
    fid="$(jq -r '.id' <<<"$finding")"
    path="$(jq -r '.path' <<<"$finding")"
    line="$(jq -r '.line' <<<"$finding")"
    side="$(jq -r '.side // "RIGHT"' <<<"$finding")"
    title="$(jq -r '.title' <<<"$finding")"
    why="$(jq -r '.why' <<<"$finding")"
    fix="$(jq -r '.fix' <<<"$finding")"
    [[ "$fid" =~ ^[0-9a-f]{16}$ ]] \
      || { printf 'run-loop: planted finding id must be 16 hex characters: %s\n' "$fid" >&2; return 1; }
    body="$(printf '%s\n\n%s\n\n**Fix:** %s\n\n<!-- crossrev:f {"id":"%s","pass":1,"leg":"review"} -->' \
      "$title" "$why" "$fix" "$fid")"
    cid="$("$GH" api --method POST "repos/$REPO/pulls/$PR/comments" \
      -f "body=$body" -f "commit_id=$EVAL_HEAD" -f "path=$path" \
      -F "line=$line" -f "side=$side" --jq .id)" || return 1
    jq --argjson c "$cid" --arg f "$fid" '. + [{comment_id:$c, finding_id:$f}]' \
      "$ids_json" >"$ids_json.tmp" && mv "$ids_json.tmp" "$ids_json"
  done

  local threads
  threads="$("$GH" api graphql -F "owner=${REPO%%/*}" -F "name=${REPO##*/}" -F "number=$PR" \
    -f query='query($owner:String!,$name:String!,$number:Int!) { repository(owner:$owner,name:$name) { pullRequest(number:$number) { reviewThreads(first:100) { nodes { id isResolved comments(first:30) { nodes { databaseId body } } } } } } }' \
    --jq '.data.repository.pullRequest.reviewThreads.nodes')" || return 1

  local marker_findings="$arm_dir/planted-marker-findings.json"
  printf '[]' >"$marker_findings"
  for (( i=0; i<n; i++ )); do
    finding="$(jq -c --argjson i "$i" '.[$i]' "$findings_json")"
    fid="$(jq -r '.id' <<<"$finding")"
    cid="$(jq -r --arg f "$fid" '.[] | select(.finding_id == $f) | .comment_id' "$ids_json")"
    local thread_id
    thread_id="$(jq -r --argjson c "$cid" \
      '.[] | select(.comments.nodes[].databaseId == $c) | .id' <<<"$threads" | head -n 1)"
    [[ -n "$thread_id" ]] \
      || { printf 'run-loop: no thread for planted finding %s\n' "$fid" >&2; return 1; }
    jq --argjson f "$finding" --arg t "$thread_id" \
      '. + [$f + {anchor:"", thread_id:$t, resolution:null, tracked_as:null}]' \
      "$marker_findings" >"$marker_findings.tmp" && mv "$marker_findings.tmp" "$marker_findings"
  done

  local ts run_id marker
  ts="$(date +%s)"
  run_id="eval-planted-$ts"
  marker="$(jq -cn --arg head "$EVAL_HEAD" --argjson ts "$ts" --arg run "$run_id" \
    --arg harness "$REVIEWER_HARNESS" --arg model "$REVIEWER_MODEL" \
    --argjson findings "$(cat "$marker_findings")" \
    '{v:1, leg:"review", pass:1, state:"complete", ts:$ts, run_id:$run, head_sha:$head,
      harness:$harness, model:$model, model_reported:$model,
      verdict:"issues-remain", findings:$findings}')"
  "$GH" api --method POST "repos/$REPO/issues/$PR/comments" \
    -f "body=$(printf 'Eval planted review.\n\n<!-- crossrev: %s -->' "$marker")" \
    --jq .id >/dev/null || return 1

  local resolve_payload_file="$arm_dir/payload-resolve.json"
  jq -c '.resolve_payload' <<<"$arm_json" >"$resolve_payload_file"
  export CROSSREV_RESOLVE_PAYLOAD="$resolve_payload_file"
  local resolve_edit
  resolve_edit="$(jq -r '.resolve_edit // empty' <<<"$arm_json")"
  if [[ -n "$resolve_edit" ]]; then
    printf '%s\n' "$resolve_edit" >"$arm_dir/resolve-edit.sh"
    export CROSSREV_RESOLVE_EDIT="$arm_dir/resolve-edit.sh"
  else
    unset CROSSREV_RESOLVE_EDIT
  fi
  unset CROSSREV_REVIEW_PAYLOAD

  LEGS=("resolve")
  EVAL_TERMINAL="none"
  EVAL_STALLED="false"
  local resolver_override rc=0
  resolver_override="$(jq -r '.resolver_harness // empty' <<<"$arm_json")"
  if [[ -n "$resolver_override" ]]; then
    eval_run_leg resolve "$arm_dir/run-resolve-1.log" --harness "$resolver_override" || rc=$?
  else
    eval_run_leg resolve "$arm_dir/run-resolve-1.log" || rc=$?
  fi
  eval_record_pass "$arm_dir" 1 resolve "$rc"
  local labels terminal
  labels="$(eval_labels)"
  if terminal="$(eval_terminal_label "$labels")"; then
    EVAL_TERMINAL="$terminal"
  fi
  printf '%s' "$labels" >"$arm_dir/labels.txt"
  return "$rc"
}

# --- artifact collection -----------------------------------------------------------
#
# Markers, findings, resolutions, run logs, read logs and usage per case and
# arm. Offline they are read out of the stand-in's state directory (the same
# bytes the legs wrote); live they are read back through the GitHub API in
# the same shapes.

eval_collect_offline() {
  local arm_dir="$1"
  local state="$ARM_STATE"

  jq -n '[]' >"$arm_dir/markers.json"
  for comment in "$state"/comment-*; do
    case "$comment" in *.author|*.issue|*.tmp) continue ;; esac
    [[ -f "$comment" ]] || continue
    grep -qF '<!-- crossrev: ' "$comment" 2>/dev/null || continue
    local id author body
    id="$(basename "$comment" | sed 's/^comment-//')"
    author="$(cat "$state/comment-$id.author" 2>/dev/null || printf 'unknown')"
    body="$(cat "$comment")"
    jq --argjson id "$id" --arg a "$author" --arg b "$body" \
      '. + [{id:$id, author:$a, body:$b}]' "$arm_dir/markers.json" >"$arm_dir/markers.tmp" \
      && mv "$arm_dir/markers.tmp" "$arm_dir/markers.json"
  done

  if [[ -f "$state/threads.json" ]]; then
    jq -c '[.[] | {thread_id:.id, isResolved, path, line,
      comments:[.comments.nodes[] | {id:.databaseId, author:.author.login, body}]}]' \
      "$state/threads.json" >"$arm_dir/findings.json"
    jq -c '{resolve_markers:[], replies:[.[].comments.nodes[1:][]? |
      {id:.databaseId, author:.author.login, body}]}' \
      "$state/threads.json" >"$arm_dir/resolutions.json"
  else
    printf '[]\n' >"$arm_dir/findings.json"
    printf '{"resolve_markers":[],"replies":[]}\n' >"$arm_dir/resolutions.json"
  fi
  # Resolve-leg markers join the thread replies in resolutions.
  jq --slurpfile m "$arm_dir/markers.json" \
    '.resolve_markers = [$m[0][] | .body
      | capture("<!-- crossrev: (?<marker>.*) -->").marker
      | fromjson? // empty | select(.leg == "resolve")]' \
    "$arm_dir/resolutions.json" >"$arm_dir/resolutions.tmp" \
    && mv "$arm_dir/resolutions.tmp" "$arm_dir/resolutions.json"

  jq -c '[.[] | .body
    | capture("<!-- crossrev: (?<marker>.*) -->").marker
    | fromjson? // empty | select(.leg != null)
    | {pass, leg, harness, model, model_reported,
       tokens:(.tokens // null),
       cost_usd:(.usage.cost_usd // null)}]' \
    "$arm_dir/markers.json" >"$arm_dir/usage.json"

  cp "$ARM_GH_LOG" "$arm_dir/reads.log"
}

eval_collect_live() {
  local arm_dir="$1"
  "$GH" api --paginate "repos/$REPO/issues/$PR/comments" \
    --jq '[.[] | {id, author:.user.login, body}] | map(select(.body | contains("<!-- crossrev: ")))' \
    >"$arm_dir/markers.json" 2>/dev/null || printf '[]\n' >"$arm_dir/markers.json"
  "$GH" api graphql -F "owner=${REPO%%/*}" -F "name=${REPO##*/}" -F "number=$PR" \
    -f query='query($owner:String!,$name:String!,$number:Int!) { repository(owner:$owner,name:$name) { pullRequest(number:$number) { reviewThreads(first:100) { nodes { id isResolved path line comments(first:30) { nodes { databaseId body author { login } } } } } } } }' \
    --jq '[.data.repository.pullRequest.reviewThreads.nodes[] |
      {thread_id:.id, isResolved, path, line,
       comments:[.comments.nodes[] | {id:.databaseId, author:.author.login, body}]}]' \
    >"$arm_dir/findings.json" 2>/dev/null || printf '[]\n' >"$arm_dir/findings.json"
  jq -c '{resolve_markers:[], replies:[.[].comments[1:][]?]}' \
    "$arm_dir/findings.json" >"$arm_dir/resolutions.json"
  jq --slurpfile m "$arm_dir/markers.json" \
    '.resolve_markers = [$m[0][] | .body
      | capture("<!-- crossrev: (?<marker>.*) -->").marker
      | fromjson? // empty | select(.leg == "resolve")]' \
    "$arm_dir/resolutions.json" >"$arm_dir/resolutions.tmp" \
    && mv "$arm_dir/resolutions.tmp" "$arm_dir/resolutions.json"
  jq -c '[.[] | .body
    | capture("<!-- crossrev: (?<marker>.*) -->").marker
    | fromjson? // empty | select(.leg != null)
    | {pass, leg, harness, model, model_reported,
       tokens:(.tokens // null),
       cost_usd:(.usage.cost_usd // null)}]' \
    "$arm_dir/markers.json" >"$arm_dir/usage.json"
}

eval_write_result() {
  local arm_dir="$1" case_id="$2" arm_id="$3" mode="$4" base="$5" base_prime="$6"
  local legs_json labels_json rel
  # The length guard is load-bearing: "${LEGS[@]}" on an empty array fails
  # under set -u on bash 3.2, which this repository still supports.
  legs_json="[]"
  if (( ${#LEGS[@]} > 0 )); then
    legs_json="$(printf '%s\n' "${LEGS[@]}" | jq -R . | jq -s -c 'map(select(length > 0))')"
  fi
  labels_json="$(tr '\n' ' ' <"$arm_dir/labels.txt" | tr -s ' ' | jq -R -c 'split(" ") | map(select(length > 0))')"
  rel="$case_id/$arm_id"
  jq -cn --arg c "$case_id" --arg a "$arm_id" --arg m "$mode" \
    --argjson legs "$legs_json" --arg t "$EVAL_TERMINAL" \
    --argjson stalled "$EVAL_STALLED" --argjson labels "$labels_json" \
    --arg b "$base" --arg bp "$base_prime" --arg h "$EVAL_HEAD" --arg r "$rel" \
    '{case:$c, arm:$a, mode:$m, legs:$legs, terminal:$t, stalled:$stalled,
      labels:$labels, base:$b,
      base_prime:(if $bp == "" then null else $bp end), head:$h,
      artifacts:{findings:($r + "/findings.json"),
        resolutions:($r + "/resolutions.json"), markers:($r + "/markers.json")}}' \
    >"$arm_dir/result.json"
  jq -c '{labels:.labels, terminal:.terminal}' "$arm_dir/result.json" >"$arm_dir/labels.json"
}

# --- main driver ---------------------------------------------------------------------

if (( LIVE )); then
  GH="gh"
else
  GH="$EVAL_STUB_DIR/gh"
  case ":$PATH:" in
    *":$EVAL_STUB_DIR:"*) ;;
    *) PATH="$EVAL_STUB_DIR:$PATH"; export PATH ;;
  esac
fi

# Per-arm offline environment: the stubs earlier on PATH, private XDG homes
# so no leg reads the operator's own config or another arm's runs, a clean
# stub slate, and no ambient runner variable leaking the hosted path in.
eval_offline_env() {
  local arm_dir="$1"
  export XDG_CONFIG_HOME="$arm_dir/wrap/xdg-config"
  export XDG_STATE_HOME="$arm_dir/xdg-state"
  mkdir -p "$XDG_CONFIG_HOME" "$XDG_STATE_HOME"
  rm -f "$XDG_CONFIG_HOME/crossrev-stub.env"
  unset RUNNER_ENVIRONMENT
  unset CROSSREV_REVIEW_PAYLOAD CROSSREV_RESOLVE_PAYLOAD CROSSREV_HARNESS_PAYLOAD
  unset CROSSREV_RESOLVE_EDIT CROSSREV_REVIEW_MODEL CROSSREV_RESOLVE_MODEL CROSSREV_HARNESS_MODEL
  unset CROSSREV_REVIEW_MODEL_USAGE CROSSREV_RESOLVE_PAYLOAD_2 CROSSREV_STUB_COUNT
}

eval_fail_result() {
  local arm_dir="$1" case_id="$2" arm_id="$3" mode="$4" base="$5" head="$6" msg="$7"
  LEGS=()
  EVAL_TERMINAL="none"
  EVAL_STALLED="false"
  EVAL_HEAD="$head"
  : >"$arm_dir/labels.txt"
  eval_write_result "$arm_dir" "$case_id" "$arm_id" "$mode" "$base" ""
  jq --arg e "$msg" '. + {error:$e}' "$arm_dir/result.json" >"$arm_dir/result.tmp" \
    && mv "$arm_dir/result.tmp" "$arm_dir/result.json"
}

failures=0
RESULT_LIST="$RESULTS_DIR/results.list"
: >"$RESULT_LIST"

for (( ci=0; ci<ncases; ci++ )); do
  case_json="$(jq -c --argjson i "$ci" '.cases[$i]' "$MANIFEST")"
  case_id="$(jq -r '.id' <<<"$case_json")"
  base="$(jq -r '.base' <<<"$case_json")"
  head="$(jq -r '.head' <<<"$case_json")"
  base_branch="$(jq -r '.base_branch' <<<"$case_json")"
  head_branch="$(jq -r '.head_branch' <<<"$case_json")"
  mode="$(jq -r '.mode' <<<"$case_json")"
  narms="$(jq -r '.arms | length' <<<"$case_json")"
  for (( ai=0; ai<narms; ai++ )); do
    arm_json="$(jq -c --argjson j "$ai" '.arms[$j]' <<<"$case_json")"
    arm_id="$(jq -r '.id' <<<"$arm_json")"
    arm_dir="$RESULTS_DIR/$case_id/$arm_id"
    mkdir -p "$arm_dir"
    : >"$arm_dir/passes.jsonl"
    printf 'run-loop: case %s arm %s (%s)\n' "$case_id" "$arm_id" "$mode"

    if (( LIVE )); then
      # Live: the current checkout is the repository under test. Policy
      # comes from its real base revision, so no synthetic base' is
      # committed and base_prime stays null in the results.
      command -v gh >/dev/null 2>&1 \
        || { eval_fail_result "$arm_dir" "$case_id" "$arm_id" "$mode" "$base" "$head" "gh is not on PATH";
             failures=$((failures+1)); printf '%s\n' "$arm_dir/result.json" >>"$RESULT_LIST"; continue; }
      git rev-parse --show-toplevel >/dev/null 2>&1 \
        || { eval_fail_result "$arm_dir" "$case_id" "$arm_id" "$mode" "$base" "$head" "not inside a git checkout";
             failures=$((failures+1)); printf '%s\n' "$arm_dir/result.json" >>"$RESULT_LIST"; continue; }
      live_head="$(gh pr view "$PR" --repo "$REPO" --json headRefOid --jq .headRefOid 2>/dev/null || true)"
      if [[ "$live_head" != "$head" ]]; then
        eval_fail_result "$arm_dir" "$case_id" "$arm_id" "$mode" "$base" "$head" \
          "head is $live_head, the manifest wants $head; refusing";
        failures=$((failures+1)); printf '%s\n' "$arm_dir/result.json" >>"$RESULT_LIST"; continue
      fi
      cat >"$arm_dir/gh-wrap" <<WRAP
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$arm_dir/reads.log"
exec gh "\$@"
WRAP
      chmod +x "$arm_dir/gh-wrap"
      : >"$arm_dir/reads.log"
      GH="$arm_dir/gh-wrap"
      WRAPPER="$BIN"
      ARM_CHECKOUT="$(git rev-parse --show-toplevel)"
      ARM_BASE_PRIME=""
      EVAL_HEAD="$head"
      export CROSSREV_GH_LOG=/dev/null
      if [[ "$mode" == "planted" ]]; then
        eval_fail_result "$arm_dir" "$case_id" "$arm_id" "$mode" "$base" "$head" \
          "planted-findings mode needs the stand-in; refused live";
        failures=$((failures+1)); printf '%s\n' "$arm_dir/result.json" >>"$RESULT_LIST"; continue
      fi
      eval_full_loop "$arm_dir" "$arm_json"
      eval_collect_live "$arm_dir"
      eval_write_result "$arm_dir" "$case_id" "$arm_id" "$mode" "$base" ""
    else
      eval_offline_env "$arm_dir"
      if ! eval_offline_setup "$case_id" "$base" "$head" "$base_branch" "$head_branch" "$arm_dir"; then
        eval_fail_result "$arm_dir" "$case_id" "$arm_id" "$mode" "$base" "$head" \
          "setup refused (see above)";
        failures=$((failures+1)); printf '%s\n' "$arm_dir/result.json" >>"$RESULT_LIST"; continue
      fi
      export CROSSREV_GH_LOG="$ARM_GH_LOG" CROSSREV_GH_ROUTES="$ARM_ROUTES"
      export CROSSREV_GH_STATE="$ARM_STATE" CROSSREV_GH_AUTHOR="$TRUSTED"
      export CROSSREV_PROMPT_LOG="$arm_dir/prompt.log" CROSSREV_ARGV_LOG="$arm_dir/argv.log"
      export CROSSREV_BROWSER_LOG="$arm_dir/browser.log"
      : >"$arm_dir/prompt.log" "$arm_dir/argv.log" "$arm_dir/browser.log"
      GH="$EVAL_STUB_DIR/gh"
      WRAPPER="$(eval_wrapper_for "$BIN" "$arm_dir/wrap")"
      EVAL_HEAD="$head"
      if [[ "$mode" == "planted" ]]; then
        eval_planted_arm "$arm_dir" "$arm_json" || failures=$((failures+1))
      else
        eval_full_loop "$arm_dir" "$arm_json"
      fi
      eval_collect_offline "$arm_dir"
      eval_write_result "$arm_dir" "$case_id" "$arm_id" "$mode" "$base" "$ARM_BASE_PRIME"
    fi
    printf '%s\n' "$arm_dir/result.json" >>"$RESULT_LIST"
    legs_csv=""
    if (( ${#LEGS[@]} > 0 )); then
      legs_csv="$(printf '%s' "${LEGS[*]}" | tr ' ' ',')"
    fi
    printf 'run-loop: case %s arm %s -> %s (%s)\n' \
      "$case_id" "$arm_id" "$EVAL_TERMINAL" "$legs_csv"
  done
done

# One base to base-prime entry per case: the fixed timestamp keeps every
# arm's synthetic commit on the same SHA, so the first arm speaks for all.
if [[ -s "$RESULT_LIST" ]]; then
  jq -s 'map(select(.base_prime != null))
    | map({key:.case, value:{base:.base, base_prime:.base_prime}})
    | from_entries' $(cat "$RESULT_LIST") >"$RESULTS_DIR/base-map.json"
  bash "$EVAL_HERE/adjudicate.sh" --results-dir "$RESULTS_DIR"
else
  printf '{}\n' >"$RESULTS_DIR/base-map.json"
fi

if (( failures > 0 )); then
  printf 'run-loop: %d arm(s) failed; results in %s\n' "$failures" "$RESULTS_DIR" >&2
  exit 1
fi
printf 'run-loop: done; results in %s\n' "$RESULTS_DIR"
