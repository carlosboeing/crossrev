#!/usr/bin/env bash
#
# capture-parity.sh — regenerate tests/fixtures/parity/.
#
# The native migration needs a fixed corpus a Go implementation can assert
# against, captured from the Bash implementation while it is the only one.
# Each fixture records the platform, the tr implementation and the locale it
# was captured under; every vector's expected value comes from calling the
# real library functions, never from a hand-written answer.
#
# Recapture deliberately: run this script when and only when the underlying
# behaviour changes on purpose, and read the diff before committing it. A
# recapture that changes an id or a prompt silently is exactly what
# tests/test-parity.sh exists to make loud.

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$HERE/.."
FIXDIR="$REPO_ROOT/tests/fixtures/parity"
mkdir -p "$FIXDIR"

export GIT_AUTHOR_NAME="crossrev"
export GIT_AUTHOR_EMAIL="test@example.com"
export GIT_COMMITTER_NAME="crossrev"
export GIT_COMMITTER_EMAIL="test@example.com"

# shellcheck source=../lib/ui.sh
source "$REPO_ROOT/lib/ui.sh"
# shellcheck source=../lib/diff.sh
source "$REPO_ROOT/lib/diff.sh"
# shellcheck source=../lib/sandbox.sh
source "$REPO_ROOT/lib/sandbox.sh"
# shellcheck source=../lib/prompt.sh
source "$REPO_ROOT/lib/prompt.sh"
# shellcheck source=../lib/state.sh
source "$REPO_ROOT/lib/state.sh"
# shellcheck source=../lib/harnesses.sh
source "$REPO_ROOT/lib/harnesses.sh"
# shellcheck source=../lib/config.sh
source "$REPO_ROOT/lib/config.sh"
# shellcheck source=../lib/legs.sh
source "$REPO_ROOT/lib/legs.sh"

platform="$(uname -s -r -m)"
tr_path="$(command -v tr)"
if tr --version </dev/null 2>/dev/null | grep -q GNU; then tr_flavor="GNU coreutils tr"; else tr_flavor="BSD tr"; fi
locale="${LC_ALL:-${LC_CTYPE:-${LANG:-unset}}}"

captured_json() {
  jq -n --arg p "$platform" --arg t "$tr_path ($tr_flavor)" --arg l "$locale" '{
    platform: $p,
    tr_implementation: $t,
    locale: $l,
    note: "state_finding_id and state_anchor pin LC_ALL=C internally, so ids and anchors are byte-oriented whatever locale this file was captured under."
  }'
}

# --- state_finding_id over (path, title, anchor) triples --------------------

fid_case() { # name path title anchor
  local id
  id="$(state_finding_id "$2" "$3" "$4")"
  jq -cn --arg n "$1" --arg p "$2" --arg t "$3" --arg a "$4" --arg i "$id" \
    '{name:$n, path:$p, title:$t, anchor:$a, id:$i}'
}

fid_cases() {
  fid_case "ascii-basic" "lib/auth.ts" "Token refresh races with logout" "abcd1234"
  fid_case "ascii-mixed-case-and-padding" "lib/auth.ts" "  token   REFRESH races with logout " "abcd1234"
  fid_case "ascii-empty-anchor" "lib/auth.ts" "Token refresh races with logout" ""
  fid_case "tab-in-title" "lib/x.ts" $'Token\trefresh\traces' "abcd1234"
  fid_case "newline-in-title" "lib/x.ts" $'Token\nrefresh\nraces' "abcd1234"
  fid_case "vertical-tab-in-title" "lib/x.ts" $'Token\vrefresh\vraces' "abcd1234"
  fid_case "form-feed-in-title" "lib/x.ts" $'Token\fraces\fagain' "abcd1234"
  fid_case "carriage-return-in-title" "lib/x.ts" $'Token\rraces\rhere' "abcd1234"
  fid_case "non-ascii-upper-case-title" "greek/x.ts" "ΣIGMA refresh races" ""
  fid_case "non-breaking-space-in-title" "w/x.ts" "$(printf 'token\xc2\xa0refresh')" ""
  fid_case "leading-tab-title" "lib/x.ts" $'\tLeading tab title' "abcd1234"
}

jq -n --argjson captured "$(captured_json)" \
  --argjson cases "$(fid_cases | jq -s .)" \
  '{captured:$captured, function:"state_finding_id", cases:$cases}' \
  >"$FIXDIR/state_finding_id.json"

# --- state_anchor over (file content, line) pairs ----------------------------

anchor_case() { # name line exists content
  local anchor=""
  if [[ "$3" == "true" ]]; then
    local f; f="$(mktemp)"
    printf '%s' "$4" >"$f"
    anchor="$(state_anchor "$f" "$2")"
    rm -f "$f"
  fi
  jq -cn --arg n "$1" --argjson l "$2" --arg e "$3" --arg c "${4-}" --arg a "$anchor" \
    '{name:$n, line:$l, exists:$e, content:(if $e == "true" then $c else null end), anchor:$a}'
}

anchor_cases() {
  anchor_case "six-line-file-line-1" 1 true \
    $'alpha beta\ngamma delta\nepsilon zeta\neta theta\niota kappa\nlambda mu\n'
  anchor_case "six-line-file-line-2" 2 true \
    $'alpha beta\ngamma delta\nepsilon zeta\neta theta\niota kappa\nlambda mu\n'
  anchor_case "missing-file" 3 false ""
  anchor_case "short-file-past-window" 9 true $'only\n'
}

