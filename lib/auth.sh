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

_auth_dir() { printf '%s/revloop/apps' "${XDG_CONFIG_HOME:-$HOME/.config}"; }
_auth_pem()  { printf '%s/%s.pem'  "$(_auth_dir)" "$1"; }
_auth_meta() { printf '%s/%s.json' "$(_auth_dir)" "$1"; }

# The owner is detected, not asked, because the repository's owner is the trust
# boundary the private key should sit on. --owner overrides.
_auth_detect_owner() {
  gh repo view --json owner --jq .owner.login 2>/dev/null || return 1
}

# "<type> <id>" for an account. /users/ resolves organisations too and returns
# the same numeric id, so one call answers both questions. The id is what
# prefills the install page with the right target.
_auth_owner_info() {
  gh api "users/$1" --jq '"\(.type) \(.id)"' 2>/dev/null || return 1
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
# revloop confirm an installation actually landed rather than telling you to go
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
<html><head><meta charset="utf-8"><title>revloop</title><style>
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
<p>revloop has the App details and is carrying on in your terminal.</p>
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
# revloop auth status
# ---------------------------------------------------------------------------

auth_status() {
  local dir; dir="$(_auth_dir)"

  if [[ ! -d "$dir" ]] || ! compgen -G "$dir/*.json" >/dev/null 2>&1; then
    ui_section "Apps"
    ui_opt "none configured"
    ui_gap
    ui_line "revloop needs an App only for automated mode — the loop running on"
    ui_line "GitHub events. Local runs use your own gh authentication."
    ui_end "Set one up with:   revloop auth login"
    return 0
  fi

  ui_section "Apps"
  local meta owner owner_type owner_id id name slug pem mode jwt installs
  for meta in "$dir"/*.json; do
    owner="$(jq -r .owner "$meta")"
    owner_type="$(jq -r .owner_type "$meta")"
    owner_id="$(jq -r '.owner_id // empty' "$meta")"
    id="$(jq -r .id "$meta")"
    name="$(jq -r .name "$meta")"
    slug="$(jq -r .slug "$meta")"
    pem="$(_auth_pem "$owner")"

    ui_ok "$owner — $name (id $id)"

    if [[ -f "$pem" ]]; then
      # stat drops the leading zero, and "600" next to a sentence about 0600
      # reads as a mismatch. Same number, printed the way it is talked about.
      mode="$(printf '%04d' "$(stat -f '%Lp' "$pem" 2>/dev/null || stat -c '%a' "$pem" 2>/dev/null)")"
      ui_line "   key $pem${mode:+ ($mode)}"
      [[ "$mode" == "0600" ]] || ui_warn \
        "the private key for $owner is mode $mode, not 0600" \
        "Any process running as you can read it, and it can mint a token for every repository this App is installed on. Fix with: chmod 600 $pem"

      # Rule 5: report what is true, not what was configured. An App installed
      # nowhere looks identical to a working one until the first API call fails.
      if jwt="$(_auth_jwt "$pem" "$id")" && installs="$(_auth_installations "$jwt")"; then
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
}

# ---------------------------------------------------------------------------
# revloop auth login
# ---------------------------------------------------------------------------

auth_login() {
  local owner="" app_name=""
  while (( $# )); do
    case "$1" in
      --owner) owner="${2:?--owner needs a value}"; shift 2 ;;
      --name)  app_name="${2:?--name needs a value}"; shift 2 ;;
      *) ui_die "unknown option for auth login: $1" "Run: revloop auth login [--owner <owner>] [--name <name>]" ;;
    esac
  done

  _ui_input_source >/dev/null || _ui_no_input

  if [[ -z "$owner" ]]; then
    owner="$(_auth_detect_owner)" || ui_die \
      "could not work out which account this App should belong to" \
      "Run this inside a git repository with a GitHub remote, or name it: revloop auth login --owner <owner>"
  fi

  local owner_type owner_id info
  info="$(_auth_owner_info "$owner")" || ui_die \
    "GitHub does not recognise the account '$owner'" \
    "Check the spelling, or pass a different one with --owner"
  read -r owner_type owner_id <<<"$info"

  local meta; meta="$(_auth_meta "$owner")"
  if [[ -f "$meta" ]]; then
    ui_section "Already configured"
    ui_ok "$owner — $(jq -r .name "$meta") (id $(jq -r .id "$meta"))"
    ui_gap
    ui_line "One App per owner is the design. Creating a second would mean a"
    ui_line "second private key to protect and rotate, for no extra reach."
    ui_end "See where it is installed with:   revloop auth status"
    return 0
  fi

  # GitHub App names are globally unique, so a bare "revloop" is very likely
  # taken. Suffixing the owner is likelier to be free and clearer in a list.
  [[ -n "$app_name" ]] || app_name="revloop-$owner"

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
  local redirect="http://localhost:$port/revloop-auth"

  local manifest
  manifest="$(jq -cn \
    --arg name "$app_name" \
    --arg url "https://github.com/carlosboeing/claude-code-resources" \
    --arg redirect "$redirect" \
    '{
      name: $name,
      url: $url,
      redirect_url: $redirect,
      public: false,
      hook_attributes: { url: "https://example.com/unused", active: false },
      default_events: [],
      default_permissions: {
        contents: "write",
        issues: "write",
        pull_requests: "write"
      }
    }')"

  local post_url
  if [[ "$owner_type" == "Organization" ]]; then
    post_url="https://github.com/organizations/$owner/settings/apps/new"
  else
    post_url="https://github.com/settings/apps/new"
  fi

  ui_section "Register a GitHub App for $owner"
  ui_line "Owner        $owner ($owner_type)"
  ui_line "Name         $app_name"
  ui_line "Permissions  contents:write, issues:write, pull_requests:write"
  ui_line "             and nothing else — no secrets, no administration, no workflows"
  ui_line "Webhook      disabled. GitHub never calls revloop; your workflows do"
  ui_line "Visibility   private to $owner"
  ui_gap
  ui_line "issues:write looks surprising and is not optional — GitHub models pull"
  ui_line "request labels under the Issues API, and the loop is label-driven."
  ui_gap
  ui_line "Two approvals in the browser: create the App, then install it. revloop"
  ui_line "follows along here — nothing to copy back."
  printf '\n'

  ui_confirm "Open GitHub?" || { ui_say "Nothing was created."; return 1; }

  local html; html="$(mktemp -t revloop-manifest).html"
  local reqfile; reqfile="$(mktemp -t revloop-redirect)"
  # shellcheck disable=SC2064  # expand now, not at trap time
  trap "rm -f '$html' '$reqfile'" RETURN

  cat >"$html" <<HTML
<!doctype html>
<meta charset="utf-8">
<title>revloop</title>
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
    ui_section "Waiting for you to approve it"
    ui_line "A browser tab is open on GitHub's App registration page."
    ui_line "Name it if you like, then approve. This picks up automatically."
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
    ui_section "Approve it in the browser"
    ui_line "GitHub sends you back to a localhost address that will not load."
    ui_line "Copy the whole URL from the address bar and paste it below."
    printf '\n'
    local pasted; pasted="$(ui_prompt "URL or code")" || ui_die \
      "no code was pasted" "Re-run: revloop auth login --owner $owner"
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
    ui_die "the state value GitHub returned does not match the one revloop sent" \
      "This request did not come from the page revloop opened. Start again: revloop auth login --owner $owner"
  fi

  local resp
  if ! resp="$(gh api --method POST "app-manifests/$code/conversions" 2>&1)"; then
    ui_die "GitHub rejected the code" \
      "Codes expire one hour after the App is created, and each works once. Re-run: revloop auth login --owner $owner"
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
  local pem_path; pem_path="$(_auth_pem "$owner")"
  (umask 077; printf '%s' "$pem" >"$pem_path")

  (umask 077; jq -n \
    --arg owner "$owner" --arg owner_type "$owner_type" --argjson owner_id "$owner_id" \
    --argjson id "$app_id" --arg slug "$slug" --arg name "$real_name" \
    --arg created "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{owner:$owner, owner_type:$owner_type, owner_id:$owner_id,
      id:$id, slug:$slug, name:$name, created:$created}' \
    >"$(_auth_meta "$owner")")

  local stored_mode
  stored_mode="$(stat -f '%Lp' "$pem_path" 2>/dev/null || stat -c '%a' "$pem_path" 2>/dev/null)"

  ui_section "Registered"
  ui_ok "App    $real_name (id $app_id)"
  if [[ "$stored_mode" == "600" ]]; then
    ui_ok "Key    $pem_path (0600)"
  else
    ui_no "Key    $pem_path — expected mode 0600, found $stored_mode"
  fi

  _auth_install_flow "$owner" "$owner_type" "$owner_id" "$slug" "$app_id" "$pem_path"
}

# revloop auth install — run the install half on its own.
#
# `login` does both halves, but the two can be separated by a closed tab, a
# declined permission prompt, or a new repository a year later. Re-running
# `login` is the wrong instrument: it refuses, correctly, because the App
# already exists.
auth_install() {
  local owner=""
  while (( $# )); do
    case "$1" in
      --owner) owner="${2:?--owner needs a value}"; shift 2 ;;
      *) ui_die "unknown option for auth install: $1" "Run: revloop auth install [--owner <owner>]" ;;
    esac
  done

  [[ -n "$owner" ]] || owner="$(_auth_detect_owner)" || ui_die \
    "could not work out which owner's App to install" \
    "Name it: revloop auth install --owner <owner>"

  local meta; meta="$(_auth_meta "$owner")"
  [[ -f "$meta" ]] || ui_die \
    "no App is configured for $owner" \
    "Register one first: revloop auth login --owner $owner"

  local owner_type owner_id slug app_id pem
  owner_type="$(jq -r .owner_type "$meta")"
  owner_id="$(jq -r '.owner_id // empty' "$meta")"
  [[ -n "$owner_id" ]] || owner_id="$(gh api "users/$owner" --jq .id 2>/dev/null)"
  slug="$(jq -r .slug "$meta")"
  app_id="$(jq -r .id "$meta")"
  pem="$(_auth_pem "$owner")"

  [[ -f "$pem" ]] || ui_die \
    "the private key for $owner is missing at $pem" \
    "Without it revloop cannot confirm the installation. Re-register: revloop auth login --owner $owner"

  _auth_install_flow "$owner" "$owner_type" "$owner_id" "$slug" "$app_id" "$pem"
}

# Second half of the flow: install the App, then confirm it landed.
#
# Registering an App that reaches no repository is not a finished job, so this
# is part of `login` rather than a link at the end of it.
_auth_install_flow() {
  local owner="$1" owner_type="$2" owner_id="$3" slug="$4" app_id="$5" pem="$6"
  local url; url="$(_auth_install_url "$slug" "$owner_type" "$owner_id")"

  ui_section "Install it on the repositories you want reviewed"
  ui_line "The App exists but reaches nothing until it is installed."
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
      ui_end "Next:   revloop init"
      return 0
    fi
    sleep 3
    (( waited += 3 )) || true
  done

  ui_warn "no installation showed up within five minutes" \
    "The App is registered and its key is stored, so nothing is lost. Install it at $url and check with: revloop auth status"
}
