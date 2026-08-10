# shellcheck shell=bash
# lib/auth.sh — GitHub App creation and key storage.
#
# One App per owner, never one globally and never one per repository. The
# private key belongs to the App, so whoever holds it can mint a token for any
# installation of that App. Per-owner matches the boundary GitHub already draws:
# a personal App for personal repos, an org-owned App whose key lives in that
# org's secrets, and a separate one for any client org later. A leak in one
# cannot reach another.

_auth_dir() { printf '%s/revloop/apps' "${XDG_CONFIG_HOME:-$HOME/.config}"; }
_auth_pem()  { printf '%s/%s.pem'  "$(_auth_dir)" "$1"; }
_auth_meta() { printf '%s/%s.json' "$(_auth_dir)" "$1"; }

# The owner is detected, not asked, because the repository's owner is the trust
# boundary the private key should sit on. --owner overrides.
_auth_detect_owner() {
  gh repo view --json owner --jq .owner.login 2>/dev/null || return 1
}

# "User" or "Organization" — decides which settings path the manifest posts to.
_auth_owner_type() {
  gh api "users/$1" --jq .type 2>/dev/null || return 1
}

# Escape a string for use inside an HTML double-quoted attribute. Ampersand
# first, or it double-escapes the entities produced by the later rules.
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
  local meta owner id name pem mode
  for meta in "$dir"/*.json; do
    owner="$(jq -r .owner "$meta")"
    id="$(jq -r .id "$meta")"
    name="$(jq -r .name "$meta")"
    pem="$(_auth_pem "$owner")"

    ui_ok "$owner — $name (id $id)"
    if [[ -f "$pem" ]]; then
      mode="$(stat -f '%Lp' "$pem" 2>/dev/null || stat -c '%a' "$pem" 2>/dev/null)"
      if [[ "$mode" == "600" ]]; then
        ui_line "   key $pem (0600)"
      else
        ui_line "   key $pem"
        ui_warn "the private key for $owner is mode $mode, not 0600" \
          "Any process running as you can read it, and it can mint a token for every repository this App is installed on. Fix with: chmod 600 $pem"
      fi
    else
      ui_no "   key missing at $pem — this App cannot mint a token"
    fi
  done
  ui_end "Rotate a key with:   revloop auth rotate --owner <owner>"
}

# ---------------------------------------------------------------------------
# revloop auth login — the GitHub App Manifest flow
# ---------------------------------------------------------------------------
#
# Creating an App is the one step that needs a human, because approving
# permissions is a consent decision GitHub deliberately does not automate.
# Everything around it is automated, and the manifest prefills the form so the
# whole class of misconfiguration disappears rather than merely getting faster
# to fix: the homepage URL, the webhook that defaults to ON, the install scope,
# and three permissions buried in a long list of three-state dropdowns.
#
# The code is pasted, not caught. Capturing the redirect would mean running a
# local HTTP listener, which is the least bash-shaped thing in this project.

auth_login() {
  local owner="" app_name=""
  while (( $# )); do
    case "$1" in
      --owner) owner="${2:?--owner needs a value}"; shift 2 ;;
      --name)  app_name="${2:?--name needs a value}"; shift 2 ;;
      *) ui_die "unknown option for auth login: $1" "Run: revloop auth login [--owner <owner>] [--name <name>]" ;;
    esac
  done

  # This flow opens a browser and then asks you to paste a code back. If there
  # is nowhere to read that paste from, say so now rather than after registering
  # an App nobody can finish setting up — rule 6 cuts both ways, and acting
  # outward when you already know you cannot finish is the worse half.
  _ui_input_source >/dev/null || _ui_no_input

  if [[ -z "$owner" ]]; then
    owner="$(_auth_detect_owner)" || ui_die \
      "could not work out which account this App should belong to" \
      "Run this inside a git repository with a GitHub remote, or name it: revloop auth login --owner <owner>"
  fi

  local owner_type; owner_type="$(_auth_owner_type "$owner")" || ui_die \
    "GitHub does not recognise the account '$owner'" \
    "Check the spelling, or pass a different one with --owner"

  # Already configured? Reuse rather than quietly creating a second App, since
  # two Apps for one owner means two keys and two things to rotate.
  local meta; meta="$(_auth_meta "$owner")"
  if [[ -f "$meta" ]]; then
    ui_section "Already configured"
    ui_ok "$owner — $(jq -r .name "$meta") (id $(jq -r .id "$meta"))"
    ui_gap
    ui_line "One App per owner is the design. Creating a second would mean a"
    ui_line "second private key to protect and rotate, for no extra reach."
    ui_end "To replace its key instead:   revloop auth rotate --owner $owner"
    return 0
  fi

  # GitHub App names are globally unique, so a bare "revloop" is very likely
  # taken by now. Suffixing the owner is both likelier to be free and clearer
  # in a list of installed Apps.
  [[ -n "$app_name" ]] || app_name="revloop-$owner"

  local state; state="$(openssl rand -hex 16)"
  local redirect="http://localhost:33517/revloop-auth"

  # hook_attributes.url is required by the form even when the webhook is off,
  # so it is present and inert. revloop is never called by GitHub — the
  # workflows call revloop — so there is nothing to deliver.
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

  # Rule 6: explain before acting outward. This opens a browser and registers an
  # identity on GitHub, so say what it will look like first.
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
  ui_line "The permissions are prefilled from a manifest, so nothing on that page"
  ui_line "is yours to get wrong. You name it and approve."
  printf '\n'

  ui_confirm "Open GitHub to register it?" || {
    ui_say "Nothing was created."
    return 1
  }

  local html; html="$(mktemp -t revloop-manifest).html"
  # shellcheck disable=SC2064  # expand $html now, not at trap time
  trap "rm -f '$html'" RETURN

  cat >"$html" <<HTML
<!doctype html>
<meta charset="utf-8">
<title>revloop — registering a GitHub App</title>
<body style="font:16px system-ui;margin:4rem auto;max-width:34rem">
<p>Sending you to GitHub to register <strong>$(_html_attr_escape "$app_name")</strong>…</p>
<p>If nothing happens, press the button.</p>
<form id="f" action="$(_html_attr_escape "$post_url")" method="post">
  <input type="hidden" name="manifest" value="$(_html_attr_escape "$manifest")">
  <input type="hidden" name="state" value="$(_html_attr_escape "$state")">
  <button type="submit">Continue to GitHub</button>
</form>
<script>document.getElementById('f').submit()</script>
</body>
HTML

  _open_browser "file://$html" || {
    ui_warn "could not open a browser automatically" \
      "Open this file yourself to continue: file://$html"
  }

  ui_section "Approve it in the browser"
  ui_line "GitHub will send you back to a localhost address that will NOT load."
  ui_line "That is expected — revloop runs no web server, so there is nothing"
  ui_line "listening. The address bar is what matters."
  ui_gap
  ui_line "Copy the whole URL from the address bar and paste it below."
  ui_line "It looks like: ${redirect}?code=abc123&state=…"
  printf '\n'

  local pasted; pasted="$(ui_prompt "URL or code")" || ui_die \
    "no code was pasted" "Re-run: revloop auth login --owner $owner"

  # Accept either the full redirect URL or a bare code, because asking someone
  # to surgically extract one query parameter is a worse ask than copying an
  # address bar — and the full URL also carries the state we can verify.
  local code returned_state=""
  if [[ "$pasted" == *"code="* ]]; then
    code="$(printf '%s' "$pasted" | sed -n 's/.*[?&]code=\([^&]*\).*/\1/p')"
    returned_state="$(printf '%s' "$pasted" | sed -n 's/.*[?&]state=\([^&]*\).*/\1/p')"
  else
    code="$pasted"
  fi

  [[ -n "$code" ]] || ui_die \
    "no code found in what you pasted" \
    "Paste the full URL from the address bar, or just the value after code="

  if [[ -n "$returned_state" && "$returned_state" != "$state" ]]; then
    ui_die "the state value GitHub returned does not match the one revloop sent" \
      "This request did not come from the page revloop opened. Start again: revloop auth login --owner $owner"
  fi

  ui_section "Exchanging the code"
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
    "Nothing was stored. Check https://github.com/settings/apps for a half-created App before retrying"

  local dir; dir="$(_auth_dir)"
  mkdir -p "$dir"
  chmod 700 "$dir"

  # Write at 0600 from the start rather than creating then chmod-ing, so the key
  # is never briefly readable.
  local pem_path; pem_path="$(_auth_pem "$owner")"
  (umask 077; printf '%s' "$pem" >"$pem_path")

  local meta_path; meta_path="$(_auth_meta "$owner")"
  (umask 077; jq -n \
    --arg owner "$owner" --arg owner_type "$owner_type" \
    --arg id "$app_id" --arg slug "$slug" --arg name "$real_name" \
    --arg created "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{owner:$owner, owner_type:$owner_type, id:($id|tonumber), slug:$slug, name:$name, created:$created}' \
    >"$meta_path")

  # Rule 5: read it back rather than assuming the write landed.
  local stored_mode
  stored_mode="$(stat -f '%Lp' "$pem_path" 2>/dev/null || stat -c '%a' "$pem_path" 2>/dev/null)"

  ui_section "Registered"
  ui_ok "App    $real_name (id $app_id)"
  if [[ "$stored_mode" == "600" ]]; then
    ui_ok "Key    $pem_path (0600)"
  else
    ui_no "Key    $pem_path — expected mode 0600, found $stored_mode"
  fi
  ui_gap
  ui_line "The App exists but is installed nowhere yet, so it can currently reach"
  ui_line "no repository at all. Install it on the ones you want reviewed:"
  ui_next "https://github.com/settings/apps/$slug/installations"
  ui_end "Then:   revloop init"
}
