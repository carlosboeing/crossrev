#!/usr/bin/env bash
#
# The resolve leg: resolutions, deferral, both dedupe tiers, persist-before-
# resolve, the commit, and the guards around all of it.
#
# A finding that is real but not fixed has to outlive the pull request, and an
# unresolved thread on a merged pull request is visible in no GitHub view. So the
# ordering here is the property under test: persist first, resolve second, and
# never resolve against a write that did not land.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

ID_FIX="aaaa000000000001"
ID_DEFER="bbbb000000000002"

config_with_issue_sink() {
  cat <<'EOF'
version: 1
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
  destination: github_issues
  github_issues:
    tracking_label: crossrev-review
    labels: [bug]
    create_missing_labels: true
    comment_on_existing_issue: false
EOF
}

# A completed review marker carrying one fixable finding and one pre-existing
# one. The pre-existing finding is the interesting case: it is verified like any
# other, and provenance rather than severity is what keeps it out of the diff.
review_marker() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(date +%s)" \
    --arg f "$ID_FIX" --arg d "$ID_DEFER" '
    {v:1, leg:"review", pass:1, state:"complete", ts:$ts, run_id:"1", head_sha:$sha,
     harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     verdict:"issues-remain",
     findings:[
       {id:$f, path:"app.ts", line:2, side:"RIGHT", severity:"high", category:"correctness", pre_existing:false,
        title:"Unchecked fetch response", why:"w", fix:"check it", anchor:"",
        thread_id:"T_FIX", resolution:null, tracked_as:null},
       {id:$d, path:"app.ts", line:1, side:"RIGHT", severity:"high", category:"maintainability", pre_existing:true,
        title:"Legacy export is untyped", why:"w", fix:"type it", anchor:"",
        thread_id:"T_DEFER", resolution:null, tracked_as:null}]}'
}

# What the resolver returns. Findings are named by the number the prompt gave
# them — 1 is ID_FIX and 2 is ID_DEFER, in the order review_marker lists them —
# and the orchestrator translates back to ids before anything is recorded. The
# marker fixtures further down deliberately stay in the id shape, because that is
# what the markers on live pull requests carry and what this still has to read.
# $3 is the commit subject the resolver returns. Defaults to one a real resolver
# would write; pass a deliberately bad one to exercise the validation.
resolve_payload() {
  local dup="${1:-null}" persist="${2:-yes}"
  local subject="${3-fix(api): check the response status before reading it}"
  local p='{"title":"Legacy export is untyped","body":"Measured before filing."}'
  [[ "$persist" == "no" ]] && p='null'
  jq -cn --argjson dup "$dup" --argjson p "$p" --arg cs "$subject" '
    {blocked:false, blocked_reason:null,
     commit_subject:(if $cs == "" then null else $cs end),
     summary:"Fixed the unchecked response. The untyped legacy export is real but predates this branch.",
     resolutions:[
       {finding_number:1, resolution:"fixed", reply:"Added the ok check.", persist:null, duplicate_of:null},
       {finding_number:2, resolution:"deferred", reply:"Confirmed real, and it predates this branch.",
        persist:$p, duplicate_of:$dup}]}'
}

config_with_file_sink() {
  cat <<'EOF'
version: 1
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
  destination: repository
  repository:
    layout: folder
    path: .crossrev/backlog
EOF
}

# A stated `layout: file` with no path, which is the only configuration whose
# resolved destination has a parent directory that does not exist yet.
config_with_default_file_backlog() {
  config_with_file_sink | sed 's/^    layout: folder$/    layout: file/; /^    path: /d'
}

# Only a deferral, nothing fixed. The case where a commit has to happen for a
# reason other than a code change.
#
# Both findings are still settled. This fixture used to answer only the
# deferred one, which is the silent under-coverage the numbering change now
# rejects: the other finding got no reply, its thread was never resolved, and
# nothing anywhere said so.
defer_only_payload() {
  jq -cn '
    {blocked:false, blocked_reason:null,
     summary:"Nothing was fixed here. The untyped legacy export is real but predates this branch.",
     resolutions:[
       {finding_number:1, resolution:"skipped", reply:"Left alone this pass.",
        persist:null, duplicate_of:null},
       {finding_number:2, resolution:"deferred", reply:"Confirmed real, and it predates this branch.",
        persist:{title:"Legacy export is untyped", body:"Measured before filing."},
        duplicate_of:null}]}'
}

# A resolver whose fix is not idempotent: run twice, it appends twice. That is
# what makes "did the second attempt start from a clean tree" observable at all
# — an idempotent fix looks identical either way.
appending_edit_script() {
  local f; f="$(mktemp)"
  printf 'printf "export const patched = 1\\n" >> app.ts\n' >"$f"
  printf '%s' "$f"
}

