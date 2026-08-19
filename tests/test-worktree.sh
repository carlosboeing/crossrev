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

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

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

rm -rf "$FIX_DIR/.crossrev-quarantine"
rm -f "$edit_script" "$bad_payload"

finish
