#!/usr/bin/env bash
# tests/test-bootstrap.sh — the entry script nothing tested until the extraction.
#
# find_checkout has three branches and two of them fail by returning a real
# directory that is not a checkout, so the failure surfaces at the exec guard
# blaming the wrong thing. That is why these are assertions rather than edits.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$HERE/.."

pass=0; fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }

# A directory shaped like a CrossRev checkout, without being one.
#
# The path is resolved with `cd -P` deliberately. On macOS `mktemp -d` hands back
# a path under /var, which is a symlink to /private/var — and find_checkout's
# symlink branch normalises with `cd -P` before returning. Comparing a physical
# path against a logical fixture would fail for a reason that has nothing to do
# with the code under test.
fake_checkout() {
  local d; d="$(cd -P "$(mktemp -d)" && pwd)"
  mkdir -p "$d/bin"
  printf '#!/usr/bin/env bash\n' >"$d/install.sh"; chmod +x "$d/install.sh"
  printf '#!/usr/bin/env bash\n' >"$d/bin/crossrev"; chmod +x "$d/bin/crossrev"
  printf '%s' "$d"
}

# An empty directory, normalised the same way, for the cases that assert a miss.
empty_dir() { (cd -P "$(mktemp -d)" && pwd); }

# The two functions, lifted out of bootstrap.sh.
#
# Sourcing the script whole would run the installer, so they are extracted
# instead. BOTH of them: find_checkout calls _is_checkout, and an extraction that
# took only the first would fail on "command not found" in every case — a
# green-looking red for the wrong reason.
#
# _is_checkout is matched as a single line, not as a /^}/ range. It is a
# one-liner with no closing brace of its own, so a range would run past it to
# find_checkout's closing brace and overlap the second range entirely — and sed
# emits a line once per matching command, so every overlapping line comes out
# twice and the eval gets syntactically broken bash.
extract_functions() {
  sed -n '/^_is_checkout() {/p; /^find_checkout() {/,/^}/p' "$ROOT/bootstrap.sh"
}

# find_checkout, run with the environment it reads.
#
# PATH is pinned rather than extended. find_checkout asks `command -v crossrev`,
# so an inherited PATH lets a real installed CrossRev answer — and that answer
# resolves back to a real checkout, which is a plausible-looking wrong result.
# The suite would pass on a machine with nothing installed and fail on the
# operator's own, which is the worst way for a test to be wrong. /usr/bin and
# /bin stay because find_checkout shells out to dirname and readlink.
#
# DEST and DEST_EXPLICIT are read by find_checkout, which arrives through the
# eval — shellcheck cannot see that far and reports both as unused.
# shellcheck disable=SC2034
run_find() {
  local pwd_dir="$1" dest="$2" explicit="$3" binpath="${4:-}"
  (
    cd "$pwd_dir" || exit 1
    DEST="$dest"; DEST_EXPLICIT="$explicit"
    PATH="${binpath:+$binpath:}/usr/bin:/bin"
    eval "$(extract_functions)" 2>/dev/null
    # A failed extraction returns nothing from every case, including the ones
    # asserting a miss — which reads as six wrong answers rather than one broken
    # harness. Say so instead.
    declare -F _is_checkout >/dev/null && declare -F find_checkout >/dev/null \
      || { printf '__extraction_failed__'; exit 0; }
    find_checkout || printf '__none__'
  )
}

# 1. Run from inside a checkout.
c="$(fake_checkout)"
is "run from inside a checkout returns that checkout" "$(run_find "$c" /nonexistent 0)" "$c"

# 2. An explicit --dir is the only place looked at.
c2="$(fake_checkout)"; other="$(fake_checkout)"
is "an explicit --dir that holds a checkout is used" "$(run_find "$other" "$c2" 1)" "$c2"
is "an explicit --dir that does not is a miss, not a search" \
  "$(run_find "$other" "$(empty_dir)" 1)" "__none__"

# 3. Where the script would have put it.
c3="$(fake_checkout)"
is "the default destination is checked last" "$(run_find "$(empty_dir)" "$c3" 0)" "$c3"

# 4. The installed-symlink walk — the site that is off by two in the old world.
c4="$(fake_checkout)"; bindir="$(empty_dir)"
ln -s "$c4/bin/crossrev" "$bindir/crossrev"
is "an installed symlink resolves back to its checkout" \
  "$(run_find "$(empty_dir)" /nonexistent 0 "$bindir")" "$c4"

# 5. A directory with install.sh but no bin/crossrev is not a checkout.
half="$(empty_dir)"; printf '#!/usr/bin/env bash\n' >"$half/install.sh"
is "install.sh alone is not enough to call it a checkout" \
  "$(run_find "$(empty_dir)" "$half" 0)" "__none__"

# 6. The hand-off target the exec guard checks.
grep -q 'exec "\$SRC/install.sh"' "$ROOT/bootstrap.sh" \
  && ok "the hand-off execs the root install.sh" \
  || notok "the hand-off execs the root install.sh" 'exec "$SRC/install.sh"' "$(grep -n 'exec ' "$ROOT/bootstrap.sh")"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
