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

# Colour is decided when lib/ui.sh is sourced, from whether stdout is a terminal
# at that moment. A vector holding refusal text would therefore carry escape
# codes when captured from a terminal and none when captured through a pipe, so
# the same behaviour would freeze two different ways. NO_COLOR settles it before
# the decision is made.
export NO_COLOR=1
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
# shellcheck source=../lib/log.sh
source "$REPO_ROOT/lib/log.sh"
# shellcheck source=../lib/credentials.sh
source "$REPO_ROOT/lib/credentials.sh"
# shellcheck source=../lib/usage.sh
source "$REPO_ROOT/lib/usage.sh"

platform="$(uname -s -r -m)"
tr_path="$(command -v tr)"
if tr --version </dev/null 2>/dev/null | grep -q GNU; then tr_flavor="GNU coreutils tr"; else tr_flavor="BSD tr"; fi
locale="${LC_ALL:-${LC_CTYPE:-${LANG:-unset}}}"
awk_path="$(command -v awk)"
# The diff oracle is entirely awk, and the three common implementations disagree
# on substr past the end, sprintf("%c", n) above 255, and reading a hex or
# exponent string as a number. A reader checking diff_views.json against their
# own awk needs to know which one answered here.
if awk --version </dev/null 2>/dev/null | grep -qi 'gnu awk'; then awk_flavor="GNU awk"
elif awk -W version </dev/null 2>&1 | grep -qi mawk;    then awk_flavor="mawk"
elif awk --version </dev/null 2>&1 | grep -qi 'awk version'; then awk_flavor="one true awk (BWK)"
else awk_flavor="unidentified awk"; fi

