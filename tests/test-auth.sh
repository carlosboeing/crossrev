#!/usr/bin/env bash
#
# App registration: the names CrossRev proposes, and the slugs they imply.
#
# The name is display text and follows ADR 0010; the slug GitHub derives from it
# is matched literally by `state_trusted_author` and must not move. Those two
# facts pull in opposite directions, which is the whole reason this suite exists:
# a change to the name that shifts the slug would break marker trust on every
# existing pull request, silently, and no other test would notice.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/ui.sh
source "$HERE/../lib/ui.sh"
# shellcheck source=../lib/auth.sh
source "$HERE/../lib/auth.sh"

pass=0 fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" || notok "$1" "contains '$3'" "$2"; }
hasnt() { [[ "$2" != *"$3"* ]] && ok "$1" || notok "$1" "does not contain '$3'" "$2"; }

# --- the display name follows ADR 0010 -------------------------------------
#
# A GitHub App's name is the string a person reads in an organisation's installed
# Apps list, beside `Claude` and `Vercel`. It is in none of the categories ADR
# 0010 keeps lowercase, so it takes the product name.

is "the loop App is named for a human to read" \
  "$(_auth_role_default_name loop acme)" "CrossRev acme"
is "and so is the refresher" \
  "$(_auth_role_default_name refresher acme)" "CrossRev Refresh acme"

# --- and the slug it implies does not move ---------------------------------
#
# This is the assertion that matters. `state_trusted_author` prints
# "<slug>[bot]" and automated mode reads markers from that author and no one
# else, so a slug change makes the loop trust an author that does not exist:
# no markers read, pass 1 for ever, nothing reconciled.

is "the loop slug is unchanged by the casing" \
  "$(_auth_slug "$(_auth_role_default_name loop acme)")" "crossrev-acme"
is "and the refresher slug is unchanged too" \
  "$(_auth_slug "$(_auth_role_default_name refresher acme)")" "crossrev-refresh-acme"
is "a custom App name is slugified the same way" \
  "$(_auth_slug "My Custom App")" "my-custom-app"

# The fixture identity the stubbed-gh suites assert against, derived the same
# way, so those suites and this one cannot drift apart.
is "the slug matches the identity the other suites use" \
  "$(_auth_slug "$(_auth_role_default_name loop acme)")[bot]" "crossrev-acme[bot]"

# An owner whose name is already mixed case keeps its own casing: the owner half
# is an identity GitHub chose, not prose CrossRev gets to restyle.
is "the owner's own casing survives" \
  "$(_auth_role_default_name loop ShoreLogic)" "CrossRev ShoreLogic"
is "and still slugs to what the App is installed as" \
  "$(_auth_slug "$(_auth_role_default_name loop ShoreLogic)")" "crossrev-shorelogic"

# --- the cached identity is revalidated, not believed -----------------------
#
# `auth login` records the name and slug once, at creation. Renaming the App in
# its settings moves both, and nothing local notices — which matters because the
# cached slug is not decoration: `state_trusted_author` reads it as the fallback
# when CROSSREV_APP_SLUG is unset, so a stale one makes automated mode trust an
# author that does not exist. No markers read, pass 1 for ever, nothing
# reconciled, and `auth status` reporting confidently the whole time.
#
# GET /app is authoritative and reachable with the key already on disk. These
# cases cover reading it, and reconciling the file against it.

XDG_CONFIG_HOME="$(mktemp -d)"; export XDG_CONFIG_HOME
STUB_DIR="$(mktemp -d)"
export CROSSREV_GH_ROUTES="$STUB_DIR/routes"
export PATH="$HERE/stub:$PATH"

# The file `auth login` writes, with the identity it had at creation. Everything
# below the name and slug is here to prove a correction leaves it alone.
meta_fixture() {
  local dir; dir="$(_auth_dir)"
  mkdir -p "$dir"
  (umask 077; jq -n \
    --arg owner ShoreLogic --arg name "$1" --arg slug "$2" \
    '{owner:$owner, owner_type:"Organization", owner_id:12345,
      id:987, slug:$slug, name:$name, role:"loop",
      created:"2026-08-13T00:00:00Z"}' >"$dir/ShoreLogic.loop.json")
  printf '%s/ShoreLogic.loop.json' "$dir"
}

# --- reading the authoritative identity ------------------------------------

printf '%s\t%s\n' '*/app --jq*' \
  '{"name":"CrossRev ShoreLogic","slug":"crossrev-shorelogic","id":987}' \
  >"$CROSSREV_GH_ROUTES"

# The JWT is opaque to this test — the stub answers on the route, not the token.
is "the authoritative identity comes back as name then slug" \
  "$(_auth_app_identity dummy-jwt | tr '\n' '|')" "CrossRev ShoreLogic|crossrev-shorelogic|"

