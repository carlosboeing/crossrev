# shellcheck shell=bash
# lib/auth.sh — GitHub App creation and key storage.
#
# One App per owner, never one globally and never one per repository. The
# private key belongs to the App, so whoever holds it can mint a token for any
# installation of that App. Per-owner matches the boundary GitHub already draws:
# a personal App for personal repos, an org-owned App whose key lives in that
# org's secrets, and a separate one for any client org later. A leak in one
# cannot reach another.
#
# The flow is: register, install, verify. All three open a browser at a URL that
# is already correct, and the terminal follows along on its own. Nothing is
# copied, pasted, or clicked out of a message.
#
# One App per owner became one App per owner **per role**, and that refines the
# rule rather than breaking it. The loop App is referenced by the jobs that check
# out a pull request branch and run a model over a diff; the refresher App can
# write a secret. Putting Secrets: write on the loop App would put secret
# rewriting one injection away from attacker-controlled text. The refresher's own
# workflow reads nothing untrusted at all — no checkout of the branch, no model,
# no diff, no comments — which is what makes the stronger permission safe there.

_auth_dir() { printf '%s/crossrev/apps' "${XDG_CONFIG_HOME:-$HOME/.config}"; }

# Where a role's key and metadata live: <owner>.<role>.pem.
#
# Keys registered before roles existed sit at <owner>.pem and belong to the loop,
# so the loop role reads that path when the roled one is absent. New
# registrations always write the roled path, so the legacy name dies out on its
# own rather than needing a migration step nobody would run.
_auth_pem() {
  local owner="$1" role="${2:-loop}" dir; dir="$(_auth_dir)"
  if [[ "$role" == "loop" && ! -f "$dir/$owner.$role.pem" && -f "$dir/$owner.pem" ]]; then
    printf '%s/%s.pem' "$dir" "$owner"; return 0
  fi
  printf '%s/%s.%s.pem' "$dir" "$owner" "$role"
}

_auth_meta() {
  local owner="$1" role="${2:-loop}" dir; dir="$(_auth_dir)"
  if [[ "$role" == "loop" && ! -f "$dir/$owner.$role.json" && -f "$dir/$owner.json" ]]; then
    printf '%s/%s.json' "$dir" "$owner"; return 0
  fi
  printf '%s/%s.%s.json' "$dir" "$owner" "$role"
}

# What each role is for, and what it is allowed to do.
#
# The refresher gets `secrets`, which is GitHub's **repository** secret
# permission. `organization_secrets` is a separate permission and is deliberately
# not requested, because a rotating credential stored at organisation level would
# be refreshed by every repository that reads it. Concurrency groups are
# repository-scoped, so several repositories means several writers, and the first
# one to refresh invalidates the rest — the exact collision the single writer
# exists to prevent. One credential, one repository, one writer.
_auth_role_permissions() {
  case "$1" in
    loop)      jq -cn '{contents:"write", issues:"write", pull_requests:"write"}' ;;
    refresher) jq -cn '{secrets:"write"}' ;;
    *) ui_die "unknown App role '$1'" "Roles are: loop (the review and resolve jobs) and refresher (the credential refresh job)." ;;
  esac
}

_auth_role_summary() {
  case "$1" in
    loop)      printf 'contents:write, issues:write, pull_requests:write' ;;
    refresher) printf 'secrets:write (repository secrets only)' ;;
  esac
}

# Which secret carries a role's private key.
#
# Named per role rather than assumed, because the two are not interchangeable and
# the consequence of confusing them is not a broken deploy — it is the refresher's
# key material sitting behind the loop App's identity, which is the exact
# privilege separation the two Apps exist to draw.
_auth_role_key_secret() {
  case "$1" in
    loop)      printf 'APP_PRIVATE_KEY' ;;
    refresher) printf 'CROSSREV_REFRESH_APP_PRIVATE_KEY' ;;
  esac
}

# The name is display text and takes the product name (ADR 0010): it is what a
# person reads in an organisation's installed Apps list, beside `Claude` and
# `Vercel`, and it is in none of the categories that ADR keeps lowercase.
#
# The spaces are safe, and that is load-bearing rather than incidental. GitHub
# derives the slug by lowercasing and turning spaces into hyphens, so every
# spelling here yields the slug the lowercase form did — and the slug is what
# `state_trusted_author` matches literally. Verified against `GET /app` on a live
# App renamed through all four spellings; asserted offline in tests/test-auth.sh
# so it cannot drift.
#
# The owner half keeps its own casing. That is an identity GitHub chose, not
# prose CrossRev gets to restyle.
_auth_role_default_name() {
  case "$1" in
    loop)      printf 'CrossRev %s' "$2" ;;
    refresher) printf 'CrossRev Refresh %s' "$2" ;;
  esac
}

# Derive a slug from an App name: lowercase, spaces to hyphens.
_auth_slug() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | tr ' ' '-'
}

# The owner is detected, not asked, because the repository's owner is the trust
# boundary the private key should sit on. --owner overrides.
_auth_detect_owner() {
  gh repo view --json owner --jq .owner.login 2>/dev/null || return 1
}

# "<type> <id>" for an account (user, organisation, or bot). /users/ resolves
# all three and returns the numeric id and type. The id prefills the install
# page with the right target.
_auth_account_info() {
  local info
  info="$(gh api "users/$1" --jq '"\(.type // empty) \(.id // empty)"' 2>/dev/null)" || return 1
  [[ "$info" != " " && -n "$info" ]] || return 1
  printf '%s' "$info"
}

# Where to install an App.
#
# /apps/<slug>/installations/new/permissions with target_id and target_type
# lands directly on the install form with the account already chosen. It works
# for private Apps, which the owner-settings path also does but without the
# prefill — one fewer decision for someone who has just approved permissions.
_auth_install_url() {
  local slug="$1" owner_type="$2" owner_id="$3"
  printf 'https://github.com/apps/%s/installations/new/permissions?target_id=%s&target_type=%s' \
    "$slug" "$owner_id" "$owner_type"
}

_html_attr_escape() {
  printf '%s' "$1" \
    | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g'
}

_open_browser() {
  local url="$1"
  if command -v open >/dev/null 2>&1; then open "$url" >/dev/null 2>&1
  elif command -v xdg-open >/dev/null 2>&1; then xdg-open "$url" >/dev/null 2>&1
  else return 1
  fi
}

# ---------------------------------------------------------------------------
# App authentication — a JWT signed with the private key
# ---------------------------------------------------------------------------
#
# An App authenticates as itself with a short-lived RS256 JWT. This is what lets
# crossrev confirm an installation actually landed rather than telling you to go
# and check. gh honours an Authorization header we set, so this needs no curl.

_b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }

