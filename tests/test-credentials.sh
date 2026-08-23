#!/usr/bin/env bash
#
# Restored credentials: read them, refuse when they are nearly dead, and never
# write them back.
#
# The failure this suite guards against is quiet and expensive. Using a refresh
# token consumes it, so a leg that refreshes in flight leaves the stored copy
# invalid, carries on with a replacement it never saved, and the next scheduled
# refresh fails hours later with nothing to point at. Everything below is a pure
# decision over a credential file, so all of it is testable with no vendor, no
# network and no billed call.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/ui.sh
source "$HERE/../lib/ui.sh"
# shellcheck source=../lib/harnesses.sh
source "$HERE/../lib/harnesses.sh"
# cred_assert_present asks preflight which secret a harness needs and whether
# this runner is one that can only get it from a secret. bin/crossrev sources
# both; this suite has to as well.
# shellcheck source=../lib/preflight.sh
source "$HERE/../lib/preflight.sh"
# shellcheck source=../lib/credentials.sh
source "$HERE/../lib/credentials.sh"

pass=0 fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" || notok "$1" "contains '$3'" "$2"; }
hasnt() { [[ "$2" != *"$3"* ]] && ok "$1" || notok "$1" "does not contain '$3'" "$2"; }

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }

# A credential in the shape codex actually stores, with an access token whose
# claims say what this test needs them to say. Signed with nothing: crossrev reads
# the claims for expiry, issuer and client id and never treats them as an
# authorisation decision, so an unsigned token is the honest fixture.
fake_credential() {
  local seconds_left="$1" header payload jwt
  header="$(printf '%s' '{"alg":"RS256","typ":"JWT"}' | b64url)"
  payload="$(jq -cn --argjson exp "$(( $(date -u +%s) + seconds_left ))" \
    '{exp:$exp, iss:"https://auth.example.com", client_id:"app_test"}' | b64url)"
  jwt="$header.$payload.signature"
  jq -cn --arg a "$jwt" \
    '{OPENAI_API_KEY:null, auth_mode:"chatgpt", last_refresh:"2026-08-01T00:00:00Z",
      tokens:{access_token:$a, refresh_token:"refresh-abc", id_token:"id-abc", account_id:"acct"}}'
}

# --- reading a token -------------------------------------------------------
f="$(mktemp)"; fake_credential 86400 >"$f"

left="$(cred_seconds_left codex "$f")"
(( left > 86000 && left <= 86400 )) && ok "the expiry is read out of the access token's own claims" \
  || notok "the expiry is read out of the access token's own claims" "about 86400" "$left"

claims="$(cred_access_token_claims codex "$f")"
is "and so is the issuer, so nothing about the vendor is hardcoded" \
  "$(jq -r .iss <<<"$claims")" "https://auth.example.com"
is "and the client id, which is the other half of a refresh request" \
  "$(jq -r .client_id <<<"$claims")" "app_test"

# A base64url payload whose length is not a multiple of four still has to decode:
# the padding is stripped by the encoder and putting it back is the reader's job.
# Getting this wrong fails on some tokens and not others, which is the worst kind.
decoded_all=1
for pad in 1 2 3 4 5; do
  probe="$(jq -cn --argjson n "$pad" '{exp:1, pad:("x" * $n)}')"
  [[ "$(cred_jwt_claims "h.$(printf '%s' "$probe" | b64url).s" | jq -r .exp)" == "1" ]] \
    || { decoded_all=0; break; }
done
(( decoded_all )) && ok "a payload of any length decodes, whatever padding the encoder stripped" \
  || notok "a payload of any length decodes, whatever padding the encoder stripped" "every length" "failed at length $pad"

bad="$(mktemp)"; printf '{"tokens":{"access_token":"not-a-jwt"}}' >"$bad"
cred_seconds_left codex "$bad" >/dev/null 2>&1 \
  && notok "an unreadable token is not silently treated as fresh" "refuse" "allow" \
  || ok "an unreadable token is not silently treated as fresh"

# --- refusing rather than refreshing in flight -----------------------------
out="$( ( cred_assert_fresh codex "$f" ) 2>&1 )"; rc=$?
is  "a credential with a day left runs"         "$rc" "0"