# --- an App that has not been renamed --------------------------------------

fresh="$(meta_fixture "CrossRev ShoreLogic" crossrev-shorelogic)"
before="$(cat "$fresh")"

is "an unchanged App reports no drift" \
  "$(_auth_sync_meta "$fresh" "CrossRev ShoreLogic" crossrev-shorelogic)" ""
is "and its file is left exactly as it was" "$(cat "$fresh")" "$before"

# --- an App renamed in its settings ----------------------------------------
#
# The 2026-08-13 rename, which is where this was found: revloop-ShoreLogic
# became CrossRev ShoreLogic, and the slug moved with it.

stale="$(meta_fixture "revloop-ShoreLogic" revloop-shorelogic)"
drift="$(_auth_sync_meta "$stale" "CrossRev ShoreLogic" crossrev-shorelogic)"

has "a renamed App reports the name that moved" "$drift" \
  "name"$'\t'"revloop-ShoreLogic"$'\t'"CrossRev ShoreLogic"
has "and the slug that moved with it" "$drift" \
  "slug"$'\t'"revloop-shorelogic"$'\t'"crossrev-shorelogic"

is "the corrected file carries the new name" "$(jq -r .name "$stale")" "CrossRev ShoreLogic"

# The assertion this whole suite exists for. `state_trusted_author` reads exactly
# this field and prints "<slug>[bot]", so a correction that stopped short of the
# slug would leave automated mode broken while reporting itself fixed.
is "and the new slug, which is what marker trust reads" \
  "$(jq -r .slug "$stale")" "crossrev-shorelogic"
is "so the trusted author resolves to the App that exists" \
  "$(jq -r .slug "$stale")[bot]" "crossrev-shorelogic[bot]"

# --- a correction rewrites two fields and no others -------------------------

is "the App id survives the correction"      "$(jq -r .id "$stale")"         "987"
is "the owner survives"                      "$(jq -r .owner "$stale")"      "ShoreLogic"
is "the owner type survives"                 "$(jq -r .owner_type "$stale")" "Organization"
is "the owner id survives"                   "$(jq -r .owner_id "$stale")"   "12345"
is "the role survives"                       "$(jq -r .role "$stale")"       "loop"
is "and the registration date is not restamped" \
  "$(jq -r .created "$stale")" "2026-08-13T00:00:00Z"

# The directory is chmod 700 and the key beside it is 0600. A correction that
# rewrote the metadata world-readable would widen the one thing that names which
# App a machine trusts.
is "the corrected file keeps its mode" \
  "$(printf '%04d' "$(stat -c '%a' "$stale" 2>/dev/null || stat -f '%Lp' "$stale" 2>/dev/null)")" \
  "0600"

# --- when GitHub cannot be reached -----------------------------------------
#
# Rule 5 cuts both ways: an unreachable API is not evidence the cached identity
# is wrong, so nothing is corrected and nothing is claimed.

printf '%s\t%s\n' '*/app --jq*' '!fail' >"$CROSSREV_GH_ROUTES"
_auth_app_identity dummy-jwt >/dev/null 2>&1
is "an unreachable /app fails rather than inventing an identity" "$?" "1"

# --- and `auth status` puts it in front of a person -------------------------
#
# The helpers above are the mechanism; this is the command an operator actually
# runs to answer "is my credential set up correctly". It answered that from the
# cache, confidently, which is the whole defect.

# A real key, because _auth_jwt signs with openssl and a fixture string would
# only prove the stub was reached. Generated once, into the same 0600 the
# registration path creates.
(umask 077; openssl genrsa -out "$(_auth_dir)/ShoreLogic.loop.pem" 2048 2>/dev/null)

{
  printf '%s\t%s\n' '*/app --jq*' \
    '{"name":"CrossRev ShoreLogic","slug":"crossrev-shorelogic","id":987}'
  printf '%s\t%s\n' '*/app/installations*' \
    '[{"account":{"login":"ShoreLogic"},"repository_selection":"selected"}]'
} >"$CROSSREV_GH_ROUTES"

stale="$(meta_fixture "revloop-ShoreLogic" revloop-shorelogic)"
out="$(auth_status 2>&1)"

has "status says the App was renamed" "$out" "renamed"
has "and shows the slug that moved, because that is the half that breaks trust" \
  "$out" "slug was revloop-shorelogic, now crossrev-shorelogic"
has "it reports the name GitHub has, not the one on disk" "$out" "CrossRev ShoreLogic"
hasnt "and does not present the stale name as current" "$out" "— revloop-ShoreLogic ("
is "the cached slug is corrected as a side effect of asking" \
  "$(jq -r .slug "$stale")" "crossrev-shorelogic"

