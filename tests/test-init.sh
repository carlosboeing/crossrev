#!/usr/bin/env bash
#
# `crossrev init` — the plan gate, the label inventory, and what it refuses to
# claim it did.
#
# This is the most consequential command crossrev has: it registers a GitHub
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
mode: automated
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

# The colour is part of the fixture, not decoration: `gh_label_ensure` reads it to
# decide between leaving a label alone and recolouring it, so a route that answers
# without one describes a label GitHub never returns.
routes_init() {
  route 'api users/*'                      '{"type":"User"}'
  route 'api repos/*/labels/crossrev/stop'  '{"name":"crossrev/stop","color":"cf222e"}'
  route 'api repos/*/labels/bug'           '{"name":"bug","color":"d4c5f9"}'
  route 'api repos/*/labels/*'             '!fail'
  route 'api repos/*/branches/main/protection' '!fail'
  route 'secret list*'                     ''
  route 'repo view --json nameWithOwner*'  "{\"nameWithOwner\":\"$FIX_REPO\"}"
  route 'repo view * --json defaultBranchRef*' '{"defaultBranchRef":{"name":"main"}}'
}

# --- the plan ------------------------------------------------------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_init
out="$("$CROSSREV" init --dry-run 2>&1)"; rc=$?

is  "--dry-run exits clean"                    "$rc" "0"
has "and says plainly that it changed nothing"  "$out" "--dry-run prints the plan and stops"
is  "it writes no workflow files"               "$(ls .github/workflows 2>/dev/null | wc -l | tr -d ' ')" "0"
is  "and it creates no labels"                  "$(count 'method POST')" "0"

# The count and the list have to agree: max_passes_per_cycle 3 means three pass labels plus
# the five fixed ones.
has "the plan states the loop's label count"    "$out" "labels            8 for the loop"
is  "and the list under it is exactly that long" \
  "$(grep -cE '^\S*│  +(create|exists|recolour) +crossrev/' <<<"$out")" "8"
has "a label that already exists in the right colour is named as existing" \
  "$out" "exists    crossrev/stop"
has "and the missing ones are marked for creation" "$out" "create    crossrev/pass-1"

# The GitHub issues destination's labels are a different set with a different owner, so they are
# counted separately.
has "the backlog labels are counted on their own"  "$out" "2 for filed issues"
has "and one that already exists is reported so"   "$out" "exists    bug"

has "the plan says where deferred work will go"    "$out" "deferred work     github_issues"
has "and where that answer came from"              "$out" "named in the repository config as 'github_issues'"
has "the plan names every file it would write"     "$out" ".github/workflows/crossrev-watchdog.yml"
# The fixture already carries a policy file, so the plan must say it would be
# replaced rather than quietly replacing it.
has "and flags the file it would overwrite"        "$out" "overwrites        .github/crossrev.yml"

# A tag only looks immutable, so the pin is the 40-character SHA.
has "the source pin is a commit, with the tag as a comment" "$out" "the SHA is the pin, the tag is a comment"
has "an unprotected default branch is a warning with its consequence" \
  "$out" "would be the only thing stopping a bad push"
has "the App is named as missing rather than assumed" "$out" "run \`crossrev auth login\` first"

# --- execution -----------------------------------------------------------
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_init
out="$("$CROSSREV" init --yes 2>&1)"; rc=$?

is  "init exits clean even with secrets outstanding" "$rc" "0"
is  "it writes all three workflows"             "$(ls .github/workflows | wc -l | tr -d ' ')" "3"
has "it creates the labels the loop needs"      "$(calls)" "name=crossrev/awaiting-resolution"
is  "and only the ones that were missing"       "$(count 'method POST repos/acme/widget/labels')" "8"

# A label description is the only place GitHub shows a reader what a label means
# without them going looking — the hover text on the pill, the second column on
# the labels page. All six carried "crossrev loop state", which answered nothing
# and left the colour as the only signal.
has "each label says what its own state means"  "$(calls)" "the resolve leg is owed"
has "including the pass label, which counts rather than states" \
  "$(calls)" "reached pass 2"