# The resolver changes code; the orchestrator commits it.
edit_script() {
  local f; f="$(mktemp)"
  printf 'printf "export const ok = 1\\nexport async function refresh() { const r = await fetch(\\"/t\\"); if (!r.ok) throw new Error(\\"bad\\") }\\n" > app.ts\n' >"$f"
  printf '%s' "$f"
}

routes_resolve() {
  local threads; threads="$(threads_response \
    "$(thread_node T_FIX app.ts 2 false "$ID_FIX")" \
    "$(thread_node T_DEFER app.ts 1 false "$ID_DEFER")")"
  route 'api --method POST repos/*/issues/42/comments*' '{"id":9002}'
  route '*reviewThreads*' "$threads"
  route '*resolveReviewThread*' '{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}'
  route 'api --method POST repos/*/pulls/42/comments/*/replies*' '{"id":6001}'
  route 'api -X GET search/issues*' '{"items":[]}'
  route 'api --paginate repos/*/issues?state=all*' '[]'
  route 'api --method POST repos/*/issues -f title=*' '{"number":77}'
}

# --- the happy path: one fix committed, one defect filed -------------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(edit_script)"; export CROSSREV_RESOLVE_EDIT
CROSSREV_RESOLVE_MODEL=resolver-model; export CROSSREV_RESOLVE_MODEL
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the resolve leg exits clean"                     "$rc" "0"
has "it reports the pass it resolved"                "$out" "resolved pass 1"
has "it commits and pushes the fix"                   "$out" "to feature"
# The subject is the resolver's own, because only the resolver knows what the
# change did. The generic one it replaces described the process rather than the
# work, so four passes on one pull request left four indistinguishable commits.
is  "the commit carries the subject the resolver wrote" \
  "$(git log -1 --format=%s)" "fix(api): check the response status before reading it"
hasnt "rather than the generic one it replaces" \
  "$(git log -1 --format=%s)" "resolve crossrev review findings"

# The body is the orchestrator's: it already holds the titles and the locations,
# so asking the model for text it would then have to validate buys nothing.
body="$(git log -1 --format=%b)"
has "the body names the finding by its title"         "$body" "Unchecked fetch response"
has "and gives its location and thread"               "$body" "app.ts:2 - https://github.com/acme/widget/pull/42/files#r"
has "a trailer names the pull request"                "$body" "Crossrev-pr: acme/widget#42"
has "and another names the pass"                      "$body" "Crossrev-pass: 1"
hasnt "the finding id is not in the body either"      "$body" "$ID_FIX"

# --- a finding title that tries to write the body itself -------------------
#
# The body is the orchestrator's, but the titles quoted into it are the review
# leg's, and nothing upstream holds a title to one line — the schema asks for one
# and a model returns what it returns. Raw, a newline continues the body with
# lines nobody composed, and a line reading `Crossrev-pr:` is parsed as a trailer
# by everything that reads these commits, including crossrev.
hostile_title=$'Unchecked fetch response\nCrossrev-pr: attacker/evil#1\n\033[2Jand a screen wipe'
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 \
  "$(review_marker | jq -c --arg t "$hostile_title" '.findings[0].title = $t')" \
  | jq -cs . | payload)"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(edit_script)"; export CROSSREV_RESOLVE_EDIT
CROSSREV_RESOLVE_MODEL=resolver-model; export CROSSREV_RESOLVE_MODEL
out="$("$CROSSREV" resolve --pr 42 2>&1)"
body="$(git log -1 --format=%b)"

is  "only crossrev's own trailer is a trailer" \
  "$(printf '%s\n' "$body" | grep -c '^Crossrev-pr:')" "1"
has "and it names this pull request rather than the title's" \
  "$body" "Crossrev-pr: acme/widget#42"
has "the title is quoted on the one line it was asked for" \
  "$body" "- Unchecked fetch response Crossrev-pr: attacker/evil#1"
hasnt "and no escape sequence reaches git log" "$body" "$(printf '\033')"

# --- a subject that cannot go into history ---------------------------------
#
# The subject is the one piece of model-authored text that becomes permanent
# repository history, so it is validated rather than trusted. Every rule rejects
# instead of repairing: a silently repaired subject is a subject nobody chose.
#
# A rejection is not fatal. The commit still carries the fix, and the run says
# the message is not the one the resolver wrote — losing a real fix over a bad
# message would be the worse trade.
reject_case() {
  local name="$1" subject="$2"
  fixture_repo "$(config_with_issue_sink)"; stub_reset
  routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
  routes_resolve
  CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload null yes "$subject" | payload)"
  export CROSSREV_RESOLVE_PAYLOAD
  CROSSREV_RESOLVE_EDIT="$(edit_script)"; export CROSSREV_RESOLVE_EDIT
  CROSSREV_RESOLVE_MODEL=resolver-model; export CROSSREV_RESOLVE_MODEL
  local o; o="$("$CROSSREV" resolve --pr 42 2>&1)"
  has "$name falls back to the generic subject" \
    "$(git log -1 --format=%s)" "fix: resolve crossrev review findings (pass 1)"
  has "and $name is reported rather than swallowed" "$o" "commit subject was rejected"
}

