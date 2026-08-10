#!/usr/bin/env bash
#
# Runner awareness: which pairings a runner can serve, and what `init` emits for
# each.
#
# Which harnesses are reachable in CI is a property of the runner rather than of
# the config, because it comes down to whether a subscription credential can live
# in a repository secret. Getting this wrong is not a crash: the workflows install
# fine, the loop fires, and the first leg dies on an authentication error that
# reads like a wrong password. So the refusal, the derivation of the refresher,
# and the two shapes of generated workflow are all asserted here.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

# $1 runner, $2 reviewer harness, $3 addresser harness, $4 optional endpoint block
config_for() {
  local runner="$1" reviewer="$2" addresser="$3" endpoint="${4:-}"
  cat <<EOF
version: 1
mode: event-driven
max_passes: 3
runner: $runner
$endpoint
reviewer:
  harness: $reviewer
  model: reviewer-model
addresser:
  harness: $addresser
  model: addresser-model
persist:
  defects: none
EOF
}

routes_init() {
  route 'api users/*'                          '{"type":"User"}'
  route 'api repos/*/labels/*'                 '!fail'
  route 'api repos/*/branches/main/protection' '{"enabled":true}'
  route 'secret list*'                         ''
  route 'repo view --json nameWithOwner*'      "{\"nameWithOwner\":\"$FIX_REPO\"}"
  route 'repo view * --json defaultBranchRef*' '{"defaultBranchRef":{"name":"main"}}'
}

# --- claude both legs on a hosted runner: the default, and the cheap case ---
fixture_repo "$(config_for github-hosted claude claude)"; stub_reset
routes_init
out="$("$REVLOOP" init --dry-run 2>&1)"; rc=$?

is  "the v1 default plans clean"                   "$rc" "0"
has "the plan names the runner"                    "$out" "runner            github-hosted"
has "and what each leg authenticates as"           "$out" "reviewer: claude by subscription"
hasnt "no refresher is mentioned, because none is needed" "$out" "refresher App"
has "the token it needs is the long-lived one"     "$out" "CLAUDE_CODE_OAUTH_TOKEN"
hasnt "and nothing asks for a rotating codex credential" "$out" "REVLOOP_CODEX_AUTH"
has "and it says the token is captured rather than pasted" \
  "$out" "captures the output"

# --- codex reviewing on a hosted runner: needs the refresher ---------------
fixture_repo "$(config_for github-hosted codex claude)"; stub_reset
routes_init
out="$("$REVLOOP" init --dry-run 2>&1)"; rc=$?

is  "codex on a hosted runner is allowed"          "$rc" "0"
has "and the plan says a refresher is needed"      "$out" "refresher App     needed"
has "with the reason, not just the fact"           "$out" "its credential rotates"
has "and that only one job writes it"              "$out" "the legs only read"
has "the refresher's own App secrets are listed"   "$out" "REVLOOP_REFRESH_APP_ID"
has "and the credential the legs restore"          "$out" "REVLOOP_CODEX_AUTH"
has "the refresher workflow is one of the files"   "$out" "revloop-token-refresh.yml"

# Derived from the pairing, never asked. Nobody is offered a choice about it.
hasnt "nothing asks whether a refresher is wanted" "$out" "Do you want"

# --- Antigravity and Kimi by subscription on a hosted runner: refused -------
#
# Refusing here beats failing at the first API call. The message has to carry the
# reason and both ways out, because "not supported" tells nobody what to change.
fixture_repo "$(config_for github-hosted agy claude)"; stub_reset
routes_init
err="$("$REVLOOP" init --dry-run 2>&1 >/dev/null)"; rc=$?

is  "agy by subscription on a hosted runner refuses" "$rc" "1"
has "and names the token lifetime as the reason"     "$err" "about an hour"
has "and offers the self-hosted fix"                 "$err" "runner: self-hosted"
has "and the change-the-harness fix"                 "$err" "name a different harness"

fixture_repo "$(config_for github-hosted kimi claude)"; stub_reset
routes_init
err="$("$REVLOOP" init --dry-run 2>&1 >/dev/null)"; rc=$?
is  "kimi by subscription on a hosted runner refuses" "$rc" "1"
has "and names its fifteen minutes"                   "$err" "15 minutes"
has "against a scheduler that cannot keep up"         "$err" "five-minute floor"

# The same harness reached through an endpoint is a static token in a secret,
# which never rotates and so never cares what runner it is on. Refusing that too
# would be refusing the one route that actually works.
kimi_endpoint='endpoints:
  kimi:
    base_url: https://api.kimi.com/coding/
    token_env: KIMI_API_KEY'
fixture_repo "$(config_for github-hosted claude claude "$kimi_endpoint")"; stub_reset
routes_init
# Point the reviewer at the endpoint, which config_for does not template.
yq -i '.reviewer.endpoint = "kimi"' .github/revloop.yml
git add -A && git commit -q -m endpoint && git push -q origin main
out="$("$REVLOOP" init --dry-run 2>&1)"; rc=$?

