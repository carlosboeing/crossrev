# shellcheck shell=bash
# lib/github.sh — every GitHub read and write crossrev makes.
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

# Tests source this file without bin/crossrev's ordering. The log helpers are
# no-ops until log_init runs, but log_redact_publish is not optional: every write
# below filters its body through it, so a file sourced alone has to bring it in.
# Same fallback config.sh uses for harnesses.sh.
if ! declare -F log_event >/dev/null 2>&1 || ! declare -F log_redact_publish >/dev/null 2>&1; then
  _gh_lib="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  # shellcheck source=log.sh
  [[ -f "$_gh_lib/log.sh" ]] && source "$_gh_lib/log.sh"
  unset _gh_lib
fi

# ---------------------------------------------------------------------------
# Reads
# ---------------------------------------------------------------------------

gh_repo_slug() {
  gh repo view --json nameWithOwner --jq .nameWithOwner 2>/dev/null || ui_die \
    "could not work out which repository this is" \
    "Run crossrev from a checkout with a GitHub remote, or pass --repo owner/name."
}

# Everything about the pull request the legs need, in one call.
gh_pr_json() {
  local repo="$1" pr="$2" out
  out="$(gh pr view "$pr" --repo "$repo" --json \
    number,title,body,url,headRefName,headRefOid,baseRefName,baseRefOid,changedFiles,labels,isCrossRepository,isDraft,headRepositoryOwner,headRepository,maintainerCanModify,state 2>/dev/null)" \
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
# bookkeeping into the PR branch, and the next pass would then review crossrev's
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
  local repo="$1" pr="$2" base="$3" head="$4"; shift 4
  local diff tmp
  diff="$(gh api -H "Accept: application/vnd.github.diff" \
            "repos/$repo/compare/$base...$head" 2>/dev/null)" \
    || ui_die "could not fetch the diff for $repo#$pr at $head" \
       "The review leg has nothing to reason about without it. Check network access and \`gh auth status\`."
  if (( $# == 0 )); then printf '%s' "$diff"; return 0; fi
  # Through a file rather than a pipe: `_diff_parse` refuses an empty input, and
  # `-s` on a fifo answers that question about the pipe rather than about what
  # is coming down it.
  tmp="$(mktemp)" || ui_die "could not create a temporary file to filter the diff" \
    "Check that the temporary directory is writable."
  printf '%s' "$diff" >"$tmp"
  diff_exclude "$tmp" "$@"
  rm -f "$tmp"
}

# Review threads, with each thread's crossrev finding ids read out of its comment
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
                                | scan("<!-- crossrev:f (\\{[^}]*\\}) -->") | .[0]
                                | fromjson | .id ],
                 comments: [ .comments.nodes[] | {author: .author.login, body} ] } ]' \
  || printf '[]'
}

# ---------------------------------------------------------------------------
# Writes — comments
# ---------------------------------------------------------------------------

# Post an overall comment. Prints the new comment's id, because every later
# What to do when the publish filter could not process a body.
#
# log_redact_publish withholds the text and returns the notice in its place.
# That is the right trade for findings text and the wrong one for a body
# carrying a pass marker: the marker is crossrev's record of what ran (ADR
# 0002), so publishing the notice instead of it would leave `crossrev status`
# reading `passes none yet` on a pull request that did run. Refuse there, for
# the same reason gh_comment_edit refuses when the API write fails.
#
# Called outside a command substitution deliberately. ui_die inside one exits
# the subshell, and the caller carries on with whatever the substitution
# captured, which is the opposite of stopping.
_gh_refuse_unfiltered() {
  case "$1" in
    *'<!-- crossrev:'*)
      ui_die "could not filter a comment body for credential shapes" \
        "That comment carries the pass marker, which is crossrev's record of what ran, so it stopped rather than publishing a body without it." ;;
  esac
}

# write to it is an edit of that id.
gh_comment_create() {
  local repo="$1" pr="$2" body="$3" id
  body="$(log_redact_publish "$body")" || _gh_refuse_unfiltered "$3"
  id="$(gh api --method POST "repos/$repo/issues/$pr/comments" \
        -f body="$body" --jq .id 2>/dev/null)" \
    || ui_die "could not post a comment on $repo#$pr" \
       "Every pass records itself in a comment, so crossrev stops rather than working without a record. Check the token has pull-requests write."
  printf '%s' "$id"
}

