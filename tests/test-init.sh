#!/usr/bin/env bash
#
# `revloop init` — the plan gate, the label inventory, and what it refuses to
# claim it did.
#
# This is the most consequential command revloop has: it registers a GitHub
# identity, writes organisation secrets, and adds files to a repository. Two
# things are therefore tested harder than the rest. A count that disagrees with
# its own list is the kind of lie a reader catches and a test usually does not.
# And a run that installs workflows while a required secret is missing must say so
# rather than printing a tick.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

config_with_issue_sink() {
  cat <<'EOF'
version: 1
mode: event-driven
max_passes: 3
reviewer:
  harness: claude
  model: reviewer-model
resolver:
  harness: claude
  model: resolver-model
sinks:
  issues:
    type: github_issue
    identity_label: revloop-review
    labels: [bug]
    create_labels: true
persist:
  defects: issues
  escalated: none
EOF
}

routes_init() {
  route 'api users/*'                      '{"type":"User"}'
  route 'api repos/*/labels/revloop/stop'  '{"name":"revloop/stop"}'
  route 'api repos/*/labels/bug'           '{"name":"bug"}'
  route 'api repos/*/labels/*'             '!fail'
  route 'api repos/*/branches/main/protection' '!fail'
  route 'secret list*'                     ''
  route 'repo view --json nameWithOwner*'  "{\"nameWithOwner\":\"$FIX_REPO\"}"
  route 'repo view * --json defaultBranchRef*' '{"defaultBranchRef":{"name":"main"}}'
}

# --- the plan ------------------------------------------------------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_init
out="$("$REVLOOP" init --dry-run 2>&1)"; rc=$?

is  "--dry-run exits clean"                    "$rc" "0"
has "and says plainly that it changed nothing"  "$out" "--dry-run prints the plan and stops"
is  "it writes no workflow files"               "$(ls .github/workflows 2>/dev/null | wc -l | tr -d ' ')" "0"
is  "and it creates no labels"                  "$(count 'method POST')" "0"

# The count and the list have to agree: max_passes 3 means three pass labels plus
# the five fixed ones.
has "the plan states the loop's label count"    "$out" "labels            8 for the loop"
is  "and the list under it is exactly that long" \
  "$(grep -cE '^\S*│  +(create|exists)  revloop/' <<<"$out")" "8"
has "a label that already exists is named as existing, not claimed as created" \
  "$out" "exists  revloop/stop"
has "and the missing ones are marked for creation" "$out" "create  revloop/pass-1"

# The issue sink's labels are a different set with a different owner, so they are
# counted separately.
has "the sink's labels are counted on their own"   "$out" "2 for filed issues"
has "and one that already exists is reported so"   "$out" "exists  bug"

has "the plan says where deferred work will go"    "$out" "deferred work     issues"
has "and where that answer came from"              "$out" "named in the repository config as 'issues'"
has "the plan names every file it would write"     "$out" ".github/workflows/revloop-watchdog.yml"
# The fixture already carries a policy file, so the plan must say it would be
# replaced rather than quietly replacing it.
has "and flags the file it would overwrite"        "$out" "overwrites        .github/revloop.yml"

# A tag only looks immutable, so the pin is the 40-character SHA.
has "the source pin is a commit, with the tag as a comment" "$out" "the SHA is the pin, the tag is a comment"
has "an unprotected default branch is a warning with its consequence" \
  "$out" "would be the only thing stopping a bad push"
has "the App is named as missing rather than assumed" "$out" "run \`revloop auth login\` first"

# --- execution -----------------------------------------------------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_init
out="$("$REVLOOP" init --yes 2>&1)"; rc=$?

is  "init exits clean even with secrets outstanding" "$rc" "0"
is  "it writes all three workflows"             "$(ls .github/workflows | wc -l | tr -d ' ')" "3"
has "it creates the labels the loop needs"      "$(calls)" "name=revloop/awaiting-resolution"
is  "and only the ones that were missing"       "$(count 'method POST repos/acme/widget/labels')" "8"

# Refusing to finish quietly. A missing source key fails at checkout before any
# review runs, which is the good kind of failure — but only if someone knows.
has "a missing source key is named, not glossed"    "$out" "REVLOOP_SOURCE_KEY"
has "and it explains why neither existing token can read the source" \
  "$out" "cannot read"
has "and gives the exact commands to fix it"        "$out" "gh repo deploy-key add"
has "the closing line does not claim success"       "$out" "will fail at the first missing secret"

# The generated workflows carry the two things the chain depends on.
# Comments are stripped first: the templates discuss cancel-in-progress and
# pull_request_target precisely to explain why neither is used, and an assertion
# that cannot tell a warning from a usage is not an assertion.
wf="$(grep -v '^[[:space:]]*#' .github/workflows/revloop-review.yml)"
has "both workflows share one concurrency group per pull request" "$wf" "group: revloop-pr-"
has "and queue rather than evict, which is not the default"       "$wf" "queue: max"
hasnt "cancel-in-progress appears nowhere, since it is unsafe here" "$wf" "cancel-in-progress"
has "every write uses the App token"                              "$wf" "steps.app.outputs.token"
hasnt "and GITHUB_TOKEN is never used for a write"                "$wf" "secrets.GITHUB_TOKEN"
hasnt "pull_request_target appears nowhere"                       "$wf" "pull_request_target"
is  "the source checkout is pinned to a 40-character SHA" \
  "$(grep -oE 'ref: [0-9a-f]{40}' <<<"$wf" | wc -l | tr -d ' ')" "1"
has "the resolve workflow shares the review workflow's group" \
  "$(cat .github/workflows/revloop-resolve.yml)" "group: revloop-pr-\${{ github.event.pull_request.number }}"

# The generated config states where deferred work goes, so `auto` is a bootstrap
# convenience rather than a runtime mode.
has "the generated config names the resolved sink" "$(cat .github/revloop.yml)" "defects: issues"

# --- --upgrade regenerates workflows and leaves policy alone -----------
printf 'version: 1\nmax_passes: 7\n' >.github/revloop.yml
printf 'stale\n' >.github/workflows/revloop-review.yml
stub_reset; routes_init
out="$("$REVLOOP" init --upgrade --yes 2>&1)"
has "--upgrade rewrites a stale workflow"       "$(cat .github/workflows/revloop-review.yml)" "name: revloop review"
has "and says it left the policy file alone"    "$out" "regenerates workflows, not policy"
is  "so the hand-edited policy survives"        "$(grep -c 'max_passes: 7' .github/revloop.yml)" "1"
has "and it flags the files it would overwrite" "$out" "overwrites        .github/workflows/revloop-review.yml"

# --- create_labels: false refuses rather than inventing ----------------
#
# A repository that governs its own label set is one where inventing labels is
# worse than stopping.
strict="$(config_with_issue_sink | sed 's/    create_labels: true/    create_labels: false/; s/    labels: \[bug\]/    labels: [needs-triage]/')"
fixture_repo "$strict"; stub_reset
routes_init
err="$("$REVLOOP" init --yes 2>&1 >/dev/null)"; rc=$?
is  "init stops when a sink label is missing"   "$rc" "1"
has "and names the label it will not invent"    "$err" "needs-triage"
has "and says what would otherwise fail later"  "$err" "filing would die after the review had already posted"

finish