jq -n --argjson captured "$(captured_json)" \
  --argjson cases "$(anchor_cases | jq -s .)" \
  '{captured:$captured, function:"state_anchor", cases:$cases}' \
  >"$FIXDIR/state_anchor.json"

# --- the marker codec --------------------------------------------------------

codec_case() { # name body
  local decoded
  decoded="$(state_marker_of "$2")"
  jq -cn --arg n "$1" --arg b "$2" --arg d "$decoded" \
    '{name:$n, body:$b, decoded:$d}'
}

codec_cases() {
  local one two
  one="$(state_marker_encode '{"v":1,"leg":"review","pass":1,"state":"complete","head_sha":"abc1234"}')"
  codec_case "one-marker" "Summary.${one}"
  two="$(state_marker_encode '{"v":1,"leg":"resolve","pass":1,"state":"complete"}')"
  codec_case "two-markers-one-body" "A.${one}${two}"
  codec_case "marker-split-across-lines" \
    "$(printf 'Summary.\n<!-- crossrev: {"v":1,"leg":"rev\niew","pass":1} -->')"

  # Each of the three vocabulary migrations, in isolation.
  codec_case "migration-top-level-dispositions" \
    "Pass.$(state_marker_encode '{"v":1,"leg":"resolve","pass":1,"state":"complete",
      "dispositions":[{"finding_id":"aaaa000000000001","resolution":"fixed"}]}')"
  codec_case "migration-per-finding-disposition" \
    "Pass.$(state_marker_encode '{"v":1,"leg":"resolve","pass":1,"state":"complete",
      "resolutions":[{"finding_id":"aaaa000000000001","disposition":"rebutted"}]}')"
  codec_case "migration-finding-disposition" \
    "Findings.$(state_marker_encode '{"v":1,"leg":"review","pass":1,
      "findings":[{"id":"aaaa000000000001","disposition":"rebutted"}]}')"
}

jq -n --argjson captured "$(captured_json)" \
  --argjson cases "$(codec_cases | jq -s .)" \
  '{captured:$captured, function:"state_marker_of", cases:$cases}' \
  >"$FIXDIR/marker_codec.json"

# --- state_marker_encode over ordered marker objects ------------------------

encode_case() { # name input_json
  local encoded
  encoded="$(state_marker_encode "$2")"
  jq -cn --arg n "$1" --argjson inp "$2" --arg enc "$encoded" \
    '{name:$n, input:$inp, encoded:$enc}'
}

encode_cases() {
  encode_case "review-complete" \
    '{"v":1,"leg":"review","pass":2,"state":"complete","verdict":"issues-remain","head_sha":"9f3c1ab","findings":[{"id":"aaaa000000000001","severity":"high","category":"security","pre_existing":false,"path":"app.ts","line":2,"title":"Fetch timeout missing"}]}'
  encode_case "review-minimal" \
    '{"v":1,"leg":"review","pass":1,"state":"complete"}'
  encode_case "resolve-complete" \
    '{"v":1,"leg":"resolve","pass":2,"state":"complete","commit_sha":"d81a3f2abc","resolutions":[{"finding_id":"aaaa000000000001","resolution":"fixed"},{"finding_id":"bbbb000000000002","resolution":"deferred","crossrev_tracked":"#45"}]}'
  encode_case "resolve-minimal" \
    '{"v":1,"leg":"resolve","pass":1,"state":"complete"}'
  encode_case "declined" \
    '{"v":1,"leg":"review","pass":3,"state":"declined","reason":"reached max_passes_per_cycle (3)"}'
  encode_case "missing-optional-fields" \
    '{"v":1,"leg":"resolve","pass":1,"state":"started","head_sha":"abc1234"}'
  encode_case "present-empty-crossrev-tracked" \
    '{"v":1,"leg":"resolve","pass":1,"state":"complete","resolutions":[{"finding_id":"aaaa000000000001","resolution":"deferred","crossrev_tracked":""}]}'
}

jq -n --argjson captured "$(captured_json)" \
  --argjson cases "$(encode_cases | jq -s .)" \
  '{captured:$captured, function:"state_marker_encode", cases:$cases}' \
  >"$FIXDIR/marker_encode.json"

# --- diff views over a single rich corpus -----------------------------------

diff_workdir="$(mktemp -d)"
diff_file="$diff_workdir/corpus.diff"

diff_corpus='diff --git a/tools/crossrev/CHANGELOG.md b/tools/crossrev/CHANGELOG.md
index e773ac4..0b34128 100644
--- a/tools/crossrev/CHANGELOG.md
+++ b/tools/crossrev/CHANGELOG.md
@@ -6,6 +6,8 @@ All notable changes to crossrev.

 ### Added

+- **A two-direction template/default drift test.** Eleven behavior leaves must agree.
+
 - `crossrev review --pr N` — one review pass. Claims before working.
 - `crossrev resolve --pr N` — verifies every finding whatever its severity.
 - `crossrev cycle --pr N` — the whole loop in one process, up to `max_passes`.
@@ -33,3 +35,4 @@ All notable changes to crossrev.
 - `crossrev status --pr N` — position and interruption.
