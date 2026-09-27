#!/usr/bin/env bash
#
# Tests for the stateful GitHub stand-in (CROSSREV_GH_STATE).
#
# Exercises the review-shaped sequence against the gh stub:
#   1. Metadata read (user login, pull request metadata, initial labels)
#   2. Claim create (issue comment create, single read, paginated list, edit)
#   3. Finding posts (pull-request review comment create, reply in thread, list, GraphQL query)
#   4. Label moves (label ensure with colour, PR label add and remove, pr view sync)
#   5. Thread resolve (GraphQL resolveReviewThread mutation, verified via threads query)
#   6. Hyphenated state directory (numeric id order whatever the path holds)
#   7. Route matching precedence (CROSSREV_GH_ROUTES matches before state)

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STUB_DIR="$(cd "$HERE/../stub" && pwd)"
export PATH="$STUB_DIR:$PATH"

pass=0
fail=0

ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" || notok "$1" "contains '$3'" "$2"; }
hasnt() { [[ "$2" != *"$3"* ]] && ok "$1" || notok "$1" "does not contain '$3'" "$2"; }

STATE_DIR="$(mktemp -d)"
# shellcheck disable=SC2064
trap "rm -rf '$STATE_DIR'" EXIT

export CROSSREV_GH_STATE="$STATE_DIR"
export CROSSREV_GH_LOG="$STATE_DIR/gh.log"
unset CROSSREV_GH_ROUTES

printf '\n=== Case 1: unseeded state starts empty ===\n'
empty_user="$(gh api user --jq .login 2>/dev/null || true)"
is "unseeded user defaults to tester or empty" "${empty_user:-tester}" "tester"

empty_labels="$(gh api repos/acme/widget/issues/42/labels 2>/dev/null || true)"
is "unseeded PR labels is empty array" "$empty_labels" "[]"

empty_issue_comments="$(gh api --paginate repos/acme/widget/issues/42/comments 2>/dev/null || true)"
is "unseeded issue comments is empty array" "$empty_issue_comments" "[]"

empty_pr_comments="$(gh api --paginate repos/acme/widget/pulls/42/comments 2>/dev/null || true)"
is "unseeded PR review comments is empty array" "$empty_pr_comments" "[]"

printf '\n=== Case 2: seeded review-shaped sequence ===\n'
cp "$HERE/user.json" "$STATE_DIR/user.json"
cp "$HERE/pr.json" "$STATE_DIR/pr.json"
cp "$HERE/labels.json" "$STATE_DIR/labels.json"
cp "$HERE/comments.json" "$STATE_DIR/issue-comments.json"
cp "$HERE/threads.json" "$STATE_DIR/threads.json"

# 1. Metadata read
login="$(gh api user --jq .login)"
is "metadata read replays seeded user login" "$login" "eval-reviewer"

pr_json="$(gh pr view 42 --repo acme/widget --json number,title,headRefOid,state)"
is "metadata read replays seeded PR number" "$(jq -r .number <<<"$pr_json")" "42"
is "metadata read replays seeded PR title" "$(jq -r .title <<<"$pr_json")" "Add refresh helper"
is "metadata read replays seeded PR headRefOid" "$(jq -r .headRefOid <<<"$pr_json")" "1111111111111111111111111111111111111111"

initial_pr_labels="$(gh api repos/acme/widget/issues/42/labels --jq '.[].name')"
has "metadata read replays seeded PR labels" "$initial_pr_labels" "enhancement"

repo_label_color="$(gh api repos/acme/widget/labels/enhancement --jq .color)"
is "metadata read replays seeded repo label colour" "$repo_label_color" "a2eeef"

# 2. Claim create
claim_id="$(gh api --method POST repos/acme/widget/issues/42/comments -f body='claim <!-- crossrev: {"pass":1,"leg":"review"} -->' --jq .id)"
has "claim create returns an allocated id" "$claim_id" "90"

claim_body="$(gh api repos/acme/widget/issues/comments/"$claim_id" --jq .body)"
has "single comment read replays claim body" "$claim_body" 'claim <!-- crossrev:'

claim_login="$(gh api repos/acme/widget/issues/comments/"$claim_id" --jq .user.login)"
is "single comment read attributes the claim to the seeded user" "$claim_login" "eval-reviewer"

issue_comments_list="$(gh api --paginate repos/acme/widget/issues/42/comments --jq '.[].body')"
has "issue comments list includes seeded comment" "$issue_comments_list" "Initial discussion on the PR"
has "issue comments list includes new claim comment" "$issue_comments_list" 'claim <!-- crossrev:'

multiline_body="$(gh api --paginate repos/acme/widget/issues/42/comments --jq '.[] | select(.id == 1002) | .body')"
is "seeded multiline body replays the newline" "$multiline_body" "$(printf 'Seeded line one\nSeeded line two')"