# 930 rather than 900: the message floors the remainder to whole minutes, so a
# fixture sitting exactly on a minute boundary reports 14 whenever a second
# elapses between building it and reading it. Rare, load-dependent, and it fails
# a test that has nothing to do with clocks.
nearly="$(mktemp)"; fake_credential 930 >"$nearly"
out="$( ( cred_assert_fresh codex "$nearly" ) 2>&1 )"; rc=$?
is  "one with fifteen minutes left refuses"     "$rc" "1"
has "and says how much is left"                 "$out" "15 minutes"
has "and names the one-hour floor"              "$out" "one-hour floor"
has "and says why refreshing here is worse than stopping" \
  "$out" "consume the refresh token"
has "and names both ways out"                   "$out" "crossrev-token-refresh"
# The amendment asks for exactly this: kill the refresher for a full token
# lifetime and the next leg must fail loudly with the manual recovery named,
# rather than hanging or quietly degrading.
has "including the manual recovery, by name"    "$out" "codex login"

expired="$(mktemp)"; fake_credential -60 >"$expired"
out="$( ( cred_assert_fresh codex "$expired" ) 2>&1 )"; rc=$?
is  "an expired credential refuses too"         "$rc" "1"
has "and says so plainly rather than in negative minutes" "$out" "expired"

unreadable="$(mktemp)"; printf '{"tokens":{}}' >"$unreadable"
out="$( ( cred_assert_fresh codex "$unreadable" ) 2>&1 )"; rc=$?
is  "a credential with no readable expiry refuses" "$rc" "1"
has "rather than assuming it is fine"           "$out" "cannot reason about"

# --- restore, then discard --------------------------------------------------
#
# The scratch home is the mechanism that makes a leg read-only. The harness may
# refresh and write back on its own — there is no flag to stop it — so the copy
# it writes into has to be one nothing reads again.
unset CODEX_HOME
CROSSREV_CODEX_AUTH="$(fake_credential 86400)"; export CROSSREV_CODEX_AUTH
cred_prepare codex
is  "a restored credential lands in a scratch home, not in ~/.codex" \
  "$(dirname "${CODEX_HOME:-none}")" "$(dirname "$(mktemp -u)")"
is  "with the credential where the harness looks for it" \
  "$(jq -r '.tokens.refresh_token' "$CODEX_HOME/auth.json")" "refresh-abc"
# GNU first, BSD second. `-f` on GNU stat is a real flag meaning "file system
# status": it succeeds and prints `File: "…"`, so a BSD-first probe never reaches
# its fallback on Linux — which is how this assertion passed on a laptop for weeks
# and failed on CI's first run. `-c` is not a BSD flag, so it fails cleanly there.
is  "and readable by nobody else" \
  "$(stat -c '%a' "$CODEX_HOME/auth.json" 2>/dev/null || stat -f '%Lp' "$CODEX_HOME/auth.json")" "600"

scratch="$CODEX_HOME"

# Two legs running at once must not share a copy. They each borrow their own,
# and neither is the one the secret holds — which is what makes "several holders"
# safe as long as none of them writes.
( cred_prepare codex; printf '%s' "$CODEX_HOME" ) >/tmp/crossrev-second-home.$$
second="$(cat /tmp/crossrev-second-home.$$)"; rm -f /tmp/crossrev-second-home.$$
[[ -n "$second" && "$second" != "$scratch" ]] \
  && ok "a second leg gets its own copy rather than sharing one" \
  || notok "a second leg gets its own copy rather than sharing one" "a different scratch home" "$second"

cred_discard
[[ -d "$scratch" ]] && notok "the scratch home is gone when the leg finishes" "gone" "still there" \
  || ok "the scratch home is gone when the leg finishes"
is  "and CODEX_HOME is unset again"             "${CODEX_HOME:-unset}" "unset"