captured_json() {
  jq -n --arg p "$platform" --arg t "$tr_path ($tr_flavor)" --arg l "$locale" \
        --arg a "$awk_path ($awk_flavor)" '{
    platform: $p,
    tr_implementation: $t,
    awk_implementation: $a,
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

# The same capture, recording the input as raw text rather than through
# --argjson.
#
# state_marker_encode is `jq -c .`, which parses and re-prints, so it
# normalises scalars. --argjson would normalise the recorded input too, and a
# reader replaying from it would start from bytes the shell had already
# rewritten. These cases exist to pin the rewriting itself, so the input has to
# survive verbatim.
encode_raw_case() { # name raw_payload
  local encoded
  encoded="$(state_marker_encode "$2")"
  jq -cn --arg n "$1" --arg inp "$2" --arg enc "$encoded" \
    '{name:$n, input_raw:$inp, encoded:$enc}'
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

  # What jq rewrites on the way out. A harness writes the payload and it has
  # never met jq before this call, so these are the bytes that reach a public
  # comment rather than a hypothetical.
  encode_raw_case "raw-escaped-solidus" \
    '{"v":1,"leg":"review","pass":1,"findings":[{"path":"a\/b.ts"}]}'
  encode_raw_case "raw-escape-printable-ascii" \
    '{"v":1,"leg":"review","pass":1,"summary":"\u0041\u007e"}'
  encode_raw_case "raw-escape-control" \
    '{"v":1,"leg":"review","pass":1,"summary":"\u0007"}'
  encode_raw_case "raw-escape-delete" \
    '{"v":1,"leg":"review","pass":1,"summary":"\u007f"}'
  encode_raw_case "raw-escape-non-ascii" \
    '{"v":1,"leg":"review","pass":1,"summary":"\u00e9\u4e2d"}'
  encode_raw_case "raw-escape-surrogate-pair" \
    '{"v":1,"leg":"review","pass":1,"summary":"\ud83d\ude00"}'
  encode_raw_case "raw-escape-line-separators" \
    '{"v":1,"leg":"review","pass":1,"summary":"a\u2028b\u2029c"}'
  encode_raw_case "raw-duplicate-key" \
    '{"v":1,"leg":"review","pass":1,"pass":2}'
  encode_raw_case "raw-insignificant-whitespace" \
    '{ "v" : 1, "leg" : "review", "pass" : [1, 2] }'
  encode_raw_case "raw-html-characters" \
    '{"v":1,"leg":"review","pass":1,"summary":"a<b&c>d"}'
  # Numbers. Every literal below is one jq preserves, which is why the port
  # passes them through verbatim rather than reformatting.
  encode_raw_case "raw-number-trailing-zero" \
    '{"v":1,"leg":"review","pass":1,"n":1.50}'
  encode_raw_case "raw-number-negative-zero" \
    '{"v":1,"leg":"review","pass":1,"n":-0.0}'
  encode_raw_case "raw-number-past-float-precision" \
    '{"v":1,"leg":"review","pass":1,"n":12345678901234567890}'
  # The one shape the port deliberately does not reproduce. jq rewrites an
  # exponent into its own canonical form, and which form that is changed
  # between jq 1.6 and 1.7, so reproducing it would pin one jq family into the
  # contract. No marker CrossRev writes carries an exponent. Frozen so the
  # divergence is visible in the oracle rather than only in a code comment.
  encode_raw_case "raw-number-exponent-divergent" \
    '{"v":1,"leg":"review","pass":1,"n":1e2}'
  encode_raw_case "raw-number-exponent-negative-divergent" \
    '{"v":1,"leg":"review","pass":1,"n":1e-2}'
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

# An existing empty file, and a comment-only one, on the WORKING-TREE path.
# Both resolve to null through yq, where an absent file never reaches it. They
# state no policy, which is what an absent file states, so they are silent.
# Frozen because both were a jq type error and a misdirected assertion before.
wt_null_case() { # name file_body -> case json
  local d; d="$(mktemp -d "$config_capture_dir/wtnull_XXXXXX")"
  (
    cd "$d" && git init -q . && git commit -q --allow-empty -m init
    mkdir -p .github
    printf '%s' "$2" > .github/crossrev.yml
    XDG_CONFIG_HOME="$d/xdg"; export XDG_CONFIG_HOME
    cfg_load
    jq -n --arg n "$1" --arg r "$2" --argjson merged "$CFG_MERGED" \
      '{name:$n, repo_yaml:$r, operator_yaml:null, base_sha:null, merged:$merged}'
  )
}
wt_null_case "wt-empty-file"   ''            >"$config_capture_dir/case_wt_empty.json"
wt_null_case "wt-comment-only" $'# nothing\n' >"$config_capture_dir/case_wt_comment.json"

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
  # A document that parses and is not a mapping. yq answers 0 and returns a
  # sequence or a scalar, so the parse refusal never fires; the merge used to
  # die with jq's own type error and two more lines after it.
  config_refusal_case "non-mapping-sequence" "shape" $'- a\n- b\n'
  config_refusal_case "non-mapping-scalar"   "shape" $'42\n'
  config_refusal_case "non-mapping-boolean"  "shape" $'true\n'
  config_refusal_base_case "non-mapping-at-base" "shape" $'- a\n- b\n'
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

cfg_cases="[$(cat "$config_capture_dir/case_defaults.json"), $(cat "$config_capture_dir/case_repo_over.json"), $(cat "$config_capture_dir/case_op_override.json"), $(cat "$config_capture_dir/case_base_fallback.json"), $(cat "$config_capture_dir/case_base_absent.json"), $(cat "$config_capture_dir/case_base_empty.json"), $(cat "$config_capture_dir/case_wt_empty.json"), $(cat "$config_capture_dir/case_wt_comment.json")]"

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

# --- prompt_commit_convention over a real base revision ---------------------
#
# The resolve prompt fixture above records an empty base_sha, so the commit
# convention contributes nothing to it and a port could restate lib/prompt.sh
# instead of reproducing it. These vectors give it its own oracle.
#
# The repository is built with pinned author, committer and dates, so the base
# revision and every recorded byte are the same on any machine.

cc_workdir="$(mktemp -d)"

cc_commit() { # <email> <subject>
  GIT_AUTHOR_NAME="capture" GIT_AUTHOR_EMAIL="$1" \
  GIT_COMMITTER_NAME="capture" GIT_COMMITTER_EMAIL="$1" \
  GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z" \
  git commit -q --allow-empty -m "$2"
}

cc_case() { # name <n_repo_subjects> <n_mine> <template|"">
  local name="$1" n_repo="$2" n_mine="$3" template="$4"
  local d out base
  d="$(mktemp -d "$cc_workdir/cc_XXXXXX")"
  (
    cd "$d" && git init -q .
    local i
    for (( i = 1; i <= n_mine; i++ )); do
      cc_commit "crossrev@example.com" "chore(crossrev): a subject the leg must not learn from $i"
    done
    for (( i = 1; i <= n_repo; i++ )); do
      cc_commit "dev@example.com" "feat(api): add the $i-th endpoint"
    done
    if [[ -n "$template" ]]; then
      printf '%s' "$template" > .gitmessage
      git add .gitmessage
      cc_commit "dev@example.com" "chore: add a commit template"
    fi
    base="$(git rev-parse HEAD)"
    out="$(prompt_commit_convention "$base" "crossrev@example.com")"
    jq -cn --arg n "$name" --argjson r "$n_repo" --argjson m "$n_mine" \
      --arg t "$template" --arg o "$out" \
      '{name:$n, repo_subjects:$r, own_subjects:$m, template:$t, rendered:$o}'
  )
}

cc_cases() {
  # No base revision at all prints nothing, which is the arm every other case
  # would otherwise hide.
  jq -cn --arg o "$(prompt_commit_convention "" "crossrev@example.com")" \
    '{name:"no-base", repo_subjects:0, own_subjects:0, template:"", rendered:$o}'
  # Under the floor takes the fallback; at and above it takes the log. The two
  # sides of that boundary were both untested.
  cc_case "four-subjects-under-the-floor"  4  0 ""
  cc_case "five-subjects-at-the-floor"     5  0 ""
  cc_case "six-subjects-above-the-floor"   6  0 ""
  # The cap is on the sample, not on how far back the filter looks: twelve of
  # crossrev's own commits sit above twenty-five repository ones.
  cc_case "twenty-of-twenty-five"         25 12 ""
  # A template is quoted, and capped at twenty lines of its own.
  cc_case "with-a-short-template"          6  0 "$(printf 'A subject line\n\nWhy, not what.\n')"
  cc_case "with-a-long-template"           6  0 "$(printf 'line %s\n' $(seq 1 25))"
  # Repository text reaches a prompt, so the quoting has to hold.
  cc_case "template-carrying-a-fence"      6  0 "$(printf 'A subject\n\n```\ncode\n```\n')"
  cc_case "own-commits-only"               0  6 ""
}

jq -n --argjson captured "$(captured_json)" \
  --argjson cases "$(cc_cases | jq -s .)" \
  '{captured:$captured, function:"prompt_commit_convention", cases:$cases}' \
  >"$FIXDIR/prompt_commit_convention.json"

rm -rf "$cc_workdir"


# --- log_redact_str and log_redact_publish over credential shapes ------------
#
# The filter is sed under LC_ALL=C, so its answers depend on the sed on the
# machine and on the byte-oriented locale the function pins. Both routes out of
# a run are captured: the string filter that reaches a harness error message,
# and the publish filter that appends a notice and fails closed.

# base64 of arbitrary bytes, on one line. Through openssl because it is already
# a dependency and spells the flag the same way on both platforms, where the
# base64 command does not.
b64() { printf '%s' "$1" | openssl base64 -A; }

redact_case() { # name text
  local filtered published published_rc
  # A sentinel byte on both captures, stripped afterwards. Command substitution
  # eats every trailing newline, and a trailing newline is exactly what
  # log_redact_publish compares to decide whether the notice is owed.
  filtered="$(log_redact_str "$2"; printf 'x')"; filtered="${filtered%x}"
  if published="$(log_redact_publish "$2"; printf 'x')"; then published_rc=0; else published_rc=$?; fi
  published="${published%x}"
  # The _b64 fields are the authoritative ones and the plain fields are a
  # reading aid. jq --arg demands valid UTF-8 and replaces every other byte
  # with U+FFFD, so the two cases built from raw \xff and \x80 bytes froze as
  # replacement characters and asserted nothing about the bytes they name. The
  # filter pins LC_ALL=C precisely because a failing harness dumps bytes that
  # are not text, so that is the case worth freezing exactly.
  jq -cn --arg n "$1" --arg t "$2" --arg f "$filtered" \
        --arg p "$published" --argjson rc "$published_rc" \
        --arg tb "$(b64 "$2")" --arg fb "$(b64 "$filtered")" --arg pb "$(b64 "$published")" \
    '{name:$n, text:$t, text_b64:$tb, redacted:$f, redacted_b64:$fb,
      published:$p, published_b64:$pb, published_rc:$rc}'
}

redact_cases() {
  redact_case "no-credential-plain-text" "a review finding about lib/auth.ts:42"
  redact_case "empty-string" ""
  # Each shape at the exact prefix length the pattern keeps, then longer.
  redact_case "anthropic-key" "token sk-ant-api03-AAAAAAAAAAAAAAAAAAAA end"
  redact_case "anthropic-key-at-prefix-length" "sk-ant-abcdef"
  redact_case "anthropic-key-one-past-prefix" "sk-ant-abcdefg"
  redact_case "github-fine-grained-pat" "github_pat_11ABCDEF0_aaaaaaaaaaaaaaaaaaaaaaaa"
  redact_case "github-classic-pat" "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
  redact_case "github-oauth-token" "gho_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
  redact_case "github-user-token" "ghu_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
  redact_case "github-server-token" "ghs_DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"
  redact_case "github-refresh-token" "ghr_EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE"
  redact_case "xai-key" "xai-abcdef0123456789ABCDEF"
  # The generic sk- rule needs twelve characters after the six-character
  # prefix, so the two sides of that boundary are the cases worth freezing.
  redact_case "generic-sk-eleven-after-prefix" "sk-abcdef01234567890"
  redact_case "generic-sk-twelve-after-prefix" "sk-abcdef01234567890a"
  redact_case "two-credentials-one-line" "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA and sk-ant-api03-BBBBBBBBBBBBBBBBBBBB"
  redact_case "credential-inside-a-sentence" "the token is ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA, so rotate it"
  redact_case "multiline-with-a-credential" $'line one\nghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nline three'
  redact_case "trailing-newline-no-credential" $'a body\n'
  redact_case "trailing-newline-with-a-credential" $'ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n'
  # Idempotence: the notice must mean a mask happened on this pass, not that a
  # masked string arrived already masked.
  redact_case "already-redacted-text" "ghp_AAAAAA…[redacted]"
  # Bytes that are not valid UTF-8 are exactly what a failing harness dumps.
  redact_case "invalid-utf8-bytes" "$(printf 'before \xff\xfe after ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA')"
  redact_case "nul-adjacent-high-bytes" "$(printf '\x80\x81\x82 ghp_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB')"
  # A byte that is not text, landing inside a token rather than beside one. The
  # first ends the six-character prefix one byte early so nothing matches; the
  # second lets the prefix complete and stops the body at the byte.
  redact_case "high-byte-inside-the-token-prefix" "$(printf 'ghp_AAAAAA\xffBBBBBBBBBBBB')"
  redact_case "high-byte-after-the-token-prefix" "$(printf 'ghp_AAAAAAA\xffBBBB')"
  # A hyphen and an underscore are both in the anthropic and xai classes and
  # neither is in the classic-github one, which is the difference between the
  # patterns most easily lost in a port.
  redact_case "classic-github-stops-at-underscore" "ghp_AAAAAA_BBBBBBBBBBBBBBBBBBBBBBBB"
  redact_case "anthropic-continues-through-hyphen" "sk-ant-api03-AAAA-BBBB-CCCC"
}

jq -n --argjson captured "$(captured_json)" \
  --argjson cases "$(redact_cases | jq -s .)" \
  --arg notice "$LOG_REDACT_NOTICE" \
  '{captured:$captured, function:"log_redact_str and log_redact_publish",
    notice:$notice, cases:$cases}' \
  >"$FIXDIR/redaction.json"