listed_claim_login="$(gh api --paginate repos/acme/widget/issues/42/comments --jq '.[] | select(.body | contains("claim <!-- crossrev:")) | .user.login')"
is "listed claim login equals the seeded user" "$listed_claim_login" "eval-reviewer"

seeded_login="$(gh api --paginate repos/acme/widget/issues/42/comments --jq '.[] | select(.id == 1001) | .user.login')"
is "listed seeded comment keeps its seed login" "$seeded_login" "author"

other_id="$(gh api --method POST repos/other/repo/issues/7/comments -f body='unrelated note' --jq .id)"
other_url="$(gh api --paginate repos/other/repo/issues/7/comments --jq '.[] | select(.id == '"$other_id"') | .issue_url')"
is "created comment replays its own issue_url" "$other_url" "https://api.github.com/repos/other/repo/issues/7"

claim_url="$(gh api --paginate repos/acme/widget/issues/42/comments --jq '.[] | select(.id == '"$claim_id"') | .issue_url')"
is "claim keeps its own issue_url" "$claim_url" "https://api.github.com/repos/acme/widget/issues/42"

wide_url="$(gh api --method GET repos/acme/widget/issues/comments --jq '.[] | select(.id == '"$other_id"') | .issue_url')"
is "repository-wide list replays the stored issue_url" "$wide_url" "https://api.github.com/repos/other/repo/issues/7"

issue42_ids="$(gh api --paginate repos/acme/widget/issues/42/comments --jq '.[].id')"
hasnt "issue 7 comment is absent from issue 42 list" "$issue42_ids" "$other_id"

issue7_ids="$(gh api --paginate repos/other/repo/issues/7/comments --jq '.[].id')"
hasnt "seed 1001 is absent from issue 7 list" "$issue7_ids" "1001"

seed_url="$(gh api --paginate repos/acme/widget/issues/42/comments --jq '.[] | select(.id == 1001) | .issue_url')"
seed_wide_url="$(gh api --method GET repos/acme/widget/issues/comments --jq '.[] | select(.id == 1001) | .issue_url')"
is "seed 1001 issue_url stays the same on both lists" "$seed_url" "$seed_wide_url"

page1_ids="$(gh api --method GET repos/acme/widget/issues/comments -F per_page=2 -F page=1 --jq '.[].id')"
is "repo-wide comments page 1 respects per_page" "$page1_ids" "$(printf '1001\n1002')"

page2_ids="$(gh api --method GET repos/acme/widget/issues/comments -F per_page=2 -F page=2 --jq '.[].id')"
is "repo-wide comments page 2 returns next slice" "$page2_ids" "$(printf '%s\n%s' "$claim_id" "$other_id")"

page3_ids="$(gh api --method GET repos/acme/widget/issues/comments -F per_page=2 -F page=3 --jq '.[].id')"
is "repo-wide comments past end returns empty array" "$page3_ids" ""

default_page2="$(gh api --method GET repos/acme/widget/issues/comments -F page=2 --jq '.[].id')"
is "repo-wide comments page 2 with default per_page is empty" "$default_page2" ""

gh api --method PATCH repos/acme/widget/issues/comments/"$claim_id" -f body='claim updated <!-- crossrev: {"pass":1,"leg":"review"} -->' >/dev/null
updated_body="$(gh api repos/acme/widget/issues/comments/"$claim_id" --jq .body)"
has "single comment read replays updated claim body" "$updated_body" "claim updated"

# 3. Finding posts
finding_id="$(gh api --method POST repos/acme/widget/pulls/42/comments \
  -f 'body=finding 1 <!-- crossrev:f {"id":"0123456789abcdef","pass":1,"leg":"review"} -->' \
  -f commit_id=1111111111111111111111111111111111111111 \
  -f path=app.ts \
  -F line=40 \
  -f side=RIGHT \
  --jq .id)"
has "finding post returns comment id" "$finding_id" "90"

reply_id="$(gh api --method POST repos/acme/widget/pulls/42/comments/"$finding_id"/replies \
  -f 'body=reply explaining fix' \
  --jq .id)"
has "reply post returns comment id" "$reply_id" "90"

pr_comments_list="$(gh api --paginate repos/acme/widget/pulls/42/comments --jq '.[].body')"
has "PR review comments list contains finding 1" "$pr_comments_list" "finding 1"
has "PR review comments list contains reply" "$pr_comments_list" "reply explaining fix"

finding_login="$(gh api --paginate repos/acme/widget/pulls/42/comments --jq '.[] | select(.id == '"$finding_id"') | .user.login')"
is "listed finding login equals the seeded user" "$finding_login" "eval-reviewer"

pr7_comments_list="$(gh api --paginate repos/acme/widget/pulls/7/comments --jq '.[].body' 2>/dev/null || true)"
hasnt "comment posted on pulls/42 is absent from pulls/7" "$pr7_comments_list" "finding 1"