# Nothing to restore is the local and self-hosted case, where the harness has its
# own login on disk. It must cost nothing and touch nothing.
#
# RUNNER_ENVIRONMENT is unset explicitly rather than assumed absent, because this
# suite runs on GitHub Actions as often as on a laptop and GitHub sets it there.
# Without this line these two assertions would pass locally and take the hosted
# path in crossrev's own CI, which is the failure mode the block below exists for.
unset RUNNER_ENVIRONMENT
unset CROSSREV_CODEX_AUTH
cred_prepare codex
is  "with no restored credential, nothing is prepared" "${CODEX_HOME:-unset}" "unset"
cred_prepare claude
is  "and claude needs no restore at all — setup-token is long-lived" "${CODEX_HOME:-unset}" "unset"

# --- a secret that never arrived, on a runner that needed one ---------------
#
# The block above and a GitHub-hosted runner whose secret is missing are the same
# environment: nothing in CROSSREV_CODEX_AUTH, nothing to restore. cred_prepare
# cannot tell them apart, so it returns 0 for both — right on a laptop, and on a
# hosted runner it starts the harness with no credential at all. Nothing here
# fails; the leg dies later inside the vendor CLI with a 401 that names nothing
# about crossrev, after a checkout, a harness install and a prompt build.
#
# RUNNER_ENVIRONMENT separates them and GitHub sets it, not crossrev:
# github-hosted on GitHub's own runners, self-hosted on the operator's. It is the
# same vocabulary the `runner:` config key already uses. Reading GITHUB_ACTIONS
# instead would be wrong — it is true on both, and a self-hosted runner has no
# secret on purpose, which is why the review template filters those env lines out
# of the workflow it generates for one.
#
# Everything below runs cred_prepare in a subshell, because refusing is a ui_die
# and ui_die exits.

# Two shapes, because a missing secret produces both. A workflow referencing a
# secret that does not exist gets the empty string — GitHub expands the reference
# rather than dropping the variable — and a hand-edited workflow that lost the
# env line gets nothing at all.
for shape in empty unset; do
  out="$(
    (
      export RUNNER_ENVIRONMENT=github-hosted
      if [[ "$shape" == "empty" ]]; then export CROSSREV_CODEX_AUTH=""; else unset CROSSREV_CODEX_AUTH; fi
      cred_prepare codex
    ) 2>&1
  )"; rc=$?
  is  "a codex secret that is $shape on a hosted runner stops the leg" "$rc" "1"
  has "and the $shape case names the secret that did not arrive" "$out" "CROSSREV_CODEX_AUTH"
done

has "and says the secret is what is missing, not the credential inside it" \
  "$out" "secret"
has "and names the runner it is talking about"  "$out" "github-hosted"
has "and gives the command that fixes it"       "$out" "gh secret set"
# Rule 5 applied to the message: the failure a reader has to be talked out of is
# assuming the harness is broken, because that is what the 401 looks like.
hasnt "and does not blame the harness for it"   "$out" "not installed"

# The same hole, one harness wider. claude has no cred_prepare branch at all —
# the case statement handles codex and falls through — so a claude leg with no
# CLAUDE_CODE_OAUTH_TOKEN gets the identical silent pass.
out="$(
  ( export RUNNER_ENVIRONMENT=github-hosted; unset CLAUDE_CODE_OAUTH_TOKEN; cred_prepare claude ) 2>&1
)"; rc=$?
is  "a missing claude token on a hosted runner stops the leg too" "$rc" "1"
has "and names the token it wanted"             "$out" "CLAUDE_CODE_OAUTH_TOKEN"

# --- and the three cases that must stay silent ------------------------------
#
# A guard that fires on a laptop is worse than no guard: it breaks the path the
# early return was written for in the first place.
rc=0; ( unset RUNNER_ENVIRONMENT; unset CROSSREV_CODEX_AUTH; cred_prepare codex ) >/dev/null 2>&1 || rc=$?
is  "a laptop with no secret is still none of crossrev's business" "$rc" "0"

rc=0; ( export RUNNER_ENVIRONMENT=self-hosted; unset CROSSREV_CODEX_AUTH; cred_prepare codex ) >/dev/null 2>&1 || rc=$?
is  "and a self-hosted runner keeps its own login on disk" "$rc" "0"

rc=0; (
  export RUNNER_ENVIRONMENT=github-hosted
  CROSSREV_CODEX_AUTH="$(fake_credential 86400)"; export CROSSREV_CODEX_AUTH
  cred_prepare codex
) >/dev/null 2>&1 || rc=$?
is  "and a hosted runner whose secret did arrive runs" "$rc" "0"

