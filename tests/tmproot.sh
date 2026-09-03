# shellcheck shell=bash
# tests/tmproot.sh — one temp-file root per suite process, removed on exit.
#
# Sourced, not run. Twelve of the thirty suites source lib/*.sh directly and
# call shell functions, without tests/harness.sh's fixture layer — its stub
# wiring, fixture_repo, route tables — so the temp-directory mechanism lives
# here on its own, and both sides source it.
#
# One root for every temp file a suite creates, removed when the suite's
# process exits. mktemp is redefined as a shell function below so every later
# call, in this file's caller and in whatever it sources or execs, lands
# under this root without editing each call site.
#
# TMPDIR is not that mechanism: on macOS, /usr/bin/mktemp ignores an inherited
# TMPDIR (measured: `env TMPDIR="$L" mktemp -d` still prints a path under the
# system temp directory, not under $L), so only rewriting the command itself
# reaches every call. TMPDIR is exported alongside the root anyway, for a
# caller that composes its own path from TMPDIR before handing mktemp an
# explicit template — lib/auth.sh:626 does exactly that — since the wrapper
# passes an explicit template through unchanged and only TMPDIR then decides
# where it lands.
#
# Safe to source twice: a suite that sources this file directly and also
# sources tests/harness.sh (which sources it too) still gets one root and one
# trap, not two.
[[ -n "${TMPROOT:-}" ]] && return 0

TMPROOT="$(command mktemp -d)"; export TMPROOT
export TMPDIR="$TMPROOT"
_tmproot_cleanup() { rm -rf "$TMPROOT"; }
trap '_tmproot_cleanup' EXIT

# An argument carrying an explicit template (a run of X's) goes through
# unchanged; anything else — -d, -u, no argument at all — gets the template
# appended so the result lands under $TMPROOT. `export -f` so a bash child of
# the suite, including the real crossrev binary under test, inherits it.
mktemp() {
  local a has_template=0
  for a in "$@"; do
    case "$a" in *XXX*) has_template=1 ;; esac
  done
  if (( has_template )); then
    command mktemp "$@"
  else
    command mktemp "$@" "$TMPROOT/tmp.XXXXXXXXXX"
  fi
}
export -f mktemp
