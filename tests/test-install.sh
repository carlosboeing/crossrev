#!/usr/bin/env bash
# tests/test-install.sh — the skills hand-off and the built binary on PATH.
#
# scripts/install-local.sh builds the native binary from the checkout and
# copies it onto PATH. npx is never invoked: the assertions are about the
# command install-local.sh would run and the file it leaves behind, both of
# which are checkable offline. The install itself runs for real, once, into a
# throwaway BIN_DIR.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tmproot.sh
source "$HERE/tmproot.sh"
ROOT="$HERE/.."

pass=0; fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
# Both report a locator rather than the haystack. The haystack is the whole of
# install-local.sh, and dumping ~200 lines per failure buries the other results.
has()   { [[ "$2" == *"$3"* ]] && ok "$1" \
            || notok "$1" "install-local.sh contains '$3'" "no such string in install-local.sh"; }
hasnt() { [[ "$2" != *"$3"* ]] && ok "$1" \
            || notok "$1" "install-local.sh does not contain '$3'" \
                     "found at line(s) $(grep -nF -- "$3" "$ROOT/scripts/install-local.sh" | cut -d: -f1 | tr '\n' ' ')"; }

src="$(cat "$ROOT/scripts/install-local.sh")"

# The install source is the checkout it runs from: no network, built by the
# script the repository already uses, version-coupled by construction.
has  "install-local.sh builds with scripts/build-binary.sh" "$src" 'scripts/build-binary.sh'
# A copy, not a symlink: the installed tool keeps working after the checkout
# moves on, because the binary carries everything it needs.
hasnt "no symlink of a shell entrypoint survives" "$src" 'ln -sf'
hasnt "nothing points at the old entrypoint anymore" "$src" 'bin/crossrev'
# The local path stays the skills source: no network, and version-coupled to
# the checkout the binary was actually built from.
has  "the skills install uses the local checkout" "$src" 'npx skills@latest add "$HERE"'
# Root skills/ holds exactly the two skills, so the filters are noise that can
# silently match nothing.
hasnt "the --skill filters are gone" "$src" '--skill pr-review'
# The remote shorthand is for someone with no checkout, and it is a hint only.
has  "the printed hint names the public repo" "$src" 'carlosboeing/crossrev'
hasnt "nothing still points at the old home" "$src" 'claude-code-resources'
hasnt "no tools/ path survives" "$src" 'tools/'

# The install, run for real into a throwaway directory.
bin="$(mktemp -d)"
out="$(CROSSREV_BIN_DIR="$bin" bash "$ROOT/scripts/install-local.sh" --yes --no-skills 2>&1)" || true
[[ -f "$bin/crossrev" && ! -L "$bin/crossrev" ]] && ok "install-local.sh copies a regular file onto PATH" \
  || notok "install-local.sh copies a regular file onto PATH" "a regular file at $bin/crossrev" "$(ls -la "$bin")"
[[ -x "$bin/crossrev" ]] && ok "and the copy is executable" \
  || notok "and the copy is executable" "executable" "$(ls -la "$bin/crossrev")"
is_version="$("$bin/crossrev" version 2>/dev/null)"
want_version="$(tr -d '[:space:]' <"$ROOT/VERSION")"
# The installer marks its build, so the copy reports base-commit-dirty from
# the stamp Go recorded — read back off the binary rather than recomputed, so
# no assumption about what counts as dirty sits between the two.
stamp="$(go version -m "$bin/crossrev" 2>/dev/null)" || true
stamp_rev="$(grep 'vcs.revision=' <<<"$stamp" | cut -d= -f2 | cut -c1-7)" || true
stamp_mod="$(grep 'vcs.modified=' <<<"$stamp" | cut -d= -f2)" || true
if [[ -n "$stamp_rev" ]]; then
  want_version="$want_version-$stamp_rev"
  [[ "$stamp_mod" == "true" ]] && want_version="$want_version-dirty"
fi
[[ "$is_version" == "$want_version" ]] && ok "and the copy reports this checkout's marked version" \
  || notok "and the copy reports this checkout's marked version" "$want_version" "$is_version"
# And the stamp names this checkout's HEAD — Go's answer, checked against
# git's, so a recomputation here would prove nothing.
head_sha="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null | cut -c1-7)" || true
if [[ -z "$stamp_rev" || -z "$head_sha" ]]; then
  ok "and the marked commit is this checkout's HEAD (unstamped build, nothing to check)"
elif [[ "$stamp_rev" == "$head_sha" ]]; then
  ok "and the marked commit is this checkout's HEAD"
else
  notok "and the marked commit is this checkout's HEAD" "$head_sha" "$stamp_rev"
fi
case "$out" in
  *"$(cd "$ROOT" && pwd)"*) ok "the source it reports is this checkout" ;;
  *) notok "the source it reports is this checkout" "$(cd "$ROOT" && pwd)" "$out" ;;
esac

# skills/ really does hold exactly two, which is what makes dropping the
# filters safe rather than lucky.
n="$(find "$ROOT/skills" -maxdepth 2 -name SKILL.md | wc -l | tr -d ' ')"
[[ "$n" == "2" ]] && ok "skills/ holds exactly the two skills" \
  || notok "skills/ holds exactly the two skills" "2" "$n"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