# --- what reaches the harness process ---------------------------------------
#
# The workflow hands one process every credential the pairing might need, and
# that process is the one reading attacker-controlled text. Each leg should hold
# exactly what it needs to authenticate and nothing else, so a prompt injection
# that reaches tool use finds no other vendor's token to exfiltrate.
strip_for() { cred_env_strip_for "$1" | tr '\n' ' ' | sed 's/ $//'; }

has "claude sheds the codex credential"          "$(strip_for claude)" "CROSSREV_CODEX_AUTH"
hasnt "and keeps the token it authenticates with" "$(strip_for claude)" "CLAUDE_CODE_OAUTH_TOKEN"
# Not stripped for claude on purpose: it is the operator's own environment, not
# something a workflow injected, and removing it would quietly move a local run
# from API billing to subscription billing.
hasnt "and leaves the operator's own API key alone" "$(strip_for claude)" "ANTHROPIC_API_KEY"

has "codex sheds claude's token"                 "$(strip_for codex)" "CLAUDE_CODE_OAUTH_TOKEN"
has "and the anthropic key, which is a foreign vendor's here" "$(strip_for codex)" "ANTHROPIC_API_KEY"
# Stripped even from codex: by then the credential is in CODEX_HOME, so the copy
# in the environment is a second one nothing needs.
has "and even its own raw credential, already written to CODEX_HOME" \
  "$(strip_for codex)" "CROSSREV_CODEX_AUTH"

has "agy sheds every credential, holding none of them" "$(strip_for agy)" "CLAUDE_CODE_OAUTH_TOKEN"
has "including the codex one"                    "$(strip_for agy)" "CROSSREV_CODEX_AUTH"

# openssl belongs to the core requirement set, not to the incidental one.
#
# This suite is the natural home for the assertion because this suite covers the
# code that needs it: the decode below runs on the leg path, so a runner without
# openssl fails mid-leg on `command not found` rather than at the preflight that
# exists to catch exactly that.
grep -qE '^\s*for t in .*\bopenssl\b' "$HERE/../lib/preflight.sh" \
  && ok "openssl is in preflight's core requirement set" \
  || notok "openssl is in preflight's core requirement set" \
           "a core loop naming openssl" \
           "$(grep -nE '^\s*for t in ' "$HERE/../lib/preflight.sh")"

grep -q 'openssl base64 -d' "$HERE/../lib/credentials.sh" \
  && ok "and the leg-path decode is what requires it" \
  || notok "and the leg-path decode is what requires it" \
           "an openssl decode in credentials.sh" "not found"

# --- an archetype-A credential stages without an expiry to read -------------
#
# opencode's auth.json holds {type, key} entries with no JWT inside, so there
# is no exp claim to reason about and the descriptor says so: assert_fresh is
# false. Staging one must not be a freshness question. Its staging path also
# carries a directory — opencode/auth.json, not a bare auth.json — so the
# staging write has to create that directory rather than assume it.
save_data="${XDG_DATA_HOME:-}"; unset XDG_DATA_HOME
unset CODEX_HOME
CROSSREV_OPENCODE_AUTH="$(jq -cn '.opencode = {type:"api", key:"stub"}')"; export CROSSREV_OPENCODE_AUTH
cred_prepare opencode; rc=$?
is "staging an archetype-A credential does not demand an expiry" "$rc" "0"
staged="${XDG_DATA_HOME:-}"
is  "and lands under the env var the descriptor names, inside its directory" \
    "$(jq -r '.opencode.key' "$staged/opencode/auth.json" 2>/dev/null)" "stub"
is  "readable by nobody else" \
    "$(stat -c '%a' "$staged/opencode/auth.json" 2>/dev/null || stat -f '%Lp' "$staged/opencode/auth.json")" "600"
rm -rf "$CRED_SCRATCH"; CRED_SCRATCH=""
unset CROSSREV_OPENCODE_AUTH XDG_DATA_HOME
[[ -n "$save_data" ]] && export XDG_DATA_HOME="$save_data" || true

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
