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
#   8. Review thread isolation (a number=7 query excludes pull 42 threads)
#   9. PR label isolation (list, add, remove and pr view per owner, repo, number)
#   10. Repo label isolation (single read, list, create, recolour per repo)
#   11. Issue comment isolation (single read, edit, repo-wide list per repo)
#   12. PR metadata isolation (pr view answers its own number and repo only)
#   13. Git objects (refs per repo; blobs, trees and commits content-addressed)
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

wide_url="$(gh api --method GET repos/other/repo/issues/comments --jq '.[] | select(.id == '"$other_id"') | .issue_url')"
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
is "repo-wide comments page 2 returns next slice" "$page2_ids" "$claim_id"

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

# 8. Second-PR isolation for review threads
threads7="$(gh api graphql -F owner=acme -F name=widget -F number=7 -f query="$threads_query")"
hasnt "threads query for pull 7 excludes the pull 42 finding" "$threads7" "finding 1"
hasnt "threads query for pull 7 excludes the seeded pull 42 thread" "$threads7" "Existing review thread comment"
hasnt "threads query for pull 7 excludes the pull 42 reply" "$threads7" "reply explaining fix"
has "threads query for pull 42 still lists the finding" "$threads_json" "finding 1"

# 9. Second-PR isolation for PR labels
gh api --method POST repos/other/repo/issues/7/labels -f labels[]=crossrev/other-pr >/dev/null
labels42_after="$(gh api repos/acme/widget/issues/42/labels --jq '.[].name')"
hasnt "issue 42 labels exclude the other issue's label" "$labels42_after" "crossrev/other-pr"
labels7="$(gh api repos/other/repo/issues/7/labels --jq '.[].name')"
has "issue 7 labels keep their own label" "$labels7" "crossrev/other-pr"
hasnt "issue 7 labels exclude issue 42's awaiting-review" "$labels7" "crossrev/awaiting-review"
hasnt "issue 7 labels exclude the seeded enhancement" "$labels7" "enhancement"
gh api --method POST repos/acme/widget/issues/42/labels -f labels[]=crossrev/scratch >/dev/null
gh api --method DELETE repos/acme/widget/issues/42/labels/crossrev%2Fscratch >/dev/null
labels7_kept="$(gh api repos/other/repo/issues/7/labels --jq '.[].name')"
has "removing a label on 42 keeps issue 7's labels" "$labels7_kept" "crossrev/other-pr"
prview7_labels="$(gh pr view 7 --repo acme/widget --json labels --jq '.labels[].name' 2>/dev/null || true)"
hasnt "pr view 7 excludes issue 42's labels" "$prview7_labels" "crossrev/awaiting-review"
prview42_labels="$(gh pr view 42 --repo acme/widget --json labels --jq '.labels[].name')"
has "pr view 42 keeps its own labels" "$prview42_labels" "crossrev/awaiting-review"

# 10. Second-repo isolation for repo labels
other_single_rc=0
gh api repos/other/repo/labels/crossrev%2Fawaiting-review >/dev/null 2>&1 || other_single_rc=$?
is "single label read on another repo exits 1" "$other_single_rc" "1"
other_repo_labels="$(gh api repos/other/repo/labels --jq '.[].name' 2>/dev/null || true)"
hasnt "other repo label list excludes this repo's created label" "$other_repo_labels" "crossrev/awaiting-review"
gh api --method POST repos/other/repo/labels -f name=other-only -f color=ffffff >/dev/null
acme_repo_labels="$(gh api repos/acme/widget/labels --jq '.[].name')"
hasnt "this repo label list excludes the other repo's label" "$acme_repo_labels" "other-only"
gh api --method PATCH repos/other/repo/labels/bug -f color=000000 >/dev/null
bug_color="$(gh api repos/acme/widget/labels/bug --jq .color)"
is "recolour on another repo keeps this repo's colour" "$bug_color" "d73a4a"

