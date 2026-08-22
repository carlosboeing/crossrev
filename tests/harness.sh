# shellcheck shell=bash
# tests/harness.sh — shared scaffolding for the stubbed-gh suites.
#
# Sourced, not run. It builds a throwaway repository with a real git history and a
# real bare origin, puts the stub `gh` and `claude` earlier on PATH, and gives each
# test a routes file to answer reads from.
#
# Nothing here touches the network, a harness, a pull request, or the developer's
# own ~/.config.

# Everything defined here is read by the suites that source this file, and a
# linter reading only this file cannot see that.
# shellcheck disable=SC2034

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CROSSREV="$HERE/../bin/crossrev"

pass=0; fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" || notok "$1" "contains '$3'" "$2"; }
hasnt() { [[ "$2" != *"$3"* ]] && ok "$1" || notok "$1" "does not contain '$3'" "$2"; }

# An operator config that is not the developer's own: a test reading the real
# ~/.config would pass or fail depending on whose machine it ran on.
XDG_CONFIG_HOME="$(mktemp -d)"; export XDG_CONFIG_HOME
XDG_STATE_HOME="$(mktemp -d)"; export XDG_STATE_HOME

# Every fixture calls `git init` twice, and git copies its sample hooks into each
# new repository from the default template. Nothing here runs a hook, so the
# fixtures init against an empty directory instead and skip the copy.
FIX_TEMPLATE="$(mktemp -d)"

# Fixture identities, so every route and assertion can name them.
FIX_REPO="acme/widget"
FIX_PR=42
FIX_USER="carlosboeing"
FIX_APP="crossrev-acme[bot]"
FIX_DIR=""       # set by fixture_repo
FIX_ORIGIN=""    # the bare repo the fixture pushes to
FIX_HEAD=""      # set by fixture_repo, a real SHA
FIX_BASE=""

# The config committed on main, so both legs run on the one harness the suite
# stubs. The two models differ, which is what keeps the divergence guard live in
# every case rather than only in the one that tests it.
fixture_default_config() {
  fixture_config local medium
}

fixture_config() {
  local mode="$1" min_fix_severity="$2"
  cat <<EOF
version: 1
mode: $mode
policy:
  min_fix_severity: $min_fix_severity
  max_passes_per_cycle: 3
  max_files_changed_per_pr: 200
  max_prs_per_day: 25
reviewer:
  harness: claude
  model: reviewer-model
resolver:
  harness: claude
  model: resolver-model
backlog:
  destination: none
EOF
}

# A throwaway repository with a bare origin.
#
# The origin's configured URL is a github.com address, and `pushInsteadOf` sends
# writes to a local bare repository so a push actually lands. The remote's own
# configuration is what a real checkout carries, so the guard reads exactly what
# it would in production and needs no test-only branch to let the suite through.
#
# $1 is the repository config to commit on main, defaulting to the one above.
# Committing it on main rather than writing it into the working tree is not
# incidental: the legs read policy from the base revision, so a config that only
# exists on the branch takes no effect at all.
#
# Sets FIX_DIR, FIX_HEAD and FIX_BASE and leaves the shell inside the checkout.
# Deliberately not `d=$(fixture_repo)`: a command substitution is a subshell, so
# the SHAs would be set and lost and every route would carry an empty head.
fixture_repo() {
  local config="${1:-$(fixture_default_config)}"
  local d bare
  d="$(mktemp -d)"; bare="$(mktemp -d)/origin.git"
  git init -q --bare --template="$FIX_TEMPLATE" "$bare"
  (
    cd "$d" || exit 1
    git init -q -b main --template="$FIX_TEMPLATE" .
    # The identity stays in the repository's own config rather than moving to
    # GIT_AUTHOR_* in the environment. The resolve leg commits inside this
    # checkout, and an exported identity would also override the one a test sets
    # on a repository it builds itself.
    git config user.email t@example.com
    git config user.name Test
    printf 'export const ok = 1\n' >app.ts
    mkdir -p .github
    printf '%s\n' "$config" >.github/crossrev.yml
    git add -A && git commit -q -m base
    git remote add origin "https://github.com/$FIX_REPO.git"
    git config "url.$bare.pushInsteadOf" "https://github.com/$FIX_REPO.git"
    git checkout -q -b feature
    printf 'export const ok = 1\nexport function refresh() { fetch("/t") }\n' >app.ts
    git add -A && git commit -q -m feature
    # Both branches in one push. The bare repository ends up holding exactly what
    # two pushes left it holding, and a push is the most expensive step here.
    git push -q origin main feature
    git checkout -q main
  )
  cd "$d" || exit 1
  FIX_DIR="$d"
  FIX_ORIGIN="$bare"
  # One rev-parse for both, in the order they are named.
  { read -r FIX_BASE; read -r FIX_HEAD; } < <(git rev-parse main feature)
}