reject_case "a multi-line subject" "fix(api): check the status
and also rewrite the parser"
reject_case "an over-long subject" \
  "fix(api): $(printf 'x%.0s' {1..120})"
# DEL is a control character that sits above the C0 range rather than inside it,
# so a check written as a range up to 0x1f lets it through. `_commit_line` strips
# it from a body line already; a subject git keeps forever gets the same rule.
reject_case "a subject carrying DEL" \
  "$(printf 'fix(api): check the\177status')"

# An absent subject is not a rejection — the resolver simply did not write one,
# and there is nothing to report.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload null yes "" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(edit_script)"; export CROSSREV_RESOLVE_EDIT
CROSSREV_RESOLVE_MODEL=resolver-model; export CROSSREV_RESOLVE_MODEL
out="$("$CROSSREV" resolve --pr 42 2>&1)"
has "no subject at all still commits, generically" \
  "$(git log -1 --format=%s)" "fix: resolve crossrev review findings (pass 1)"
hasnt "and says nothing about a rejection"            "$out" "commit subject was rejected"

# --- the convention the subject is meant to match --------------------------
#
# The prompt shows the repository's own recent commit subjects, so the resolver
# matches what the repository does rather than a default. Read from the BASE
# revision: a branch that could seed this would be choosing the style of the
# commit written onto it, which is ADR 0003's reasoning applied to a message.
prompt="$(cat "$PROMPT_LOG")"
has "the prompt shows the repository's commit convention" \
  "$prompt" "This repository's commit convention"

# crossrev's own commits are excluded from the sample. Left in, the leg would
# learn the generic subject it is replacing and reproduce it — the fixture has
# just made two of them above.
hasnt "and does not teach the leg the subject it is replacing" \
  "$prompt" "resolve crossrev review findings (pass"

# A repository with too little history has no convention to read, and saying so
# beats showing three subjects and letting the leg find a pattern in them.
has "a short history asks for Conventional Commits instead" \
  "$prompt" "too short to read a convention from"
# Checked against the bare repo rather than `git ls-remote origin`: the fixture's
# fetch URL is a real github.com address it must never contact, so ls-remote there
# would fail and prove nothing either way.
is  "and it is pushed, not just committed" \
  "$(git rev-parse HEAD)" "$(git -C "$FIX_ORIGIN" rev-parse refs/heads/feature)"

has "it files the deferred defect"                    "$out" "filed 1 issue(s) for deferred work"
has "the issue carries the identity label"            "$(calls)" "labels[]=crossrev-review"
has "and the repository's own taxonomy alongside it"  "$(calls)" "labels[]=bug"
is  "it replies in each thread rather than at top level" \
  "$(count 'pulls/42/comments/5000/replies')" "2"
has "it resolves the threads it settled"              "$out" "resolved 2 thread(s)"
has "the summary lists what was deferred and where"   "$(calls)" "Deferred work filed"
# Each line leads with where the finding is, not with its id. The id named
# nothing a reader could reach: it lives in an HTML comment marker, so it does
# not render, and a browser find on this page matches only the line itself.
has "a deferred line leads with the finding's location" \
  "$(calls)" "- [\`app.ts:1\`](https://github.com/acme/widget/pull/42/files#r"
hasnt "and never with the finding id"                 "$(calls)" "- \`$ID_DEFER\` —"
has "the loop is handed back to the reviewer"         "$(calls)" "labels[]=crossrev/awaiting-review"

# Provenance governs what happens after verification, never whether it happens.
# The fixture's pre-existing finding is `high` deliberately: under the severity
# split it would otherwise be the one thing above the threshold, and the point of
# the boolean is that provenance outranks the scale.
has "a pre-existing finding still reached the resolver" \
  "$(cat "$PROMPT_LOG")" "Legacy export is untyped"
has "and the prompt tells it not to fix that one here" \
  "$(cat "$PROMPT_LOG")" "verify, then stop"
has "the prompt marks it as one the resolver may not fix" \
  "$(cat "$PROMPT_LOG")" "May fix: no"
hasnt "a high-severity pre-existing finding is not in the commit" \
  "$(git log -1 --format=%B)" "$ID_DEFER"
has  "it is filed to the backlog instead"             "$(calls)" "Legacy export is untyped"

# The resolver is blind to the quarantined paths while the diff still carries
# their changes, so the reviewer can raise findings it cannot act on. The rule
# is only useful if the list is concrete: "instruction files" is not a path.
has "the prompt says which paths are not in the checkout" \
  "$(cat "$PROMPT_LOG")" "deliberately not in the checkout"
has "and names them rather than describing them" \
  "$(cat "$PROMPT_LOG")" "CLAUDE.md, AGENTS.md, GEMINI.md"
has "and says what to return for one"                 "$(cat "$PROMPT_LOG")" "quarantined and the finding was reported rather than verified"