hasnt "rather than one generic string across all of them" \
  "$(calls)" "description=crossrev loop state"

# The source checkout and its deploy key are retired. The action is public, so
# `uses:` reaches it with no credential — and a secret set that still asked for
# a key nobody can produce would block an install that has nothing wrong with it.
hasnt "no deploy-key secret is demanded any more"   "$out" "CROSSREV_SOURCE_KEY"
hasnt "and no deploy-key instructions are printed"  "$out" "gh repo deploy-key add"
has "the closing line does not claim success"       "$out" "will fail at the first missing secret"

# The generated workflows carry the two things the chain depends on.
# Comments are stripped first: the templates discuss cancel-in-progress and
# pull_request_target precisely to explain why neither is used, and an assertion
# that cannot tell a warning from a usage is not an assertion.
wf="$(grep -v '^[[:space:]]*#' .github/workflows/crossrev-review.yml)"
has "both workflows share one concurrency group per pull request" "$wf" "group: crossrev-pr-"
has "and queue rather than evict, which is not the default"       "$wf" "queue: max"
hasnt "cancel-in-progress appears nowhere, since it is unsafe here" "$wf" "cancel-in-progress"
has "every write uses the App token"                              "$wf" "steps.app.outputs.token"
hasnt "and GITHUB_TOKEN is never used for a write"                "$wf" "secrets.GITHUB_TOKEN"
hasnt "pull_request_target appears nowhere"                       "$wf" "pull_request_target"
hasnt "pushes do not trigger another automatic review"            "$wf" "synchronize"
has "automatic pull request events skip draft pull requests"       "$wf" "github.event.pull_request.draft == false"
has "public commenters cannot trigger an uncapped review"          "$wf" "github.event.comment.author_association"
has "the comment gate accepts only repository relationships"       "$wf" "OWNER\",\"MEMBER\",\"COLLABORATOR"
# The automatic/human distinction survived the collapse to `uses:` as an action
# input rather than a shell branch. It controls the policy caps and the draft
# skip, so losing it would uncap the loop silently.
has "the trigger is derived from the event, not assumed"           "$wf" "trigger: \${{ github.event_name == 'pull_request' && 'automatic' || 'human' }}"
hasnt "the unverified effective-permission endpoint is not guessed" \
  "$wf" 'collaborators/$ACTOR/permission'
# The pin is the SHA and the tag is a comment, so a moved tag cannot change a
# repository's review behaviour with nothing in its own history to show for it.
is  "the action is pinned to a 40-character SHA" \
  "$(grep -oE 'uses: carlosboeing/crossrev@[0-9a-f]{40}' <<<"$wf" | wc -l | tr -d ' ')" "1"
hasnt "and never to a floating tag" "$wf" "carlosboeing/crossrev@v"
expected_ref="$(git -C "$HERE/.." describe --tags 2>/dev/null || printf 'untagged')"
has "the source pin comment is the described ref rather than a stale version tag" \
  "$wf" "# $expected_ref"
# The retired apparatus, asserted gone rather than assumed gone.
hasnt "no second checkout of the source survives"   "$wf" ".crossrev-src"
hasnt "and no deploy key is referenced"             "$wf" "CROSSREV_SOURCE_KEY"
has "the resolve workflow shares the review workflow's group" \
  "$(cat .github/workflows/crossrev-resolve.yml)" "group: crossrev-pr-\${{ github.event.pull_request.number }}"

# The token is revoked once. The explicit `DELETE installation/token` step
# closes the write window at the end of the job; `skip-token-revoke` stops the
# action's post-job from trying a second time and annotating
# "Token revocation failed: Bad credentials".
for wf in review resolve token-refresh; do
  src="$(cat "$HERE/../templates/crossrev-$wf.yml")"
  has "crossrev-$wf.yml still revokes the token itself" "$src" "DELETE installation/token"
  has "and tells the action not to revoke it again" "$src" "skip-token-revoke: true"
