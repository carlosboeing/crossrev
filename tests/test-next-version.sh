#!/usr/bin/env bash
# tests/test-next-version.sh — the next-version range as a black box.
#
# scripts/next-version.sh measures from the nearest reachable version tag, but
# a release cut on a pull request that was then squash-merged into main is not
# reachable from main: the tag stays on the pull request's commit while main
# carries a different commit with an identical tree. The script must measure
# from the tag named by VERSION instead, resolving it to the main-line commit
# that carries the release, and only fall back to the nearest reachable tag
# when no such tag exists.
#
# Each case builds a throwaway repository holding a copy of the script (which
# derives the repository root from its own path) and a VERSION file:
#   - a squash-merged release with only fixes after it reports a patch bump,
#     not the minor the re-counted feats would imply
#   - a release tagged on the main line behaves as before
#   - no tag matching VERSION falls back to the nearest reachable version tag
#   - the floating v0 tag is never chosen as the measuring point

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tmproot.sh
source "$HERE/tmproot.sh"
ROOT="$HERE/.."

pass=0; fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" || notok "$1" "contains '$3'" "$2"; }

SCRIPT="$ROOT/scripts/next-version.sh"

# A throwaway repository with the script copied in and an identity set.
# Prints the directory.
mkcase() {
  local d
  d="$(mktemp -d)"
  git -C "$d" init -q -b main
  git -C "$d" config user.email crossrev@example.test
  git -C "$d" config user.name "CrossRev Test"
  mkdir -p "$d/scripts"
  cp "$SCRIPT" "$d/scripts/next-version.sh"
  printf '%s' "$d"
}

commit() { # dir file content subject
  printf '%s\n' "$3" >"$1/$2"
  git -C "$1" add -A
  git -C "$1" commit -q -m "$4"
}

# --- 1. the release tag sits on a squashed-away side commit ---
sq="$(mkcase)"
commit "$sq" VERSION "0.9.0" "chore(release): cut 0.9.0"
git -C "$sq" tag v0.9.0
commit "$sq" feat.txt "a feature" "feat: something that shipped in 0.10.0"
side_parent="$(git -C "$sq" rev-parse HEAD)"
commit "$sq" VERSION "0.10.0" "chore(release): cut 0.10.0 (#321)"
squash_tree="$(git -C "$sq" rev-parse 'HEAD^{tree}')"
# The tag points at the pull request's commit, which main never merged: same
# tree as the squash, but a different commit on no branch. The message differs
# the way a pull request title differs from its squash, so the two commits
# hash differently instead of collapsing into one object.
side_commit="$(git -C "$sq" commit-tree "$squash_tree" -p "$side_parent" -m "chore(release): cut 0.10.0")"
git -C "$sq" tag v0.10.0 "$side_commit"
commit "$sq" fix.txt "a fix" "fix: something after the release"
sq_out="$(bash "$sq/scripts/next-version.sh" 2>&1)"
has "squash-merged release: measures since the VERSION tag" "$sq_out" "since v0.10.0"
has "squash-merged release: fixes only mean a patch" "$sq_out" "0.10.1   (patch"

# --- 2. the release tag sits on the main line ---
ml="$(mkcase)"
commit "$ml" VERSION "0.9.0" "chore(release): cut 0.9.0"
git -C "$ml" tag v0.9.0
commit "$ml" feat.txt "a feature" "feat: something that shipped in 0.10.0"
commit "$ml" VERSION "0.10.0" "chore(release): cut 0.10.0"
git -C "$ml" tag v0.10.0
git -C "$ml" tag v0
commit "$ml" fix.txt "a fix" "fix: something after the release"
ml_out="$(bash "$ml/scripts/next-version.sh" 2>&1)"
has "main-line release: measures since the VERSION tag" "$ml_out" "since v0.10.0"
has "main-line release: fixes only mean a patch" "$ml_out" "0.10.1   (patch"

# --- 3. no tag matches VERSION: nearest reachable version tag, as before ---
fb="$(mkcase)"
commit "$fb" VERSION "0.9.0" "chore(release): cut 0.9.0"
git -C "$fb" tag v0.9.0
commit "$fb" feat.txt "a feature" "feat: something landed"
printf '0.10.0\n' >"$fb/VERSION"
fb_out="$(bash "$fb/scripts/next-version.sh" 2>&1)"
has "missing VERSION tag: falls back to the reachable tag" "$fb_out" "since v0.9.0"
has "missing VERSION tag: the feat means a minor" "$fb_out" "(minor"

# --- 4. the floating v0 tag is never the measuring point ---
fl="$(mkcase)"
commit "$fl" VERSION "0.9.0" "chore(release): cut 0.9.0"
git -C "$fl" tag v0.9.0
commit "$fl" fix.txt "a fix" "fix: something after the release"
git -C "$fl" tag v0
printf '0.10.0\n' >"$fl/VERSION"
fl_out="$(bash "$fl/scripts/next-version.sh" 2>&1)"
has "floating v0: measures since the version tag, not v0" "$fl_out" "since v0.9.0"
has "floating v0: fixes only mean a patch" "$fl_out" "0.10.1   (patch"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