-- `runs_per_day` bounded a number that was not runs.
+- `max_prs_per_day` counts distinct pull requests repository-wide.
+- `max_passes_per_cycle` replaces the scattered caps.
 - `crossrev watchdog` — finds pull requests stuck waiting on a leg.
diff --git a/docs/my notes.md b/docs/my notes.md
index 1111111..2222222 100644
--- a/docs/my notes.md	
+++ b/docs/my notes.md	
@@ -6,2 +6,3 @@ heading
  existing line
+added line
  context line
diff --git "a/docs/caf\303\251.md" "b/docs/caf\303\251.md"
index 3333333..4444444 100644
--- "a/docs/caf\303\251.md"
+++ "b/docs/caf\303\251.md"
@@ -20,1 +20,2 @@ heading
  kept line
+new line
diff --git a/src/old_name.ts b/src/new_name.ts
similarity index 85%
rename from src/old_name.ts
rename to src/new_name.ts
--- a/src/old_name.ts
+++ b/src/new_name.ts
@@ -1,2 +1,2 @@
-old version
+new version
  shared context
diff --git a/src/created.ts b/src/created.ts
new file mode 100644
--- /dev/null
+++ b/src/created.ts
@@ -0,0 +1,2 @@
+export const first = 1;
+export const second = 2;
diff --git a/src/deleted.ts b/src/deleted.ts
deleted file mode 100644
--- a/src/deleted.ts
+++ /dev/null
@@ -1,2 +0,0 @@
-export const old1 = 1;
-export const old2 = 2;
diff --git a/src/edges.ts b/src/edges.ts
index 5555555..6666666 100644
--- a/src/edges.ts
+++ b/src/edges.ts
@@ -1 +1 @@
-const only = 1;
+const only = 2;
@@ -10,3 +10,5 @@ a heading
 kept context
--- a removed line whose own text starts with two dashes
+++ an added line whose own text starts with two pluses
 last kept line
\ No newline at end of file
diff --git a/BACKLOG.md b/BACKLOG.md
index 1111111..2222222 100644
--- a/BACKLOG.md
+++ b/BACKLOG.md
@@ -1,1 +1,2 @@
  kept
+added
diff --git a/BACKLOG.md.old b/BACKLOG.md.old
index 1111111..2222222 100644
--- a/BACKLOG.md.old
+++ b/BACKLOG.md.old
@@ -1,1 +1,2 @@
  kept
+added
diff --git a/BACKLOGxmd b/BACKLOGxmd
index 1111111..2222222 100644
--- a/BACKLOGxmd
+++ b/BACKLOGxmd
@@ -1,1 +1,2 @@
  kept
+added
diff --git a/docs/backlog/item.md b/docs/backlog/item.md
index 1111111..2222222 100644
--- a/docs/backlog/item.md
+++ b/docs/backlog/item.md
@@ -1,1 +1,2 @@
  kept
+added
diff --git a/docs/backlogged.md b/docs/backlogged.md
index 1111111..2222222 100644
--- a/docs/backlogged.md
+++ b/docs/backlogged.md
@@ -1,1 +1,2 @@
  kept
+added
diff --git a/docs/backlog(new).md b/docs/backlog(new).md
index 1111111..2222222 100644
--- a/docs/backlog(new).md
+++ b/docs/backlog(new).md
@@ -1,1 +1,2 @@
  kept
+added'
printf '%s' "$diff_corpus" >"$diff_file"

anchor_diff_case() { # name path side line bound
  local res
  res="$(diff_anchor "$diff_file" "$2" "$3" "$4" "$5")"
  jq -cn --arg n "$1" --arg p "$2" --arg s "$3" --argjson l "$4" --argjson b "$5" --arg r "$res" \
    '{name:$n, path:$p, side:$s, line:$l, bound:$b, result:$r}'
}