done
wd="$(cat "$HERE/../templates/crossrev-watchdog.yml")"
hasnt "the watchdog has no explicit revoke of its own" "$wd" "DELETE installation/token"
hasnt "so it does not skip the action's post-job revocation" "$wd" "skip-token-revoke"
has "init keeps skip-token-revoke on the review workflow" \
  "$(cat .github/workflows/crossrev-review.yml)" "skip-token-revoke: true"
has "and on the resolve workflow" \
  "$(cat .github/workflows/crossrev-resolve.yml)" "skip-token-revoke: true"
hasnt "and does not add it to the watchdog" \
  "$(cat .github/workflows/crossrev-watchdog.yml)" "skip-token-revoke"

# The generated config states where deferred work goes, so `auto` is a bootstrap
# convenience rather than a runtime mode.
has "the generated config names the resolved backlog" "$(cat .github/crossrev.yml)" "destination: github_issues"

# --- --upgrade regenerates workflows and leaves policy alone -----------
printf 'version: 1\npolicy:\n  max_passes_per_cycle: 7\n' >.github/crossrev.yml
printf 'stale\n' >.github/workflows/crossrev-review.yml
stub_reset; routes_init
out="$("$CROSSREV" init --upgrade --yes 2>&1)"
has "--upgrade rewrites a stale workflow"       "$(cat .github/workflows/crossrev-review.yml)" "name: crossrev review"
has "and says it left the policy file alone"    "$out" "regenerates workflows, not policy"
is  "so the hand-edited policy survives"        "$(grep -c 'max_passes_per_cycle: 7' .github/crossrev.yml)" "1"
has "and it flags the files it would overwrite" "$out" "overwrites        .github/workflows/crossrev-review.yml"

# --- --upgrade recolours labels minted under the old single purple -----
#
# Every loop label used to be created with one hex, so the label row on a pull
# request carried no signal at a glance. Six hues fix that, and the migration is
# `init --upgrade` rather than a script: `gh_label_ensure` recolours a label that
# already exists. A repository that never upgrades keeps working — the colour is
# cosmetic, unlike the label itself, which the chain cannot run without.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_init
route_first 'api repos/*/labels/bug'      '{"name":"bug","color":"d4c5f9"}'
route_first 'api repos/*/labels/crossrev/*' '{"name":"old","color":"5319e7"}'
out="$("$CROSSREV" init --upgrade --dry-run 2>&1)"

has "the plan calls a wrong-coloured label a recolour, not an existing one" \
  "$out" "recolour  crossrev/converged"
hasnt "and does not claim it would create one"  "$out" "create    crossrev/converged"

stub_reset; routes_init
route_first 'api repos/*/labels/bug'      '{"name":"bug","color":"d4c5f9"}'
route_first 'api repos/*/labels/crossrev/*' '{"name":"old","color":"5319e7"}'
out="$("$CROSSREV" init --upgrade --yes 2>&1)"

is  "every loop label is recoloured, none created" \
  "$(count 'method PATCH repos/acme/widget/labels/crossrev/')" "8"
is  "and no loop label is created, because they all already existed" \
  "$(count 'method POST repos/acme/widget/labels -f name=crossrev/')" "0"
has "the run says how many it recoloured"       "$out" "recoloured 8"
has "converged is green"                        "$(calls)" "labels/crossrev/converged -f color=1a7f37"
has "halted is orange"                          "$(calls)" "labels/crossrev/halted -f color=bc4c00"
# Red is reserved for the one label a human applies, so a red pill in a pull
# request list always means somebody pulled the brake.
has "stop is red"                               "$(calls)" "labels/crossrev/stop -f color=cf222e"
has "a pass label is grey, because it is informational rather than a state" \
  "$(calls)" "labels/crossrev/pass-2 -f color=57606a"
hasnt "and nothing is minted in the old single purple any more" "$(calls)" "color=5319e7"