gh_comment_edit() {
  local repo="$1" id="$2" body="$3"
  body="$(log_redact_publish "$body")" || _gh_refuse_unfiltered "$3"
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
  body="$(log_redact_publish "$body")" || _gh_refuse_unfiltered "$7"
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
  body="$(log_redact_publish "$body")" || _gh_refuse_unfiltered "$4"
  gh api --method POST "repos/$repo/pulls/$pr/comments/$root_comment_id/replies" \
    -f body="$body" >/dev/null 2>&1 && return 0
  ui_warn "could not reply in the thread rooted at comment $root_comment_id on $repo#$pr" \
    "The resolution is still recorded in the pass marker, but the collaborator reading the thread will not see the reason. Check the token has pull-requests write."
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
# — which is every fresh repository, on the most consequential command crossrev
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

# Tier 1 dedupe: exact, against crossrev's own issues.
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
        | select([ (.body // "") | scan("<!-- crossrev:f (\\{[^}]*\\}) -->") | .[]
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
  # The title masks through log_redact_str rather than log_redact_publish: an
  # issue title is one line and the publish notice is a paragraph, so the note
  # rides on the body where a reader can act on it.
  title="$(log_redact_str "$title")"
  body="$(log_redact_publish "$body")" || _gh_refuse_unfiltered "$3"
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
  local body
  body="$(log_redact_publish "$3")" || _gh_refuse_unfiltered "$3"
  gh api --method POST "repos/$1/issues/$2/comments" \
    -f body="$body" >/dev/null 2>&1 || true
}

# ---------------------------------------------------------------------------
# Writes — code
# ---------------------------------------------------------------------------

# The end of git's own output, for an error message that would otherwise state
# only that git refused.
#
# The tail rather than the head, for the reason legs_harness_error takes the tail
# of a harness stream: whatever ran last is the thing that failed, and anything a
# hook printed on its way there is banner. A repository whose pre-commit hook
# runs its test suite prints thousands of lines before the refusal, so a head cut
# reads back the first assertion and none of the diagnosis.
#
# Not shared with legs_harness_error, which greps for error-shaped words first. A
# hook's refusal need not contain one — the refusal that prompted this printed
# `1:sentence-length:…` and matched none of that helper's keywords.
_gh_git_tail() {
  local text="$1" cap="${2:-400}" picked
  picked="$(printf '%s' "$text" | grep -v '^[[:space:]]*$' | tail -5)"
  [[ -n "$picked" ]] || return 1
  (( ${#picked} > cap )) && picked="…${picked: -cap}"
  printf '%s' "$picked"
}

# Commit whatever the resolver changed, and push it to the PR's own branch.
#
# The push guard in legs.sh runs first and is not optional. Branch protection is
# a backstop behind it, not a substitute: it fires after a bad push is attempted
# and says nothing about which branch was targeted.
#
# $6 is the repository's `git.hooks` setting. It defaults to skip here as well as
# in the config, so a caller that has not been taught the parameter still gets
# the documented behaviour rather than the opposite one. See ADR 0017.
#
# The new SHA comes back in GH_COMMIT_SHA rather than on stdout, and that is the
# whole reason the global exists. lib/run.sh states the rule in its own header:
# anything that can call ui_die is called directly, never inside a command
# substitution, because `exit` inside `$( )` ends the subshell. Read from stdout,
# this function was the exception — a refused commit exited a subshell, `set -e`
# killed the process from the outside, and the fatal path never reached the code
# that records why on the pull request.
GH_COMMIT_SHA=""
gh_commit_and_push() {
  GH_COMMIT_SHA=""
  local branch="$1" message="$2" expected_head="$3" remote="${4:-origin}" expected_repo="${5:-}"
  local hooks="${6:-skip}" sha

  # Whole commands rather than a flag array. bash 3.2 is the floor here, and
  # `"${empty[@]}"` under `set -u` is an unbound-variable error there — so a
  # repository setting `git.hooks: run` would die before git ran at all, with a
  # bash message rather than the hook's. Built this way the array is never empty.
  local -a commit_cmd=(git
    -c user.name="${CROSSREV_GIT_NAME:-crossrev}"
    -c user.email="${CROSSREV_GIT_EMAIL:-crossrev@users.noreply.github.com}"
    commit -q)
  local -a push_cmd=(git push)
  if [[ "$hooks" != "run" ]]; then
    commit_cmd+=(--no-verify)
    # A pre-push hook is a host repository hook firing on an automated action for
    # the same reason a pre-commit hook is, and git spells the two flags apart.
    push_cmd+=(--no-verify)
  fi
  commit_cmd+=(-m "$message")

  # What to tell someone whose commit was refused, which depends on whether their
  # own hooks were in play at all.
  local hooks_advice
  if [[ "$hooks" == "run" ]]; then
    hooks_advice="This repository sets git.hooks: run, so its own commit hooks ran on this commit and one of them may have refused it. Setting git.hooks: skip makes the resolver commit the way it already does on a GitHub-hosted runner, which has no hooks installed."
  else
    hooks_advice="The repository's own git hooks were skipped, so this is git itself refusing rather than a hook."
  fi

  git add -A >/dev/null 2>&1 || true
  if git diff --cached --quiet 2>/dev/null; then return 0; fi

  # The remote's URLs are read again here rather than trusted from the guard
  # that ran before the model did. This is the last point before a commit leaves
  # the machine, and a leg that edits the working tree has had a git repository
  # in front of it since then.
  legs_resolve_push_repo "$remote"
  [[ -n "$expected_repo" && "$LEGS_PUSH_REPO" == "$expected_repo" ]] || ui_die \
    "remote '$remote' pushes to '${LEGS_PUSH_REPO:-a URL CrossRev could not read}', but the head repository of this pull request is '${expected_repo:-unknown}'" \
    "CrossRev pushes only to the head repository of the pull request under review. The resolver's changes are still in the working tree and nothing was pushed. Check \`git config --get-all remote.$remote.pushurl\`."

  # Declared before the assignment, not on the same line. `local x="$(cmd)"`
  # reports the status of `local`, which always succeeds, so the `||` below would
  # never fire and a refused commit would run on into the push.
  local commit_out commit_why
  # The run log brackets the commit, so a stall inside it — a hook running the
  # repository's test suite is the observed case — is attributable to this step
  # rather than to the leg as a whole.
  log_event commit "start branch=$branch"
  if ! commit_out="$("${commit_cmd[@]}" 2>&1)"; then
    commit_why="$(_gh_git_tail "$commit_out")" || commit_why="git printed nothing on either stream."
    log_event commit "failed: $commit_why"
    ui_die "could not commit the resolver's changes — $commit_why" \
      "The working tree still holds them, so nothing is lost. $hooks_advice Check \`git status\` in the checkout."
  fi
  log_event commit "exit=0"

  # Re-read the remote head immediately before pushing. A human pushing to the
  # same branch mid-leg is a normal event, and overwriting them is not.
  #
  # An unreachable remote is reported rather than read as "nobody else pushed" —
  # those look identical from an empty ls-remote, and treating the second as the
  # first turns a skipped check into a silent one.
  local remote_head remote_push_url
  remote_push_url="$(git remote get-url --push "$remote" 2>/dev/null || git remote get-url "$remote" 2>/dev/null || printf '%s' "$remote")"
  if ! remote_head="$(git ls-remote "$remote_push_url" "refs/heads/$branch" 2>/dev/null | cut -f1)"; then
    remote_head=""
  fi
  if [[ -z "$remote_head" ]]; then
    ui_warn "could not read $branch on $remote, so the check for a concurrent push did not run" \
      "If someone pushed to that branch while this leg was working, this push may not include their commit. Confirm the branch looks right before merging."
  elif [[ -n "$expected_head" && "$remote_head" != "$expected_head" ]]; then
    ui_die "$branch moved while this leg was running — it is now at ${remote_head:0:7}, not ${expected_head:0:7}" \
      "Someone else pushed. The fix is committed locally and not pushed; rebase onto the new head and re-run: crossrev resolve --pr <n>"
  fi

  local push_out push_why
  log_event push "start branch=$branch remote=$remote"
  if ! push_out="$("${push_cmd[@]}" "$remote" "HEAD:refs/heads/$branch" 2>&1)"; then
    push_why="$(_gh_git_tail "$push_out")" || push_why="git printed nothing on either stream."
    log_event push "failed: $push_why"
    ui_die "could not push to $branch — $push_why" \
      "The commit exists locally. If branch protection refused it, that is the backstop working — check the rule, or push by hand."
  fi
  log_event push "exit=0"

  sha="$(git rev-parse HEAD)"
  # shellcheck disable=SC2034  # read by run.sh after the call returns
  GH_COMMIT_SHA="$sha"
}