# --- legs_github_slug over remote URLs ---------------------------------------
#
# Host isolation, not substring matching. The refusals are the point: a host
# that merely contains github.com, a userinfo @ that lives in the path rather
# than the authority, and a path that is not exactly two segments.

slug_case() { # name url
  local out rc
  out="$(legs_github_slug "$2")" && rc=0 || rc=$?
  jq -cn --arg n "$1" --arg u "$2" --arg o "$out" --argjson rc "$rc" \
    '{name:$n, url:$u, slug:(if $rc == 0 then $o else null end), rc:$rc}'
}

slug_cases() {
  slug_case "https" "https://github.com/carlosboeing/crossrev"
  slug_case "https-dot-git" "https://github.com/carlosboeing/crossrev.git"
  slug_case "https-trailing-slash" "https://github.com/carlosboeing/crossrev/"
  slug_case "https-trailing-slash-and-git" "https://github.com/carlosboeing/crossrev.git/"
  slug_case "https-with-userinfo" "https://token@github.com/carlosboeing/crossrev"
  slug_case "https-with-user-and-password" "https://user:pass@github.com/carlosboeing/crossrev"
  slug_case "https-with-port" "https://github.com:443/carlosboeing/crossrev"
  slug_case "ssh-scheme" "ssh://git@github.com/carlosboeing/crossrev.git"
  slug_case "git-scheme" "git://github.com/carlosboeing/crossrev.git"
  slug_case "scp-style" "git@github.com:carlosboeing/crossrev.git"
  slug_case "scp-style-no-user" "github.com:carlosboeing/crossrev"
  slug_case "uppercase-host" "https://GitHub.COM/carlosboeing/crossrev"
  slug_case "mixed-case-path-is-kept" "https://github.com/CarlosBoeing/CrossRev"
  slug_case "dots-and-dashes-in-names" "https://github.com/some-org/some.repo_name"
  # Refusals.
  slug_case "host-that-only-contains-github" "https://github.com.example.net/a/b"
  slug_case "host-with-github-as-a-prefix" "https://github.community/a/b"
  # Each of the three below fails a DIFFERENT way to reach the refusal, and
  # every case above them fails for one reason. Without these, a rule that
  # compared the host by suffix, stripped userinfo on any @ rather than only one
  # in the authority, or split on // rather than ://, would refuse every frozen
  # case for its own reason and admit a host that is not github.com.
  slug_case "host-ending-in-the-real-host" "https://notgithub.com/a/b"
  slug_case "host-ending-in-the-real-host-as-a-word" "https://mygithub.com/a/b"
  slug_case "userinfo-at-in-the-path-before-the-real-host" "https://example.com/x@github.com/a/b"
  slug_case "double-slash-in-a-local-path" "/tmp//github.com/a/b"
  slug_case "colon-in-a-path-segment-after-a-slash" "github.com/a:b"
  slug_case "gitlab" "https://gitlab.com/a/b"
  slug_case "local-path-holding-the-host" "/home/dev/github.com/a/b"
  slug_case "relative-path" "../github.com/a/b"
  slug_case "userinfo-at-in-the-path" "https://example.com/github.com@a/b"
  slug_case "one-path-segment" "https://github.com/onlyone"
  slug_case "three-path-segments" "https://github.com/a/b/c"
  slug_case "empty-path" "https://github.com/"
  slug_case "no-path" "https://github.com"
  slug_case "colon-after-a-slash-is-a-path" "/tmp/x:github.com/a/b"
  slug_case "empty-string" ""
  slug_case "path-segment-with-a-space" "https://github.com/a b/c"
  slug_case "path-segment-with-a-tilde" "https://github.com/a~b/c"
}

