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
export PATH="$HERE/stub:$PATH"
# shellcheck source=../lib/ui.sh
source "$HERE/../lib/ui.sh"
# shellcheck source=../lib/harnesses.sh
source "$HERE/../lib/harnesses.sh"
# shellcheck source=../lib/credentials.sh
source "$HERE/../lib/credentials.sh"
# shellcheck source=../lib/auth.sh
source "$HERE/../lib/auth.sh"

# In offline test suites and CI, there is no controlling terminal (/dev/tty).
# Stub the input source to /dev/stdin so _ui_input_source succeeds, and stub
# ui_confirm to print the prompt and decline rather than blocking on /dev/tty.
_ui_input_source() { printf '/dev/stdin'; }
ui_confirm() {
  printf '◆  %s  [y/N]\n' "$1"
  return 1
}

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
export CROSSREV_BROWSER_LOG="$STUB_DIR/browser.log"
: >"$CROSSREV_BROWSER_LOG"
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


XDG_CONFIG_HOME="$(mktemp -d)"; export XDG_CONFIG_HOME

{
  printf '%s\t%s\n' '*users/ShoreLogic*' '{"type":"Organization","id":12345}'
  printf '%s\t%s\n' '*users/crossrev-shorelogic\[bot\]*' '{"type":"Bot","id":99999}'
} >"$CROSSREV_GH_ROUTES"

collision_out="$( (auth_login --owner ShoreLogic) 2>&1 || true )"
has "collision detection refuses when the App exists on GitHub" \
  "$collision_out" "a GitHub App named 'CrossRev ShoreLogic' already exists"
has "and explains that CrossRev cannot reuse an existing App without metadata" \
  "$collision_out" "CrossRev cannot reuse an existing App when local metadata is missing"
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

# --- standalone auth install does not claim a step count --------------------
#
# Running auth install on its own is not halfway through a login flow, so it must
# not print "Step 2 of 2".

meta_fixture "CrossRev ShoreLogic" crossrev-shorelogic >/dev/null
(umask 077; openssl genrsa -out "$(_auth_dir)/ShoreLogic.loop.pem" 2048 2>/dev/null)

{
  printf '%s\t%s\n' '*/app/installations*' \
    '[{"account":{"login":"ShoreLogic"},"repository_selection":"selected"}]'
} >"$CROSSREV_GH_ROUTES"

install_out="$( (auth_install --owner ShoreLogic) 2>&1 || true )"
has "standalone auth install prints install section" \
  "$install_out" "Install the App on the repositories you want reviewed"
hasnt "standalone auth install does not print a step count" \
  "$install_out" "Step 2 of 2"
hasnt "standalone auth install does not mention step 1" \
  "$install_out" "Step 1 of 2"
has "standalone auth install intercepted the browser call" \
  "$(cat "$CROSSREV_BROWSER_LOG")" \
  "https://github.com/apps/crossrev-shorelogic/installations/new/permissions?target_id=12345&target_type=Organization"

# --- a key that cannot sign is refused before it is installed ---------------
#
# `auth rotate` proves the new key before replacing the old one, with
# `jwt="$(_auth_jwt "$keyfile" "$app_id")" || ui_die "could not sign a token"`.
# _auth_jwt ended in printf, so its status was printf's and that guard could not
# fire: a file openssl refused to sign with still produced a token, with an empty
# signature segment, and the operator was told GitHub had rejected the key rather
# than that nothing had signed it.

XDG_CONFIG_HOME="$(mktemp -d)"; export XDG_CONFIG_HOME
meta_fixture "CrossRev ShoreLogic" crossrev-shorelogic >/dev/null
(umask 077; openssl genrsa -out "$(_auth_dir)/ShoreLogic.loop.pem" 2048 2>/dev/null)
installed_key="$(cat "$(_auth_dir)/ShoreLogic.loop.pem")"
: >"$CROSSREV_GH_ROUTES"

not_a_key="$STUB_DIR/not-a-key.pem"
printf 'this is not a private key\n' >"$not_a_key"

