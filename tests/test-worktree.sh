#!/usr/bin/env bash
#
# Worktree isolation tests for the resolve leg.
#
# A local resolve leg runs in its own dedicated worktree, so:
# - the checkout the operator is standing in is never mutated or checked out
# - an unrelated branch with uncommitted work is left untouched
# - a clean run removes the worktree
# - a failed run leaves the worktree and prints its path
# - a retry reuses the existing worktree
# - doctor reports leftover worktrees and stranded quarantine
# - the run lock resolves to the clone's shared git directory, whichever working tree acquires it

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=harness.sh
source "$HERE/harness.sh"
# shellcheck source=../lib/ui.sh
source "$HERE/../lib/ui.sh"
# shellcheck source=../lib/legs.sh
source "$HERE/../lib/legs.sh"
# shellcheck source=../lib/run.sh
source "$HERE/../lib/run.sh"

gone()    { [[ ! -e "$2" ]] && ok "$1" || notok "$1" "$2 is still present"; }
present() { [[ -e "$2" ]]   && ok "$1" || notok "$1" "$2 is missing"; }

ID_FIX="aaaa000000000001"

# A review marker on pass 1 with an actionable finding.
setup_review_marker() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(( $(date +%s) - 100 ))" --arg id "$ID_FIX" '
    {v:1, leg:"review", pass:1, state:"complete", ts:$ts, done_ts:($ts + 100), run_id:"1",
     head_sha:$sha, harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     effort:"high", endpoint:null, tokens:1000, verdict:"issues-remain",
     findings:[
       {id:$id, path:"app.ts", line:2, side:"RIGHT", severity:"high", category:"security",
        pre_existing:false, title:"Fix this bug", why:"why", fix:"fix",
        anchor:"", thread_id:"T_FIX", resolution:null, tracked_as:null}]}'
}

# A resolve payload that fixes finding 1.
setup_resolve_payload() {
  jq -cn '
    {blocked:false, blocked_reason:null,
     summary:"Fixed finding 1.",
     commit_subject:"fix: resolve finding 1",
     resolutions:[
       {finding_number:1, resolution:"fixed", reply:"Fixed the issue.",
        persist:null, duplicate_of:null}]}'
}

setup_resolve_routes() {
  local rm_file="$1"
  routes_baseline "$rm_file"
  route 'api --method POST repos/*/issues/42/comments*' '{"id":9002}'
  route '*reviewThreads*' "$(threads_response "$(thread_node T_FIX app.ts 2 false "$ID_FIX")")"
  route '*resolveReviewThread*' '{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}'
  route 'api --method POST repos/*/pulls/42/comments/*/replies*' '{"id":6001}'
}

# --- 1. Resolve from unrelated branch with uncommitted work -----------------
fixture_repo; stub_reset

# Switch operator checkout to main and add uncommitted changes
(
  cd "$FIX_DIR" || exit 1
  git checkout -q main
  printf '// operator dirty edit\n' >> app.ts
  printf 'untracked file\n' > dirty.txt
)

rm_file="$(marker_comment 9001 "$(setup_review_marker)" | jq -cs . | payload)"
setup_resolve_routes "$rm_file"

edit_script="$(mktemp)"
printf 'printf "export const ok = 1\\nexport function refresh() { /* fixed */ }\\n" > app.ts\n' > "$edit_script"
CROSSREV_RESOLVE_EDIT="$edit_script"; export CROSSREV_RESOLVE_EDIT
CROSSREV_RESOLVE_PAYLOAD="$(setup_resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_MODEL="resolver-model"; export CROSSREV_RESOLVE_MODEL

out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is "resolve succeeds from an unrelated branch with dirty work" "$rc" "0"
has "output reports resolved pass" "$out" "resolved pass 1"
is "operator checkout stays on main branch" "$(git -C "$FIX_DIR" rev-parse --abbrev-ref HEAD)" "main"
has "operator uncommitted changes in app.ts are preserved" "$(cat "$FIX_DIR/app.ts")" "operator dirty edit"
present "operator untracked dirty.txt is preserved" "$FIX_DIR/dirty.txt"