# --- a repository backlog write has to be inside the commit ---------------
#
# The GitHub issues destination writes outside the tree, so ordering cannot hurt it. A repository backlog writes
# into the working tree, and a commit that ran first left that write behind: on
# an ephemeral runner it died with the container while its thread resolved,
# because `tracked` was non-empty. That is the "work disappears" failure
# persist-before-resolve exists to prevent, one step further along.
fixture_repo "$(config_with_file_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(edit_script)"; export CROSSREV_RESOLVE_EDIT
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the repository-backlog leg exits clean"          "$rc" "0"
has "the deferral is recorded to the repository backlog" "$out" "filed 1 issue(s) for deferred work"
is  "the backlog file is in the commit, not just the tree" \
  "$(git show --name-only --format= HEAD | grep -c '^\.crossrev/backlog/')" "1"
is  "so nothing of it is left behind in the tree" \
  "$(git status --porcelain -- .crossrev | wc -l | tr -d ' ')" "0"
is  "and the code fix rides in the same commit" \
  "$(git show --name-only --format= HEAD | grep -c '^app\.ts$')" "1"

# The other half: a pass that defers and fixes nothing still has a tree write to
# carry, and a commit guarded only on the fix count skipped exactly that case.
fixture_repo "$(config_with_file_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(defer_only_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "a deferral-only pass exits clean"                "$rc" "0"
is  "it still commits, because the backlog wrote to the tree" \
  "$(git show --name-only --format= HEAD | grep -c '^\.crossrev/backlog/')" "1"
has "and says what the commit is for, not 'fix'"      "$(git log -1 --format=%s)" "chore: record deferred crossrev findings"
hasnt "no fix was claimed, so nothing warns about one" \
  "$out" "changed no files"

# --- the default file layout has to write where it says it wrote ----------
#
# `layout: file` with no path resolves to `.crossrev/backlog.md`, and `.crossrev`
# does not exist in a repository that has never run the loop. The append then
# failed on the missing directory without stopping the function, which printed
# the path anyway — so the finding was counted filed and its thread resolved
# against a file nobody had written. The folder layout was never exposed to it,
# because it creates its directory first.
fixture_repo "$(config_with_default_file_backlog)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(defer_only_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the default file layout exits clean"             "$rc" "0"
is  "and the file it names is actually written" \
  "$(git show --name-only --format= HEAD | grep -c '^\.crossrev/backlog\.md$')" "1"
has "carrying the deferred finding it claimed to file" \
  "$(cat .crossrev/backlog.md)" "Legacy export is untyped"
has "and the summary points at that path"             "$out" "filed 1 issue(s) for deferred work"

# --- the review leg's own comments must not silence the resolver ---------
#
# Every inline comment the review leg posts carries a finding marker, so a posted
# set read without filtering on the leg finds every id already there. The reply
# is then skipped as a duplicate while the thread is resolved anyway, and the
# collaborator gets a resolved thread with no explanation in it. The baseline
# stubs pulls/42/comments as [], which is exactly why nothing caught this: the
# one condition that triggers it is the one the fixture never had.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
route_first "api --paginate repos/*/pulls/42/comments*" \
  "@$(POSTED_LEG=review posted_comments "$ID_FIX" "$ID_DEFER" | payload)"
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(edit_script)"; export CROSSREV_RESOLVE_EDIT
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the leg completes with the review leg's comments present" "$rc" "0"
is  "it still replies in both threads" \
  "$(count 'pulls/42/comments/5000/replies')" "2"
has "and still resolves what it settled"              "$out" "resolved 2 thread(s)"

# The other half of the same rule: a resolve-leg marker DOES mean "replied
# already", so a retry after a crash must not reply twice.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
route_first "api --paginate repos/*/pulls/42/comments*" \
  "@$(POSTED_LEG=resolve posted_comments "$ID_FIX" | payload)"
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(edit_script)"; export CROSSREV_RESOLVE_EDIT
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "a finding already replied to is not replied to twice" \
  "$(count 'pulls/42/comments/5000/replies')" "1"

# --- destination: none files nothing and leaves the thread open -----------
fixture_repo; stub_reset      # the default config has backlog.destination: none
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload null no | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(edit_script)"; export CROSSREV_RESOLVE_EDIT
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "with no backlog destination the leg still completes" "$rc" "0"
is  "nothing is filed anywhere"                       "$(count 'method POST repos/acme/widget/issues -f title=')" "0"
has "and the summary says the thread stays open"      "$(calls)" "not persisted anywhere"
is  "only the settled thread is resolved, not the deferred one" \
  "$(count 'resolveReviewThread')" "1"

# --- dedupe tier 1: crossrev already filed this exact finding --------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
existing="$(jq -cn --arg d "$ID_DEFER" '
  [{number:55, pull_request:null,
    body:("Filed earlier. <!-- crossrev:f " + ({id:$d, pass:1, leg:"resolve"} | tojson) + " -->")}]')"