_auth_jwt() {
  local pem="$1" app_id="$2" now header payload signing_input sig
  now="$(date +%s)"
  header='{"alg":"RS256","typ":"JWT"}'
  # Backdated 60s because GitHub rejects a JWT whose iat is in the future, and
  # clock skew between here and GitHub is not ours to control.
  payload="$(jq -cn --argjson iat "$((now - 60))" --argjson exp "$((now + 540))" \
    --argjson iss "$app_id" '{iat:$iat, exp:$exp, iss:$iss}')"
  signing_input="$(printf '%s' "$header" | _b64url).$(printf '%s' "$payload" | _b64url)"
  sig="$(printf '%s' "$signing_input" | openssl dgst -sha256 -sign "$pem" -binary | _b64url)"
  printf '%s.%s' "$signing_input" "$sig"
}

# Accounts this App is installed on, one per line as "<login> <selection>".
_auth_installations() {
  local jwt="$1"
  gh api -H "Authorization: Bearer $jwt" /app/installations \
    --jq '.[] | "\(.account.login) \(.repository_selection)"' 2>/dev/null || return 1
}

# What the App calls itself, as GitHub has it: name on the first line, slug on
# the second. Authoritative, and reachable with the key already on disk — the
# same JWT the installations call above is already holding.
_auth_app_identity() {
  local jwt="$1" name slug
  { read -r name; read -r slug; } < <(
    gh api -H "Authorization: Bearer $jwt" /app --jq '.name, .slug' 2>/dev/null)
  # A reachable API that answered with neither is not evidence of anything, and
  # an empty slug written back would be worse than the stale one it replaced.
  [[ -n "$name" && -n "$slug" ]] || return 1
  printf '%s\n%s\n' "$name" "$slug"
}

# Reconcile the cached identity against the authoritative one, correcting it.
#
# Prints one line per field that moved, as tab-separated "<field> <was> <now>",
# and nothing at all when the two agree — so a caller tests for output rather
# than remembering which way a return code points. Tabs rather than spaces
# because an App name is free text and routinely contains spaces: CrossRev
# proposes one that does.
#
# It writes, which a status command otherwise does not. The justification is that
# this file is CrossRev's own cache of a fact GitHub owns, not operator config,
# and the cached slug is what `state_trusted_author` falls back to. Diagnosing
# that drift and then leaving it in place would report a fault it had already
# found and could have fixed, and the only repair left would be editing JSON by
# hand.
_auth_sync_meta() {
  local meta="$1" name="$2" slug="$3" was_name was_slug drift=""
  was_name="$(jq -r '.name // empty' "$meta" 2>/dev/null)"
  was_slug="$(jq -r '.slug // empty' "$meta" 2>/dev/null)"

  [[ "$was_name" == "$name" ]] || drift+="name"$'\t'"$was_name"$'\t'"$name"$'\n'
  [[ "$was_slug" == "$slug" ]] || drift+="slug"$'\t'"$was_slug"$'\t'"$slug"$'\n'
  [[ -n "$drift" ]] || return 0

  # umask rather than create-then-chmod, and a rename rather than a truncate in
  # place, so nothing ever reads a half-written file or one briefly wider than
  # the 0600 the original was created with.
  (umask 077; jq --arg n "$name" --arg s "$slug" '.name = $n | .slug = $s' \
    "$meta" >"$meta.tmp" && mv "$meta.tmp" "$meta") || return 1

  printf '%s' "$drift"
}

# ---------------------------------------------------------------------------
# A one-shot local listener
# ---------------------------------------------------------------------------
#
# The design deferred this, betting that pasting one value was a small cost. It
# is not: the browser lands on ERR_CONNECTION_REFUSED, which reads as broken no
# matter how well the terminal warned you, and the reflex is to hit Reload
# rather than read the address bar. Accepting one connection is about forty
# lines and is not a web server.
#
# It stays best-effort. Every failure falls through to the paste, so it cannot
# make anything worse than it already was.

_port_free() { ! nc -z localhost "$1" >/dev/null 2>&1; }

_free_port() {
  local p
  for p in 33517 33518 33519 33520 33521 33522; do
    _port_free "$p" && { printf '%s' "$p"; return 0; }
  done
  return 1
}

_listener_available() { command -v nc >/dev/null 2>&1; }

