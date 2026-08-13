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

# GitHub derives a slug by lowercasing and turning spaces into hyphens. Modelled
# here rather than asserted from a live API, so the suite stays offline.
slugify() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | tr ' ' '-'; }

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
  "$(slugify "$(_auth_role_default_name loop acme)")" "crossrev-acme"
is "and the refresher slug is unchanged too" \
  "$(slugify "$(_auth_role_default_name refresher acme)")" "crossrev-refresh-acme"

# The fixture identity the stubbed-gh suites assert against, derived the same
# way, so those suites and this one cannot drift apart.
is "the slug matches the identity the other suites use" \
  "$(slugify "$(_auth_role_default_name loop acme)")[bot]" "crossrev-acme[bot]"

# An owner whose name is already mixed case keeps its own casing: the owner half
# is an identity GitHub chose, not prose CrossRev gets to restyle.
is "the owner's own casing survives" \
  "$(_auth_role_default_name loop ShoreLogic)" "CrossRev ShoreLogic"
is "and still slugs to what the App is installed as" \
  "$(slugify "$(_auth_role_default_name loop ShoreLogic)")" "crossrev-shorelogic"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