route_first 'api --paginate repos/*/issues?state=all*' "$existing"
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(edit_script)"; export CROSSREV_RESOLVE_EDIT
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "an already-filed finding files nothing"          "$(count 'method POST repos/acme/widget/issues -f title=')" "0"
has "and it says the finding was already tracked"     "$(calls)" "already tracked as #55"
has "the thread still resolves, because it is tracked" "$out" "resolved 2 thread(s)"

# --- dedupe tier 2: the resolver matched a human-filed issue ------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
route_first 'api -X GET search/issues*' \
  '{"items":[{"number":31,"title":"app.ts exports are untyped","state":"open","body":"Noticed a while ago."}]}'
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload 31 | payload)"; export CROSSREV_RESOLVE_PAYLOAD
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "a matched human-filed issue files nothing"       "$(count 'method POST repos/acme/widget/issues -f title=')" "0"
has "and the summary names the issue it matched"      "$(calls)" "matches the existing issue #31"
has "the candidates were handed to the model to judge" "$(cat "$PROMPT_LOG")" "**#31** (open)"
hasnt "and crossrev did not comment on the human's issue by default" \
  "$(calls)" "repos/acme/widget/issues/31/comments"

# A closed candidate counts the same: re-filing something explicitly closed is the
# most irritating duplicate available.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
route_first 'api -X GET search/issues*' \
  '{"items":[{"number":19,"title":"untyped exports","state":"closed","body":"Decided against."}]}'
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload 19 | payload)"; export CROSSREV_RESOLVE_PAYLOAD
out="$("$CROSSREV" resolve --pr 42 2>&1)"
is  "a closed matching issue files nothing"           "$(count 'method POST repos/acme/widget/issues -f title=')" "0"
has "and the closed candidate was offered for judgement" "$(cat "$PROMPT_LOG")" "(closed)"

# --- persist before resolve ---------------------------------------------
#
# A backlog write that fails must leave the thread open and the resolution
# unrecorded, rather than resolving against a write that did not land.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
route_first 'api --method POST repos/*/issues -f title=*' '!fail'
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(edit_script)"; export CROSSREV_RESOLVE_EDIT
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "a failed filing does not fail the whole leg"     "$rc" "0"
has "it says the write did not land"                  "$out" "could not file an issue"
has "and the summary records that nothing was persisted" "$(calls)" "not persisted anywhere"
is  "the deferred thread is left open"                "$(count 'resolveReviewThread')" "1"

# --- commit idempotency -------------------------------------------------
#
# Pushed code cannot dedupe the way comments can, so the resolve leg records the
# SHA it produced. A recovery that finds one skips the fix step entirely.
fixture_repo "$(config_with_issue_sink)"; stub_reset
addr_claim="$(jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(date +%s)" --arg f "$ID_FIX" --arg d "$ID_DEFER" '
  {v:1, leg:"resolve", pass:1, state:"started", ts:$ts, run_id:"1", head_sha:$sha,
   harness:"claude", model:"resolver-model", model_reported:"resolver-model",
   blocked:false, blocked_reason:null, commit_sha:"cafe0000cafe0000cafe0000cafe0000cafe0000",
   summary:"Recovered.",
   resolutions:[{finding_id:$f, resolution:"fixed", reply:"done", persist:null, duplicate_of:null},
                 {finding_id:$d, resolution:"disputed", reply:"not real", persist:null, duplicate_of:null}]}')"
comments="$( { marker_comment 9001 "$(review_marker)"; marker_comment 9002 "$addr_claim"; } | jq -cs . | payload)"
routes_baseline "$comments"
routes_resolve
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "recovery with a recorded SHA exits clean"        "$rc" "0"
has "it does not run the resolver again"             "$out" "already recorded its resolutions"
has "and it does not re-run the fix step"             "$out" "already pushed cafe000"
is  "so nothing new is committed"                     "$(git log -1 --format=%s)" "feature"

# --- recovery across the wrap_up → summary rename ------------------------
#
# The recovery branch above is exactly where an upgrade lands badly: the previous
# attempt recorded its resolutions under the old field name, so the resolver is
# not run again and there is no second chance to produce the text. Reading only
# `.summary` there publishes a summary comment with nothing in it, and the marker
# says the pass completed.
fixture_repo "$(config_with_issue_sink)"; stub_reset
old_claim="$(jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(date +%s)" --arg f "$ID_FIX" --arg d "$ID_DEFER" '
  {v:1, leg:"resolve", pass:1, state:"started", ts:$ts, run_id:"1", head_sha:$sha,
   harness:"claude", model:"resolver-model", model_reported:"resolver-model",
   blocked:false, blocked_reason:null, commit_sha:"cafe0000cafe0000cafe0000cafe0000cafe0000",
   wrap_up:"Recovered from a marker written before the rename.",
   resolutions:[{finding_id:$f, resolution:"fixed", reply:"done", persist:null, duplicate_of:null},
                 {finding_id:$d, resolution:"disputed", reply:"not real", persist:null, duplicate_of:null}]}')"
