# shellcheck shell=bash
# lib/credentials.sh — subscription credentials that have to survive a runner.
#
# Three of the four harnesses authenticate with a rotating OAuth credential
# rather than a static key, and using a refresh token consumes it: the vendor
# hands back a replacement and invalidates what you presented. One holder is
# fine. Several copies are not — the first to refresh kills every other copy,
# and a job holding a dead one that writes back overwrites the good one.
#
# So the rule this file exists to enforce: **a leg restores, reads, and
# discards.** It never refreshes and it never writes back. Exactly one job — the
# refresher workflow, on its own concurrency group — writes the stored
# credential, and it is the only caller of cred_refresh_*.
#
# Nothing here is needed on a laptop or a self-hosted runner, where each harness
# keeps its own credential on disk and refreshes it the ordinary way. Every
# function returns quietly when no restored credential is in play, so the local
# path costs nothing.

CRED_SCRATCH=""

# An hour. A leg with less than this refuses rather than running, because the
# refresh it would trigger mid-flight is the one that breaks the chain.
CRED_MIN_SECONDS=3600

# ---------------------------------------------------------------------------
# Reading a token without trusting it
# ---------------------------------------------------------------------------
#
# The claims are read for expiry, issuer and client id only. Nothing here treats
# a claim as an authorisation decision — the vendor does that — so an unsigned
# read is the right tool and verifying the signature would need the vendor's
# JWKS for no gain.

_cred_b64url_decode() {
  local s="$1"
  case $(( ${#s} % 4 )) in
    2) s="$s==" ;;
    3) s="$s=" ;;
    1) return 1 ;;   # not a valid base64url length
  esac
  printf '%s' "$s" | tr '_-' '/+' | openssl base64 -d -A 2>/dev/null
}

# The claims of a JWT, as JSON. Non-zero if it does not look like one.
cred_jwt_claims() {
  local jwt="$1" payload
  [[ "$jwt" == *.*.* ]] || return 1
  payload="${jwt#*.}"; payload="${payload%%.*}"
  _cred_b64url_decode "$payload" | jq -c . 2>/dev/null || return 1
}

# Codex stores its credential as {tokens:{access_token,refresh_token,id_token,
# account_id}, OPENAI_API_KEY, auth_mode, last_refresh}. The access token is a
# JWT and carries everything the refresher needs to address its own vendor.
cred_codex_claims() {
  local file="$1" token
  token="$(jq -r '.tokens.access_token // empty' "$file" 2>/dev/null)"
  [[ -n "$token" ]] || return 1
  cred_jwt_claims "$token"
}

# Seconds until the stored access token expires. Negative when it already has.
cred_seconds_left() {
  local file="$1" claims exp now
  claims="$(cred_codex_claims "$file")" || return 1
  exp="$(jq -r '.exp // empty' <<<"$claims")"
  [[ -n "$exp" ]] || return 1
  now="$(date -u +%s)"
  printf '%s' "$(( exp - now ))"
}

_cred_human_duration() {
  local s="$1"
  if (( s < 0 ));      then printf 'expired'
  elif (( s < 3600 )); then printf '%d minutes' "$(( s / 60 ))"
  elif (( s < 172800 )); then printf '%d hours' "$(( s / 3600 ))"
  else printf '%d days' "$(( s / 86400 ))"
  fi
}

# ---------------------------------------------------------------------------
# Restore, read, discard
# ---------------------------------------------------------------------------

# Put a restored credential where the harness looks for it, in a directory that
# is thrown away when the leg finishes.
#
# The scratch home is the mechanism, not hygiene: the harness may well refresh
# and write back on its own, and there is no flag to stop it. Writing into a
# throwaway copy means that write reaches a directory nobody reads again,
# instead of a file something later hands back to the secret.
cred_prepare() {
  local harness="$1"
  case "$harness" in
    codex)
      [[ -n "${CROSSREV_CODEX_AUTH:-}" ]] || return 0
      CRED_SCRATCH="$(mktemp -d)"
      (umask 077; printf '%s' "$CROSSREV_CODEX_AUTH" >"$CRED_SCRATCH/auth.json")
      cred_assert_fresh codex "$CRED_SCRATCH/auth.json"
      export CODEX_HOME="$CRED_SCRATCH"
      ;;
    *) return 0 ;;
  esac
}

# Which environment variables must not reach a given harness.
#
# The workflow hands every credential the pairing might need to one process, and
# each leg needs exactly one of them. The rest are for a harness that is not
# running, so passing them on is pure exposure: the agent process is the one
# reading attacker-controlled text, and a prompt injection that reaches tool use
# can read its own environment. A model that never sees the other vendor's token
# cannot be talked into exfiltrating it.
#
# CROSSREV_CODEX_AUTH is stripped even from codex, because by then the credential
# has been written into CODEX_HOME and the raw copy in the environment is a
# second one nobody needs.
cred_env_strip_for() {
  local harness="$1" v
  for v in CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_API_KEY CROSSREV_CODEX_AUTH; do
    case "$harness:$v" in
      # What each harness legitimately needs, and nothing more.
      #
      # ANTHROPIC_API_KEY stays for claude deliberately. It is the operator's own
      # environment rather than something a workflow injected, and stripping it
      # would silently move a local run from API billing to subscription billing
      # — a substitution nobody asked for, in a tool whose divergence guard
      # exists to catch exactly that class of quiet swap. For the other harnesses
      # it is a foreign vendor's credential and goes.
      claude:CLAUDE_CODE_OAUTH_TOKEN|claude:ANTHROPIC_API_KEY) continue ;;
    esac
    printf '%s\n' "$v"
  done
}

cred_discard() {
  [[ -n "$CRED_SCRATCH" ]] || return 0
  rm -rf "$CRED_SCRATCH"
  CRED_SCRATCH=""
  unset CODEX_HOME
}