anchor_diff_cases() {
  local CH="tools/crossrev/CHANGELOG.md"
  anchor_diff_case "changelog-right-inside-hunk" "$CH" RIGHT 13 3
  anchor_diff_case "changelog-right-added-line" "$CH" RIGHT 9 3
  anchor_diff_case "changelog-left-inside-hunk" "$CH" LEFT 11 3
  anchor_diff_case "changelog-left-deleted-line" "$CH" LEFT 34 3
  anchor_diff_case "changelog-right-snap-dist-1" "$CH" RIGHT 14 3
  anchor_diff_case "changelog-right-snap-forward" "$CH" RIGHT 5 3
  anchor_diff_case "changelog-right-snap-dist-2" "$CH" RIGHT 15 3
  anchor_diff_case "changelog-right-snap-dist-3" "$CH" RIGHT 16 3
  anchor_diff_case "changelog-right-past-bound-3" "$CH" RIGHT 17 3
  anchor_diff_case "changelog-right-snap-bound-4" "$CH" RIGHT 17 4
  anchor_diff_case "changelog-right-bound-0-exact" "$CH" RIGHT 13 0
  anchor_diff_case "changelog-right-bound-0-miss" "$CH" RIGHT 14 0
  anchor_diff_case "changelog-right-tie-earlier" "$CH" RIGHT 24 11
  anchor_diff_case "changelog-right-far-miss" "$CH" RIGHT 24 3
  anchor_diff_case "changelog-left-added-misses" "$CH" LEFT 37 3
  anchor_diff_case "changelog-right-deleted-misses" "$CH" RIGHT 33 3
  anchor_diff_case "spaced-path-inside-hunk" "docs/my notes.md" RIGHT 7 3
  anchor_diff_case "spaced-path-snap" "docs/my notes.md" RIGHT 9 3
  anchor_diff_case "quoted-path-inside-hunk" "docs/café.md" RIGHT 21 3
  anchor_diff_case "quoted-path-snap" "docs/café.md" RIGHT 22 3
  anchor_diff_case "quoted-path-escaped-literal-misses" "docs/caf\\303\\251.md" RIGHT 21 3
  anchor_diff_case "rename-old-path" "src/old_name.ts" LEFT 1 3
  anchor_diff_case "rename-new-path" "src/new_name.ts" RIGHT 1 3
  anchor_diff_case "created-file" "src/created.ts" RIGHT 1 3
  anchor_diff_case "deleted-file" "src/deleted.ts" LEFT 1 3
  anchor_diff_case "absent-file" "absent.ts" RIGHT 1 3
  # The three shapes lib/diff.sh calls out in its own comments. A hunk header
  # with counts omitted (lib/diff.sh:152), an in-hunk line whose own text opens
  # with -- or ++ (lib/diff.sh:159), and the no-newline annotation (lib/diff.sh:164).
  local ED="src/edges.ts"
  anchor_diff_case "edges-omitted-counts-right" "$ED" RIGHT 1 3
  anchor_diff_case "edges-omitted-counts-left" "$ED" LEFT 1 3
  anchor_diff_case "edges-dashdash-body-line-left" "$ED" LEFT 11 3
  anchor_diff_case "edges-plusplus-body-line-right" "$ED" RIGHT 11 3
  anchor_diff_case "edges-context-before-no-newline" "$ED" RIGHT 12 3
}