comments="$( { marker_comment 9001 "$(review_marker)"; marker_comment 9002 "$old_claim"; } | jq -cs . | payload)"
routes_baseline "$comments"
routes_resolve
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "a pre-rename claim still recovers"               "$rc" "0"
has "and its text survives into the summary comment"  "$(calls)" "Recovered from a marker written before the rename."
hasnt "so the marker carries no dead copy of the old field" "$(calls)" "wrap_up"

# --- escalation halts the loop and summons a human ---------------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
escalating="$(jq -cn '
  {blocked:false, blocked_reason:null, summary:"One point needs you.",
   resolutions:[{finding_number:1, resolution:"escalated", reply:"We disagree twice over.", persist:null, duplicate_of:null},
                 {finding_number:2, resolution:"disputed", reply:"Not real here.", persist:null, duplicate_of:null}]}')"
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$escalating" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
out="$("$CROSSREV" resolve --pr 42 2>&1)"

has "an escalated finding applies crossrev/stop"       "$(calls)" "labels[]=crossrev/stop"
has "and halts rather than handing back to the reviewer" "$(calls)" "labels[]=crossrev/halted"
has "and says a human is needed"                      "$out" "need a human decision"
is  "the escalated thread is left open"               "$(count 'resolveReviewThread')" "1"

# --- the seam where model output enters the orchestrator ----------------
#
# On PR 5 the resolver returned three finding ids that were each one or two
# characters off the ones crossrev had handed it. Nothing checked them against the
# set crossrev itself generated, so four things keyed on a string nothing matched:
# the reply went to the bottom of the pull request instead of into the thread, no
# thread resolved, the resolution was written against an id no finding has, and
# the summary table fell back to printing the raw hash. All four degraded and
# none said so, which is worse than failing outright — the next pass reads the
# record.
#
# Findings are numbered in the prompt now, so the harness's own schema
# enforcement rules out the mistyped identifier. What the schema cannot express
# is a per-run range, a duplicate or an omission.

# The numbering has to reach the model, or it has nothing to answer with.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(edit_script)"; export CROSSREV_RESOLVE_EDIT
out="$("$CROSSREV" resolve --pr 42 2>&1)"
prompt="$(cat "$PROMPT_LOG")"

has "each finding is numbered in the prompt"          "$prompt" "### 1. \`$ID_FIX\`"
has "and numbered in the order the marker lists them" "$prompt" "### 2. \`$ID_DEFER\`"
has "the id stays beside the number for quoting"      "$prompt" "$ID_DEFER"
has "and the model is told which one to return"       "$prompt" "\`\"finding_number\": 2\`"

# The round trip: numbers in, ids out. Everything downstream still keys on the
# id, which is why no live marker needs migrating.
has "a numbered resolution reaches the right thread" "$(calls)" "pulls/42/comments/5000/replies"
is  "both threads are settled by number"              "$(count 'resolveReviewThread')" "2"
has "and the marker records the resolution against the finding's id" \
  "$(calls)" "\"id\":\"$ID_FIX\",\"path\":\"app.ts\""
hasnt "the number itself is not stored, only used"    "$(calls)" "finding_number"

# Range. A hash could be mistyped into another plausible-looking hash; a number
# past the end cannot hide, and this is the failure that actually happened.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
out_of_range="$(jq -cn '
  {blocked:false, blocked_reason:null, summary:"s",
   resolutions:[{finding_number:1, resolution:"fixed", reply:"r", persist:null, duplicate_of:null},
                 {finding_number:7, resolution:"fixed", reply:"r", persist:null, duplicate_of:null}]}')"
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$out_of_range" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
err="$("$CROSSREV" resolve --pr 42 2>&1 >/dev/null)"; rc=$?

is  "a finding number nothing was numbered with is fatal" "$rc" "1"
has "and the error names the number and the range"    "$err" "finding number(s) 7 do not exist"
has "it says the shape was right, so this is drift"   "$err" "contradicts what it was given"
is  "nothing was replied to"                          "$(count 'replies')" "0"
is  "and no thread was resolved against a guess"      "$(count 'resolveReviewThread')" "0"

# Drift gets a second attempt; a shape mismatch still does not. The two mean
# opposite things about who is at fault — every shipped harness constrains its
# own output, so a wrong shape is an adapter bug that a retry reproduces, while
# wrong content with a right shape is the model and no adapter is involved.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$out_of_range" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_PAYLOAD_2="$(resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD_2
CROSSREV_STUB_COUNT="$(mktemp)"; export CROSSREV_STUB_COUNT
CROSSREV_RESOLVE_EDIT="$(edit_script)"; export CROSSREV_RESOLVE_EDIT
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "drift is asked once more rather than costing the pass" "$rc" "0"
has "and says so before retrying"                     "$out" "asked once more"
is  "the second answer is the one that is acted on"   "$(count 'resolveReviewThread')" "2"
unset CROSSREV_RESOLVE_PAYLOAD_2 CROSSREV_STUB_COUNT

