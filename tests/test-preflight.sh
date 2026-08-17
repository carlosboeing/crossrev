#!/usr/bin/env bash
# tests/test-preflight.sh — the dependency and credential checks.
#
# Every tool preflight probes is a script this file writes, so a case describes
# the machine it wants rather than inheriting whatever is installed. That is not
# tidiness: the two defects this suite covers both passed on a developer's laptop
# and failed on a GitHub runner, and a check that reads the real PATH would have
# gone on passing here for the same reason.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$HERE/.."

pass=0; fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" || notok "$1" "contains '$3'" "$2"; }
hasnt() { [[ "$2" != *"$3"* ]] && ok "$1" || notok "$1" "does not contain '$3'" "$2"; }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }

# A throwaway PATH holding one script per tool in the core requirement set.
#
# `gh` answers only the API paths named in STUB_GH_REACHABLE, which is how a case
# says "this is an App installation token" or "this is a personal token" without
# a token existing anywhere. jq's real path is baked in at build time because the
# stub needs it to honour --jq, and the directory it lives in shadows the real jq.
stub_path() {
  local d; d="$(mktemp -d)"

  {
    printf '#!/usr/bin/env bash\n'
    printf 'JQ=%q\n' "$(command -v jq)"
    cat <<'STUB'
set -uo pipefail
[[ "${1:-}" == "--version" ]] && { printf 'gh version 2.62.0 (2026-01-01)\n'; exit 0; }
[[ "${1:-}" == "api" ]] || exit 0
gh_path="${2:-}"
case " ${STUB_GH_REACHABLE:-} " in
  *" $gh_path "*) ;;
  *) printf 'gh: HTTP 403: Resource not accessible by integration\n' >&2; exit 1 ;;
esac
case "$gh_path" in
  user)                      body='{"login":"carlosboeing"}' ;;
  installation/repositories) body='{"total_count":2}' ;;
  rate_limit)                body='{"rate":{"limit":5000}}' ;;
  *)                         body='{}' ;;
esac
jq_expr=""; prev=""
for a in "$@"; do [[ "$prev" == "--jq" ]] && { jq_expr="$a"; break; }; prev="$a"; done
if [[ -n "$jq_expr" ]]; then
  printf '%s' "$body" | "$JQ" -r "$jq_expr"
else
  printf '%s\n' "$body"
fi
STUB
  } >"$d/gh"

  printf '#!/usr/bin/env bash\nprintf "git version 2.54.0\\n"\n'                >"$d/git"
  printf '#!/usr/bin/env bash\nprintf "jq-1.7.1\\n"\n'                          >"$d/jq"
  printf '#!/usr/bin/env bash\nprintf "yq (mikefarah/yq) version v4.53.3\\n"\n' >"$d/yq"
  printf '#!/usr/bin/env bash\nprintf "OpenSSL 3.6.3 1 Jul 2026\\n"\n'          >"$d/openssl"

  chmod +x "$d"/*
  printf '%s' "$d"
}

# preflight_check's report and its exit status, run against a stub PATH.
#
# The subshell is what makes the PATH override local to the case, and sourcing
# both libraries the way action.yml does keeps this honest about the environment
# the composite action actually calls it in.
run_preflight() {
  local dir="$1" need="${2:-core}" out rc
  out="$( {
    PATH="$dir:$PATH"
    export PATH
    # shellcheck source=lib/ui.sh
    source "$ROOT/lib/ui.sh"
    # shellcheck source=lib/preflight.sh
    source "$ROOT/lib/preflight.sh"
    preflight_check "$need"
  } 2>&1 )"
  rc=$?
  printf '%s\n--rc %d\n' "$out" "$rc"
}

# The report only; the status is asserted from the trailing marker.
report() { sed '$d' <<<"$1"; }
status() { sed -n 's/^--rc //p' <<<"$1"; }

# ---------------------------------------------------------------------------
# gh is proved usable by an endpoint the token can actually reach
# ---------------------------------------------------------------------------
#
# Automated mode authenticates as a GitHub App installation on every run, and an
# installation token is scoped to the installation rather than to a user, so it
# cannot read GET /user. Probing that endpoint reported a working token as
# unauthenticated and killed the leg before it did any work.

d="$(stub_path)"

r="$(STUB_GH_REACHABLE="installation/repositories rate_limit" run_preflight "$d")"
is    "an App installation token passes the gh check" "$(status "$r")" "0"
hasnt "and is not reported as unauthenticated"        "$(report "$r")" "not authenticated"
hasnt "and is not told to run a command a runner cannot run" "$(report "$r")" "gh auth login"
has   "and the identity comes from an endpoint the token can read" \
      "$(report "$r")" "GitHub App installation"

r="$(STUB_GH_REACHABLE="user rate_limit" run_preflight "$d")"
is  "a personal token still passes"        "$(status "$r")" "0"
has "and still reports the login it names" "$(report "$r")" "authenticated as carlosboeing"

# The universal fallback: a token that reaches neither identity endpoint is still
# a working token, and rate_limit is the one route every token type can take.
r="$(STUB_GH_REACHABLE="rate_limit" run_preflight "$d")"
is    "a token that reaches only rate_limit passes" "$(status "$r")" "0"
hasnt "without claiming an identity it never read"  "$(report "$r")" "authenticated as "

r="$(STUB_GH_REACHABLE="" run_preflight "$d")"
is  "a token that reaches nothing fails the check" "$(status "$r")" "1"
has "and locally the fix is still gh auth login"   "$(report "$r")" "gh auth login"

# On a runner there is no interactive login to run and nothing wrong with the
# credential's shape, so the same failure has to name a different fix.
r="$(GITHUB_ACTIONS=true STUB_GH_REACHABLE="" run_preflight "$d")"
is    "a rejected token on a runner still fails"  "$(status "$r")" "1"
hasnt "but is not told to run gh auth login"      "$(report "$r")" "gh auth login"
has   "and is pointed at the token the workflow passed" "$(report "$r")" "app-token"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