threads_query='query($owner:String!,$name:String!,$number:Int!) {
  repository(owner:$owner,name:$name) {
    pullRequest(number:$number) {
      reviewThreads(first:100) {
        nodes {
          id isResolved isOutdated path line
          comments(first:30) { nodes { databaseId body author { login } } }
        }
      }
    }
  }
}'

threads_json="$(gh api graphql -F owner=acme -F name=widget -F number=42 -f query="$threads_query")"
has "graphql threads contains seeded thread" "$threads_json" "Existing review thread comment"
has "graphql threads contains finding 1" "$threads_json" "finding 1"
has "graphql threads contains reply" "$threads_json" "reply explaining fix"

new_thread_id="$(jq -r '.data.repository.pullRequest.reviewThreads.nodes[] | select(.comments.nodes[].databaseId == '"$finding_id"') | .id' <<<"$threads_json")"
is_resolved="$(jq -r '.data.repository.pullRequest.reviewThreads.nodes[] | select(.id == "'"$new_thread_id"'") | .isResolved' <<<"$threads_json")"
is "new review thread starts unresolved" "$is_resolved" "false"

# 4. Label moves
missing_rc=0
gh api repos/acme/widget/labels/crossrev%2Fawaiting-review >/dev/null 2>&1 || missing_rc=$?
is "querying uncreated label exits 1" "$missing_rc" "1"

gh api --method POST repos/acme/widget/labels -f name=crossrev/awaiting-review -f color=d4c5f9 -f description='Awaiting review' >/dev/null
gh api --method PATCH repos/acme/widget/labels/crossrev%2Fawaiting-review -f color=0075ca >/dev/null
recoloured="$(gh api repos/acme/widget/labels/crossrev%2Fawaiting-review --jq .color)"
is "label recolour replays new colour" "$recoloured" "0075ca"

gh api --method POST repos/acme/widget/issues/42/labels -f labels[]=crossrev/awaiting-review >/dev/null
gh api --method POST repos/acme/widget/issues/42/labels -f labels[]=crossrev/pass-1 >/dev/null

current_labels="$(gh api repos/acme/widget/issues/42/labels --jq '.[].name')"
has "PR labels include awaiting-review" "$current_labels" "crossrev/awaiting-review"
has "PR labels include pass-1" "$current_labels" "crossrev/pass-1"
has "PR labels still include seeded enhancement" "$current_labels" "enhancement"

gh api --method DELETE repos/acme/widget/issues/42/labels/crossrev%2Fpass-1 >/dev/null
after_remove="$(gh api repos/acme/widget/issues/42/labels --jq '.[].name')"
has "PR labels keep awaiting-review after removal" "$after_remove" "crossrev/awaiting-review"
hasnt "PR labels no longer include pass-1" "$after_remove" "crossrev/pass-1"

pr_view_labels="$(gh pr view 42 --repo acme/widget --json labels --jq '.labels[].name')"
has "pr view reflects added label" "$pr_view_labels" "crossrev/awaiting-review"
hasnt "pr view reflects removed label" "$pr_view_labels" "crossrev/pass-1"

# 5. Thread resolve
resolve_query='mutation($threadId:ID!) {
  resolveReviewThread(input:{threadId:$threadId}) { thread { isResolved } }
}'

resolve_res="$(gh api graphql -f threadId="$new_thread_id" -f query="$resolve_query")"
is "resolveReviewThread mutation returns isResolved true" "$(jq -r .data.resolveReviewThread.thread.isResolved <<<"$resolve_res")" "true"

updated_threads="$(gh api graphql -F owner=acme -F name=widget -F number=42 -f query="$threads_query")"
resolved_state="$(jq -r '.data.repository.pullRequest.reviewThreads.nodes[] | select(.id == "'"$new_thread_id"'") | .isResolved' <<<"$updated_threads")"
is "review threads query replays resolved state as true" "$resolved_state" "true"

# 6. Hyphenated state directory ordering
hyphen_dir="$STATE_DIR/case-dir"
mkdir -p "$hyphen_dir"
cat >"$hyphen_dir/issue-comments.json" <<'EOF'
[
  {"id": 100, "body": "three-digit seed", "user": {"login": "author"}},
  {"id": 9, "body": "one-digit seed", "user": {"login": "author"}}
]
EOF
export CROSSREV_GH_STATE="$hyphen_dir"
hyphen_order="$(gh api --paginate repos/acme/widget/issues/42/comments --jq '.[].id')"
is "hyphenated state dir lists comments in numeric id order" "$hyphen_order" "$(printf '9\n100')"
export CROSSREV_GH_STATE="$STATE_DIR"

# 7. Route matching precedence
routes_file="$STATE_DIR/routes"
printf 'api user*\t{"login":"routed-user"}\n' >"$routes_file"
export CROSSREV_GH_ROUTES="$routes_file"
routed_login="$(gh api user --jq .login)"
is "explicit route table entry overrides state" "$routed_login" "routed-user"

printf '\n  %d passed, %d failed\n\n' "$pass" "$fail"
(( fail == 0 ))
