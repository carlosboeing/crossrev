#!/usr/bin/env bash
#
# The commit convention the resolve prompt shows.
#
# Unit-level rather than through a leg, because the interesting cases are about
# a repository's own history — a long one, a short one, and one whose recent
# commits are crossrev's own. Building three fixtures with real histories through
# the leg harness would cost far more than it proves.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/ui.sh
source "$HERE/../lib/ui.sh"
# shellcheck source=../lib/sandbox.sh
source "$HERE/../lib/sandbox.sh"
# shellcheck source=../lib/prompt.sh
source "$HERE/../lib/prompt.sh"

pass=0 fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$(( pass + 1 )); }
notok() { printf '  FAIL  %s\n    want: %s\n    got:  %s\n' "$1" "$2" "$3"; fail=$(( fail + 1 )); }
has()   { case "$2" in *"$3"*) ok "$1" ;; *) notok "$1" "contains: $3" "$2" ;; esac; }
hasnt() { case "$2" in *"$3"*) notok "$1" "absent: $3" "$2" ;; *) ok "$1" ;; esac; }

MINE="crossrev@users.noreply.github.com"

# A repository whose commits are its own, in its own style.
repo="$(mktemp -d)"
git -C "$repo" init -q
git -C "$repo" config user.email "dev@example.com"
git -C "$repo" config user.name "Dev"
for s in \
  "feat(api): add the widget endpoint" \
  "fix(api): reject an empty payload" \
  "refactor(store): split the cache from the reader" \
  "test(api): cover the empty payload path" \
  "docs: describe the widget endpoint" \
  "chore(deps): bump the linter"
do
  git -C "$repo" commit -q --allow-empty -m "$s"
done
base="$(git -C "$repo" rev-parse HEAD)"

out="$(cd "$repo" && prompt_commit_convention "$base" "$MINE")"
has "a repository with history has its subjects shown" \
  "$out" "feat(api): add the widget endpoint"
has "and the most recent one too"                     "$out" "chore(deps): bump the linter"
has "the leg is told to match them"                   "$out" "Match what they do"
hasnt "and is not told to fall back"                  "$out" "too short to read a convention from"

# crossrev's own commits are excluded. Left in, the leg learns the generic
# subject this replaces and reproduces it — which is the whole defect.
git -C "$repo" -c user.email="$MINE" -c user.name=crossrev \
  commit -q --allow-empty -m "fix: resolve crossrev review findings (pass 1)"
git -C "$repo" -c user.email="$MINE" -c user.name=crossrev \
  commit -q --allow-empty -m "fix: resolve crossrev review findings (pass 2)"
base="$(git -C "$repo" rev-parse HEAD)"

out="$(cd "$repo" && prompt_commit_convention "$base" "$MINE")"
hasnt "crossrev's own commits are not shown back to it" \
  "$out" "resolve crossrev review findings"
has "while the repository's own subjects still are" \
  "$out" "feat(api): add the widget endpoint"

# Under five subjects is a coincidence rather than a convention.
short="$(mktemp -d)"
git -C "$short" init -q
git -C "$short" config user.email "dev@example.com"
git -C "$short" config user.name "Dev"
git -C "$short" commit -q --allow-empty -m "first"
git -C "$short" commit -q --allow-empty -m "second"
sbase="$(git -C "$short" rev-parse HEAD)"

out="$(cd "$short" && prompt_commit_convention "$sbase" "$MINE")"
has "a two-commit history asks for Conventional Commits" \
  "$out" "too short to read a convention from"
hasnt "and shows no subjects to read a pattern into"  "$out" "first"

# A commit template is commit-specific and small, so it belongs with the log.
printf '# type(scope): subject\n' >"$repo/.gitmessage"
git -C "$repo" add .gitmessage
git -C "$repo" commit -q -m "chore: add a commit template"
tbase="$(git -C "$repo" rev-parse HEAD)"
out="$(cd "$repo" && prompt_commit_convention "$tbase" "$MINE")"
has "a .gitmessage template is shown alongside"       "$out" "type(scope): subject"

# A pull request whose base could not be read gets no section at all, rather
# than an empty heading claiming the repository has no convention.
out="$(cd "$repo" && prompt_commit_convention "" "$MINE")"
is_empty() { [[ -z "$1" ]] && ok "$2" || notok "$2" "nothing" "$1"; }
is_empty "$out" "no base revision prints nothing rather than a bare heading"

rm -rf "$repo" "$short"
printf '\n  %s passed, %s failed\n' "$pass" "$fail"
(( fail == 0 ))
