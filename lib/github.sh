# shellcheck shell=bash
# lib/github.sh — every GitHub read and write revloop makes.
#
# The agent process holds no GitHub credential. Every inline comment, reply,
# resolution, label, issue, commit and push is made here, from the intent a
# skill emitted. That is the structural guard standing behind the quarantine and
# the untrusted-input rules, and it holds even when both of those fail: an
# injection that reaches tool use still cannot post as the App, push a commit, or
# read a secret.
#
# Keeping the whole boundary in one file is also what makes it stubbable. The
# test suite puts a fake `gh` earlier on PATH and asserts on the calls, so every
# decision in here is exercised offline against fixtures.

# ---------------------------------------------------------------------------
# Reads
# ---------------------------------------------------------------------------

gh_repo_slug() {
  gh repo view --json nameWithOwner --jq .nameWithOwner 2>/dev/null || ui_die \
    "could not work out which repository this is" \
    "Run revloop from a checkout with a GitHub remote, or pass --repo owner/name."
}

# Everything about the pull request the legs need, in one call.
gh_pr_json() {
  local repo="$1" pr="$2" out
  out="$(gh pr view "$pr" --repo "$repo" --json \
    number,title,body,url,headRefName,headRefOid,baseRefName,baseRefOid,changedFiles,labels,isCrossRepository,isDraft,headRepositoryOwner,headRepository,state 2>/dev/null)" \
    || ui_die "could not read $repo#$pr" \
       "Check the number, and that \`gh auth status\` passes for that repository."
  printf '%s' "$out"
}

gh_default_branch() {
  gh repo view "$1" --json defaultBranchRef --jq .defaultBranchRef.name 2>/dev/null || printf 'main'
}

# Whether a workflow run is still going: `queued`, `in_progress`, `completed`,
# or one of the newer waiting states.
#
# This is how an automated leg's liveness is knowable from any machine — the
# marker carries GITHUB_RUN_ID and this turns it into an answer. Prints nothing
# when there is no answer to be had: a run in another repository, a token
# without `actions: read`, or no network. Every caller has to treat an empty
# result as "unknown" rather than as "finished", because a status that says a
# leg died because the API was unreachable is the failure this whole read exists
# to remove.
gh_workflow_run_status() {
  local repo="$1" run_id="$2"
  [[ "$run_id" =~ ^[0-9]+$ ]] || return 0
  gh run view "$run_id" --repo "$repo" --json status --jq '.status // empty' 2>/dev/null || true
}

# One page of repository-wide issue comments updated since an epoch timestamp.
# Pull-request conversation comments are issue comments in GitHub's REST API;
# each response carries the issue_url that identifies the pull request.
gh_repo_issue_comments_page() {
  local repo="$1" cutoff="$2" page="$3" since
  if since="$(date -u -r "$cutoff" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null)"; then :
  else since="$(date -u -d "@$cutoff" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null)" || return 1
  fi
  gh api --method GET "repos/$repo/issues/comments" \
    -f since="$since" -F per_page=100 -F page="$page" 2>/dev/null
}