# The retry starts from the tree the first attempt saw, not from its leftovers.
#
# A resolver edits files before it answers, and a rejected answer is discarded
# while its edits are not. Left in place, the second resolver reads a tree the
# first one already changed — it applies a non-idempotent fix twice, or finds a
# finding already fixed and calls it skipped — and `git add -A` commits whatever
# is sitting there, against resolutions that describe neither attempt.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$out_of_range" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_PAYLOAD_2="$(resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD_2
CROSSREV_STUB_COUNT="$(mktemp)"; export CROSSREV_STUB_COUNT
CROSSREV_RESOLVE_EDIT="$(appending_edit_script)"; export CROSSREV_RESOLVE_EDIT
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "a leg that retried after editing files still exits clean" "$rc" "0"
is  "the discarded attempt's edit is applied once, not twice" \
  "$(git show HEAD:app.ts | grep -c 'const patched')" "1"
is  "and nothing of it is left loose in the tree" \
  "$(git status --porcelain | wc -l | tr -d ' ')" "0"
has "the run says the discarded edits were put back" "$out" "put back"
unset CROSSREV_RESOLVE_PAYLOAD_2 CROSSREV_STUB_COUNT CROSSREV_RESOLVE_EDIT

# And it leaves the staging area as it found it.
#
# The capture is a tree of everything that was in the checkout, staged and
# unstaged alike, so putting it back through the repository's own index would
# stage every unstaged change the run happened to find. crossrev is routinely run
# in a checkout somebody is working in, and a pass that then fixes nothing hands
# that checkout back with a staging area crossrev invented.
both_disputed="$(jq -cn '
  {blocked:false, blocked_reason:null, summary:"Neither holds up in this codebase.",
   resolutions:[{finding_number:1, resolution:"disputed", reply:"Not real here.", persist:null, duplicate_of:null},
                 {finding_number:2, resolution:"disputed", reply:"Not real here either.", persist:null, duplicate_of:null}]}')"

fixture_repo "$(config_with_issue_sink)"; stub_reset
printf 'export const staged = 1\n' >staged.ts; git add staged.ts
printf 'export const loose = 1\n' >>app.ts
before_status="$(git status --porcelain)"
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$out_of_range" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_PAYLOAD_2="$(printf '%s' "$both_disputed" | payload)"; export CROSSREV_RESOLVE_PAYLOAD_2
CROSSREV_STUB_COUNT="$(mktemp)"; export CROSSREV_STUB_COUNT
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "a leg that retried in a checkout someone was working in exits clean" "$rc" "0"
is  "the staged change is still the only staged change" \
  "$(git diff --cached --name-only | tr '\n' ' ')" "staged.ts "
is  "and the unstaged one is still unstaged" \
  "$(git status --porcelain)" "$before_status"
unset CROSSREV_RESOLVE_PAYLOAD_2 CROSSREV_STUB_COUNT

# A second rejected answer ends the leg, and its edits go back too.
#
# The retry restores; the exhausted budget used not to, which left the last
# rejected attempt's edits in the checkout with nothing on the pull request to
# say they were there. The next run captures them as its own baseline and commits
# them under resolutions describing neither attempt — the same divergence, one
# run later.
twice_over="$(jq -cn '
  {blocked:false, blocked_reason:null, summary:"s",
   resolutions:[{finding_number:1, resolution:"fixed", reply:"r", persist:null, duplicate_of:null},
                 {finding_number:1, resolution:"fixed", reply:"r", persist:null, duplicate_of:null}]}')"

fixture_repo "$(config_with_issue_sink)"; stub_reset
printf 'export const staged = 1\n' >staged.ts; git add staged.ts
printf 'export const loose = 1\n' >>app.ts
before_status="$(git status --porcelain)"; before_app="$(cat app.ts)"
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$out_of_range" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_PAYLOAD_2="$(printf '%s' "$twice_over" | payload)"; export CROSSREV_RESOLVE_PAYLOAD_2
CROSSREV_STUB_COUNT="$(mktemp)"; export CROSSREV_STUB_COUNT
CROSSREV_RESOLVE_EDIT="$(appending_edit_script)"; export CROSSREV_RESOLVE_EDIT
err="$("$CROSSREV" resolve --pr 42 2>&1 >/dev/null)"; rc=$?

is  "a second answer that also contradicts the prompt is fatal" "$rc" "1"
has "and the error says both answers were wrong"      "$err" "twice returned an answer that contradicts"
is  "neither rejected attempt's edit is left in the tree" "$(cat app.ts)" "$before_app"
is  "the checkout is exactly as the leg found it"     "$(git status --porcelain)" "$before_status"
is  "and nothing was committed over it"               "$(git log -1 --format=%s)" "feature"
unset CROSSREV_RESOLVE_PAYLOAD_2 CROSSREV_STUB_COUNT CROSSREV_RESOLVE_EDIT