# The unit the guard reads. A signature openssl never produced must not come back
# as a token, and must not come back as success.
jwt_out="$(_auth_jwt "$not_a_key" 987 2>/dev/null)"; jwt_rc=$?
is "_auth_jwt fails when openssl cannot sign" "$jwt_rc" "1"
is "and prints nothing rather than a token with an empty signature" "$jwt_out" ""

# jq --argjson rejects a non-numeric iss, which left the claims empty and the
# token malformed in the same silent way.
jwt_out="$(_auth_jwt "$(_auth_dir)/ShoreLogic.loop.pem" "not-a-number" 2>/dev/null)"; jwt_rc=$?
is "_auth_jwt fails when the App id is not a whole number" "$jwt_rc" "1"
is "and prints nothing for a non-numeric App id" "$jwt_out" ""

# The positive control, so the refusals above are not just a function that always
# fails: a real key and a real id still mint a three-segment JWT.
jwt_out="$(_auth_jwt "$(_auth_dir)/ShoreLogic.loop.pem" 987 2>/dev/null)"; jwt_rc=$?
is "a real key still signs" "$jwt_rc" "0"
is "and the token still has three segments" \
  "$(awk -F. '{print NF}' <<<"$jwt_out")" "3"
is "carrying a signature segment rather than an empty one" \
  "$(awk -F. '{print ($3 == "") ? "empty" : "signed"}' <<<"$jwt_out")" "signed"

# And the command an operator runs. The stubbed gh answers every route, so a
# token that reached GitHub would be accepted and the bad key installed.
rotate_out="$( (auth_rotate --owner ShoreLogic --key "$not_a_key") 2>&1 || true )"
has "auth rotate refuses a key it cannot sign with" \
  "$rotate_out" "could not sign a token with $not_a_key"
hasnt "and does not report the rotation done" "$rotate_out" "Rotated"
hasnt "nor blames GitHub for a token that was never signed" \
  "$rotate_out" "GitHub rejected a token"
is "the key already installed is left untouched" \
  "$(cat "$(_auth_dir)/ShoreLogic.loop.pem")" "$installed_key"

# A path with no file at all is caught one guard earlier, by the -f test, so it
# names the missing path rather than the signing failure.
missing_out="$( (auth_rotate --owner ShoreLogic --key "$STUB_DIR/absent.pem") 2>&1 || true )"
has "a key file that does not exist reports the path instead" \
  "$missing_out" "there is no file at $STUB_DIR/absent.pem"

# --- a login redirect must carry the state CrossRev generated ---------------
#
# auth_login posts a random state with the manifest and reads it back off the
# redirect. The loopback listener binds localhost only, so a request reaching it
# came from a process on the operator's own machine — which is precisely what the
# state exists to tell apart from the browser CrossRev opened. Checking it only
# when one came back accepted a request carrying a code and no state at all: the
# code was exchanged and the App's private key written to disk.
#
# The listener socket is stood in for here. What these cases drive is the parsing
# and the check around it, which is where the defect was and what changed.

XDG_CONFIG_HOME="$(mktemp -d)"; export XDG_CONFIG_HOME
export CROSSREV_GH_LOG="$STUB_DIR/gh.log"

# A real key, because the flow signs with it before reporting the install.
conv_pem="$(openssl genrsa 2048 2>/dev/null)"
jq -n --arg pem "$conv_pem" \
  '{id:987, slug:"crossrev-shorelogic", name:"CrossRev ShoreLogic", pem:$pem}' \
  >"$STUB_DIR/conversion.json"

login_routes() {
  {
    printf '%s\t%s\n' '*users/ShoreLogic*' '{"type":"Organization","id":12345}'
    printf '%s\t%s\n' '*users/crossrev-shorelogic\[bot\]*' '!fail'
    printf '%s\t%s\n' '*app-manifests*' "@$STUB_DIR/conversion.json"
    printf '%s\t%s\n' '*/app/installations*' \
      '[{"account":{"login":"ShoreLogic"},"repository_selection":"selected"}]'
  } >"$CROSSREV_GH_ROUTES"
  : >"$CROSSREV_GH_LOG"
  XDG_CONFIG_HOME="$(mktemp -d)"; export XDG_CONFIG_HOME
}