jq -n --argjson captured "$(captured_json)" \
  --argjson cases "$(slug_cases | jq -s .)" \
  '{captured:$captured, function:"legs_github_slug", cases:$cases}' \
  >"$FIXDIR/github_slug.json"

# --- credential reads: strip sets, JWT claims and durations -------------------
#
# The strip set is a function of the descriptor, not a list held here: every
# credential name any harness declares, minus the names this harness keeps.
# Freezing it means a descriptor edit that widens what one harness sees is a
# failed vector rather than a silent grant.

cred_strip_case() { # harness
  jq -cn --arg h "$1" --argjson names "$(cred_env_strip_for "$1" | jq -Rs 'split("\n") | map(select(length > 0))')" \
    '{harness:$h, strip:$names}'
}

cred_strip_cases() {
  local h
  while IFS= read -r h; do cred_strip_case "$h"; done < <(harness_names)
  # A name no descriptor declares strips everything and keeps nothing.
  cred_strip_case "not-a-harness"
}

# A JWT the capture builds rather than one anybody issued: header and claims are
# fixed text, and the signature segment is never read.
cred_jwt() { # claims_json
  local h p
  h="$(printf '%s' '{"alg":"none","typ":"JWT"}' | openssl base64 -A | tr '/+' '_-' | tr -d '=')"
  p="$(printf '%s' "$1" | openssl base64 -A | tr '/+' '_-' | tr -d '=')"
  printf '%s.%s.%s' "$h" "$p" "c2ln"
}

jwt_case() { # name jwt
  local claims rc
  claims="$(cred_jwt_claims "$2")" && rc=0 || rc=$?
  jq -cn --arg n "$1" --arg j "$2" --arg c "$claims" --argjson rc "$rc" \
    '{name:$n, jwt:$j, claims:(if $rc == 0 then $c else null end), rc:$rc}'
}

jwt_cases() {
  jwt_case "expiry-and-issuer" "$(cred_jwt '{"exp":1893456000,"iss":"https://example.test","aud":"crossrev"}')"
  jwt_case "no-expiry" "$(cred_jwt '{"iss":"https://example.test"}')"
  jwt_case "empty-claims-object" "$(cred_jwt '{}')"
  jwt_case "unicode-in-a-claim" "$(cred_jwt '{"name":"été","exp":1}')"
  # Padding: base64url drops the = signs, so the decoder restores them by
  # length. A remainder of one is not a valid length and must refuse.
  jwt_case "payload-needing-two-pad-bytes" "$(cred_jwt '{"a":1}')"
  jwt_case "payload-needing-one-pad-byte" "$(cred_jwt '{"ab":1}')"
  jwt_case "payload-needing-no-pad" "$(cred_jwt '{"abc":1}')"
  # Refusals.
  jwt_case "not-a-jwt-no-dots" "abcdef"
  jwt_case "one-dot-only" "abc.def"
  jwt_case "payload-is-not-base64url" "aaa.!!!!.ccc"
  jwt_case "payload-decodes-to-non-json" "$(printf 'aaa.%s.ccc' "$(printf 'not json' | openssl base64 -A | tr '/+' '_-' | tr -d '=')")"
  jwt_case "empty-payload-segment" "aaa..ccc"
  jwt_case "empty-string" ""
}