is  "a harness reached through an endpoint is allowed on any runner" "$rc" "0"
has "and the plan says which endpoint it goes through" "$out" "via the 'kimi' endpoint"
has "the secret it needs is the one the endpoint names, not a vendor name" \
  "$out" "KIMI_API_KEY"

# --- self-hosted serves everything -----------------------------------------
fixture_repo "$(config_for self-hosted agy codex)"; stub_reset
routes_init
out="$("$REVLOOP" init --yes 2>&1)"; rc=$?

is  "self-hosted serves a pairing a hosted runner cannot" "$rc" "0"
hasnt "and needs no refresher, because credentials refresh on disk" "$out" "refresher App"
hasnt "no rotating credential is asked for"        "$out" "REVLOOP_CODEX_AUTH"
hasnt "and no long-lived token either"             "$out" "CLAUDE_CODE_OAUTH_TOKEN"
is  "three workflows, not four"                    "$(ls .github/workflows | wc -l | tr -d ' ')" "3"

wf="$(cat .github/workflows/revloop-review.yml)"
has "the workflow asks for the self-hosted runner"  "$wf" "runs-on: [self-hosted, revloop]"
# Two labels rather than one: `self-hosted` alone matches every self-hosted
# runner the owner has, including ones set up for something else.
has "by both labels"                                "$wf" "revloop]"
hasnt "it does not install a harness that is already there" "$wf" "npm install"
hasnt "and passes no credential, since the machine is logged in" "$wf" "CLAUDE_CODE_OAUTH_TOKEN"
hasnt "the fence markers are stripped, not left in the file" "$wf" "revloop:only"

# --- the hosted workflows carry the other half ------------------------------
fixture_repo "$(config_for github-hosted codex claude)"; stub_reset
routes_init
out="$("$REVLOOP" init --yes 2>&1)"; rc=$?

is  "init writes the refresher workflow when the pairing needs it" \
  "$(ls .github/workflows | wc -l | tr -d ' ')" "4"
wf="$(cat .github/workflows/revloop-review.yml)"
# Both harnesses, not just the one the earlier version of this assertion checked.
# Neither is on GitHub's runner images, and installing only Claude does not fail:
# `run_resolve_leg` falls back, warns in one line nobody reads in a CI log, and
# both legs run Claude. The loop completes and the cross-model property is gone.
has "a hosted workflow installs the addresser's harness"  "$wf" "npm install -g @anthropic-ai/claude-code"
has "AND the reviewer's, which is a different one"        "$wf" "npm install -g @openai/codex"
has "and passes the credentials in as secrets"       "$wf" "REVLOOP_CODEX_AUTH"
has "on GitHub's own runner"                         "$wf" "runs-on: ubuntu-latest"

# A pairing on one harness installs it once rather than twice.
fixture_repo "$(config_for github-hosted claude claude)"; stub_reset
routes_init
"$REVLOOP" init --yes >/dev/null 2>&1
is  "a same-harness pairing installs it once, not twice" \
  "$(grep -c 'npm install -g @anthropic-ai/claude-code' .github/workflows/revloop-review.yml)" "1"
hasnt "and installs nothing it does not use" \
  "$(cat .github/workflows/revloop-review.yml)" "@openai/codex"

fixture_repo "$(config_for github-hosted codex claude)"; stub_reset
routes_init
out="$("$REVLOOP" init --yes 2>&1)"

refresh="$(grep -v '^[[:space:]]*#' .github/workflows/revloop-token-refresh.yml)"
has "the refresher runs on a schedule"               "$refresh" "cron:"
has "on its own concurrency group, so there is one writer" \
  "$refresh" "group: revloop-token-refresh"
hasnt "and never cancels a refresh in flight"        "$refresh" "cancel-in-progress"
has "it authenticates as the refresher App, not the loop's" \
  "$refresh" "REVLOOP_REFRESH_APP_ID"
# The isolation argument, asserted rather than described: this job never touches
# anything a pull request author can influence.
hasnt "it never checks out the repository under review" "$refresh" "ref: refs/pull"
hasnt "and never checks out a head branch either"       "$refresh" "head.ref"
hasnt "no model runs in it"                             "$refresh" "revloop review"

has "a scripted run names the App it cannot register for you" \
  "$out" "revloop auth login --owner acme --role refresher"
has "and says why a blanket --yes did not cover it"  "$out" "needs a browser"

# --- what init refuses -------------------------------------------------------
#
# Each of these installs cleanly and then fails at runtime if it is not caught
# here, which is the expensive order to find out.