# Stub wiring. Call after fixture_repo, once per case, so each case gets a clean
# call log and a clean route table.
GH_LOG=""; GH_ROUTES=""; PROMPT_LOG=""; ARGV_LOG=""; BROWSER_LOG=""
stub_reset() {
  local d; d="$(mktemp -d)"
  GH_LOG="$d/gh.log"; GH_ROUTES="$d/routes"; PROMPT_LOG="$d/prompt"; ARGV_LOG="$d/argv"
  BROWSER_LOG="$d/browser.log"
  : >"$GH_LOG"; : >"$GH_ROUTES"; : >"$ARGV_LOG"; : >"$BROWSER_LOG"
  export CROSSREV_GH_LOG="$GH_LOG" CROSSREV_GH_ROUTES="$GH_ROUTES" CROSSREV_PROMPT_LOG="$PROMPT_LOG"
  export CROSSREV_BROWSER_LOG="$BROWSER_LOG"
  # Appended to, not overwritten: `crossrev run` invokes the harness twice, and
  # what a test about permission needs is which leg got which flags.
  export CROSSREV_ARGV_LOG="$ARGV_LOG"
  export PATH="$HERE/stub:$PATH"
  unset CROSSREV_REVIEW_PAYLOAD CROSSREV_RESOLVE_PAYLOAD CROSSREV_HARNESS_PAYLOAD CROSSREV_RESOLVE_EDIT

  # GitHub sets RUNNER_ENVIRONMENT=github-hosted on its own runners, and this
  # suite runs there. Left alone every fixture inherits it and takes the hosted
  # path, so `cred_assert_present` demands a harness secret no fixture sets and
  # the leg dies — a test failing in CI while passing on a laptop, for a reason
  # that has nothing to do with what it is testing.
  #
  # Same shape as the GITHUB_ACTIONS leak that STUB_ON_RUNNER already exists for
  # in tests/test-preflight.sh: an ambient variable that means one thing to the
  # code under test and another to the machine running it. The default here is
  # the developer machine, because that is what every fixture is a model of; a
  # test that wants the hosted path sets the variable itself.
  unset RUNNER_ENVIRONMENT
}

# The routes file is line-based, so a multi-line response is spooled to a file and
# referenced. Without this a diff fixture would silently shred every route added
# after it, and the tests would fail for a reason that has nothing to do with
# crossrev.
route() {
  local pat="$1" resp="$2" f
  if [[ "$resp" == *$'\n'* ]]; then
    f="$(mktemp)"; printf '%s' "$resp" >"$f"; resp="@$f"
  fi
  printf '%s\t%s\n' "$pat" "$resp" >>"$GH_ROUTES"
}
# Routes match in file order, so overriding one the baseline already set means
# putting it in front rather than behind.
route_first() {
  local tmp; tmp="$(mktemp)"
  cp "$GH_ROUTES" "$tmp"
  : >"$GH_ROUTES"
  route "$1" "$2"
  cat "$tmp" >>"$GH_ROUTES"
}

calls()  { cat "$GH_LOG"; }

# grep -c prints 0 AND exits 1 on no match, so a `|| printf 0` fallback prints two
# zeroes and every count assertion compares against garbage.
count() {
  local n
  n="$(grep -c -- "$1" "$GH_LOG" 2>/dev/null)" || n=0
  printf '%s' "${n:-0}"
}

payload() {
  local f; f="$(mktemp)"
  cat >"$f"
  printf '%s' "$f"
}