# The page the browser lands on. Deliberately theme-aware and plain: it exists
# to say "this worked, go back to your terminal" and nothing else.
_auth_done_page() {
  cat <<'HTML'
<!doctype html>
<html><head><meta charset="utf-8"><title>crossrev</title><style>
:root{color-scheme:light dark}
body{font:16px/1.6 system-ui,-apple-system,sans-serif;margin:0;min-height:100vh;
display:grid;place-items:center;background:#fbfbfa;color:#1f1b16}
@media (prefers-color-scheme:dark){body{background:#16130f;color:#f0ece4}}
.c{max-width:26rem;padding:2rem;text-align:center}
h1{font-size:1.25rem;margin:0 0 .5rem}
p{margin:.5rem 0;opacity:.75}
.t{margin-top:1.5rem;font-size:.875rem;opacity:.55}
</style></head><body><div class="c">
<h1>Registered</h1>
<p>crossrev has the App details and is carrying on in your terminal.</p>
<p class="t">You can close this tab.</p>
</div></body></html>
HTML
}

# Listen on $1 until a request carrying the OAuth code arrives, writing every
# raw request to $2. Returns non-zero if nothing matching arrived in time.
#
# `nc -k` keeps the socket open across connections, and that detail is the whole
# design. One connection is not safe to assume: browsers open speculative ones
# and anything on the machine can probe a listening port. Serving a single
# connection and re-binding in a loop was tried and is wrong — the re-bind takes
# long enough that a request arriving in the gap gets connection-refused, which
# is exactly the failure this listener exists to remove. Measured: a decoy
# request followed a second later by the real one lost the real one every time.
#
# The cost of -k is that only the first connection receives the response body,
# because nc's stdin is consumed once. That is the right trade. In the real flow
# the redirect *is* the first connection, so it gets the page; anything later is
# a favicon request nobody sees fail. And the worst case — a decoy arriving
# first — is a blank tab with the flow still completing on its own, rather than
# a missed code and a fall back to pasting.
_listen_for_code() {
  local port="$1" outfile="$2" total="${3:-300}"
  local body len pid waited=0
  body="$(_auth_done_page)"
  # Byte count, not character count: Content-Length is in bytes, and a wrong one
  # truncates the page in the browser.
  len="$(printf '%s' "$body" | wc -c | tr -d ' ')"

  {
    printf 'HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: %s\r\nConnection: close\r\n\r\n%s' \
      "$len" "$body" | nc -k -l "$port" >"$outfile" 2>/dev/null
  } &
  pid=$!

  while (( waited < total )); do
    grep -q 'code=' "$outfile" 2>/dev/null && break
    # nc gone without a code means it never bound, or this build has no -k.
    # Either way there is nothing left to wait for; the paste fallback covers it.
    kill -0 "$pid" 2>/dev/null || break
    sleep 1
    (( waited++ )) || true
  done

  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  grep -q 'code=' "$outfile" 2>/dev/null
}

# ---------------------------------------------------------------------------
# Long-lived tokens, and the date they stop working
# ---------------------------------------------------------------------------
#
# `claude setup-token` prints a token valid for a year and says plainly that you
# will not see it again. So there is nothing to inspect eleven months later and
# no way to recover it — the first sign of expiry is a CI failure on a day nobody
# is looking. Recording the creation date at the moment it is set is the only
# point at which the information exists.
#
# The ledger holds dates, never tokens.

_auth_tokens_file() { printf '%s/crossrev/tokens.json' "${XDG_CONFIG_HOME:-$HOME/.config}"; }

# auth_token_record <repo> <secret_name> <valid_days>
auth_token_record() {
  local repo="$1" name="$2" days="$3" file existing
  file="$(_auth_tokens_file)"
  mkdir -p "$(dirname "$file")"
  existing="$(cat "$file" 2>/dev/null)"; [[ -n "$existing" ]] || existing='{}'
  (umask 077; jq -c --arg r "$repo" --arg n "$name" \
      --arg c "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --argjson d "$days" \
      '.[$r] = ((.[$r] // {}) | .[$n] = {created:$c, valid_days:$d})' \
      <<<"$existing" >"$file.tmp" && mv "$file.tmp" "$file")
}

# Days until a recorded token expires, or non-zero if nothing was recorded.
auth_token_days_left() {
  local repo="$1" name="$2" entry created days start now
  entry="$(jq -c --arg r "$repo" --arg n "$name" '.[$r][$n] // empty' \
    "$(_auth_tokens_file)" 2>/dev/null)"
  [[ -n "$entry" ]] || return 1
  created="$(jq -r '.created' <<<"$entry")"
  days="$(jq -r '.valid_days' <<<"$entry")"
  # BSD date wants -j -f; GNU date wants -d. Try both rather than assuming which
  # machine this is, since the same command runs on a laptop and on a runner.
  start="$(date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$created" +%s 2>/dev/null \
        || date -u -d "$created" +%s 2>/dev/null)" || return 1
  now="$(date -u +%s)"
  printf '%s' "$(( days - (now - start) / 86400 ))"
}

# ---------------------------------------------------------------------------
# crossrev auth status
# ---------------------------------------------------------------------------

auth_status() {
  local dir; dir="$(_auth_dir)"

  if [[ ! -d "$dir" ]] || ! compgen -G "$dir/*.json" >/dev/null 2>&1; then
    ui_section "Apps"
    ui_opt "none configured"
    ui_gap
    ui_line "CrossRev needs an App only for automated mode — the loop running on"
    ui_line "GitHub events. Local runs use your own gh authentication."
    ui_end "Set one up with:   crossrev auth login"
    _auth_status_tokens
    return 0
  fi

  ui_section "Apps"
  local meta owner role owner_type owner_id id name slug pem mode jwt installs
  local identity real_name real_slug drift field was now
  for meta in "$dir"/*.json; do
    # Owner and role come out of the file rather than out of its name. Anything
    # registered before roles existed has no role key, and it is the loop's.
    owner="$(jq -r .owner "$meta")"
    role="$(jq -r '.role // "loop"' "$meta")"
    owner_type="$(jq -r .owner_type "$meta")"
    owner_id="$(jq -r '.owner_id // empty' "$meta")"
    id="$(jq -r .id "$meta")"
    name="$(jq -r .name "$meta")"
    slug="$(jq -r .slug "$meta")"
    pem="$(_auth_pem "$owner" "$role")"

    # Rule 5, before the first line is printed rather than after: the name and
    # slug just read came out of a file written once, at creation. Renaming the
    # App in its settings moves both and nothing local notices, so `auth status`
    # — the command whose entire job is answering "is my credential set up
    # correctly" — would answer confidently from a file that can be wrong.
    # Revalidate against GET /app first, and report what came back.
    jwt=""; drift=""
    if [[ -f "$pem" ]] && jwt="$(_auth_jwt "$pem" "$id")" \
       && identity="$(_auth_app_identity "$jwt")"; then
      real_name="$(sed -n 1p <<<"$identity")"
      real_slug="$(sed -n 2p <<<"$identity")"
      drift="$(_auth_sync_meta "$meta" "$real_name" "$real_slug")"
      # Report the identity GitHub has, including in the install URL below,
      # which was being built from the stale slug too.
      [[ -z "$drift" ]] || { name="$real_name"; slug="$real_slug"; }
    fi

    ui_ok "$owner — $name (id $id, role $role: $(_auth_role_summary "$role"))"

    if [[ -n "$drift" ]]; then
      while IFS=$'\t' read -r field was now; do
        [[ -n "$field" ]] || continue
        ui_line "   $field was $was, now $now"
      done <<<"$drift"
      ui_warn \
        "$owner's App was renamed since CrossRev recorded it — the cached copy has been corrected" \
        "The slug is the half that matters. state_trusted_author falls back to it when CROSSREV_APP_SLUG is unset, so an automated run started from this machine was trusting an author that does not exist: no markers read, pass 1 for ever, nothing reconciled. Generated workflows pass the slug from the token step's app-slug output and were never affected."
    fi

    if [[ -f "$pem" ]]; then
      # stat drops the leading zero, and "600" next to a sentence about 0600
      # reads as a mismatch. Same number, printed the way it is talked about.
      #
      # GNU first, BSD second, and the order is load-bearing. `-f` on GNU stat
      # means "file system status" — a real flag that SUCCEEDS and prints
      # `File: "…"`, so a BSD-first probe never reaches its fallback on Linux and
      # this check warned that a correctly-moded key was wrong. `-c` is not a BSD
      # flag at all, so it fails cleanly and the fallback fires. Same reversal at
      # the two other sites; see tests/test-credentials.sh.
      mode="$(printf '%04d' "$(stat -c '%a' "$pem" 2>/dev/null || stat -f '%Lp' "$pem" 2>/dev/null)")"
      ui_line "   key $pem${mode:+ ($mode)}"
      [[ "$mode" == "0600" ]] || ui_warn \
        "the private key for $owner is mode $mode, not 0600" \
        "Any process running as you can read it, and it can mint a token for every repository this App is installed on. Fix with: chmod 600 $pem"

      # Rule 5: report what is true, not what was configured. An App installed
      # nowhere looks identical to a working one until the first API call fails.
      if [[ -n "$jwt" ]] && installs="$(_auth_installations "$jwt")"; then
        if [[ -n "$installs" ]]; then
          while read -r acct sel; do
            ui_line "   installed on $acct ($sel repositories)"
          done <<<"$installs"
        else
          ui_no "   installed nowhere — it can reach no repository at all"
          # owner_id was added after the first Apps were registered, so recover
          # it rather than degrading the message for anything created earlier.
          [[ -n "$owner_id" ]] || owner_id="$(gh api "users/$owner" --jq .id 2>/dev/null)"
          [[ -n "$owner_id" ]] && ui_next "install: $(_auth_install_url "$slug" "$owner_type" "$owner_id")"
        fi
      else
        ui_line "   could not check installations — the key may not match this App"
      fi
    else
      ui_no "   key missing at $pem — this App cannot mint a token"
    fi
  done
  ui_end "An App reaches only the repositories it is installed on."
  _auth_status_tokens
}

# The other half of "is this still going to work tomorrow".
_auth_status_tokens() {
  local file; file="$(_auth_tokens_file)"
  [[ -f "$file" ]] || return 0
  local repos repo names name left
  repos="$(jq -r 'keys[]' "$file" 2>/dev/null)" || return 0
  [[ -n "$repos" ]] || return 0

  ui_section "Long-lived tokens"
  while read -r repo; do
    names="$(jq -r --arg r "$repo" '.[$r] | keys[]' "$file")"
    while read -r name; do
      left="$(auth_token_days_left "$repo" "$name")" || continue
      if (( left < 0 )); then
        ui_no "$repo — $name expired $(( -left )) days ago"
        ui_line "   Every run authenticating with it is failing. Re-issue it and set the secret again."
      elif (( left < 60 )); then
        local h_name seed_cmd
        h_name="$(jq -r --arg s "$name" '.harnesses[] | select(.credential.secret == $s) | .name // empty' <<<"$HARNESS_JSON")"
        [[ -n "$h_name" ]] && seed_cmd="$(harness_get "$h_name" .credential.seed_command)" || seed_cmd=""
        if [[ -n "$seed_cmd" ]]; then
          ui_warn "$name on $repo expires in $left days" \
            "It cannot be re-read once issued, so nothing recovers it after the fact — the first sign of expiry is a CI failure on a day nobody is looking. Re-issue it with \`$seed_cmd\` and set the secret again."
        else
          ui_warn "$name on $repo expires in $left days" \
            "It cannot be re-read once issued, so nothing recovers it after the fact — the first sign of expiry is a CI failure on a day nobody is looking. Re-issue it and set the secret again."
        fi
      else
        ui_ok "$repo — $name, $left days left"
      fi
    done <<<"$names"
  done <<<"$repos"
  ui_end "Dates only — CrossRev never stores a token, and this one cannot be read back."
}

# ---------------------------------------------------------------------------
# crossrev auth login
# ---------------------------------------------------------------------------

auth_login() {
  local owner="" app_name="" role="loop"
  while (( $# )); do
    case "$1" in
      --owner) owner="${2:?--owner needs a value}"; shift 2 ;;
      --name)  app_name="${2:?--name needs a value}"; shift 2 ;;
      --role)  role="${2:?--role needs a value}"; shift 2 ;;
      *) ui_die "unknown option for auth login: $1" "Run: crossrev auth login [--owner <owner>] [--role loop|refresher] [--name <name>]" ;;
    esac
  done
  _auth_role_permissions "$role" >/dev/null   # rejects an unknown role before anything opens

  _ui_input_source >/dev/null || _ui_no_input

  if [[ -z "$owner" ]]; then
    owner="$(_auth_detect_owner)" || ui_die \
      "could not work out which account this App should belong to" \
      "Run this inside a git repository with a GitHub remote, or name it: crossrev auth login --owner <owner>"
  fi

  local owner_type owner_id info
  info="$(_auth_account_info "$owner")" || ui_die \
    "GitHub does not recognise the account '$owner'" \
    "Check the spelling, or pass a different one with --owner"
  read -r owner_type owner_id <<<"$info"

  local meta; meta="$(_auth_meta "$owner" "$role")"
  if [[ -f "$meta" ]]; then
    ui_section "Already configured"
    ui_ok "$owner — $(jq -r .name "$meta") (id $(jq -r .id "$meta"), role $role)"
    ui_gap
    ui_line "One App per owner per role is the design. Creating a second would mean"
    ui_line "a second private key to protect and rotate, for no extra reach."
    ui_end "See where it is installed with:   crossrev auth status"
    return 0
  fi

  # GitHub App names are globally unique, so a bare "crossrev" is very likely
  # taken. Suffixing the owner is likelier to be free and clearer in a list.
  [[ -n "$app_name" ]] || app_name="$(_auth_role_default_name "$role" "$owner")"

  local slug; slug="$(_auth_slug "$app_name")"
  if _auth_account_info "${slug}[bot]" >/dev/null 2>&1; then
    ui_die "a GitHub App named '$app_name' already exists" \
      "CrossRev cannot reuse an existing App when local metadata is missing. Register a separate App with: crossrev auth login --name <name>"
  fi

  local state; state="$(openssl rand -hex 16)"

  # Bind the port BEFORE building the manifest, so redirect_url names a port we
  # know is free. Hardcoding it first is how you get a redirect to a port
  # something else already owns, with nothing to do about it afterwards.
  local port="" use_listener=0
  if _listener_available && port="$(_free_port)"; then
    use_listener=1
  else
    port=33517
  fi
  local redirect="http://localhost:$port/crossrev-auth"

  local manifest
  manifest="$(jq -cn \
    --arg name "$app_name" \
    --arg url "https://github.com/carlosboeing/crossrev" \
    --arg redirect "$redirect" \
    --argjson perms "$(_auth_role_permissions "$role")" \
    '{
      name: $name,
      url: $url,
      redirect_url: $redirect,
      public: false,
      hook_attributes: { url: "https://example.com/unused", active: false },
      default_events: [],
      default_permissions: $perms
    }')"

  local post_url
  if [[ "$owner_type" == "Organization" ]]; then
    post_url="https://github.com/organizations/$owner/settings/apps/new"
  else
    post_url="https://github.com/settings/apps/new"
  fi

  ui_section "Register a GitHub App for $owner"
  ui_line "Owner        $owner ($owner_type)"
  ui_line "Name         $app_name (override with --name)"
  ui_line "Role         $role"
  ui_line "Permissions  $(_auth_role_summary "$role")"
  ui_line "             and nothing else"
  ui_line "Webhook      disabled. GitHub never calls CrossRev; your workflows do"
  ui_line "Visibility   private to $owner"
  ui_gap
  if [[ "$role" == "refresher" ]]; then
    ui_line "This App exists to write one secret, on a schedule, and does nothing"
    ui_line "else. Its workflow never checks out a pull request branch, never runs"
    ui_line "a model and never reads a diff or a comment — there is nothing in it"
    ui_line "to inject into, which is what makes secrets:write safe here and unsafe"
    ui_line "on the App the review jobs use."
  else
    ui_line "issues:write looks surprising and is not optional — GitHub models pull"
    ui_line "request labels under the Issues API, and the loop is label-driven."
  fi
  ui_gap
  ui_line "Two approvals in the browser: create the App, then install it. CrossRev"
  ui_line "follows along here — nothing to copy back."
  printf '\n'

  ui_confirm "Open GitHub in your browser to create the App?" || { ui_say "Nothing was created."; return 1; }

  local html; html="$(mktemp -t crossrev-manifest).html"
  local reqfile; reqfile="$(mktemp -t crossrev-redirect)"
  # shellcheck disable=SC2064  # expand now, not at trap time
  trap "rm -f '$html' '$reqfile'" RETURN

  cat >"$html" <<HTML
<!doctype html>
<meta charset="utf-8">
<title>crossrev</title>
<body style="font:16px system-ui;margin:4rem auto;max-width:34rem">
<p>Sending you to GitHub to register <strong>$(_html_attr_escape "$app_name")</strong>&hellip;</p>
<p>If nothing happens, press the button.</p>
<form id="f" action="$(_html_attr_escape "$post_url")" method="post">
  <input type="hidden" name="manifest" value="$(_html_attr_escape "$manifest")">
  <input type="hidden" name="state" value="$(_html_attr_escape "$state")">
  <button type="submit">Continue to GitHub</button>
</form>
<script>document.getElementById('f').submit()</script>
</body>
HTML

  local code="" returned_state=""

  if (( use_listener )); then
    ui_section "Step 1 of 2: Create the GitHub App"
    ui_line "A browser tab is open on GitHub's App registration page."
    ui_line "Review the settings and create the App. CrossRev detects the creation"
    ui_line "and continues automatically."
    printf '\n'

    _open_browser "file://$html" || ui_warn \
      "could not open a browser automatically" \
      "Open this to continue: file://$html"

    if _listen_for_code "$port" "$reqfile" 300; then
      local reqline; reqline="$(head -1 "$reqfile")"
      code="$(sed -n 's/.*[?&]code=\([^& ]*\).*/\1/p' <<<"$reqline")"
      returned_state="$(sed -n 's/.*[?&]state=\([^& ]*\).*/\1/p' <<<"$reqline")"
    else
      ui_warn "nothing arrived on localhost:$port within five minutes" \
        "Falling back to pasting the code by hand. If the browser is showing a page that will not load, the address bar still has what is needed."
    fi
  else
    _open_browser "file://$html" || ui_warn \
      "could not open a browser automatically" \
      "Open this to continue: file://$html"
  fi

  # Paste fallback. Reached when there is no nc, no free port, or the listener
  # timed out. It is the floor, not the plan.
  if [[ -z "$code" ]]; then
    ui_section "Step 1 of 2: Paste the registration code"
    ui_line "GitHub redirected your browser to a localhost address that will not load."
    ui_line "Copy the full URL from the address bar and paste it below."
    printf '\n'
    local pasted; pasted="$(ui_prompt "URL or code")" || ui_die \
      "no code was pasted" "Re-run: crossrev auth login --owner $owner"
    if [[ "$pasted" == *"code="* ]]; then
      code="$(sed -n 's/.*[?&]code=\([^&]*\).*/\1/p' <<<"$pasted")"
      returned_state="$(sed -n 's/.*[?&]state=\([^&]*\).*/\1/p' <<<"$pasted")"
    else
      code="$pasted"
    fi
  fi

  [[ -n "$code" ]] || ui_die \
    "no code found" \
    "Paste the full URL from the address bar, or just the value after code="

  if [[ -n "$returned_state" && "$returned_state" != "$state" ]]; then
    ui_die "the state value GitHub returned does not match the one CrossRev sent" \
      "This request did not come from the page crossrev opened. Start again: crossrev auth login --owner $owner"
  fi

  local resp
  if ! resp="$(gh api --method POST "app-manifests/$code/conversions" 2>&1)"; then
    ui_die "GitHub rejected the code" \
      "Codes expire one hour after the App is created, and each works once. Re-run: crossrev auth login --owner $owner"
  fi

  local app_id slug real_name pem
  app_id="$(jq -r .id <<<"$resp")"
  slug="$(jq -r .slug <<<"$resp")"
  real_name="$(jq -r .name <<<"$resp")"
  pem="$(jq -r .pem <<<"$resp")"

  [[ "$app_id" != "null" && -n "$pem" && "$pem" != "null" ]] || ui_die \
    "GitHub's response did not contain an App id and private key" \
    "Nothing was stored. Check for a half-created App before retrying"

  local dir; dir="$(_auth_dir)"
  mkdir -p "$dir"; chmod 700 "$dir"

  # umask rather than create-then-chmod, so the key is never briefly readable.
  # New registrations always take the roled path, never the legacy one.
  local pem_path; pem_path="$(_auth_dir)/$owner.$role.pem"
  (umask 077; printf '%s' "$pem" >"$pem_path")

  (umask 077; jq -n \
    --arg owner "$owner" --arg owner_type "$owner_type" --argjson owner_id "$owner_id" \
    --argjson id "$app_id" --arg slug "$slug" --arg name "$real_name" --arg role "$role" \
    --arg created "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{owner:$owner, owner_type:$owner_type, owner_id:$owner_id,
      id:$id, slug:$slug, name:$name, role:$role, created:$created}' \
    >"$(_auth_dir)/$owner.$role.json")

  # GNU first, BSD second — see the note at auth_status. Reversed, a successful
  # registration on Linux reported its own key as the wrong mode.
  local stored_mode
  stored_mode="$(stat -c '%a' "$pem_path" 2>/dev/null || stat -f '%Lp' "$pem_path" 2>/dev/null)"

  ui_section "Registered"
  ui_ok "App    $real_name (id $app_id)"
  if [[ "$stored_mode" == "600" ]]; then
    ui_ok "Key    $pem_path (0600)"
  else
    ui_no "Key    $pem_path — expected mode 0600, found $stored_mode"
  fi

  _auth_install_flow "$owner" "$owner_type" "$owner_id" "$slug" "$app_id" "$pem_path" "Step 2 of 2: "
}

# crossrev auth install — run the install half on its own.
#
# `login` does both halves, but the two can be separated by a closed tab, a
# declined permission prompt, or a new repository a year later. Re-running
# `login` is the wrong instrument: it refuses, correctly, because the App
# already exists.
auth_install() {
  local owner="" role="loop"
  while (( $# )); do
    case "$1" in
      --owner) owner="${2:?--owner needs a value}"; shift 2 ;;
      --role)  role="${2:?--role needs a value}"; shift 2 ;;
      *) ui_die "unknown option for auth install: $1" "Run: crossrev auth install [--owner <owner>] [--role loop|refresher]" ;;
    esac
  done

  [[ -n "$owner" ]] || owner="$(_auth_detect_owner)" || ui_die \
    "could not work out which owner's App to install" \
    "Name it: crossrev auth install --owner <owner>"

  local meta; meta="$(_auth_meta "$owner" "$role")"
  [[ -f "$meta" ]] || ui_die \
    "no $role App is configured for $owner" \
    "Register one first: crossrev auth login --owner $owner --role $role"

  local owner_type owner_id slug app_id pem
  owner_type="$(jq -r .owner_type "$meta")"
  owner_id="$(jq -r '.owner_id // empty' "$meta")"
  [[ -n "$owner_id" ]] || owner_id="$(gh api "users/$owner" --jq .id 2>/dev/null)"
  slug="$(jq -r .slug "$meta")"
  app_id="$(jq -r .id "$meta")"
  pem="$(_auth_pem "$owner" "$role")"

  [[ -f "$pem" ]] || ui_die \
    "the $role private key for $owner is missing at $pem" \
    "Without it crossrev cannot confirm the installation. Re-register: crossrev auth login --owner $owner --role $role"

  _auth_install_flow "$owner" "$owner_type" "$owner_id" "$slug" "$app_id" "$pem"
}

# Second half of the flow: install the App, then confirm it landed.
#
# Registering an App that reaches no repository is not a finished job, so this
# is part of `login` rather than a link at the end of it.
_auth_install_flow() {
  local owner="$1" owner_type="$2" owner_id="$3" slug="$4" app_id="$5" pem="$6"
  local step_prefix="${7:-}"
  local url; url="$(_auth_install_url "$slug" "$owner_type" "$owner_id")"

  ui_section "${step_prefix}Install the App on the repositories you want reviewed"
  ui_line "The App exists on GitHub, but reaches nothing until it is installed."
  ui_line "Choose 'Only select repositories' unless you mean all of them."
  printf '\n'

  _open_browser "$url" || ui_warn \
    "could not open a browser automatically" \
    "Install it here: $url"

  local jwt installs waited=0
  ui_line "Waiting for the installation to appear..."
  while (( waited < 300 )); do
    if jwt="$(_auth_jwt "$pem" "$app_id")" && installs="$(_auth_installations "$jwt")" \
       && [[ -n "$installs" ]]; then
      ui_gap
      while read -r acct sel; do
        ui_ok "installed on $acct ($sel repositories)"
      done <<<"$installs"
      ui_end "Next:   crossrev init"
      return 0
    fi
    sleep 3
    (( waited += 3 )) || true
  done

  ui_warn "no installation showed up within five minutes" \
    "The App is registered and its key is stored, so nothing is lost. Install it at $url and check with: crossrev auth status"
}

# ---------------------------------------------------------------------------
# crossrev auth rotate
# ---------------------------------------------------------------------------
#
# GitHub exposes no API for generating an App private key. It is a web-UI action
# and there is no way around that, so this is a guided flow rather than a call:
# open the right page, wait for the file, prove it works, install it, and say
# what is left to do by hand.
#
# The proof is the part that matters. A rotation that stores an unverified key
# leaves you with a secret nobody has tested and an old key you have been told to
# delete — which is how a repository ends up with no working credential at all.
# So the new key mints a JWT and calls the API as the App before anything is
# replaced, and the old key is kept until that succeeds.

auth_rotate() {
  local owner="" role="loop" keyfile=""
  while (( $# )); do
    case "$1" in
      --owner) owner="${2:?--owner needs a value}"; shift 2 ;;
      --role)  role="${2:?--role needs a value}"; shift 2 ;;
      --key)   keyfile="${2:?--key needs a value}"; shift 2 ;;
      *) ui_die "unknown option for auth rotate: $1" \
           "Run: crossrev auth rotate [--owner <owner>] [--role loop|refresher] [--key <downloaded.pem>]" ;;
    esac
  done

  [[ -n "$owner" ]] || owner="$(_auth_detect_owner)" || ui_die \
    "could not work out which owner's key to rotate" \
    "Name it: crossrev auth rotate --owner <owner>"

  local meta; meta="$(_auth_meta "$owner" "$role")"
  [[ -f "$meta" ]] || ui_die \
    "no $role App is configured for $owner" \
    "There is nothing to rotate. Register one with: crossrev auth login --owner $owner --role $role"

  local app_id slug pem
  app_id="$(jq -r .id "$meta")"
  slug="$(jq -r .slug "$meta")"
  pem="$(_auth_pem "$owner" "$role")"

  local settings_url
  if [[ "$(jq -r .owner_type "$meta")" == "Organization" ]]; then
    settings_url="https://github.com/organizations/$owner/settings/apps/$slug#private-key"
  else
    settings_url="https://github.com/settings/apps/$slug#private-key"
  fi

  ui_section "Rotate the private key for $(jq -r .name "$meta")"
  ui_line "Current key   $pem"
  ui_line "App           id $app_id, role $role"
  ui_gap
  ui_line "GitHub has no API for generating an App key, so this part happens in"
  ui_line "the browser: press 'Generate a private key' and the .pem downloads."
  ui_line "CrossRev picks it up, proves it works as this App, and installs it."
  ui_gap
  ui_line "Nothing is replaced until the new key authenticates, and the old one"
  ui_line "keeps working until you delete it on GitHub — so a failure here leaves"
  ui_line "you exactly where you started."
  printf '\n'

  if [[ -z "$keyfile" ]]; then
    ui_confirm "Open GitHub?" || { ui_say "Nothing was changed."; return 1; }
    _open_browser "$settings_url" || ui_warn \
      "could not open a browser automatically" \
      "Generate the key here: $settings_url"

    # Watch the downloads folder rather than asking for a path. The file lands
    # with a name GitHub chooses, and typing it out is the step people get wrong.
    local downloads="${HOME}/Downloads" waited=0 found=""
    ui_line "Watching $downloads for a new .pem..."
    while (( waited < 300 )); do
      found="$(find "$downloads" -maxdepth 1 -name "$slug*.private-key.pem" -newermt '-5 minutes' 2>/dev/null | head -1)"
      [[ -n "$found" ]] && break
      sleep 2; (( waited += 2 )) || true
    done
    if [[ -n "$found" ]]; then
      keyfile="$found"
      ui_ok "found $keyfile"
    else
      keyfile="$(ui_prompt "Path to the downloaded .pem")" || ui_die \
        "no key file was named" "Re-run: crossrev auth rotate --owner $owner --role $role --key <path>"
    fi
  fi

  keyfile="${keyfile/#\~/$HOME}"
  [[ -f "$keyfile" ]] || ui_die "there is no file at $keyfile" \
    "Point --key at the .pem GitHub downloaded."

  # Prove it before installing it.
  local jwt
  jwt="$(_auth_jwt "$keyfile" "$app_id")" || ui_die \
    "could not sign a token with $keyfile" \
    "It has to be the RSA private key GitHub generated for this App. Nothing was changed."
  gh api -H "Authorization: Bearer $jwt" /app --jq .slug >/dev/null 2>&1 || ui_die \
    "GitHub rejected a token signed with $keyfile for App id $app_id" \
    "That key belongs to a different App, or the download is incomplete. The existing key is untouched."

  local dest backup; dest="$(_auth_dir)/$owner.$role.pem"
  backup="$dest.previous"
  [[ -f "$pem" ]] && cp "$pem" "$backup" && chmod 600 "$backup"
  (umask 077; cat "$keyfile" >"$dest")
  # The legacy unroled path would otherwise keep winning for the loop role and
  # the rotation would look successful while nothing had changed.
  [[ "$pem" != "$dest" && -f "$pem" ]] && rm -f "$pem"

  ui_section "Rotated"
  ui_ok "new key installed at $dest, and it authenticates as this App"
  [[ -f "$backup" ]] && ui_line "   previous key kept at $backup"
  ui_gap
  ui_line "Two things are still yours to do, and both are outward-facing:"
  ui_next "delete the old key on GitHub: $settings_url"
  # The role's own secret, never a hardcoded APP_PRIVATE_KEY. Told to update
  # that one after rotating the refresher's key, someone following the
  # instruction literally would put the refresher's key material behind the loop
  # App's identity — handing secrets:write to the job that reads a pull request
  # diff, which is the one thing the two-App split exists to prevent.
  ui_next "update $(_auth_role_key_secret "$role") wherever it is stored: crossrev init --upgrade, or gh secret set"
  if [[ "$role" == "refresher" ]]; then
    ui_line "   repository-scoped: this key can write secrets, so it must never be"
    ui_line "   an organisation secret visible to every workflow in the org"
  fi
  ui_end "Until the secret carries the new key, CI is still authenticating with the old one."
}

