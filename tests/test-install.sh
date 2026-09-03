#!/usr/bin/env bash
# tests/test-install.sh — the skills hand-off and the PATH symlink.
#
# npx is never invoked: the assertions are about the command install.sh would
# run and the link it leaves behind, both of which are checkable offline.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tmproot.sh
source "$HERE/tmproot.sh"
ROOT="$HERE/.."

pass=0; fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
# Both report a locator rather than the haystack. The haystack is the whole of
# install.sh, and dumping ~190 lines per failure buries the six other results.
has()   { [[ "$2" == *"$3"* ]] && ok "$1" \
            || notok "$1" "install.sh contains '$3'" "no such string in install.sh"; }
hasnt() { [[ "$2" != *"$3"* ]] && ok "$1" \
            || notok "$1" "install.sh does not contain '$3'" \
                     "found at line(s) $(grep -nF -- "$3" "$ROOT/install.sh" | cut -d: -f1 | tr '\n' ' ')"; }

src="$(cat "$ROOT/install.sh")"

# The local path stays the install source: no network, and version-coupled to
# the checkout the binary actually runs from.
has  "the skills install uses the local checkout" "$src" 'npx skills@latest add "$HERE"'
# Root skills/ holds exactly the two skills, so the filters are noise that can
# silently match nothing.
hasnt "the --skill filters are gone" "$src" '--skill pr-review'
# The remote shorthand is for someone with no checkout, and it is a hint only.
has  "the printed hint names the public repo" "$src" 'carlosboeing/crossrev'
hasnt "nothing still points at the old home" "$src" 'claude-code-resources'
hasnt "no tools/ path survives" "$src" 'tools/'

# The PATH symlink: created, and resolving to something executable.
bin="$(mktemp -d)"
out="$(CROSSREV_BIN_DIR="$bin" bash "$ROOT/install.sh" --yes --no-skills 2>&1)" || true
[[ -L "$bin/crossrev" ]] && ok "install.sh symlinks bin/crossrev onto PATH" \
  || notok "install.sh symlinks bin/crossrev onto PATH" "a symlink at $bin/crossrev" "$(ls -la "$bin")"
[[ -x "$bin/crossrev" ]] && ok "and the link resolves to an executable" \
  || notok "and the link resolves to an executable" "executable" "$(readlink "$bin/crossrev")"
has "the source it reports is this checkout" "$out" "$(cd "$ROOT" && pwd)"

# skills/ really does hold exactly two, which is what makes dropping the
# filters safe rather than lucky.
n="$(find "$ROOT/skills" -maxdepth 2 -name SKILL.md | wc -l | tr -d ' ')"
[[ "$n" == "2" ]] && ok "skills/ holds exactly the two skills" \
  || notok "skills/ holds exactly the two skills" "2" "$n"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