# The reads every leg makes before it does anything: which repo, which user, the
# pull request itself, the diff, and the comments carrying markers.
#
# $1 is a file holding the issue-comments JSON array (the markers), $2 the labels
# on the pull request.
routes_baseline() {
  local comments_file="$1" labels="${2:-[]}"

  route 'repo view --json nameWithOwner*' "{\"nameWithOwner\":\"$FIX_REPO\"}"
  route 'repo view * --json defaultBranchRef*' '{"defaultBranchRef":{"name":"main"}}'
  route 'api user*' "{\"login\":\"$FIX_USER\"}"
  route "pr view $FIX_PR --repo * --json *" "$(jq -cn \
    --argjson n "$FIX_PR" --arg h "$FIX_HEAD" --arg b "$FIX_BASE" --argjson l "$labels" \
    '{number:$n, title:"Add refresh", body:"Adds a refresh helper.", url:"https://github.com/x",
      headRefName:"feature", headRefOid:$h, baseRefName:"main", baseRefOid:$b,
      changedFiles:1, labels:$l, isCrossRepository:false, maintainerCanModify:false, isDraft:false,
      headRepositoryOwner:{login:"acme"}, headRepository:{name:"widget"}, state:"OPEN"}')"
  route '*Accept: application/vnd.github.diff*' 'diff --git a/app.ts b/app.ts
--- a/app.ts
+++ b/app.ts
@@ -1 +1,2 @@
 export const ok = 1
+export function refresh() { fetch("/t") }'
  route "api --paginate repos/*/issues/$FIX_PR/comments*" "@$comments_file"
  route "api --paginate repos/*/pulls/$FIX_PR/comments*" '[]'
  # Keep the repository-wide route after the specific per-pull-request route:
  # routes are matched in file order, and the broader one must not shadow it.
  route 'api --method GET repos/*/issues/comments -f since=* -F per_page=100 -F page=*' '[]'
}

# A review-comments payload in which each named finding id has already been posted.
#
# Built with `tojson` rather than with escaped quotes inside the jq program, and
# that is not a style choice: an inline `"$(jq … '…\"…')"` passed as an argument
# gets mangled by bash before jq ever sees it — the braces and everything after
# the first comma disappear. The same program assigned to a variable first is
# fine. Keeping the escapes out entirely means it cannot come back.
posted_comments() {
  local out="[]" id
  for id in "$@"; do
    out="$(jq -c --arg id "$id" --arg a "$FIX_USER" --arg leg "${POSTED_LEG:-review}" \
      '. + [{body: ("posted <!-- crossrev:f " + ({id:$id, pass:1, leg:$leg} | tojson) + " -->"),
             user: {login: $a}}]' <<<"$out")"
  done
  printf '%s' "$out"
}

# One review thread, with the finding ids its comments carry.
thread_node() {
  local tid="$1" path="$2" line="$3" resolved="$4"; shift 4
  local ids="[]" id
  for id in "$@"; do ids="$(jq -c --arg i "$id" '. + [$i]' <<<"$ids")"; done
  jq -cn --arg id "$tid" --arg p "$path" --argjson l "$line" --argjson r "$resolved" \
    --argjson ids "$ids" --arg a "$FIX_USER" '
    {id:$id, isResolved:$r, isOutdated:false, path:$p, line:$l,
     comments:{nodes: [ $ids[] | {databaseId:5000, author:{login:$a},
       body: ("finding <!-- crossrev:f " + ({id:., pass:1, leg:"review"} | tojson) + " -->")} ]}}'
}

# The GraphQL envelope around a set of thread nodes.
threads_response() {
  local nodes="[]" n
  for n in "$@"; do nodes="$(jq -c --argjson n "$n" '. + [$n]' <<<"$nodes")"; done
  jq -cn --argjson n "$nodes" \
    '{data:{repository:{pullRequest:{reviewThreads:{nodes:$n}}}}}'
}

# A marker comment as GitHub would return it, authored by $3.
marker_comment() {
  local id="$1" marker="$2" author="${3:-$FIX_USER}" body="${4:-Summary.}"
  jq -cn --argjson id "$id" --arg a "$author" --arg b "$body" --arg m "$marker" \
    '{id:$id, body:($b + "\n\n<!-- crossrev: " + $m + " -->"), user:{login:$a},
      created_at:"2026-08-11T00:00:00Z"}'
}

# The last body written to an issue comment, reconstructed from the stub log.
# The claim is posted then edited, so the whole log holds several copies.
last_body() {
  awk -v pat="issues/comments/$1 -f body=" '
    index($0, pat)  { buf = substr($0, index($0, pat) + length(pat)); open = 1; next }
    open && /^(api|gh|repo|pr|secret|issue) / { last = buf; open = 0 }
    open            { buf = buf "\n" $0 }
    END             { if (open) last = buf; print last }' "$GH_LOG"
}

finish() {
  printf '\n  %d passed, %d failed\n' "$pass" "$fail"
  (( fail == 0 ))
}