# The confirmation stub above declines, which is right for every case before
# this one. These have to get past it.
ui_confirm() { printf '◆  %s  [y/N]\n' "$1"; return 0; }

# The state is generated inside auth_login and never printed, so the stand-in
# browser reads it off the page it was handed, the way a real one does before
# posting the manifest to GitHub.
browser_state=""
_open_browser() {
  printf '%s\n' "$1" >>"$CROSSREV_BROWSER_LOG"
  # The install step opens a github.com URL through the same helper; only the
  # manifest page is a local file with a state on it.
  [[ "$1" == file://* ]] &&
    browser_state="$(sed -n 's/.*name="state" value="\([^"]*\)".*/\1/p' "${1#file://}")"
  return 0
}

# What the redirect put on the wire. %STATE% stands for the value the page
# carried, so a case can echo it back, alter it, or leave it out entirely.
listener_query=""
_listener_available() { [[ -n "$listener_query" ]]; }
_free_port() { printf '33517'; }
_listen_for_code() {
  [[ -n "$listener_query" ]] || return 1
  printf 'GET /crossrev-auth?%s HTTP/1.1\r\n' "${listener_query//%STATE%/$browser_state}" >"$2"
  return 0
}

key_written() { [[ -f "$(_auth_dir)/ShoreLogic.loop.pem" ]] && printf 'yes' || printf 'no'; }

# The positive control. Without it, "refused" below could just as well mean the
# listener path never completes at all.
login_routes
listener_query='code=good-code&state=%STATE%'
matched_out="$( (auth_login --owner ShoreLogic </dev/null) 2>&1 || true )"
has "a redirect carrying the state CrossRev sent registers the App" \
  "$matched_out" "App    CrossRev ShoreLogic (id 987)"
is "and the private key is written" "$(key_written)" "yes"

# The defect. A code with no state reached the conversion call and the key landed
# on disk.
login_routes
listener_query='code=attacker-code'
nostate_out="$( (auth_login --owner ShoreLogic </dev/null) 2>&1 || true )"
has "a redirect with a code and no state is refused" \
  "$nostate_out" "the state value GitHub returned does not match the one CrossRev sent"
hasnt "and the App is not registered" "$nostate_out" "App    CrossRev ShoreLogic"
is "and no private key is written" "$(key_written)" "no"
hasnt "and the code is never exchanged" "$(cat "$CROSSREV_GH_LOG")" "app-manifests"

# A state that came back wrong was already refused, and still is.
login_routes
listener_query='code=good-code&state=not-the-one'
wrongstate_out="$( (auth_login --owner ShoreLogic </dev/null) 2>&1 || true )"
has "a redirect carrying the wrong state is still refused" \
  "$wrongstate_out" "the state value GitHub returned does not match the one CrossRev sent"
is "and writes no key either" "$(key_written)" "no"

# The paste fallback is a person reading their own address bar, not a redirect,
# and the prompt documents the bare code as an answer. It stays accepted.
login_routes
listener_query=''
paste_out="$( (printf 'pasted-bare-code\n' | auth_login --owner ShoreLogic) 2>&1 || true )"
has "a pasted bare code still registers the App" \
  "$paste_out" "App    CrossRev ShoreLogic (id 987)"
is "and still writes the private key" "$(key_written)" "yes"
has "and it is the pasted code that was exchanged" \
  "$(cat "$CROSSREV_GH_LOG")" "app-manifests/pasted-bare-code/conversions"

# A pasted URL is still held to the state it carries.
login_routes
listener_query=''
pastebad_out="$( (printf 'http://localhost:33517/crossrev-auth?code=c&state=not-the-one\n' \
  | auth_login --owner ShoreLogic) 2>&1 || true )"
has "a pasted URL carrying the wrong state is refused" \
  "$pastebad_out" "the state value GitHub returned does not match the one CrossRev sent"
is "and writes no key" "$(key_written)" "no"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))