# Refuse rather than refresh in flight.
#
# An in-flight rotation invalidates the stored copy silently: this job carries
# on with the replacement it never saved, the secret still holds the consumed
# one, and the next scheduled refresh fails with nothing to point at. Stopping
# here is loud, cheap and recoverable.
cred_assert_fresh() {
  local harness="$1" file="$2" left
  left="$(cred_seconds_left "$file")" || ui_die \
    "the restored $harness credential does not carry a readable expiry" \
    "crossrev reads the access token's exp claim to decide whether it is safe to run. A credential it cannot read is one it cannot reason about, so it stops. Re-seed the secret from a fresh \`$harness login\`."

  if (( left < CRED_MIN_SECONDS )); then
    ui_die "the restored $harness credential has $(_cred_human_duration "$left") left, under crossrev's one-hour floor" \
      "Refreshing it here would consume the refresh token and leave the stored copy dead, so this leg stops instead. Run the crossrev-token-refresh workflow, or re-seed the secret with \`$harness login\` on a machine with a browser."
  fi
  return 0
}

# ---------------------------------------------------------------------------
# The single writer
# ---------------------------------------------------------------------------
#
# Only the refresher workflow reaches this. Everything about it is derived from
# the credential rather than hardcoded: the issuer and the client id come out of
# the access token's own claims, and the token endpoint comes from the issuer's
# OpenID discovery document. That is not fastidiousness — the endpoint is not
# where the obvious guess puts it (it is /api/accounts/oauth/token, not
# /oauth/token), so a hardcoded URL would have shipped broken.

_cred_discovery_token_endpoint() {
  local issuer="$1"
  curl -fsS --max-time 20 "${issuer%/}/.well-known/openid-configuration" 2>/dev/null \
    | jq -r '.token_endpoint // empty'
}

# Refresh a codex credential. Reads the current one from $1, prints the new one.
#
# Prints nothing and returns non-zero on any failure, so a caller cannot mistake
# a half-answer for a credential and write it back over the good one.
#
# Every diagnostic below goes to stderr, and that is load-bearing rather than
# tidy: stdout is the return value. A reason written to stdout is captured into
# the caller's `new="$(cred_refresh_codex …)"` and thrown away with it, leaving
# the operator the generic failure and none of the detail — which defeats the
# whole point of reading the body, since token_expired and invalid_client need
# different fixes and the difference is only in there.
cred_refresh_codex() {
  local file="$1" claims issuer client_id endpoint refresh_token resp
  command -v curl >/dev/null 2>&1 || ui_die \
    "curl is not installed, and refreshing a credential is an HTTP call to the vendor" \
    "Install curl. Every runner family ships it; this is only reachable on an unusual image."

  claims="$(cred_codex_claims "$file")" || { ui_say "the stored credential has no readable access token" >&2; return 1; }
  issuer="$(jq -r '.iss // empty' <<<"$claims")"
  client_id="$(jq -r '.client_id // empty' <<<"$claims")"
  refresh_token="$(jq -r '.tokens.refresh_token // empty' "$file")"

  [[ -n "$issuer" && -n "$client_id" && -n "$refresh_token" ]] || {
    ui_say "the stored credential is missing an issuer, a client id or a refresh token" >&2
    return 1
  }

  # `|| true` so an unreachable discovery document reaches the check below rather
  # than the caller's `set -e`. It is suppressed today, because every caller runs
  # this on the left of a `||` — but that is a property of the callers, not of
  # this function, and the next caller should not have to know it.
  endpoint="$(_cred_discovery_token_endpoint "$issuer" || true)"
  [[ -n "$endpoint" ]] || { ui_say "could not read a token endpoint from $issuer's discovery document" >&2; return 1; }

  # No -f: a rejection comes back as a JSON body naming the reason, and -f throws
  # that body away in favour of an exit code. "token_expired" and
  # "invalid_client" need different fixes, and the difference is only in the body.
  local http
  resp="$(jq -cn --arg ct "refresh_token" --arg cid "$client_id" --arg rt "$refresh_token" \
            '{grant_type:$ct, client_id:$cid, refresh_token:$rt, scope:"openid profile email offline_access"}' \
          | curl -sS --max-time 60 -w '\n%{http_code}' -X POST "$endpoint" \
              -H 'Content-Type: application/json' --data-binary @- 2>/dev/null)" \
    || { ui_say "could not reach $endpoint at all" >&2; return 1; }
  http="${resp##*$'\n'}"; resp="${resp%$'\n'*}"

  if [[ "$http" != 2* ]]; then
    ui_say "the vendor rejected the refresh (HTTP $http): $(jq -r '.error.message // .error_description // .error // "no reason given"' <<<"$resp" 2>/dev/null)" >&2
    return 1
  fi

  local new_access new_refresh new_id
  new_access="$(jq -r '.access_token // empty' <<<"$resp")"
  new_refresh="$(jq -r '.refresh_token // empty' <<<"$resp")"
  new_id="$(jq -r '.id_token // empty' <<<"$resp")"
  [[ -n "$new_access" ]] || { ui_say "the vendor's response carried no access token" >&2; return 1; }

  # A response that returns no replacement refresh token means this one was not
  # consumed, so keeping it is correct rather than a fallback.
  [[ -n "$new_refresh" ]] || new_refresh="$refresh_token"
  [[ -n "$new_id" ]] || new_id="$(jq -r '.tokens.id_token // empty' "$file")"

  jq -c --arg a "$new_access" --arg r "$new_refresh" --arg i "$new_id" \
        --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '.tokens.access_token = $a
     | .tokens.refresh_token = $r
     | .tokens.id_token = $i
     | .last_refresh = $now' "$file"
}