# 11. Second-PR isolation for issue comments
other_read_rc=0
gh api repos/other/repo/issues/comments/"$claim_id" >/dev/null 2>&1 || other_read_rc=$?
is "single comment read on another repo exits 1" "$other_read_rc" "1"
gh api --method PATCH repos/other/repo/issues/comments/"$other_id" -f body='unrelated note edited' >/dev/null
issue42_bodies="$(gh api --paginate repos/acme/widget/issues/42/comments --jq '.[].body')"
hasnt "edited other-issue body is absent from issue 42 list" "$issue42_bodies" "unrelated note edited"
issue7_bodies="$(gh api --paginate repos/other/repo/issues/7/comments --jq '.[].body')"
has "edited body replays on its own issue" "$issue7_bodies" "unrelated note edited"
wide_acme="$(gh api --method GET repos/acme/widget/issues/comments --jq '.[].id')"
hasnt "repo-wide list for acme/widget excludes the other repo comment" "$wide_acme" "$other_id"
wide_other="$(gh api --method GET repos/other/repo/issues/comments --jq '.[].id')"
has "repo-wide list for other/repo includes its own comment" "$wide_other" "$other_id"
hasnt "repo-wide list for other/repo excludes acme/widget seeds" "$wide_other" "1001"

# 12. Second-PR isolation for PR metadata
pr7_title="$(gh pr view 7 --repo acme/widget --json number,title --jq .title)"
is "pr view 7 does not replay the seeded title" "$pr7_title" "Pull Request"
pr42_other_repo="$(gh pr view 42 --repo other/repo --json number,title --jq .title)"
is "pr view 42 on another repo does not replay the seed" "$pr42_other_repo" "Pull Request"
pr42_same_repo="$(gh pr view 42 --repo acme/widget --json number,title --jq .title)"
is "pr view 42 on its repo still replays the seed" "$pr42_same_repo" "Add refresh helper"

# 13. Git objects: refs are per-repo, objects are content-addressed
printf '{"ref":"refs/heads/feature-x","sha":"1111111111111111111111111111111111111111"}' \
  | gh api --method POST repos/acme/widget/git/refs --input - >/dev/null
other_ref_rc=0
gh api repos/other/repo/git/refs/heads/feature-x >/dev/null 2>&1 || other_ref_rc=$?
is "ref read on another repo exits 1" "$other_ref_rc" "1"
own_ref_sha="$(gh api repos/acme/widget/git/refs/heads/feature-x --jq .object.sha)"
is "ref read on its repo replays the sha" "$own_ref_sha" "1111111111111111111111111111111111111111"
printf '{"ref":"refs/heads/feature-x","sha":"2222222222222222222222222222222222222222","force":true}' \
  | gh api --method PATCH repos/other/repo/git/refs/heads/feature-x --input - >/dev/null
own_ref_kept="$(gh api repos/acme/widget/git/refs/heads/feature-x --jq .object.sha)"
is "patching the ref on another repo keeps this repo's sha" "$own_ref_kept" "1111111111111111111111111111111111111111"
blob_sha="$(printf '{"content":"hello-blob"}' | gh api --method POST repos/acme/widget/git/blobs --input - --jq .sha)"
blob_cross="$(gh api repos/other/repo/git/blobs/"$blob_sha" --jq .content)"
is "blob content is addressable from another repo path by design" "$blob_cross" "hello-blob"
tree_sha="$(printf '{"base_tree":"x","tree":[]}' | gh api --method POST repos/acme/widget/git/trees --input - --jq .sha)"
tree_cross="$(gh api repos/other/repo/git/trees/"$tree_sha" --jq '.tree | length')"
is "tree object is addressable from another repo path by design" "$tree_cross" "0"
commit_sha="$(printf '{"message":"gen 1","tree":"abc"}' | gh api --method POST repos/acme/widget/git/commits --input - --jq .sha)"
commit_cross="$(gh api repos/other/repo/git/commits/"$commit_sha" --jq .message)"
is "commit object is addressable from another repo path by design" "$commit_cross" "gen 1"

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