# ---------------------------------------------------------------------------
# crossrev auth refresh — the single writer
# ---------------------------------------------------------------------------
#
# Called by the refresher workflow and by nobody else. It is the only place that
# writes a rotating harness credential, which is the whole reason the chain
# holds: using a refresh token consumes it, so several writers means the first
# one silently invalidates the rest.

auth_refresh() {
  local harness="" repo="" secret="" scope=""
  while (( $# )); do
    case "$1" in
      --harness) harness="${2:?--harness needs a value}"; shift 2 ;;
      --repo)    repo="${2:?--repo needs a value}"; shift 2 ;;
      --secret)  secret="${2:?--secret needs a value}"; shift 2 ;;
      --org)     scope="${2:?--org needs a value}"; shift 2 ;;
      *) ui_die "unknown option for auth refresh: $1" \
           "Run: crossrev auth refresh [--harness <name>] [--repo owner/name | --org owner] [--secret NAME]" ;;
    esac
  done

  harness_load
  if [[ -z "$harness" ]]; then
    local count refresh_harnesses
    refresh_harnesses="$(jq -r '[ .harnesses[] | select(.credential.refresher == true) | .name ] | join(", ")' <<<"$HARNESS_JSON")"
    count="$(jq -r '[ .harnesses[] | select(.credential.refresher == true) ] | length' <<<"$HARNESS_JSON")"
    if (( count > 1 )); then
      ui_die "more than one harness is configured with a refresher ($refresh_harnesses)" \
        "Specify which harness to refresh with --harness <name>."
    elif (( count == 1 )); then
      harness="$(jq -r '.harnesses[] | select(.credential.refresher == true) | .name' <<<"$HARNESS_JSON")"
    else
      ui_die "no harness is configured with a refresher" \
        "CrossRev only refreshes credentials that rotate on ephemeral runners."
    fi
  fi

  if [[ "$(harness_get "$harness" .credential.refresher)" != "true" ]]; then
    ui_die "crossrev only refreshes credentials that rotate on ephemeral runners, and '$harness' does not need a refresher" \
      "Claude's setup-token is long-lived and needs no refresher; Antigravity uses seed-and-self-refresh; only single-writer rotating credentials use the refresher workflow."
  fi

  if [[ -z "$secret" ]]; then
    secret="$(harness_get "$harness" .credential.secret)"
    [[ -n "$secret" ]] || secret="CROSSREV_${harness^^}_AUTH"
  fi

  local seed_hint
  seed_hint="$(harness_get "$harness" .credential.seed_hint)"
  [[ -n "$seed_hint" ]] || seed_hint="re-seed the secret by hand"

  [[ -n "${!secret:-}" ]] || ui_die \
    "$secret is not set, so there is no credential to refresh" \
    "The refresher workflow passes the secret in as this variable. $seed_hint"

  [[ -n "$repo" || -n "$scope" ]] || repo="$(gh_repo_slug)"
  [[ -n "$repo" || -n "$scope" ]] || ui_die \
    "could not work out where to write the refreshed credential" \
    "Pass --repo owner/name or --org owner."

  # EXIT rather than RETURN: every failure below is a ui_die, which exits the
  # process, and a RETURN trap does not fire on the way out. That would leave a
  # live credential in a temp file after the one command whose whole job is not
  # to leak one.
  local current; current="$(mktemp)"
  # shellcheck disable=SC2064  # expand now, not at trap time
  trap "rm -f '$current'" EXIT
  (umask 077; printf '%s' "${!secret}" >"$current")

  local before after new
  before="$(cred_seconds_left "$harness" "$current")" || before=""
  new="$(cred_refresh "$harness" "$current")" || ui_die \
    "the refresh did not produce a new credential" \
    "The stored secret is untouched, so the chain still holds until it expires. Re-seed it by hand if this keeps failing: $seed_hint."

  local check; check="$(mktemp)"
  # shellcheck disable=SC2064  # expand now, not at trap time
  trap "rm -f '$current' '$check'" EXIT
  (umask 077; printf '%s' "$new" >"$check")
  # Rule 5: do not report success for something unverified — and an expiry that
  # cannot be read is unverified, not "probably fine". A vendor response that is
  # HTTP 200 with a malformed access token would otherwise be written back over a
  # working credential, reported as a success, and rejected by every leg from
  # then on. Refuse instead: the stored secret still holds something that works.
  after="$(cred_seconds_left "$harness" "$check")" || ui_die \
    "the refreshed credential's expiry cannot be read, so crossrev will not write it back" \
    "The vendor answered, but what came back does not parse as a token with an exp claim. The stored secret is untouched and still works until it expires. Re-seed it by hand if this repeats: $seed_hint."

  # An expiry no later than the one it replaces means the refresh did not happen,
  # and writing it back would burn a refresh token for nothing.
  if [[ -n "$before" ]] && (( after <= before )); then
    ui_die "the refreshed credential expires no later than the one it replaces" \
      "The vendor answered but did not issue a new token. The stored secret is untouched. Check the account's session has not been revoked: $harness login status"
  fi

  if [[ -n "$scope" ]]; then
    printf '%s' "$new" | gh secret set "$secret" --org "$scope" --visibility all >/dev/null || ui_die \
      "could not write $secret at the $scope organisation level" \
      "The refresher App needs secrets:write on that organisation. Check: crossrev auth status"
  else
    printf '%s' "$new" | gh secret set "$secret" --repo "$repo" >/dev/null || ui_die \
      "could not write $secret on $repo" \
      "The refresher App needs secrets:write on that repository. Check: crossrev auth status"
  fi

  # Explicitly, rather than leaving it to the EXIT trap. The trap is the
  # backstop for the fatal paths; on the ordinary one the credential should stop
  # existing on disk the moment it is no longer needed, not whenever the process
  # happens to end.
  rm -f "$current" "$check"

  ui_section "Refreshed"
  ui_ok "$secret now holds a credential valid for $(_cred_human_duration "${after:-0}")"
  ui_end "This is the only job that writes it. Every leg restores a copy and discards it."
}