# duplicate_of names an issue the orchestrator retrieved. Inventing one makes
# crossrev comment on an unrelated issue and resolve the thread claiming the
# finding is tracked there.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
route_first 'api -X GET search/issues*' \
  '{"items":[{"number":19,"title":"untyped exports","state":"open","body":"b"}]}'
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload 404 | payload)"; export CROSSREV_RESOLVE_PAYLOAD
err="$("$CROSSREV" resolve --pr 42 2>&1 >/dev/null)"; rc=$?

is  "an issue number nobody offered is fatal"         "$rc" "1"
has "and the error names it"                          "$err" "duplicate_of names issue(s) 404"
is  "so no unrelated issue is commented on"           "$(count 'issues/404')" "0"

# --- a reply that could not be threaded is not silence ------------------
#
# The top-level fallback exists for a real case: an inline comment GitHub refused
# to anchor, so there is no thread to reply to. Losing it would be worse than
# keeping it. What is not acceptable is that it used to change where a reply
# landed without changing anything the run said about it, so a pass that threaded
# none of its replies read exactly like one that threaded all of them.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
route_first 'api --method POST repos/*/pulls/42/comments/*/replies*' '!fail'
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the leg still finishes, because the reply is not lost" "$rc" "0"
has "the run says how many replies missed their thread" "$out" "2 replies could not be threaded"
has "and the pull request records it too, not just the terminal" \
  "$(calls)" "could not be posted in the review threads they answer"

# And it survives a recovery, which is where a count held only in a local goes
# missing. A run that stops between posting the fallback and writing the summary
# comes back with the reply already on the pull request, so the loop skips it as
# a duplicate and never counts it — and a counter starting at zero then reports a
# clean pass over a degraded one, which is the silence the count exists to
# remove. The reply's own marker is on an issue comment rather than a review
# comment, and that is the record recovery reads.
fixture_repo "$(config_with_issue_sink)"; stub_reset
resumed="$( { marker_comment 9001 "$(review_marker)"
              POSTED_LEG=resolve posted_comments "$ID_FIX" | jq -c '.[]'
            } | jq -cs . | payload)"
routes_baseline "$resumed"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "the resumed leg exits clean"                     "$rc" "0"
is  "the reply already on the pull request is not posted twice" \
  "$(count 'pulls/42/comments/5000/replies')" "1"
has "the resumed run still says a reply missed its thread" \
  "$out" "1 reply could not be threaded"
has "and the summary comment still records it" \
  "$(calls)" "One reply could not be posted in the review thread it answers"

# --- a marker written before the numbering still recovers ---------------
#
# The number is a wire format between the orchestrator and the model, and it
# stops existing the moment the payload is validated. So the shape stored on the
# pull request did not change, and the resolve markers already sitting on live
# pull requests stay readable — this is the same recovery branch the wrap_up
# rename had to be careful about, and the reason it needs no migration of its own.
fixture_repo "$(config_with_issue_sink)"; stub_reset
pre_numbering="$(jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(date +%s)" --arg f "$ID_FIX" --arg d "$ID_DEFER" '
  {v:1, leg:"resolve", pass:1, state:"started", ts:$ts, run_id:"1", head_sha:$sha,
   harness:"claude", model:"resolver-model", model_reported:"resolver-model",
   blocked:false, blocked_reason:null, commit_sha:"cafe0000cafe0000cafe0000cafe0000cafe0000",
   summary:"Recorded before findings were numbered.",
   resolutions:[{finding_id:$f, resolution:"fixed", reply:"done", persist:null, duplicate_of:null},
                 {finding_id:$d, resolution:"disputed", reply:"not real", persist:null, duplicate_of:null}]}')"
comments="$( { marker_comment 9001 "$(review_marker)"; marker_comment 9002 "$pre_numbering"; } | jq -cs . | payload)"
routes_baseline "$comments"
routes_resolve
out="$("$CROSSREV" resolve --pr 42 2>&1)"; rc=$?

is  "a claim recorded against ids still recovers"     "$rc" "0"
has "its text survives into the summary comment"      "$(calls)" "Recorded before findings were numbered."
is  "and its resolutions still reach their threads"  "$(count 'resolveReviewThread')" "2"

# --- the divergence guard, layer two ----------------------------------
#
# Two legs configured to differ but answered by the same model is the failure the
# whole cross-model design exists to prevent, and it completes normally unchecked.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
routes_resolve
CROSSREV_RESOLVE_PAYLOAD="$(resolve_payload | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_MODEL=reviewer-model; export CROSSREV_RESOLVE_MODEL
err="$("$CROSSREV" resolve --pr 42 2>&1 >/dev/null)"; rc=$?
is  "the same answering model on both legs halts"     "$rc" "1"
has "and the error names the model that answered both" "$err" "the same model answered each: reviewer-model"
export CROSSREV_RESOLVE_MODEL=resolver-model

finish
