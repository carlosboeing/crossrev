#!/usr/bin/env bash
#
# Diff-layer tests: the line-number gutter the review leg reads, and the anchor
# check that decides whether GitHub will accept a finding's line.
#
# The fixture is the real one. On pull request 14 the reviewer put a finding on
# `tools/revloop/CHANGELOG.md:14` when the sentence it faulted was on line 13,
# and 13 is the last line of the hunk — so 14 was one line outside the diff and
# GitHub refused the comment. Both halves of the fix are pinned here: the gutter
# that stops the number being counted, and the snap that repairs it if it is.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/diff.sh
source "$HERE/../lib/diff.sh"

pass=0 fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" || notok "$1" "contains '$3'" "$2"; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# --- the fixture -----------------------------------------------------------
#
# Hunk one covers new lines 6..13 and old lines 6..11; hunk two covers new
# 35..38 and old 33..35. Line 14 exists in the file and in neither hunk, which
# is the whole point. The two hunks are far enough apart that nothing snaps
# across the gap between them.
#
# The blank context lines are written bare rather than as a single space. Git
# emits the space, and enough tooling strips it on the way here that a parser
# which only recognises a leading space is a parser that miscounts on a diff
# somebody's editor touched.
cat >"$tmp/pr14.diff" <<'DIFF'
diff --git a/tools/revloop/CHANGELOG.md b/tools/revloop/CHANGELOG.md
index e773ac4..0b34128 100644
--- a/tools/revloop/CHANGELOG.md
+++ b/tools/revloop/CHANGELOG.md
@@ -6,6 +6,8 @@ All notable changes to revloop.

 ### Added

+- **A two-direction template/default drift test.** Eleven behavior leaves must agree.
+
 - `revloop review --pr N` — one review pass. Claims before working.
 - `revloop resolve --pr N` — verifies every finding whatever its severity.
 - `revloop cycle --pr N` — the whole loop in one process, up to `max_passes`.
@@ -33,3 +35,4 @@ All notable changes to revloop.
 - `revloop status --pr N` — position and interruption.
-- `runs_per_day` bounded a number that was not runs.
+- `max_prs_per_day` counts distinct pull requests repository-wide.
+- `max_passes_per_cycle` replaces the scattered caps.
 - `revloop watchdog` — finds pull requests stuck waiting on a leg.
DIFF

# --- the gutter ------------------------------------------------------------
numbered="$(diff_number "$tmp/pr14.diff")"

is "the file header carries no gutter" \
  "$(grep -c '^diff --git a/tools/revloop/CHANGELOG.md' <<<"$numbered")" "1"
is "the hunk header carries no gutter" \
  "$(grep -c '^@@ -6,6 +6,8 @@' <<<"$numbered")" "1"

# The number the reviewer needs is now read rather than derived. `max_passes`
# sits on new line 13, and the gutter has to say so without being counted to.
maxpasses="$(grep 'up to `max_passes`' <<<"$numbered")"
has "a context line carries both its old and its new number" "$maxpasses" "  11   13 |"

added="$(grep 'two-direction template' <<<"$numbered")"
has "an added line has a new number and a dash for the old one" "$added" "   -    9 |"
has "and keeps the diff marker it carried" "$added" "|+- **A two-direction"

deleted="$(grep 'runs_per_day' <<<"$numbered")"
has "a deleted line has an old number and a dash for the new one" "$deleted" "  34    - |"

blanks="$(grep -c '^   6    6 |$' <<<"$numbered")"
is "a bare blank line inside a hunk still counts as context" "$blanks" "1"

is "the gutter preserves every line of the diff" \
  "$(wc -l <"$tmp/pr14.diff" | tr -d ' ')" "$(printf '%s\n' "$numbered" | wc -l | tr -d ' ')"

# --- the anchor check ------------------------------------------------------
CH=tools/revloop/CHANGELOG.md

is "a context line inside the hunk is used as given" \
  "$(diff_anchor "$tmp/pr14.diff" "$CH" RIGHT 13)" "13"
is "an added line is a line GitHub accepts" \
  "$(diff_anchor "$tmp/pr14.diff" "$CH" RIGHT 9)" "9"

# The regression. Without the snap this is empty, and the finding lands as a
# top-level comment naming a location instead of on the line it faults.
is "a line one past the end of a hunk snaps back into it" \
  "$(diff_anchor "$tmp/pr14.diff" "$CH" RIGHT 14)" "13"
is "and a line just before a hunk snaps forward into it" \
  "$(diff_anchor "$tmp/pr14.diff" "$CH" RIGHT 5)" "6"

is "a line far from any hunk is not snapped to one" \
  "$(diff_anchor "$tmp/pr14.diff" "$CH" RIGHT 24)" ""
is "a path the diff does not carry anchors nowhere" \
  "$(diff_anchor "$tmp/pr14.diff" "lib/other.sh" RIGHT 9)" ""

# --- the two sides count separately ----------------------------------------
#
# Old 11 and new 13 are the same line of text. A parser that pools the two
# columns would accept either number on either side and anchor half of them
# to the wrong line.
is "the left side counts in the old file's numbering" \
  "$(diff_anchor "$tmp/pr14.diff" "$CH" LEFT 11)" "11"
is "a deleted line is anchorable on the left" \
  "$(diff_anchor "$tmp/pr14.diff" "$CH" LEFT 34)" "34"
is "an added line's number is not a left-side line" \
  "$(diff_anchor "$tmp/pr14.diff" "$CH" LEFT 37)" "35"
is "and a deleted line's number is not a right-side line" \
  "$(diff_anchor "$tmp/pr14.diff" "$CH" RIGHT 33)" "35"

# --- ties and bounds -------------------------------------------------------
is "the snap is bounded, and three lines away is the last one it reaches" \
  "$(diff_anchor "$tmp/pr14.diff" "$CH" RIGHT 16)" "13"
is "four lines away is past the bound" \
  "$(diff_anchor "$tmp/pr14.diff" "$CH" RIGHT 17)" ""
is "and the bound is settable for a caller that wants a wider reach" \
  "$(diff_anchor "$tmp/pr14.diff" "$CH" RIGHT 17 4)" "13"

# Line 24 is eleven lines from both hunk one's last line and hunk two's first.
# The tie goes to the earlier line: a finding names something at or after the
# number the reviewer gave more often than before it.
is "an equidistant line snaps to the earlier of the two" \
  "$(diff_anchor "$tmp/pr14.diff" "$CH" RIGHT 24 11)" "13"

# --- degenerate input ------------------------------------------------------
: >"$tmp/empty.diff"
is "an empty diff anchors nothing" \
  "$(diff_anchor "$tmp/empty.diff" "$CH" RIGHT 9)" ""
is "and numbers to nothing rather than failing" "$(diff_number "$tmp/empty.diff")" ""

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