# The diff under review, with paths matching an exclude pattern dropped.
#
# The exclusion is not cosmetic: a repository backlog means the resolver commits its own
# bookkeeping into the PR branch, and the next pass would then review revloop's
# notes about the last pass.
#
# Pinned to the two revisions the leg already loaded rather than asked for by
# pull request number, because `repos/{repo}/pulls/{n}` returns whatever the
# diff is at the moment of the call. The line numbers in it are what a finding
# anchors to and what the orchestrator checks that anchor against, while the
# comment is posted against `head_sha` read earlier in the same leg — so two
# reads of a moving target would let a push between them validate lines from one
# revision and post them against another. One read of the pair, from `gh pr
# view`, cannot disagree with itself.
#
# `base...head` is the three-dot comparison, so it diffs from the merge base
# exactly as the pull request view does. Verified against pull request 14: the
# two endpoints returned byte-identical output.
gh_pr_diff() {
  local repo="$1" pr="$2" base="$3" head="$4" exclude_re="${5:-}" diff
  diff="$(gh api -H "Accept: application/vnd.github.diff" \
            "repos/$repo/compare/$base...$head" 2>/dev/null)" \
    || ui_die "could not fetch the diff for $repo#$pr at $head" \
       "The review leg has nothing to reason about without it. Check network access and \`gh auth status\`."
  if [[ -z "$exclude_re" ]]; then printf '%s' "$diff"; return 0; fi
  awk -v ex="$exclude_re" '
    /^diff --git / { p = $3; sub(/^a\//, "", p); keep = (p ~ ex) ? 0 : 1 }
    keep' <<<"$diff"
}

# Review threads, with each thread's revloop finding ids read out of its comment
# bodies. Thread identity comes from GraphQL because the REST review-comment API
# does not expose the node id the resolve mutation needs.
gh_review_threads() {
  local repo="$1" pr="$2"
  local owner="${repo%%/*}" name="${repo##*/}"
  gh api graphql -F owner="$owner" -F name="$name" -F number="$pr" -f query='
    query($owner:String!,$name:String!,$number:Int!) {
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
    }' 2>/dev/null \
  | jq -c '[ .data.repository.pullRequest.reviewThreads.nodes[]
             | { id, isResolved, isOutdated, path, line,
                 root_comment_id: (.comments.nodes[0].databaseId // null),
                 finding_ids: [ .comments.nodes[].body // ""
                                | scan("<!-- revloop:f (\\{[^}]*\\}) -->") | .[0]
                                | fromjson | .id ],
                 comments: [ .comments.nodes[] | {author: .author.login, body} ] } ]' \
  || printf '[]'
}

# ---------------------------------------------------------------------------
# Writes — comments
# ---------------------------------------------------------------------------

# Post an overall comment. Prints the new comment's id, because every later
# write to it is an edit of that id.
gh_comment_create() {
  local repo="$1" pr="$2" body="$3" id
  id="$(gh api --method POST "repos/$repo/issues/$pr/comments" \
        -f body="$body" --jq .id 2>/dev/null)" \
    || ui_die "could not post a comment on $repo#$pr" \
       "Every pass records itself in a comment, so revloop stops rather than working without a record. Check the token has pull-requests write."
  printf '%s' "$id"
}

gh_comment_edit() {
  local repo="$1" id="$2" body="$3"
  gh api --method PATCH "repos/$repo/issues/comments/$id" -f body="$body" >/dev/null 2>&1 \
    || ui_die "could not update comment $id on $repo" \
       "The pass marker lives in that comment, so leaving it stale would misreport what happened. Retry, or check the token's permissions."
}

# An inline comment anchored to a line and a side of the diff.
#
# GitHub rejects a comment whose line is not part of the diff — the commonest
# cause being a finding on a deleted line sent as RIGHT. Losing the finding
# would be worse than moving it, so this falls back to a top-level comment that
# names the location, and says it did.
gh_review_comment_create() {
  local repo="$1" pr="$2" commit="$3" path="$4" line="$5" side="$6" body="$7"
  if gh api --method POST "repos/$repo/pulls/$pr/comments" \
       -f body="$body" -f commit_id="$commit" -f path="$path" \
       -F line="$line" -f side="$side" >/dev/null 2>&1; then
    printf 'inline'
    return 0
  fi
  ui_warn "GitHub would not anchor a comment to $path:$line ($side) on $repo#$pr" \
    "The finding is posted as a top-level comment naming that location instead, so it is not lost. A finding on a deleted line needs side LEFT."
  gh_comment_create "$repo" "$pr" "**$path:$line** ($side)

$body" >/dev/null
  printf 'fallback'
}

# Reply inside an existing review thread, addressed by the thread's first
# comment. Replying at top level instead is what makes a PR unreadable.
gh_review_reply() {
  local repo="$1" pr="$2" root_comment_id="$3" body="$4"
  gh api --method POST "repos/$repo/pulls/$pr/comments/$root_comment_id/replies" \
    -f body="$body" >/dev/null 2>&1 && return 0
  ui_warn "could not reply in the thread rooted at comment $root_comment_id on $repo#$pr" \
    "The disposition is still recorded in the pass marker, but the collaborator reading the thread will not see the reason. Check the token has pull-requests write."
  return 1
}

gh_thread_resolve() {
  local thread_id="$1"
  gh api graphql -f threadId="$thread_id" -f query='
    mutation($threadId:ID!) {
      resolveReviewThread(input:{threadId:$threadId}) { thread { isResolved } }
    }' >/dev/null 2>&1 && return 0
  ui_warn "could not resolve review thread $thread_id" \
    "The thread stays open, so the next pass sees it as unsettled and may raise it again. Resolve it by hand, or retry the leg."
  return 1
}

# ---------------------------------------------------------------------------
# Writes — labels
# ---------------------------------------------------------------------------

gh_label_exists() {
  gh api "repos/$1/labels/$2" >/dev/null 2>&1
}

# The hex a label currently carries, or nothing if it does not exist. Lowercased,
# because GitHub accepts either case on write and answers in one of them.
#
# Absence is the answer here rather than an error, so the pipeline's failure is
# swallowed. Without the `|| true` a missing label fails the pipeline under
# `pipefail`, and the caller's plain assignment then takes `set -e` down with it
# — which is every fresh repository, on the most consequential command revloop
# has.
# Filtered with a piped jq rather than `gh --jq`, so the call is byte-identical to
# the existence check it replaces. Not a style choice: the offline suite matches
# routes on the whole argument string, and an extra flag would silently miss every
# label fixture in it.
gh_label_colour() {
  gh api "repos/$1/labels/$2" 2>/dev/null \
    | jq -r '.color // empty' 2>/dev/null \
    | tr '[:upper:]' '[:lower:]' || true
}

# Prints "created", "recoloured" or "exists", so a caller reporting an inventory
# can tell the truth about which it did. A plan claiming to create a label it did
# not create is the same class of lie as a count that disagrees with its own list.
#
# Recolouring an existing label is what makes the six loop colours need no
# migration: `init --upgrade` on a repository minted under the old single purple
# brings all six into line. A failed recolour is a warning rather than a fatal,
# because a label with the wrong colour still drives the chain.
gh_label_ensure() {
  local repo="$1" name="$2" colour="${3:-ededed}" desc="${4:-}" current
  current="$(gh_label_colour "$repo" "$name")"
  if [[ -z "$current" ]]; then
    gh api --method POST "repos/$repo/labels" \
      -f name="$name" -f color="$colour" -f description="$desc" >/dev/null 2>&1 \
      || ui_die "could not create the label '$name' on $repo" \
         "Init could not establish the declared colour and description. Create it by hand, or grant the token issues write."
    printf 'created'
    return 0
  fi
  if [[ "$current" == "$(tr '[:upper:]' '[:lower:]' <<<"$colour")" ]]; then
    printf 'exists'
    return 0
  fi
  if gh api --method PATCH "repos/$repo/labels/$name" -f color="$colour" >/dev/null 2>&1; then
    printf 'recoloured'
  else
    ui_warn "could not update the colour of '$name' on $repo" \
      "The label still exists and the loop still runs on it, so this is cosmetic — the pull request's label row just carries less signal than it should. Recolour it by hand, or grant the token issues write."
    printf 'exists'
  fi
}

# ---------------------------------------------------------------------------
# Writes — issues, and not filing twice
# ---------------------------------------------------------------------------

# Tier 1 dedupe: exact, against revloop's own issues.
#
# Every filed issue carries the finding's hidden marker, the same mechanism
# every other outward write uses. Deterministic, no model, no false positives —
# this is what stops three pull requests touching one legacy bug filing it three
# times.
gh_issue_by_finding() {
  local repo="$1" label="$2" finding_id="$3"
  gh api --paginate "repos/$repo/issues?state=all&labels=$label&per_page=100" 2>/dev/null \
    | jq -r --arg id "$finding_id" '
        .[] | select(.pull_request == null)
        | select([ (.body // "") | scan("<!-- revloop:f (\\{[^}]*\\}) -->") | .[]
                   | fromjson | .id ] | index($id))
        | .number' 2>/dev/null | head -1
}

# Tier 2 dedupe: fuzzy, against every issue in the repository.
#
# No shared key exists between a finding and a human-written issue about the
# same bug, so the orchestrator retrieves and the model judges. Open AND
# recently-closed both, because closing an issue is a decision and re-filing
# something explicitly closed is the most irritating duplicate available.
gh_issue_candidates() {
  local repo="$1" path="$2" terms="${3:-}" q
  q="repo:$repo is:issue $(basename "$path")"
  [[ -n "$terms" ]] && q="$q $terms"
  gh api -X GET search/issues --raw-field q="$q" --raw-field per_page=10 2>/dev/null \
    | jq -c '[ .items[]? | {number, title, state, body: ((.body // "")[0:500])} ]' \
    || printf '[]'
}

# $4 is a space-separated label list. GitHub's issue API takes them as an array,
# so each one is its own repeated field.
gh_issue_create() {
  local repo="$1" title="$2" body="$3" labels="$4" n l
  local -a args=(-f "title=$title" -f "body=$body") split=()
  read -ra split <<<"$labels"
  if (( ${#split[@]} > 0 )); then
    for l in "${split[@]}"; do [[ -n "$l" ]] && args+=(-f "labels[]=$l"); done
  fi
  n="$(gh api --method POST "repos/$repo/issues" "${args[@]}" --jq .number 2>/dev/null)" || true
  if [[ -z "$n" ]]; then
    # Deliberately not fatal to the whole leg, and deliberately loud: the caller
    # must leave the thread open rather than resolve it against a write that did
    # not land, which is exactly how deferred work disappears.
    ui_warn "could not file an issue on $repo for a deferred finding" \
      "The thread stays open and unresolved instead, so the finding is still visible on the pull request. Check that the backlog labels exist and the token has issues write."
    return 1
  fi
  printf '%s' "$n"
}

gh_issue_comment() {
  gh api --method POST "repos/$1/issues/$2/comments" -f body="$3" >/dev/null 2>&1 || true
}

# ---------------------------------------------------------------------------
# Writes — code
# ---------------------------------------------------------------------------

# Commit whatever the resolver changed, and push it to the PR's own branch.
#
# The push guard in legs.sh runs first and is not optional. Branch protection is
# a backstop behind it, not a substitute: it fires after a bad push is attempted
# and says nothing about which branch was targeted.
gh_commit_and_push() {
  local branch="$1" message="$2" expected_head="$3" sha

  # Never stage revloop's own source checkout. The generated workflows put it at
  # .revloop-src inside GITHUB_WORKSPACE, which is the same tree `git add -A` is
  # walking, and git stages an embedded repository as a gitlink with a warning
  # rather than an error — so every resolve run that fixed anything pushed a
  # submodule entry into someone's pull request. The quieter half of the same
  # bug: with .revloop-src always staged, `git diff --cached --quiet` below was
  # never true, so the "reported fixes but changed no files" guard could not
  # fire at all. `:(top)` anchors both pathspecs to the repository root, so this
  # holds wherever the leg is invoked from.
  git add -A -- ':(top)' ':(exclude,top).revloop-src' >/dev/null 2>&1 || true
  if git diff --cached --quiet 2>/dev/null; then printf ''; return 0; fi

  git -c user.name="${REVLOOP_GIT_NAME:-revloop}" \
      -c user.email="${REVLOOP_GIT_EMAIL:-revloop@users.noreply.github.com}" \
      commit -q -m "$message" || ui_die \
    "could not commit the resolver's changes" \
    "The working tree still holds them, so nothing is lost. Check \`git status\` in the checkout."

  # Re-read the remote head immediately before pushing. A human pushing to the
  # same branch mid-leg is a normal event, and overwriting them is not.
  #
  # An unreachable remote is reported rather than read as "nobody else pushed" —
  # those look identical from an empty ls-remote, and treating the second as the
  # first turns a skipped check into a silent one.
  local remote_head
  if ! remote_head="$(git ls-remote origin "refs/heads/$branch" 2>/dev/null | cut -f1)"; then
    remote_head=""
  fi
  if [[ -z "$remote_head" ]]; then
    ui_warn "could not read $branch on origin, so the check for a concurrent push did not run" \
      "If someone pushed to that branch while this leg was working, this push may not include their commit. Confirm the branch looks right before merging."
  elif [[ -n "$expected_head" && "$remote_head" != "$expected_head" ]]; then
    ui_die "$branch moved while this leg was running — it is now at ${remote_head:0:7}, not ${expected_head:0:7}" \
      "Someone else pushed. The fix is committed locally and not pushed; rebase onto the new head and re-run: revloop resolve --pr <n>"
  fi

  git push origin "HEAD:refs/heads/$branch" >/dev/null 2>&1 || ui_die \
    "could not push to $branch" \
    "The commit exists locally. If branch protection refused it, that is the backstop working — check the rule, or push by hand."

  sha="$(git rev-parse HEAD)"
  printf '%s' "$sha"
}