duration_case() { # seconds
  jq -cn --argjson s "$1" --arg d "$(_cred_human_duration "$1")" '{seconds:$s, human:$d}'
}

duration_cases() {
  # Every boundary of the four-arm ladder, on both sides.
  duration_case -1; duration_case 0; duration_case 1
  duration_case 59; duration_case 60; duration_case 61
  duration_case 3599; duration_case 3600; duration_case 3601
  duration_case 172799; duration_case 172800; duration_case 172801
  duration_case 86400; duration_case 604800
}

jq -n --argjson captured "$(captured_json)" \
  --argjson strip "$(cred_strip_cases | jq -s .)" \
  --argjson jwt "$(jwt_cases | jq -s .)" \
  --argjson durations "$(duration_cases | jq -s .)" \
  --argjson min_seconds "$CRED_MIN_SECONDS" \
  '{captured:$captured,
    function:"cred_env_strip_for, cred_jwt_claims and _cred_human_duration",
    cred_min_seconds:$min_seconds,
    strip_sets:$strip, jwt_cases:$jwt, duration_cases:$durations}' \
  >"$FIXDIR/credentials.json"

usage_workdir="$(mktemp -d)"

# --- usage normalization, price keys and table costs -----------------------
#
# The most arithmetic-heavy surface in the tool. Rates are per-token dollars
# scaled to nano-dollars so the sum stays integral, and three separate rules
# refuse to price rather than guess. A port that rounds once more or once less
# than jq does produces a different cost with no error anywhere, so the answers
# are frozen rather than described.

usage_file() { # json_text -> path
  local f; f="$(mktemp "$usage_workdir/u_XXXXXX")"
  printf '%s' "$1" >"$f"
  printf '%s' "$f"
}

parse_case() { # name parser json_text
  local f out
  f="$(usage_file "$3")"
  case "$2" in
    claude)   out="$(usage_parse_claude "$f")" ;;
    codex)    out="$(usage_parse_codex_events "$f")" ;;
    grok)     out="$(usage_parse_grok "$f")" ;;
    agy)      out="$(usage_parse_agy "$f")" ;;
    opencode) out="$(usage_parse_opencode_export "$f")" ;;
  esac
  jq -cn --arg n "$1" --arg p "$2" --arg i "$3" --arg o "$out" \
    '{name:$n, parser:$p, input:$i, record:$o}'
}