exclude_diff_case() { # name exclusion_args...
  local name="$1"; shift
  local out
  out="$(diff_exclude "$diff_file" "$@")"
  local ex_json
  if [[ $# -eq 0 ]]; then
    ex_json="[]"
  else
    ex_json="$(jq -cn '$ARGS.positional' --args "$@")"
  fi
  jq -cn --arg n "$name" --argjson ex "$ex_json" --arg out "$out" \
    '{name:$n, exclusions:$ex, output:$out}'
}

exclude_diff_cases() {
  exclude_diff_case "exclude-backlog-file" "BACKLOG.md"
  exclude_diff_case "exclude-backlog-prefix-dir" "docs/backlog"
  exclude_diff_case "exclude-backlog-trailing-slash" "docs/backlog/"
  exclude_diff_case "exclude-multiple" "BACKLOG.md" "docs/backlog"
  exclude_diff_case "exclude-spaced-path" "docs/my notes.md"
  exclude_diff_case "exclude-regex-metachar" "docs/backlog(new).md"
  exclude_diff_case "exclude-rename-source" "src/old_name.ts"
  exclude_diff_case "exclude-rename-target" "src/new_name.ts"
  exclude_diff_case "exclude-none"
}

# A second corpus, of shapes git does not produce and the awk still has to
# answer for. The rich corpus above is well-formed on purpose, so nothing in it
# says what a truncated hunk or an unreadable header does. Those answers were
# hand-written in the port instead of captured, which is the one thing the
# oracle exists to stop.
#
# It is a separate corpus rather than more sections in the first one, because
# appending to that one would move every line number the 31 frozen anchor
# queries name.
malformed_file="$diff_workdir/malformed.diff"
malformed_corpus='diff --git a/src/truncated.ts b/src/truncated.ts
--- a/src/truncated.ts
+++ b/src/truncated.ts
@@ -1,9 +1,9 @@
 one
-two
+deux
diff --git a/src/unreadable.ts b/src/unreadable.ts
--- a/src/unreadable.ts
+++ b/src/unreadable.ts
@@ garbage @@
 ctx after an unreadable header
+added after an unreadable header
diff --git a/src/nospace.ts b/src/nospace.ts
--- a/src/nospace.ts
+++ b/src/nospace.ts
@@-1,2 +1,2 @@
 ctx after a header with no space
diff --git a/src/bare.ts b/src/bare.ts
--- a/src/bare.ts
+++ b/src/bare.ts
@@
 ctx after a bare at-at
diff --git a/src/nosides.ts b/src/nosides.ts
index 3333333..4444444 100644
Binary files a/src/nosides.ts and b/src/nosides.ts differ
\ weird line outside any hunk
diff --git a/src/inhunk.ts b/src/inhunk.ts
--- a/src/inhunk.ts
+++ b/src/inhunk.ts
@@ -1,3 +1,3 @@
 before
--- a side line appearing inside a hunk
+++ its added counterpart
 after'
printf '%s' "$malformed_corpus" >"$malformed_file"

malformed_anchor_case() { # name path side line bound
  local res
  res="$(diff_anchor "$malformed_file" "$2" "$3" "$4" "$5")"
  jq -cn --arg n "$1" --arg p "$2" --arg s "$3" --argjson l "$4" --argjson b "$5" --arg r "$res" \
    '{name:$n, path:$p, side:$s, line:$l, bound:$b, result:$r}'
}

malformed_anchor_cases() {
  # A hunk whose header claims nine lines and holds three still numbers the
  # three it has, from the start the header names.
  malformed_anchor_case "truncated-first-line"      "src/truncated.ts" RIGHT 1 3
  malformed_anchor_case "truncated-past-what-it-has" "src/truncated.ts" RIGHT 9 3
  # An unreadable header numbers from zero, because awk reads no number at all.
  malformed_anchor_case "unreadable-header-right"   "src/unreadable.ts" RIGHT 0 3
  malformed_anchor_case "unreadable-header-line-one" "src/unreadable.ts" RIGHT 1 3
  # No space after the at-at reads the fields shifted.
  malformed_anchor_case "nospace-header-right"      "src/nospace.ts" RIGHT 1 3
  malformed_anchor_case "bare-at-at-right"          "src/bare.ts" RIGHT 0 3
  # A section with no side lines names no path, so nothing anchors to it.
  malformed_anchor_case "no-side-lines"             "src/nosides.ts" RIGHT 1 3
  # in_hunk is cleared only by a diff --git line, so a --- inside a hunk is a
  # deletion rather than a header.
  malformed_anchor_case "side-line-inside-hunk-left" "src/inhunk.ts" LEFT 2 3
  malformed_anchor_case "side-line-inside-hunk-right" "src/inhunk.ts" RIGHT 2 3
}

malformed_numbered="$(diff_number "$malformed_file")"

numbered="$(diff_number "$diff_file")"

jq -n --argjson captured "$(captured_json)" \
  --arg corpus "$diff_corpus" \
  --arg diff_number "$numbered" \
  --argjson anchors "$(anchor_diff_cases | jq -s .)" \
  --argjson excludes "$(exclude_diff_cases | jq -s .)" \
  --arg malformed_corpus "$malformed_corpus" \
  --arg malformed_diff_number "$malformed_numbered" \
  --argjson malformed_anchors "$(malformed_anchor_cases | jq -s .)" \
  '{captured:$captured, function:"diff_views", corpus:$corpus, diff_number:$diff_number, anchors:$anchors, excludes:$excludes, malformed:{corpus:$malformed_corpus, diff_number:$malformed_diff_number, anchors:$malformed_anchors}}' \
  >"$FIXDIR/diff_views.json"

rm -rf "$diff_workdir"

# --- configuration merge and refusals ---------------------------------------

config_capture_dir="$(mktemp -d)"

(
  cd "$config_capture_dir"
  mkdir -p r_def && cd r_def && git init -q . && git commit -q --allow-empty -m init
  XDG_CONFIG_HOME="$config_capture_dir/xdg_def"; export XDG_CONFIG_HOME
  cfg_load
  jq -n --argjson merged "$CFG_MERGED" \
    '{name:"defaults", repo_yaml:null, operator_yaml:null, base_sha:null, merged:$merged}'
) >"$config_capture_dir/case_defaults.json"

(
  cd "$config_capture_dir"
  mkdir -p r_over && cd r_over && git init -q . && git commit -q --allow-empty -m init
  mkdir -p .github
  repo_yaml='version: 1
mode: automated
runner: self-hosted
policy:
  min_fix_severity: high
  max_passes_per_cycle: 5
  max_prs_per_day: 50
git:
  hooks: run
logs:
  retention_days: 30
  keep_transcripts: true
reviewer:
  harness: claude
  model: claude-3-7-sonnet
resolver:
  harness: codex
  model: o3
endpoints:
  repo_ep:
    base_url: https://repo.example.com/v1
    token_env: REPO_KEY'
  printf '%s\n' "$repo_yaml" > .github/crossrev.yml
  XDG_CONFIG_HOME="$config_capture_dir/xdg_over"; export XDG_CONFIG_HOME
  cfg_load
  jq -n --arg r "$repo_yaml" --argjson merged "$CFG_MERGED" \
    '{name:"repo-over-defaults", repo_yaml:$r, operator_yaml:null, base_sha:null, merged:$merged}'
) >"$config_capture_dir/case_repo_over.json"

(
  cd "$config_capture_dir"
  mkdir -p r_op && cd r_op && git init -q . && git commit -q --allow-empty -m init
  mkdir -p .github
  repo_yaml='version: 1
endpoints:
  kimi:
    base_url: https://public.example/
    token_env: KIMI_API_KEY
  repo_only:
    base_url: https://repo.example/
    token_env: REPO_KEY'
  printf '%s\n' "$repo_yaml" > .github/crossrev.yml
  xdg="$config_capture_dir/xdg_op"
  mkdir -p "$xdg/crossrev"
  op_yaml='version: 1
endpoints:
  kimi:
    base_url: http://mine.local/
    token_env: KIMI_API_KEY
  operator_only:
    base_url: http://operator.local/
    token_env: OP_KEY'
  printf '%s\n' "$op_yaml" > "$xdg/crossrev/config.yml"
  XDG_CONFIG_HOME="$xdg"; export XDG_CONFIG_HOME
  cfg_load
  jq -n --arg r "$repo_yaml" --arg o "$op_yaml" --argjson merged "$CFG_MERGED" \
    '{name:"operator-endpoint-override", repo_yaml:$r, operator_yaml:$o, base_sha:null, merged:$merged}'
) >"$config_capture_dir/case_op_override.json"

(
  cd "$config_capture_dir"
  mkdir -p r_fall && cd r_fall && git init -q .
  repo_yaml='version: 1
policy:
  max_passes_per_cycle: 7'
  printf '%s\n' "$repo_yaml" > .crossrev.yml
  git add .crossrev.yml
  GIT_AUTHOR_NAME="crossrev" GIT_AUTHOR_EMAIL="test@example.com" \
  GIT_COMMITTER_NAME="crossrev" GIT_COMMITTER_EMAIL="test@example.com" \
  GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z" \
  git commit -q -m "base policy"
  base_sha="$(git rev-parse HEAD)"
  rm -f .crossrev.yml
  git commit -q --allow-empty -m "head commit"
  XDG_CONFIG_HOME="$config_capture_dir/xdg_fall"; export XDG_CONFIG_HOME
  cfg_load "$base_sha"
  jq -n --arg r "$repo_yaml" --arg b "$base_sha" --argjson merged "$CFG_MERGED" \
    '{name:"base-fallback-crossrev-yml", repo_yaml:$r, operator_yaml:null, base_sha:$b, merged:$merged}'
) >"$config_capture_dir/case_base_fallback.json"

# No config at the base revision at all. This is what most repositories look
# like, and it must stay silent: defaults apply and nothing is reported. Frozen
# because cfg_load can no longer tell absent from broken by the text alone, so
# the two answers are now produced by different code.
(
  cd "$config_capture_dir"
  mkdir -p r_absent && cd r_absent && git init -q .
  GIT_AUTHOR_NAME="crossrev" GIT_AUTHOR_EMAIL="test@example.com" \
  GIT_COMMITTER_NAME="crossrev" GIT_COMMITTER_EMAIL="test@example.com" \
  GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z" \
  git commit -q --allow-empty -m "base with no config"
  base_sha="$(git rev-parse HEAD)"
  XDG_CONFIG_HOME="$config_capture_dir/xdg_absent"; export XDG_CONFIG_HOME
  cfg_load "$base_sha"
  jq -n --arg b "$base_sha" --argjson merged "$CFG_MERGED" \
    '{name:"base-absent", repo_yaml:null, operator_yaml:null, base_sha:$b, merged:$merged}'
) >"$config_capture_dir/case_base_absent.json"

# A config that exists at the base revision and holds nothing. git show returns
# exit 0 and no bytes, which reads as no policy rather than as a parse failure.
# It is the case that made absent and broken indistinguishable before the fix.
(
  cd "$config_capture_dir"
  mkdir -p r_empty && cd r_empty && git init -q .
  mkdir -p .github
  : > .github/crossrev.yml
  git add .github/crossrev.yml
  GIT_AUTHOR_NAME="crossrev" GIT_AUTHOR_EMAIL="test@example.com" \
  GIT_COMMITTER_NAME="crossrev" GIT_COMMITTER_EMAIL="test@example.com" \
  GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z" \
  git commit -q -m "base with an empty config"
  base_sha="$(git rev-parse HEAD)"
  XDG_CONFIG_HOME="$config_capture_dir/xdg_empty"; export XDG_CONFIG_HOME
  cfg_load "$base_sha"
  jq -n --arg b "$base_sha" --argjson merged "$CFG_MERGED" \
    '{name:"base-empty", repo_yaml:"", operator_yaml:null, base_sha:$b, merged:$merged}'
) >"$config_capture_dir/case_base_empty.json"

config_refusal_case() { # name family yaml
  local name="$1" family="$2" yaml="$3"
  local d; d="$(mktemp -d "$config_capture_dir/refusal_XXXXXX")"
  (
    cd "$d" && git init -q . && git commit -q --allow-empty -m init
    mkdir -p .github
    printf '%s\n' "$yaml" > .github/crossrev.yml
    XDG_CONFIG_HOME="$d/xdg"; export XDG_CONFIG_HOME
    local err
    err="$({ cfg_load >/dev/null; } 2>&1 || true)"
    jq -cn --arg n "$name" --arg f "$family" --arg y "$yaml" --arg e "$err" \
      '{name:$n, family:$f, driver:"load", call:[], config:$y, error:$e}'
  )
}

# Some values are only refused at the point a caller asks for them, so cfg_load
# alone never reaches them. The vector has to ask.
config_refusal_call_case() { # name family yaml call...
  local name="$1" family="$2" yaml="$3"; shift 3
  local d; d="$(mktemp -d "$config_capture_dir/refusal_XXXXXX")"
  (
    cd "$d" && git init -q . && git commit -q --allow-empty -m init
    mkdir -p .github
    printf '%s\n' "$yaml" > .github/crossrev.yml
    XDG_CONFIG_HOME="$d/xdg"; export XDG_CONFIG_HOME
    local err
    err="$({ cfg_load >/dev/null && "$@" >/dev/null; } 2>&1 || true)"
    jq -cn --arg n "$name" --arg f "$family" --arg y "$yaml" --arg e "$err" \
      --args '{name:$n, family:$f, driver:"call", call:$ARGS.positional, config:$y, error:$e}' "$@"
  )
}

# The version refusal composes its "where" from the base revision rather than the
# working tree. That arm builds a different string and no vector covered it.
config_refusal_base_case() { # name family yaml
  local name="$1" family="$2" yaml="$3"
  local d; d="$(mktemp -d "$config_capture_dir/refusal_XXXXXX")"
  (
    cd "$d" && git init -q .
    mkdir -p .github
    printf '%s\n' "$yaml" > .github/crossrev.yml
    git add .github/crossrev.yml
    GIT_AUTHOR_NAME="crossrev" GIT_AUTHOR_EMAIL="test@example.com" \
    GIT_COMMITTER_NAME="crossrev" GIT_COMMITTER_EMAIL="test@example.com" \
    GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z" \
    git commit -q -m "base config"
    local base_sha; base_sha="$(git rev-parse HEAD)"
    rm -f .github/crossrev.yml
    GIT_AUTHOR_NAME="crossrev" GIT_AUTHOR_EMAIL="test@example.com" \
    GIT_COMMITTER_NAME="crossrev" GIT_COMMITTER_EMAIL="test@example.com" \
    GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z" \
    git commit -q --allow-empty -m "head commit"
    XDG_CONFIG_HOME="$d/xdg"; export XDG_CONFIG_HOME
    local err
    err="$({ cfg_load "$base_sha" >/dev/null; } 2>&1 || true)"
    # The refusal names the revision it read, so the message holds a commit SHA.
    # That SHA is a property of how this repository was built, not of the
    # behaviour being frozen, and a Go port is handed a different revision.
    # Record the placeholder; tests/test-parity.sh normalises the same way.
    err="${err//$base_sha/<base_sha>}"
    jq -cn --arg n "$name" --arg f "$family" --arg y "$yaml" --arg e "$err" \
      '{name:$n, family:$f, driver:"load_at_base", call:[], config:$y, error:$e}'
  )
}

config_refusal_cases() {
  config_refusal_case "version-mismatch" "version" $'version: 99\n'
  config_refusal_case "severity-invalid" "severity" $'version: 1\npolicy:\n  min_fix_severity: medum\n'
  config_refusal_case "pass-count-invalid" "pass_count" $'version: 1\npolicy:\n  max_passes_per_cycle: 0\n'
  config_refusal_case "git-hooks-invalid" "git_hooks" $'version: 1\ngit:\n  hooks: skipp\n'
  config_refusal_case "log-retention-invalid" "log_retention" $'version: 1\nlogs:\n  retention_days: 0\n'
  config_refusal_case "keep-transcripts-invalid" "log_transcripts" $'version: 1\nlogs:\n  keep_transcripts: yes please\n'
  # One refusal, no jq noise, no unrelated key. That is only true since the
  # command-substitution fix; before it this capture recorded three errors, the
  # last of them wrong, and jq's own version-specific text with them.
  config_refusal_case "malformed-yaml" "parse" $'version: 1\npolicy:\n  - this is not\n  a mapping: [unclosed\n'
  config_refusal_base_case "version-mismatch-at-base" "version" $'version: 99\n'
  # The base revision is the path automated mode takes. A file that will not
  # parse there used to fall back to {} and revert every stated policy value to
  # a default, with exit 0 and nothing printed. It refuses now, with one message
  # naming both the file and the revision, and a hint that reads the revision.
  config_refusal_base_case "malformed-yaml-at-base" "parse" \
    $'version: 1\npolicy:\n  - this is not\n  a mapping: [unclosed\n'
  config_refusal_call_case "endpoint-without-base-url" "endpoint" \
    $'version: 1\nendpoints:\n  local:\n    token_env: LOCAL_TOKEN\n' cfg_endpoint local
  config_refusal_call_case "endpoint-without-token-env" "endpoint" \
    $'version: 1\nendpoints:\n  local:\n    base_url: http://127.0.0.1:11434\n' cfg_endpoint local
  # cfg_assert_backlog refuses both of these from cfg_load, before anything
  # resolves them. They were `call` vectors while cfg_resolve_backlog owned the
  # refusal, and a command substitution swallowed its exit.
  config_refusal_case "backlog-layout-invalid" "backlog" \
    $'version: 1\nbacklog:\n  destination: repository\n  repository:\n    layout: flat\n'
  config_refusal_case "backlog-destination-invalid" "backlog" \
    $'version: 1\nbacklog:\n  destination: elsewhere\n'
}

cfg_cases="[$(cat "$config_capture_dir/case_defaults.json"), $(cat "$config_capture_dir/case_repo_over.json"), $(cat "$config_capture_dir/case_op_override.json"), $(cat "$config_capture_dir/case_base_fallback.json"), $(cat "$config_capture_dir/case_base_absent.json"), $(cat "$config_capture_dir/case_base_empty.json")]"

jq -n --argjson captured "$(captured_json)" \
  --argjson cases "$cfg_cases" \
  --argjson refusals "$(config_refusal_cases | jq -s .)" \
  '{captured:$captured, function:"cfg_load", cases:$cases, refusals:$refusals}' \
  >"$FIXDIR/config_merge.json"

rm -rf "$config_capture_dir"

# --- labels colour and description ------------------------------------------

label_case() { # label
  local l="$1"
  local c d
  c="$(legs_label_colour "$l")"
  d="$(legs_label_description "$l")"
  jq -cn --arg l "$l" --arg c "$c" --arg d "$d" \
    '{label:$l, colour:$c, description:$d}'
}

label_cases() {
  label_case "crossrev/awaiting-review"
  label_case "crossrev/awaiting-resolution"
  label_case "crossrev/converged"
  label_case "crossrev/halted"
  label_case "crossrev/stop"
  label_case "crossrev/pass-1"
  label_case "crossrev/pass-7"
  label_case "crossrev/watchdog-retried"
  label_case "crossrev/unknown-fallback"
}

jq -n --argjson captured "$(captured_json)" \
  --argjson labels "$(label_cases | jq -s .)" \
  '{captured:$captured, function:"legs_label", labels:$labels}' \
  >"$FIXDIR/labels.json"

# --- both assembled prompts, byte for byte -----------------------------------

workdir="$(mktemp -d)"

diff_text='diff --git a/app.ts b/app.ts
--- a/app.ts
+++ b/app.ts
@@ -1,2 +1,3 @@
 export const ok = 1
+export function refresh() { fetch("/t") }
 export const stale = 2'
printf '%s' "$diff_text" >"$workdir/diff.patch"

review_meta='{"repo":"acme/widget","pr":42,"pass":2,"head_sha":"9f3c1ab4d2e5",
  "title":"Add refresh helper","min_fix_severity":"medium",
  "body":"Adds a refresh helper.\n\nFixes the timeout."}'
prior='[
  {"id":"aaaa000000000001","path":"app.ts","line":2,"severity":"high",
   "category":"security","pre_existing":false,
   "title":"Fetch without a timeout hangs the request"},
  {"id":"bbbb000000000002","path":"app.ts","line":3,"severity":"low",
   "pre_existing":true,"title":"stale is unused"}
]'
threads='[
  {"id":"t1","isResolved":false,"isOutdated":false,"path":"app.ts","line":2,
   "comments":[
    {"databaseId":5001,"author":{"login":"alice"},
     "body":"No timeout here. <!-- crossrev:f {\"id\":\"aaaa000000000001\",\"pass\":1,\"leg\":\"review\"} -->"},
    {"databaseId":5002,"author":{"login":"bob"},"body":"Agreed."}]},
  {"id":"t2","isResolved":true,"isOutdated":false,"path":"app.ts","line":3,
   "comments":[{"databaseId":5003,"author":{"login":"alice"},"body":"Pre-existing."}]}
]'