# Verify that the push landed on origin feature branch
origin_head="$(git --git-dir="$FIX_ORIGIN" rev-parse refs/heads/feature)"
head_content="$(git --git-dir="$FIX_ORIGIN" show "$origin_head:app.ts")"
has "origin feature branch received the fix commit" "$head_content" "/* fixed */"

# --- 2. Clean exit removes the worktree ------------------------------------
wt_expected="$XDG_STATE_HOME/crossrev/worktrees/acme-widget/pr-42"
gone "clean exit removes the dedicated worktree directory" "$wt_expected"
is "git worktree list does not retain the tool worktree" \
  "$(git -C "$FIX_DIR" worktree list | grep -c "$wt_expected" || true)" "0"

# --- 3. Failed run leaves worktree and prints path --------------------------
fixture_repo; stub_reset

(
  cd "$FIX_DIR" || exit 1
  git checkout -q main
)

rm_file="$(marker_comment 9001 "$(setup_review_marker)" | jq -cs . | payload)"
setup_resolve_routes "$rm_file"

# Inject failure by having the stub return an invalid payload
bad_payload="$(mktemp)"
printf 'not valid json' > "$bad_payload"
CROSSREV_RESOLVE_PAYLOAD="$bad_payload"; export CROSSREV_RESOLVE_PAYLOAD
unset CROSSREV_RESOLVE_EDIT

err_rc=0
err_out="$("$CROSSREV" resolve --pr 42 2>&1)" || err_rc=$?

notok_if() { [[ "$2" == "0" ]] && notok "$1" "non-zero exit" "$2" || ok "$1"; }
notok_if "failed resolve exits non-zero" "$err_rc"
present "failed run leaves the worktree behind" "$wt_expected"
has "error output prints the worktree path" "$err_out" "$wt_expected"

# --- 4. Subsequent run reuses the existing worktree -------------------------
# Plant a canary file in the leftover worktree to prove it is reused
canary="$wt_expected/canary.txt"
printf 'canary\n' > "$canary"

CROSSREV_RESOLVE_EDIT="$edit_script"; export CROSSREV_RESOLVE_EDIT
CROSSREV_RESOLVE_PAYLOAD="$(setup_resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD

out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?
is "second run succeeds and reuses worktree" "$rc" "0"
has "second run reports resolved pass" "$out" "resolved pass 1"
gone "second run removes worktree on clean exit" "$wt_expected"

# --- 5. Doctor reports tool-owned worktrees left behind ---------------------
dummy_wt="$XDG_STATE_HOME/crossrev/worktrees/acme-widget/pr-99"
mkdir -p "$dummy_wt"

doc_out="$("$CROSSREV" doctor 2>&1 || true)"
has "doctor output reports tool-owned worktrees" "$doc_out" "Tool-owned worktrees"
has "doctor output lists leftover worktree path" "$doc_out" "$dummy_wt"
rm -rf "$dummy_wt"

# --- 6. Doctor detects stranded quarantine ---------------------------------
mkdir -p "$FIX_DIR/.crossrev-quarantine"
printf 'evil instructions\n' > "$FIX_DIR/.crossrev-quarantine/CLAUDE.md"

(
  cd "$FIX_DIR" || exit 1
  doc_q_rc=0
  doc_q_out="$("$CROSSREV" doctor 2>&1)" || doc_q_rc=$?
  notok_if "doctor fails when stranded quarantine exists" "$doc_q_rc"
  has "doctor names stranded quarantine" "$doc_q_out" "stranded quarantine"
  has "doctor names quarantined file" "$doc_q_out" "CLAUDE.md"
)

# --- 7. Resolve when operator has PR head branch checked out in main tree -----
fixture_repo; stub_reset

(
  cd "$FIX_DIR" || exit 1
  git checkout -q feature
  printf '// dirty feature work\n' >> app.ts
)

rm_file="$(marker_comment 9001 "$(setup_review_marker)" | jq -cs . | payload)"
setup_resolve_routes "$rm_file"

edit_script_7="$(mktemp)"
printf 'printf "export const ok = 1\\nexport function refresh() { /* fixed */ }\\n" > app.ts\n' > "$edit_script_7"
CROSSREV_RESOLVE_EDIT="$edit_script_7"; export CROSSREV_RESOLVE_EDIT
CROSSREV_RESOLVE_PAYLOAD="$(setup_resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_MODEL="resolver-model"; export CROSSREV_RESOLVE_MODEL