parse_cases() {
  # claude: the split wins over the modelUsage write sum, and the excess lands
  # in cache_write_unsplit rather than being dropped.
  parse_case "claude-two-models-with-a-split" claude '{"modelUsage":{"claude-opus-5[1m]":{"inputTokens":100,"outputTokens":50,"cacheReadInputTokens":900,"cacheCreationInputTokens":300},"claude-sonnet-5":{"inputTokens":10,"outputTokens":5,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}},"usage":{"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":50},"output_tokens_details":{"thinking_tokens":40}},"total_cost_usd":0.1234}'
  parse_case "claude-split-covers-the-writes" claude '{"modelUsage":{"m":{"inputTokens":1,"outputTokens":1,"cacheReadInputTokens":0,"cacheCreationInputTokens":100}},"usage":{"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":0}}}'
  parse_case "claude-split-exceeds-the-writes" claude '{"modelUsage":{"m":{"inputTokens":1,"outputTokens":1,"cacheReadInputTokens":0,"cacheCreationInputTokens":10}},"usage":{"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":0}}}'
  parse_case "claude-no-split-at-all" claude '{"modelUsage":{"m":{"inputTokens":1,"outputTokens":2,"cacheReadInputTokens":3,"cacheCreationInputTokens":4}}}'
  parse_case "claude-canonical-model-wins-over-the-key" claude '{"modelUsage":{"vendor/m[1m]":{"canonicalModel":"m-canonical","inputTokens":1,"outputTokens":1,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}}}'
  parse_case "claude-bracket-suffix-stripped-from-the-key" claude '{"modelUsage":{"claude-opus-5[1m]":{"inputTokens":1,"outputTokens":1,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}}}'
  parse_case "claude-models-sorted-by-total-descending" claude '{"modelUsage":{"small":{"inputTokens":1,"outputTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0},"large":{"inputTokens":100,"outputTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}}}'
  parse_case "claude-cost-present-without-model-usage" claude '{"total_cost_usd":0.5}'
  parse_case "claude-cost-is-not-a-number" claude '{"modelUsage":{"m":{"inputTokens":1}},"total_cost_usd":"0.5"}'
  parse_case "claude-empty-object" claude '{}'
  parse_case "claude-not-json" claude 'not json at all'
  # codex: cached tokens are folded inside input_tokens, so fresh is a
  # subtraction and the only derived field.
  parse_case "codex-last-turn-wins" codex '{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":3}}
{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":7}}'
  parse_case "codex-no-cached-field" codex '{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":3}}'
  parse_case "codex-cached-exceeds-input" codex '{"type":"turn.completed","usage":{"input_tokens":5,"cached_input_tokens":9,"output_tokens":1}}'
  parse_case "codex-no-completed-turn" codex '{"type":"turn.started"}'
  parse_case "codex-empty-stream" codex ''
  # grok, agy and opencode.
  parse_case "grok-full-record" grok '{"modelUsage":{"grok-4.6-build":{}},"usage":{"input_tokens":10,"cache_read_input_tokens":5,"cache_creation_input_tokens":2,"output_tokens":3,"reasoning_tokens":9},"total_cost_usd":0.25}'
  parse_case "grok-no-usage" grok '{"modelUsage":{"grok-4.6-build":{}}}'
  parse_case "grok-no-model-usage" grok '{"usage":{"input_tokens":1,"output_tokens":1}}'
  parse_case "agy-vendor-total-ignored" agy '{"usage":{"input_tokens":10,"cache_read_tokens":90,"output_tokens":5,"thinking_tokens":2,"total_tokens":15}}'
  parse_case "agy-no-usage" agy '{}'
  parse_case "opencode-full-export" opencode '{"info":{"model":{"id":"anthropic/claude-sonnet-5"},"tokens":{"input":10,"output":20,"reasoning":5,"cache":{"read":100,"write":30}}}}'
  parse_case "opencode-no-model-id" opencode '{"info":{"tokens":{"input":1,"output":1,"cache":{"read":0,"write":0}}}}'
  parse_case "opencode-no-tokens" opencode '{"info":{"model":{"id":"m"}}}'
}

price_key_case() { # reported
  jq -cn --arg r "$1" --arg k "$(usage_price_key "$1")" '{reported:$r, key:$k}'
}

price_key_cases() {
  price_key_case ""
  price_key_case "claude-opus-5"
  price_key_case "CLAUDE-OPUS-5"
  price_key_case "claude-opus-5[1m]"
  price_key_case "Claude-Opus-5[1M]"
  # A listed key carrying a provider prefix is matched by its bare id.
  price_key_case "grok-4.6"
  price_key_case "xai/grok-4.6"
  # A reported id that merely contains a listed bare id takes the longest one,
  # which is how grok-4.6-build prices as xai/grok-4.6 and not as xai/grok-4.5.
  price_key_case "grok-4.6-build"
  price_key_case "openai/gpt-5.6-terra"
  price_key_case "gpt-5.6-terra-preview"
  price_key_case "claude-opus-5-20260101"
  price_key_case "anthropic/claude-sonnet-5"
  price_key_case "a-model-nobody-listed"
  price_key_case "[only-a-suffix]"
  # `version` is a key in the table and is not a model. The exact-match arm
  # answers it, which is a thing a port has to reproduce rather than fix.
  price_key_case "version"
}

price_case() { # name usage_json model
  jq -cn --arg n "$1" --arg u "$2" --arg m "$3" \
    --arg o "$(usage_price "$2" "$3")" \
    '{name:$n, usage:$u, model:$m, priced:$o}'
}

price_cases() {
  local zero ph
  zero="$(usage_zero)"
  price_case "unlisted-model-does-not-price" "$zero" "a-model-nobody-listed"
  price_case "empty-model-does-not-price" "$zero" ""
  price_case "zero-record-on-a-listed-model" "$zero" "claude-opus-5"
  # Every bucket the anthropic entry lists a rate for.
  ph="$(jq -c '.input_fresh=1000|.output=500|.cache_read=2000' <<<"$zero")"
  price_case "input-output-and-cache-read" "$ph" "claude-opus-5"
  price_case "one-input-token" "$(jq -c '.input_fresh=1' <<<"$zero")" "claude-opus-5"
  price_case "cache-write-5m" "$(jq -c '.cache_write_5m=1000' <<<"$zero")" "claude-opus-5"
  price_case "cache-write-1h-uses-the-above-1hr-rate" "$(jq -c '.cache_write_1h=1000' <<<"$zero")" "claude-opus-5"
  # An unresolvable write TTL refuses where the two write rates differ, and
  # prices where the entry lists no separate above-1hr rate at all.
  price_case "unsplit-write-refuses-on-anthropic" "$(jq -c '.cache_write_unsplit=1000' <<<"$zero")" "claude-opus-5"
  price_case "unsplit-write-prices-where-no-1hr-rate-is-listed" "$(jq -c '.cache_write_unsplit=1000' <<<"$zero")" "gpt-5.6"
  # A bucket holding tokens whose rate the entry omits refuses; the same bucket
  # at zero still prices, because only a nonzero bucket counts.
  price_case "write-bucket-with-no-listed-rate-refuses" "$(jq -c '.cache_write_5m=10' <<<"$zero")" "gpt-5.5"
  price_case "same-entry-prices-with-that-bucket-empty" "$(jq -c '.input_fresh=1000|.output=100' <<<"$zero")" "gpt-5.5"
  # A per-request long-context break a cumulative total cannot rule out.
  price_case "under-the-272k-break" "$(jq -c '.input_fresh=271999' <<<"$zero")" "gpt-5.5"
  price_case "at-the-272k-break" "$(jq -c '.input_fresh=272000' <<<"$zero")" "gpt-5.5"
  price_case "break-counts-every-input-bucket" "$(jq -c '.input_fresh=200000|.cache_read=72000' <<<"$zero")" "gpt-5.5"
  price_case "output-alone-does-not-reach-the-break" "$(jq -c '.output=400000' <<<"$zero")" "gpt-5.5"
  price_case "at-the-200k-break" "$(jq -c '.input_fresh=200000' <<<"$zero")" "xai/grok-4.6"
  price_case "under-the-200k-break" "$(jq -c '.input_fresh=199999' <<<"$zero")" "xai/grok-4.6"
  # Reasoning is excluded from the total and therefore from the cost.
  price_case "reasoning-is-not-priced" "$(jq -c '.output=500|.reasoning=400' <<<"$zero")" "claude-opus-5"
  # An unlisted model clears a cost the adapter had already reported.
  price_case "unlisted-model-clears-a-reported-cost" "$(jq -c '.input_fresh=1000|.cost_usd=9|.cost_source="harness"|.price_table="x"' <<<"$zero")" "a-model-nobody-listed"
  # Rates below a nano-dollar round once, at the rate rather than at the sum.
  price_case "cache-read-at-half-a-nano-dollar" "$(jq -c '.cache_read=1' <<<"$zero")" "claude-opus-5"
  price_case "cache-read-at-scale" "$(jq -c '.cache_read=1000000' <<<"$zero")" "claude-opus-5"
}

billing_case() { # name harness endpoint api_key
  local out
  if [[ -n "$4" ]]; then out="$(ANTHROPIC_API_KEY="$4" usage_billing_for "$2" "$3")"
  else out="$(ANTHROPIC_API_KEY="" usage_billing_for "$2" "$3")"; fi
  jq -cn --arg n "$1" --arg h "$2" --arg e "$3" --arg k "$4" --arg o "$out" \
    '{name:$n, harness:$h, endpoint:$e, anthropic_api_key_set:($k | length > 0), billing:$o}'
}

billing_cases() {
  local h
  while IFS= read -r h; do
    billing_case "$h-vendor-no-key" "$h" "vendor" ""
    billing_case "$h-named-endpoint" "$h" "an-endpoint" ""
    billing_case "$h-vendor-with-anthropic-key" "$h" "vendor" "sk-ant-test"
  done < <(harness_names)
  billing_case "empty-endpoint-string" "claude" "" ""
  billing_case "null-endpoint-string" "claude" "null" ""
  billing_case "unknown-harness" "not-a-harness" "vendor" ""
}

format_cost_cases() {
  # No value on an exact half-cent boundary. usage_format_cost is printf %.2f,
  # and bash's builtin converts its argument with strtold, so the rounding is
  # decided in long double. That type is 80-bit on x86-64 and 64-bit on arm64,
  # and 0.005 lands on opposite sides of the boundary in the two: one answers
  # ~$0.01 and the other ~$0.00. Freezing either would make the vector a
  # property of the machine that captured it. 0.0049 and 0.0051 sit either side
  # with room to spare and answer the same everywhere. 0.125 is exactly
  # representable, so both widths agree on it and it stays.
  local v
  for v in "" "0" "0.004" "0.0049" "0.0051" "0.125" "1" "12.345" "-1.5" "1e-3" "1E3" "abc" "0.1.2" " 1"; do
    jq -cn --arg v "$v" --arg o "$(usage_format_cost "$v")" '{value:$v, formatted:$o}'
  done
}

footnote_cases() {
  local cs b
  for cs in "" "null" "harness" "table" "other"; do
    for b in "" "null" "subscription" "api" "endpoint"; do
      jq -cn --arg c "$cs" --arg b "$b" --arg o "$(usage_footnote "$cs" "$b")" \
        '{cost_source:$c, billing:$b, footnote:$o}'
    done
  done
}

jq -n --argjson captured "$(captured_json)" \
  --argjson zero "$(usage_zero)" \
  --argjson with_total "$(usage_with_total "$(usage_zero)")" \
  --argjson parses "$(parse_cases | jq -s .)" \
  --argjson price_keys "$(price_key_cases | jq -s .)" \
  --argjson prices "$(price_cases | jq -s .)" \
  --argjson billing "$(billing_cases | jq -s .)" \
  --argjson formats "$(format_cost_cases | jq -s .)" \
  --argjson footnotes "$(footnote_cases | jq -s .)" \
  --arg price_table_version "$(jq -r '.version // ""' "$(_usage_prices_file)")" \
  '{captured:$captured,
    function:"usage normalization, price keys, table costs and presentation",
    price_table_version:$price_table_version,
    zero:$zero, zero_with_total:$with_total,
    parse_cases:$parses, price_key_cases:$price_keys, price_cases:$prices,
    billing_cases:$billing, format_cost_cases:$formats, footnote_cases:$footnotes}' \
  >"$FIXDIR/usage.json"