# Copied, not printed through a substitution: a command substitution strips the
# final newline, and the vector pins the skill's exact bytes.
cp "$REPO_ROOT/skills/pr-review/SKILL.md" "$workdir/review-skill.md"
prompt_review "$workdir/review-prompt.txt" "$workdir/review-skill.md" \
  "$workdir/diff.patch" "$review_meta" "$prior" "$threads"

resolve_meta='{"repo":"acme/widget","pr":42,"pass":2,"head_sha":"9f3c1ab4d2e5",
  "title":"Add refresh helper","min_fix_severity":"medium",
  "backlog":"backlog/tasks","base_sha":"","crossrev_email":""}'
findings='[
  {"number":1,"id":"aaaa000000000001","severity":"high","category":"security",
   "pre_existing":false,"path":"app.ts","line":2,
   "title":"Fetch without a timeout hangs the request",
   "why":"A hung request blocks the worker.","fix":"Pass a timeout.","may_fix":true},
  {"number":2,"id":"cccc000000000003","severity":"low","category":"style",
   "pre_existing":true,"path":"app.ts","line":3,"title":"stale is unused",
   "why":"Dead code invites drift.","may_fix":false}
]'
candidates='{
  "aaaa000000000001":[{"number":17,"state":"OPEN","title":"Requests can hang forever"}]
}'