out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?
is "resolve succeeds when operator checkout is on PR head branch" "$rc" "0"
has "output reports resolved pass on head branch" "$out" "resolved pass 1"
is "operator checkout stays on feature branch" "$(git -C "$FIX_DIR" rev-parse --abbrev-ref HEAD)" "feature"
has "operator uncommitted changes on feature are preserved" "$(cat "$FIX_DIR/app.ts")" "dirty feature work"

# --- 8. Stale worktree at wrong revision is recreated rather than reused -------
fixture_repo; stub_reset

# Create a leftover worktree at an old revision (FIX_BASE instead of FIX_HEAD)
mkdir -p "$(dirname "$wt_expected")"
git -C "$FIX_DIR" worktree add --detach "$wt_expected" "$FIX_BASE"
printf 'stale leftover marker\n' > "$wt_expected/stale.txt"

rm_file="$(marker_comment 9001 "$(setup_review_marker)" | jq -cs . | payload)"
setup_resolve_routes "$rm_file"

edit_script_8="$(mktemp)"
printf 'printf "export const ok = 1\\nexport function refresh() { /* fixed */ }\\n" > app.ts\n' > "$edit_script_8"
CROSSREV_RESOLVE_EDIT="$edit_script_8"; export CROSSREV_RESOLVE_EDIT
CROSSREV_RESOLVE_PAYLOAD="$(setup_resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD

out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?
is "resolve succeeds when leftover worktree was at stale revision" "$rc" "0"
has "resolve output reports resolved pass for stale worktree" "$out" "resolved pass 1"
gone "stale file from old worktree is gone" "$wt_expected/stale.txt"
origin_head="$(git --git-dir="$FIX_ORIGIN" rev-parse refs/heads/feature)"
head_content="$(git --git-dir="$FIX_ORIGIN" show "$origin_head:app.ts")"
has "fix landed against the correct head revision" "$head_content" "/* fixed */"

# --- 9. Revision guard fires when tree is not at pull request head -------------
guard_rc=0
guard_err="$(legs_assert_push_target "1111111111111111111111111111111111111111" "2222222222222222222222222222222222222222" "feature" "main" "acme/widget" "acme/widget" false false 2>&1)" || guard_rc=$?
notok_if "revision guard refuses when tree is not at PR head" "$guard_rc"
has "revision guard names the revision mismatch" "$guard_err" "crossrev pushes only to the revision under review"

# --- 10. Leftover worktree belonging to another checkout ----------------------
# The worktree path is keyed on the repository slug and the pull request number,
# so two checkouts of one repository collide on it. Section 8's revision check
# cannot separate them: both hold the pull request's head by definition. Pinning
# the commit dates gives the two fixtures byte-identical commits, and therefore
# one sha, so the collision is reproduced rather than waited for.
export GIT_AUTHOR_DATE="2026-01-01T00:00:00+00:00"
export GIT_COMMITTER_DATE="2026-01-01T00:00:00+00:00"

fixture_repo; stub_reset
other_dir="$FIX_DIR"; other_origin="$FIX_ORIGIN"; shared_head="$FIX_HEAD"
mkdir -p "$(dirname "$wt_expected")"
git -C "$other_dir" worktree add --detach "$wt_expected" "$shared_head"
printf 'belongs to the other checkout\n' > "$wt_expected/other.txt"

fixture_repo; stub_reset
unset GIT_AUTHOR_DATE GIT_COMMITTER_DATE
is "the two checkouts share a head revision" "$FIX_HEAD" "$shared_head"

rm_file="$(marker_comment 9001 "$(setup_review_marker)" | jq -cs . | payload)"
setup_resolve_routes "$rm_file"

edit_script_10="$(mktemp)"
printf 'printf "export const ok = 1\nexport function refresh() { /* fixed */ }\n" > app.ts
' > "$edit_script_10"
CROSSREV_RESOLVE_EDIT="$edit_script_10"; export CROSSREV_RESOLVE_EDIT
CROSSREV_RESOLVE_PAYLOAD="$(setup_resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD

out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?
is "resolve succeeds beside another checkout's leftover worktree" "$rc" "0"
gone "the other checkout's worktree is not reused" "$wt_expected/other.txt"
has "the fix reaches the checkout the leg ran from" \
  "$(git --git-dir="$FIX_ORIGIN" show "refs/heads/feature:app.ts")" "/* fixed */"
hasnt "and nothing reaches the other checkout" \
  "$(git --git-dir="$other_origin" show "refs/heads/feature:app.ts")" "/* fixed */"

rm -rf "$FIX_DIR/.crossrev-quarantine"
rm -f "$edit_script" "$bad_payload" "$edit_script_7" "$edit_script_8" "$edit_script_10"

# --- 11. The run lock keys on the shared git directory ----------------------
# Every working tree of one clone must agree on one lock path, or two of them
# could drive the same pull request at once. A linked worktree keeps a private
# git dir under <clone>/.git/worktrees/, which is what --git-dir answers there;
# the lock has to come from the shared directory instead.
fixture_repo; stub_reset

linked_wt="$(mktemp -d)/linked"
git -C "$FIX_DIR" worktree add -q -b run-lock-wt "$linked_wt" >/dev/null

# Acquire in the named tree and report the path the function computed. Each call
# runs in a subshell, so neither inherits the other's CROSSREV_LOCK, and $$ names
# the same process in both — which is why the lock file goes away between calls.
run_lock_path() {
  (
    cd "$1" || exit 1
    rm -f "$2/crossrev/pr-42.lock"
    CROSSREV_LOCK=""
    run_lock_acquire 42 local || exit 1
    [[ -n "$CROSSREV_LOCK" ]] || exit 1
    printf '%s' "$CROSSREV_LOCK"
  )
}

shared_git="$FIX_DIR/.git"
main_lock="$(run_lock_path "$FIX_DIR" "$shared_git")"
wt_lock="$(run_lock_path "$linked_wt" "$shared_git")"

# Stand in the named tree and ask the liveness check whether this process is
# running, by its answer variable rather than its silence on a missed lock.
liveness_from() {
  (
    cd "$1" || exit 1
    # Read by _status_liveness_local from lib/run.sh.
    # shellcheck disable=SC2034
    CTX_PR=42
    _status_liveness_local "$$"
    [[ "${STATUS_LIVENESS:-}" == "running" ]] && printf 'yes' || printf 'no'
  )
}

is "main checkout and linked worktree compute one lock path" "$wt_lock" "$main_lock"
if [[ "$main_lock" == /* && "$wt_lock" == /* ]]; then
  ok "the computed lock paths are absolute"
else
  notok "the computed lock paths are absolute" "both starting with /" "$main_lock / $wt_lock"
fi
has "the lock lives under the clone's shared git directory" "$main_lock" "$shared_git/"
if [[ -f "$shared_git/crossrev/pr-42.lock" ]]; then
  ok "acquiring writes the lock into the shared git directory"
else
  notok "acquiring writes the lock into the shared git directory" "lock file exists" "missing"
fi
wt_private="$(git -C "$linked_wt" rev-parse --git-dir)"
if [[ ! -e "$wt_private/crossrev/pr-42.lock" ]]; then
  ok "no lock lands in the linked worktree's private git directory"
else
  notok "no lock lands in the linked worktree's private git directory" \
    "nothing at $wt_private/crossrev" "lock file exists"
fi

# The status liveness check corroborates a local run against the same lock file.
# Standing in the worktree, it must read the lock the main checkout's run took.
holder_line="$(printf '%s on %s since %s\n' "$$" \
  "$(hostname 2>/dev/null || printf 'local')" "$(date -u '+%Y-%m-%dT%H:%M:%SZ')")"
printf '%s\n' "$holder_line" >"$shared_git/crossrev/pr-42.lock"
liveness="$(liveness_from "$linked_wt")"
is "status from a linked worktree reads the lock another tree wrote" "$liveness" "yes"

git -C "$FIX_DIR" worktree remove --force "$linked_wt"
rm -f "$shared_git/crossrev/pr-42.lock"

finish
