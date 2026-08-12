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
REVLOOP="$HERE/../bin/revloop"

pass=0; fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" || notok "$1" "contains '$3'" "$2"; }
hasnt() { [[ "$2" != *"$3"* ]] && ok "$1" || notok "$1" "does not contain '$3'" "$2"; }

# An operator config that is not the developer's own: a test reading the real
# ~/.config would pass or fail depending on whose machine it ran on.
XDG_CONFIG_HOME="$(mktemp -d)"; export XDG_CONFIG_HOME

# Fixture identities, so every route and assertion can name them.
FIX_REPO="acme/widget"
FIX_PR=42
FIX_USER="carlosboeing"
FIX_APP="revloop-acme[bot]"
FIX_DIR=""       # set by fixture_repo
FIX_ORIGIN=""    # the bare repo the fixture pushes to
FIX_HEAD=""      # set by fixture_repo, a real SHA
FIX_BASE=""

# The config committed on main, so both legs run on the one harness the suite
# stubs. The two models differ, which is what keeps the divergence guard live in
# every case rather than only in the one that tests it.
fixture_default_config() {
  cat <<'EOF'
version: 1
mode: single-run
max_passes: 3
reviewer:
  harness: claude
  model: reviewer-model
resolver:
  harness: claude
  model: resolver-model
  fix_at: medium
persist:
  defects: none
caps:
  runs_per_day: 12
  max_files_changed: 200
EOF
}

# A throwaway repository with a bare origin.
#
# The origin's FETCH url is a github.com address so the push guard sees the right
# thing, and its PUSH url is the local bare repo so the push actually lands. Both
# are ordinary git configuration rather than a hole in the guard.
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
  git init -q --bare "$bare"
  (
    cd "$d" || exit 1
    git init -q -b main .
    git config user.email t@example.com
    git config user.name Test
    printf 'export const ok = 1\n' >app.ts
    mkdir -p .github
    printf '%s\n' "$config" >.github/revloop.yml
    git add -A && git commit -q -m base
    git remote add origin "https://github.com/$FIX_REPO.git"
    git remote set-url --push origin "$bare"
    git push -q origin main
    git checkout -q -b feature
    printf 'export const ok = 1\nexport function refresh() { fetch("/t") }\n' >app.ts
    git add -A && git commit -q -m feature
    git push -q origin feature
  )
  cd "$d" || exit 1
  FIX_DIR="$d"
  FIX_ORIGIN="$bare"
  FIX_BASE="$(git rev-parse main)"
  FIX_HEAD="$(git rev-parse HEAD)"
}

# Stub wiring. Call after fixture_repo, once per case, so each case gets a clean
# call log and a clean route table.
GH_LOG=""; GH_ROUTES=""; PROMPT_LOG=""
stub_reset() {
  local d; d="$(mktemp -d)"
  GH_LOG="$d/gh.log"; GH_ROUTES="$d/routes"; PROMPT_LOG="$d/prompt"
  : >"$GH_LOG"; : >"$GH_ROUTES"
  export REVLOOP_GH_LOG="$GH_LOG" REVLOOP_GH_ROUTES="$GH_ROUTES" REVLOOP_PROMPT_LOG="$PROMPT_LOG"
  export PATH="$HERE/stub:$PATH"
  unset REVLOOP_REVIEW_PAYLOAD REVLOOP_RESOLVE_PAYLOAD REVLOOP_HARNESS_PAYLOAD REVLOOP_RESOLVE_EDIT
}

# The routes file is line-based, so a multi-line response is spooled to a file and
# referenced. Without this a diff fixture would silently shred every route added
# after it, and the tests would fail for a reason that has nothing to do with
# revloop.
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
      changedFiles:1, labels:$l, isCrossRepository:false, isDraft:false,
      headRepositoryOwner:{login:"acme"}, headRepository:{name:"widget"}, state:"OPEN"}')"
  route '*Accept: application/vnd.github.diff*' 'diff --git a/app.ts b/app.ts
--- a/app.ts
+++ b/app.ts
@@ -1 +1,2 @@
 export const ok = 1
+export function refresh() { fetch("/t") }'
  route "api --paginate repos/*/issues/$FIX_PR/comments*" "@$comments_file"
  route "api --paginate repos/*/pulls/$FIX_PR/comments*" '[]'
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
      '. + [{body: ("posted <!-- revloop:f " + ({id:$id, pass:1, leg:$leg} | tojson) + " -->"),
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
       body: ("finding <!-- revloop:f " + ({id:., pass:1, leg:"review"} | tojson) + " -->")} ]}}'
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
    '{id:$id, body:($b + "\n\n<!-- revloop: " + $m + " -->"), user:{login:$a},
      created_at:"2026-08-11T00:00:00Z"}'
}

finish() {
  printf '\n  %d passed, %d failed\n' "$pass" "$fail"
  (( fail == 0 ))
}