cp "$REPO_ROOT/skills/pr-resolve/SKILL.md" "$workdir/resolve-skill.md"
prompt_resolve "$workdir/resolve-prompt.txt" "$workdir/resolve-skill.md" \
  "$workdir/diff.patch" "$resolve_meta" "$findings" "$threads" "$candidates"

for leg in review resolve; do
  if [[ $leg == review ]]; then inputs_meta="$review_meta"; else inputs_meta="$resolve_meta"; fi
  if [[ $leg == review ]]; then prior_or_findings="$prior"; else prior_or_findings="$findings"; fi

  # --rawfile keeps every byte exact, including the trailing newline.
  jq -n --argjson captured "$(captured_json)" \
    --rawfile skill "$workdir/$leg-skill.md" \
    --rawfile diff "$workdir/diff.patch" \
    --rawfile prompt "$workdir/$leg-prompt.txt" \
    --argjson meta "$inputs_meta" \
    --argjson prior_or_findings "$prior_or_findings" \
    --argjson threads "$threads" \
    --argjson candidates "$candidates" \
    --arg leg "$leg" '
    {
      captured: $captured,
      function: ("prompt_" + $leg),
      inputs: ({
        skill: $skill,
        diff: $diff,
        meta: $meta,
        threads: $threads
      } + (if $leg == "review"
           then {prior: $prior_or_findings}
           else {findings: $prior_or_findings, candidates: $candidates} end)),
      prompt: $prompt
    }' >"$FIXDIR/prompt_$leg.json"
done

rm -rf "$workdir"
printf 'parity vectors written to %s\n' "$FIXDIR"
