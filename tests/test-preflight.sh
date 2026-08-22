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

  # openssl as GitHub's hosted runners have it, which is the only machine that
  # exposed the defect: the subcommand is `openssl version`, and this build
  # rejects the --version flag outright rather than accepting it as an alias.
  cat >"$d/openssl" <<'STUB'
#!/usr/bin/env bash
[[ "${1:-}" == "version" ]] && { printf 'OpenSSL 3.0.2 15 Mar 2022\n'; exit 0; }
printf 'Invalid command '"'"'%s'"'"'; type "help" for a list.\n' "${1:-}" >&2
exit 1
STUB

  chmod +x "$d"/*

  # The stub directory is the whole PATH, not the front of it — otherwise "this
  # tool is missing" only means "missing from the stubs", and the real one two
  # entries along answers instead. So the handful of commands preflight and ui
  # reach for come in as symlinks, `bash` among them because the stubs are
  # `#!/usr/bin/env bash` and env resolves that through PATH like anything else.
  local ext p
  for ext in bash env uname grep head sed cat tr cut awk; do
    p="$(command -v "$ext")" && ln -sf "$p" "$d/$ext"
  done

  printf '%s' "$d"
}

# Replace one tool in a stub PATH. Each case below wants a machine that is wrong
# in exactly one way, and rebuilding the whole directory to say so would bury it.
stub_tool() {
  local dir="$1" name="$2" body="$3"
  printf '#!/usr/bin/env bash\n%s\n' "$body" >"$dir/$name"
  chmod +x "$dir/$name"
}

# preflight_check's report and its exit status, run against a stub PATH.
#
# The subshell is what makes the PATH override local to the case, and sourcing
# both libraries the way action.yml does keeps this honest about the environment
# the composite action actually calls it in.
run_preflight() {
  local dir="$1" need="${2:-core}" out rc
  out="$( {
    PATH="$dir"
    export PATH
    # A GitHub runner exports GITHUB_ACTIONS=true into every step, and this suite
    # runs there too — so a case meaning "not on a runner" has to say so rather
    # than assume the variable is absent. Inheriting it made the local-message
    # case pass on a laptop and fail in CI, which is precisely the shape of
    # mistake the rest of this file is built to keep out.
    GITHUB_ACTIONS="${STUB_ON_RUNNER:-}"
    export GITHUB_ACTIONS
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
r="$(STUB_ON_RUNNER=true STUB_GH_REACHABLE="" run_preflight "$d")"
is    "a rejected token on a runner still fails"  "$(status "$r")" "1"
hasnt "but is not told to run gh auth login"      "$(report "$r")" "gh auth login"
has   "and is pointed at the token the workflow passed" "$(report "$r")" "app-token"

# ---------------------------------------------------------------------------
# A tool is checked by whether it runs, not by whether it exists
# ---------------------------------------------------------------------------
#
# `_tool_version` probed everything with --version, folded stderr into the
# capture, and printed whatever came back when no version parsed out of it — so
# openssl's refusal of the flag was displayed as its version, under a tick. That
# made the whole check vacuous: any tool that merely existed passed it, which is
# the opposite of what preflight is for.

r="$(STUB_GH_REACHABLE="user rate_limit" run_preflight "$d")"
is    "openssl is probed with its own subcommand"        "$(status "$r")" "0"
has   "and the version it reports is the version"        "$(report "$r")" "openssl 3.0.2"
hasnt "not its refusal of a flag it does not have"       "$(report "$r")" "Invalid command"

# A tool that runs but says nothing version-shaped is the general case openssl
# was one instance of, and it has to fail rather than tick.
b="$(stub_path)"
stub_tool "$b" yq 'printf "yq: error while loading shared libraries\n" >&2; exit 1'
r="$(STUB_GH_REACHABLE="user rate_limit" run_preflight "$b")"
is    "a tool that reports no version fails the check" "$(status "$r")" "1"
has   "and says it is installed rather than missing"   "$(report "$r")" "yq — installed, but it did not report a version"
hasnt "so nobody is sent to install what is already there" "$(report "$r")" "yq — not found"

# The distinction the previous case rests on: genuinely missing still reads as
# missing, and still names the install command.
m="$(stub_path)"
rm -f "$m/yq"
r="$(STUB_GH_REACHABLE="user rate_limit" run_preflight "$m")"
is  "a missing tool still fails the check"    "$(status "$r")" "1"
has "and is still reported as not found"      "$(report "$r")" "yq — not found"
has "with the command that would install it"  "$(report "$r")" "Install with:"

# ---------------------------------------------------------------------------
# Pairing support and secrets from descriptor
# ---------------------------------------------------------------------------
# shellcheck source=lib/ui.sh
source "$ROOT/lib/ui.sh"
# shellcheck source=lib/harnesses.sh
source "$ROOT/lib/harnesses.sh"
# shellcheck source=lib/preflight.sh
source "$ROOT/lib/preflight.sh"

is "preflight_harness_secret claude" "$(preflight_harness_secret claude)" "CLAUDE_CODE_OAUTH_TOKEN"
! preflight_harness_secret agy >/dev/null 2>&1
is "preflight_harness_secret agy returns 1" "$?" 0

msg="$(preflight_pairing_supported github-hosted agy)"
rc=$?
is "a hosted agy pairing is refused" "$rc" "1"
has "hosted agy refusal names 56 minutes" "$msg" "56 minutes"
has "hosted agy refusal says CrossRev cannot seed into hosted runner yet" "$msg" "CrossRev has no way to seed it into a hosted runner yet"

preflight_pairing_supported github-hosted codex >/dev/null 2>&1
is "a hosted codex pairing is supported" "$?" 0

preflight_pairing_supported self-hosted agy >/dev/null 2>&1
is "a self-hosted agy pairing is supported" "$?" 0

# A review-only harness is a pairing fact before it is a credential fact:
# doctor must report the leg limit for automated mode rather than leave it to a
# failing job, even though the credential itself would be fine. The third
# argument speaks the descriptor's vocabulary, review and resolve.
preflight_pairing_supported github-hosted opencode review >/dev/null 2>&1
is "a hosted opencode review pairing is supported" "$?" 0
msg="$(preflight_pairing_supported github-hosted opencode resolve)"; rc=$?
is "a hosted opencode resolve pairing is refused" "$rc" "1"
has "and the refusal says the harness is review-only" "$msg" "review-only"

# Without naming a leg — the historical call shape — the credential answer is
# unchanged.
preflight_pairing_supported github-hosted opencode >/dev/null 2>&1
is "a hosted opencode pairing with no leg named still passes on its archetype" "$?" 0

preflight_needs_refresher github-hosted codex ""
is "preflight_needs_refresher github-hosted codex is true" "$?" 0

! preflight_needs_refresher github-hosted claude ""
is "preflight_needs_refresher github-hosted claude is false" "$?" 0

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