# --- the backlog labels belong to the repository, so init never repaints them --
#
# `bug` is the repository's own taxonomy. init creates one that is missing so
# filing does not die after the review has already posted, and leaves the colour
# of one it finds alone — recolouring somebody's `bug` label because crossrev once
# minted one would be overstepping. The plan has to say the same thing execution
# does: a plan promising a recolour that never happens is the same class of lie
# as one claiming to create a label it does not create.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_init
route_first 'api repos/*/labels/bug'       '{"name":"bug","color":"d73a4a"}'
route_first 'api repos/*/labels/crossrev/*' '{"name":"old","color":"5319e7"}'
out="$("$CROSSREV" init --upgrade --dry-run 2>&1)"

has "a backlog label in the repository's own colour is reported as existing" \
  "$out" "exists    bug"
hasnt "and never as a recolour the run would not perform" "$out" "recolour  bug"

stub_reset; routes_init
route_first 'api repos/*/labels/bug'       '{"name":"bug","color":"d73a4a"}'
route_first 'api repos/*/labels/crossrev/*' '{"name":"old","color":"5319e7"}'
out="$("$CROSSREV" init --upgrade --yes 2>&1)"
is  "and execution leaves its colour exactly as the repository set it" \
  "$(count 'method PATCH repos/acme/widget/labels/bug')" "0"

# --- create_missing_labels: false refuses rather than inventing --------
#
# A repository that governs its own label set is one where inventing labels is
# worse than stopping.
strict="$(config_with_issue_sink | sed 's/    create_missing_labels: true/    create_missing_labels: false/; s/    labels: \[bug\]/    labels: [needs-triage]/')"
fixture_repo "$strict"; stub_reset
routes_init
err="$("$CROSSREV" init --yes 2>&1 >/dev/null)"; rc=$?
is  "init stops when a backlog label is missing" "$rc" "1"
has "and names the label it will not invent"    "$err" "needs-triage"
has "and says why it stops"                     "$err" "asked crossrev to use existing labels only"

# --- reading the secrets that are already there --------------------------
#
# Nothing covered the found case. The baseline routes every `secret list` to an
# empty string, which is what an absent secret and a failed query both look like
# once stderr is discarded — so the plan could report a secret missing that was
# sitting right there, and tell you to go set it again.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_init
route_first 'secret list --repo*' "$(printf 'APP_ID\t2026-08-10T11:34:05Z\nAPP_PRIVATE_KEY\t2026-08-10T11:34:06Z')"
out="$("$CROSSREV" init --dry-run 2>&1)"

has  "a secret that is set is reported as set"      "$out" "APP_ID — already set"
hasnt "and is not also reported as missing"         "$out" "APP_ID — MISSING"
# APP_PRIVATE_KEY is stubbed as present above; the harness credential is not, so
# it is the one that must still come back MISSING. Asserting this on a secret
# that is no longer required at all would test nothing.
has  "while one that is absent is still missing"    "$out" "CLAUDE_CODE_OAUTH_TOKEN — MISSING"

# One query per scope, not two per secret. Seven secrets meant fourteen calls to
# render one plan, which is what gave a transient failure seven chances to land.
is "the repository's secrets are read once, not once per secret" \
  "$(count 'secret list --repo')" "1"

# --- a query that failed is not an absent secret -------------------------
#
# The plan gate is the one place an operator decides whether to hand this command
# a live repository. Reporting a secret missing because the call to check it fell
# over is the worst available answer: it reads as a clean fact.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_init
route_first 'secret list --repo*' '!fail'
out="$("$CROSSREV" init --dry-run 2>&1)"; rc=$?

is    "a secret query that fails stops the run"      "$rc" "1"
has   "and says which repository it could not read"  "$out" "could not read the secrets on acme/widget"
hasnt "rather than reporting every secret missing"   "$out" "APP_ID — MISSING"