# Running it again is the honest test of a self-healing read: the drift is gone,
# so the warning goes with it rather than firing on every invocation.
out="$(auth_status 2>&1)"
hasnt "a second run reports no drift, having fixed it" "$out" "renamed"
has "and still reports the installation" "$out" "installed on ShoreLogic"

# An App name is free text and routinely contains spaces — CrossRev proposes one
# with a space in it. So the field the report splits on cannot be a space, or a
# rename away from a two-word name prints the halves in the wrong places.
stale="$(meta_fixture "CrossRev Shore Logic" crossrev-shore-logic)"
out="$(auth_status 2>&1)"

has "a rename away from a name with spaces reports the old name whole" \
  "$out" "name was CrossRev Shore Logic, now CrossRev ShoreLogic"
is "and still corrects the slug" "$(jq -r .slug "$stale")" "crossrev-shorelogic"

# --- generalised account lookup --------------------------------------------
printf '%s\t%s\n' '*users/ShoreLogic*' '{"type":"Organization","id":12345}' >"$CROSSREV_GH_ROUTES"
is "_auth_account_info resolves an organisation" \
  "$(_auth_account_info ShoreLogic)" "Organization 12345"

printf '%s\t%s\n' '*users/carlosboeing*' '{"type":"User","id":3394597}' >"$CROSSREV_GH_ROUTES"
is "_auth_account_info resolves a user" \
  "$(_auth_account_info carlosboeing)" "User 3394597"

printf '%s\t%s\n' '*users/crossrev-acme\[bot\]*' '{"type":"Bot","id":99999}' >"$CROSSREV_GH_ROUTES"
is "_auth_account_info resolves a bot account" \
  "$(_auth_account_info "crossrev-acme[bot]")" "Bot 99999"

printf '%s\t%s\n' '*users/nonexistent*' '!fail' >"$CROSSREV_GH_ROUTES"
_auth_account_info nonexistent >/dev/null 2>&1
is "_auth_account_info fails when the account does not exist" "$?" "1"

# --- collision detection on auth login --------------------------------------
#
# When local metadata is missing but the App still exists on GitHub, auth login
# must detect the collision via the bot user existence probe before opening a
# browser, refuse to proceed, and explain both ways forward (reuse and --name).

# Decline confirmations in tests rather than waiting on /dev/tty
ui_confirm() {
  printf '◆  %s  [y/N]\n' "$1"
  return 1
}

XDG_CONFIG_HOME="$(mktemp -d)"; export XDG_CONFIG_HOME

{
  printf '%s\t%s\n' '*users/ShoreLogic*' '{"type":"Organization","id":12345}'
  printf '%s\t%s\n' '*users/crossrev-shorelogic\[bot\]*' '{"type":"Bot","id":99999}'
} >"$CROSSREV_GH_ROUTES"

collision_out="$( (auth_login --owner ShoreLogic) 2>&1 || true )"
has "collision detection refuses when the App exists on GitHub" \
  "$collision_out" "a GitHub App named 'CrossRev ShoreLogic' already exists"
has "and names reuse by generating a fresh private key" \
  "$collision_out" "generate a fresh private key"
has "and names the --name override" \
  "$collision_out" "crossrev auth login --name"
hasnt "and does not prompt to open a browser" \
  "$collision_out" "Open GitHub"

# --- probe returns 404 and login proceeds to confirmation -------------------
#
# When the bot account does not exist, the flow proceeds past the existence check
# to the confirmation panel.

{
  printf '%s\t%s\n' '*users/ShoreLogic*' '{"type":"Organization","id":12345}'
  printf '%s\t%s\n' '*users/crossrev-shorelogic\[bot\]*' '!fail'
} >"$CROSSREV_GH_ROUTES"

proceed_out="$( (echo "n" | auth_login --owner ShoreLogic) 2>&1 || true )"
has "flow proceeds to registration panel when App does not exist" \
  "$proceed_out" "Register a GitHub App for ShoreLogic"
has "the panel shows the --name override option" \
  "$proceed_out" "Name         CrossRev ShoreLogic (override with --name)"
has "and prompts before opening a browser" \
  "$proceed_out" "Open GitHub in your browser to create the App?"

# --- custom --name passed to auth login -------------------------------------
{
  printf '%s\t%s\n' '*users/ShoreLogic*' '{"type":"Organization","id":12345}'
  printf '%s\t%s\n' '*users/my-custom-app\[bot\]*' '!fail'
} >"$CROSSREV_GH_ROUTES"

custom_out="$( (echo "n" | auth_login --owner ShoreLogic --name "My Custom App") 2>&1 || true )"
has "the panel reflects the custom name and override hint" \
  "$custom_out" "Name         My Custom App (override with --name)"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))