rm -rf "$usage_workdir"


# --- push targets, over real repositories -----------------------------------
#
# The one Phase-2 surface whose answer needs a repository rather than a string.
# git pushes to every remote.<name>.pushurl entry, so a second entry pointing
# somewhere else is a refusal rather than a value; and a pushInsteadOf rewrite
# is a warning that does not stop the push. Each case builds its own repository
# with pinned identity so the capture is reproducible.

push_workdir="$(mktemp -d)"

push_case() { # name <config lines, one per argument thereafter>
  local name="$1"; shift
  local d out err rc line
  d="$(mktemp -d "$push_workdir/pr_XXXXXX")"
  out="$(
    cd "$d" && git init -q . 2>/dev/null
    for line in "$@"; do
      # Each line is `key<TAB>value`, added rather than set so a repeated key
      # keeps both entries — which is the whole point of the two-pushurl cases.
      git config --add "${line%%$'\t'*}" "${line#*$'\t'}"
    done
    LEGS_PUSH_REPO=""
    legs_resolve_push_repo origin 2>"$d/err"
    printf '%s' "$LEGS_PUSH_REPO"
  )" && rc=0 || rc=$?
  err="$(cat "$d/err" 2>/dev/null || true)"
  jq -cn --arg n "$name" --argjson cfg "$(printf '%s\n' "$@" | jq -Rs 'split("\n")|map(select(length>0))')" \
    --arg o "$out" --arg e "$err" --argjson rc "$rc" \
    '{name:$n, config:$cfg, push_repo:$o, stderr:$e, rc:$rc}'
}