# --- the policy file states the pairing that was provisioned -------------
#
# init derives the secret list, and whether a refresher App is needed at all,
# from the resolved pairing. A policy file naming a different one leaves the
# repository provisioned for a leg that never runs — a Codex credential and a
# cron refresher for a reviewer the committed config says is Claude. Same rule
# the backlog already follows: init resolves the answer and writes it down.
# Only the reviewer, so the two legs differ and a write-through that collapsed
# them into one value would show up rather than passing by coincidence. Its model
# goes too, so the resolved-to-nothing path is exercised rather than described.
paired="$(config_with_issue_sink \
  | awk '!d && $0=="  harness: claude" { $0="  harness: codex"; d=1 } 1' \
  | grep -v '^  model: reviewer-model$')"
fixture_repo "$paired"; stub_reset
routes_init
"$CROSSREV" init --yes >/dev/null 2>&1
pol="$(cat .github/crossrev.yml)"

is "the written policy names the reviewer init provisioned for" \
  "$(yq -r '.reviewer.harness' <<<"$pol")" "codex"
is "and the resolver alongside it" \
  "$(yq -r '.resolver.harness' <<<"$pol")" "claude"
is "a model from the resolved config is carried through" \
  "$(yq -r '.resolver.model' <<<"$pol")" "resolver-model"
# The template ships a model per leg. Carrying one through under a harness that
# never had it is how you get `model: claude-fable-5` under `harness: codex`.
is "while a leg with no model of its own gets none, not the template's" \
  "$(yq -r '.reviewer.model' <<<"$pol")" "null"

# --- harness installer lines ---------------------------------------------
#
# Workflows install harnesses from vendor install scripts, never npm install -g.
fixture_repo "$(config_with_issue_sink)"; stub_reset
routes_init
"$CROSSREV" init --yes >/dev/null 2>&1
wf_review="$(cat .github/workflows/crossrev-review.yml)"
has "rendered review workflow carries claude install script" "$wf_review" "curl -fsSL https://claude.ai/install.sh"
hasnt "and no npm install -g" "$wf_review" "npm install -g"

# Paired codex and claude
paired_harness="$(config_with_issue_sink \
  | awk '!d && $0=="  harness: claude" { $0="  harness: codex"; d=1 } 1')"
fixture_repo "$paired_harness"; stub_reset
routes_init
"$CROSSREV" init --yes >/dev/null 2>&1
wf_paired="$(cat .github/workflows/crossrev-review.yml)"
has "codex/claude pairing renders codex install line" "$wf_paired" "curl -fsSL https://chatgpt.com/codex/install.sh"
has "and claude install line" "$wf_paired" "curl -fsSL https://claude.ai/install.sh"

install_line_for() {
  local cfg="$1"
  bash -c '
    source "'"$HERE"'/../lib/ui.sh"
    source "'"$HERE"'/../lib/harnesses.sh"
    source "'"$HERE"'/../lib/config.sh"
    source "'"$HERE"'/../lib/init.sh"
    CFG_MERGED="$(jq -cn --argjson d "$(_cfg_defaults)" --argjson r "$(_cfg_yaml_text_to_json "$1")" '\''$d * $r'\'')"
    export CFG_MERGED
    _init_harness_install_line
  ' _ "$cfg"
}

agy_cfg="$(cat <<'EOF'
version: 1
reviewer:
  harness: agy
resolver:
  harness: agy
EOF
)"
agy_line="$(install_line_for "$agy_cfg")"
has "an agy leg renders Antigravity install script rather than nothing" "$agy_line" "curl -fsSL https://antigravity.google/cli/install.sh"

ep_cfg="$(cat <<'EOF'
version: 1
reviewer:
  harness: claude
  endpoint: my-ep
resolver:
  harness: claude
endpoints:
  my-ep:
    type: anthropic
    base_url: https://api.example.com
    secret: MY_EP_TOKEN
EOF
)"
ep_line="$(install_line_for "$ep_cfg")"
has "a leg on a named endpoint still renders the endpoint host installer" "$ep_line" "curl -fsSL https://claude.ai/install.sh"
is  "and not a second one" "$(grep -c 'curl -fsSL' <<<"$ep_line" | tr -d ' ')" "1"

finish