# A runner value it does not recognise. Rendering treats anything unknown as
# hosted while the refresher derivation matches the exact string, so a typo would
# emit hosted workflows with no refresher and the credential would expire days
# later with nothing pointing at the cause.
fixture_repo "$(config_for github_hosted codex claude)"; stub_reset
routes_init
err="$("$REVLOOP" init --dry-run 2>&1 >/dev/null)"; rc=$?
is  "an unrecognised runner value refuses"      "$rc" "1"
has "and says what the two legal values are"    "$err" "exactly github-hosted or self-hosted"
has "and why a typo is worse than an error"     "$err" "expiring weeks later"

# Only the claude adapter speaks endpoints; the others refuse at invocation.
ep='endpoints:
  kimi:
    base_url: https://api.kimi.com/coding/
    token_env: KIMI_API_KEY'
fixture_repo "$(config_for github-hosted codex claude "$ep")"; stub_reset
routes_init
yq -i '.reviewer.endpoint = "kimi"' .github/revloop.yml
git add -A && git commit -q -m ep && git push -q origin main
err="$("$REVLOOP" init --dry-run 2>&1 >/dev/null)"; rc=$?
is  "an endpoint on a harness that cannot use one refuses" "$rc" "1"
has "and names the harness and the endpoint"    "$err" "runs on 'codex', which cannot use one"
has "and gives the fix"                         "$err" "Use harness: claude with endpoint: kimi"

# --- the instructions have to agree with the warnings ------------------------
#
# `init` warns that an organisation-level rotating credential breaks every other
# repository reading it. The remediation it prints for the same secret must not
# then tell someone to create one — an instruction copied verbatim is exactly
# where an inconsistency gets acted on.
fixture_repo "$(config_for github-hosted codex claude)"; stub_reset
routes_init
route_first 'api users/*' '{"type":"Organization"}'
out="$("$REVLOOP" init --yes 2>&1)"

has "on an org, the codex credential is still seeded repository-scoped" \
  "$out" "gh secret set REVLOOP_CODEX_AUTH --repo acme/widget"
hasnt "never with --org, which is the misconfiguration init warns about" \
  "$out" "gh secret set REVLOOP_CODEX_AUTH --org"
has "and it says why, rather than leaving the flag to look arbitrary" \
  "$out" "Concurrency groups do not span repositories"

# --- rotating the right key updates the right secret -------------------------
#
# `auth rotate` takes a --role, and the two roles' keys are not interchangeable.
# Told to update APP_PRIVATE_KEY after rotating the refresher's key, someone
# following that literally puts the refresher's key material behind the loop
# App's identity — handing secrets:write to the job that reads a pull request
# diff, which is the one thing the two-App split exists to prevent.
key_secret() (
  # shellcheck source=../lib/ui.sh
  source "$HERE/../lib/ui.sh"
  # shellcheck source=../lib/auth.sh
  source "$HERE/../lib/auth.sh"
  _auth_role_key_secret "$1"
)
is  "the loop role's key lives in APP_PRIVATE_KEY"  "$(key_secret loop)" "APP_PRIVATE_KEY"
is  "and the refresher's in its own secret"        "$(key_secret refresher)" "REVLOOP_REFRESH_APP_PRIVATE_KEY"

# --- every rendered workflow is still valid YAML -----------------------------
#
# Stripping whole blocks out of a YAML file with awk is exactly the edit that
# breaks indentation, and the failure surfaces on GitHub as "invalid workflow
# file" long after anyone is watching. Asserting on content would not catch it:
# a file can contain every string this suite greps for and still not parse.
#
# A fresh fixture, because the refusal cases above deliberately leave a
# repository with no workflows in it at all.
fixture_repo "$(config_for github-hosted codex claude)"; stub_reset
routes_init
"$REVLOOP" init --yes >/dev/null 2>&1
for f in .github/workflows/revloop-*.yml; do
  if yq -e '.jobs' "$f" >/dev/null 2>&1; then
    ok "$(basename "$f") parses as YAML with a jobs block"
  else
    notok "$(basename "$f") parses as YAML with a jobs block" "valid YAML" "$(yq '.' "$f" 2>&1 | head -1)"
  fi
done

fixture_repo "$(config_for self-hosted agy codex)"; stub_reset
routes_init
"$REVLOOP" init --yes >/dev/null 2>&1
for f in .github/workflows/revloop-*.yml; do
  if yq -e '.jobs' "$f" >/dev/null 2>&1; then
    ok "$(basename "$f") still parses after the self-hosted blocks are stripped"
  else
    notok "$(basename "$f") still parses after the self-hosted blocks are stripped" \
      "valid YAML" "$(yq '.' "$f" 2>&1 | head -1)"
  fi
done
is "and runs-on survives as a list rather than a mangled string" \
  "$(yq -r '.jobs.review["runs-on"] | join(",")' .github/workflows/revloop-review.yml)" \
  "self-hosted,revloop"

finish