push_cases() {
  push_case "one-fetch-url" "remote.origin.url	https://github.com/o/r.git"
  push_case "one-push-url" \
    "remote.origin.url	https://github.com/o/r.git" \
    "remote.origin.pushurl	https://github.com/o/r.git"
  push_case "push-url-overrides-a-different-fetch-url" \
    "remote.origin.url	https://github.com/fetch-org/fetch-repo.git" \
    "remote.origin.pushurl	https://github.com/push-org/push-repo.git"
  push_case "two-push-urls-agreeing" \
    "remote.origin.pushurl	https://github.com/o/r.git" \
    "remote.origin.pushurl	git@github.com:o/r.git"
  push_case "two-push-urls-disagreeing" \
    "remote.origin.pushurl	https://github.com/o/r.git" \
    "remote.origin.pushurl	https://github.com/o/other.git"
  push_case "scp-style-fetch-url" "remote.origin.url	git@github.com:o/r.git"
  push_case "non-github-url-refuses" "remote.origin.url	https://gitlab.com/o/r.git"
  push_case "host-that-only-contains-github-refuses" \
    "remote.origin.url	https://github.com.example.net/o/r.git"
  push_case "no-remote-at-all"
  push_case "remote-with-no-url" "remote.origin.fetch	+refs/heads/*:refs/remotes/origin/*"
  # A rewrite reaches this code only where no explicit pushurl is set, so the
  # two URL lists come from one source and pair positionally.
  push_case "push-insteadof-rewrite-to-the-same-repository" \
    "remote.origin.url	https://github.com/o/r.git" \
    "url.git@github.com:o/r.git.pushInsteadOf	https://github.com/o/r.git"
  push_case "push-insteadof-rewrite-to-another-repository" \
    "remote.origin.url	https://github.com/o/r.git" \
    "url.git@github.com:elsewhere/other.git.pushInsteadOf	https://github.com/o/r.git"
  push_case "push-insteadof-rewrite-off-github" \
    "remote.origin.url	https://github.com/o/r.git" \
    "url.https://gitlab.com/o/r.git.pushInsteadOf	https://github.com/o/r.git"
}

jq -n --argjson captured "$(captured_json)" \
  --argjson cases "$(push_cases | jq -s .)" \
  '{captured:$captured, function:"legs_resolve_push_repo", cases:$cases}' \
  >"$FIXDIR/push_target.json"

rm -rf "$push_workdir"

# --- run-log paths and quarantine paths --------------------------------------
#
# Surface 3 of the acceptance contract: the on-disk layout, and the owner/repo
# to owner-repo slugging that names a run directory. Both are read from pinned
# environment rather than from the operator's own home.

runlog_case() { # name repo pr xdg_state home run_id
  local d
  d="$(XDG_STATE_HOME="$4" HOME="$5" GITHUB_RUN_ID="$6" log_run_dir "$2" "$3")"
  jq -cn --arg n "$1" --arg r "$2" --arg p "$3" --arg x "$4" --arg h "$5" \
    --arg g "$6" --arg d "$d" \
    '{name:$n, repo:$r, pr:$p, xdg_state_home:$x, home:$h, github_run_id:$g, dir:$d}'
}

runlog_cases() {
  runlog_case "xdg-state-set" "carlosboeing/crossrev" "42" "/state" "/home/dev" "12345"
  runlog_case "xdg-state-unset-falls-back-to-home" "carlosboeing/crossrev" "42" "" "/home/dev" "12345"
  # The slug replaces every slash, so a nested name flattens rather than nesting.
  runlog_case "nested-repository-name" "org/team/repo" "1" "/state" "/home/dev" "9"
  runlog_case "no-slash-in-the-name" "repo" "1" "/state" "/home/dev" "9"
  runlog_case "dots-and-dashes-survive" "some-org/some.repo" "7" "/state" "/home/dev" "9"
  runlog_case "trailing-slash-in-xdg-state" "o/r" "1" "/state/" "/home/dev" "9"
}

# The local run id is the process id, so it cannot be frozen as a literal. What
# is frozen is the shape: GITHUB_RUN_ID wins where it is set, and local-<pid>
# is the fallback.
runlog_local_id_shape="$(GITHUB_RUN_ID="" bash -c 'source "'"$REPO_ROOT"'/lib/log.sh"; log_run_id' | sed -E 's/^local-[0-9]+$/local-<pid>/')"

jq -n --argjson captured "$(captured_json)" \
  --argjson cases "$(runlog_cases | jq -s .)" \
  --arg local_id_shape "$runlog_local_id_shape" \
  --arg quarantine_dir "$CROSSREV_QUARANTINE" \
  --argjson quarantined "$(_sandbox_paths | jq -Rs 'split("\n")|map(select(length>0))')" \
  --argjson sandbox_args "$(
      { while IFS= read -r h; do
          jq -cn --arg h "$h" --arg a "$(sandbox_args_for "$h")" '{harness:$h, args:$a}'
        done < <(harness_names); } | jq -s .)" \
  '{captured:$captured,
    function:"log_run_dir, log_run_id, _sandbox_paths and sandbox_args_for",
    local_run_id_shape:$local_id_shape,
    run_dirs:$cases,
    quarantine_dir:$quarantine_dir,
    quarantined_paths:$quarantined,
    sandbox_args:$sandbox_args}' \
  >"$FIXDIR/paths.json"

printf 'parity vectors written to %s\n' "$FIXDIR"
