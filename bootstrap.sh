#!/usr/bin/env bash
#
# bootstrap.sh — one command, from nothing to a working `crossrev`.
#
# This is the only file meant to be fetched and run directly. Everything else
# assumes a checkout already exists; this is what produces one.
#
# **It is deliberately self-contained.** It cannot source lib/ui.sh, because the
# whole reason it is running is that lib/ui.sh is not on the machine yet. So it
# carries its own thirty lines of prompting and says less than the rest of the
# tool does. Anything it would have to explain twice belongs in install.sh, which
# runs immediately after it and has the real one.
#
# **It is fetched, not cloned.** The repository is public, so raw.githubusercontent
# serves this file anonymously — no token, no gh, no credential of any kind:
#
#   curl -fsSL https://raw.githubusercontent.com/carlosboeing/crossrev/main/bootstrap.sh | bash
#
# Everything below assumes nothing but bash, git and coreutils.
#
# **Every step it takes is optional.** Someone who already has a checkout, or who
# cloned it by hand, should not be made to clone again — so it looks for an
# existing one first and uses it. It is safe to re-run.

set -euo pipefail

REPO="${CROSSREV_REPO:-carlosboeing/crossrev}"
DEST="${CROSSREV_SRC_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/crossrev}"
# Empty means the repository's default branch. A tag or SHA here is how you get
# a reproducible install — "what did I install?" has an answer only if the ref
# was not a moving branch.
REF="${CROSSREV_REF:-}"
ASSUME_YES=0
DEST_EXPLICIT=0
INSTALL_ARGS=()

while (( $# )); do
  case "$1" in
    --dir)  DEST="${2:?--dir needs a path}"; DEST_EXPLICIT=1; shift 2 ;;
    --repo) REPO="${2:?--repo needs owner/name}"; shift 2 ;;
    --ref)  REF="${2:?--ref needs a branch, tag or SHA}"; shift 2 ;;
    --yes|-y) ASSUME_YES=1; INSTALL_ARGS+=(--yes); shift ;;
    --skills|--no-skills) INSTALL_ARGS+=("$1"); shift ;;
    --help|-h)
      echo "usage: bootstrap.sh [--dir <path>] [--repo owner/name] [--ref <tag|branch|sha>] [--yes] [--skills|--no-skills]"
      exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
done

# --- the minimum UI this file needs -----------------------------------------
#
# Reading from /dev/tty rather than stdin is not defensive here, it is required:
# fetched and piped into bash, this script IS stdin, so `read` would consume the
# rest of itself. With no terminal at all there is nobody to ask, and the answer
# is to proceed only when --yes said so in advance.

if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
  _b=$'\033[1m'; _d=$'\033[2m'; _r=$'\033[0m'; _g=$'\033[32m'; _y=$'\033[33m'
else
  _b=''; _d=''; _r=''; _g=''; _y=''
fi

say()  { printf '  %s\n' "$1"; }
ok()   { printf '  %s✓%s %s\n' "$_g" "$_r" "$1"; }
step() { printf '\n%s◇  %s%s\n' "$_b" "$1" "$_r"; }
die()  { printf '\n%serror%s  %s\n       %s\n\n' "$_y" "$_r" "$1" "$2" >&2; exit 1; }

ask() {
  (( ASSUME_YES )) && return 0
  [[ -r /dev/tty ]] || die "there is no terminal to ask at, and this would create $DEST" \
    "Re-run with --yes to accept, or --dir to choose somewhere else."
  local reply
  printf '%s◆  %s%s  [Y/n] ' "$_b" "$1" "$_r"
  read -r reply </dev/tty || return 1
  [[ -z "$reply" || "$reply" =~ ^[Yy] ]]
}

# --- is there already a checkout? -------------------------------------------
#
# Three places worth looking, cheapest first. Cloning over the top of a checkout
# someone already has is the rudest thing this script could do.

# Both files, not just install.sh. A directory carrying an install.sh and no
# bin/crossrev is some other project, and treating it as a checkout produces the
# silent failure this predicate exists to prevent: a real directory, returned
# confidently, that cannot run the tool.
_is_checkout() { [[ -f "$1/install.sh" && -f "$1/bin/crossrev" ]]; }

find_checkout() {
  local p

  # An explicit --dir is an instruction about where to install from, so it is the
  # only place looked at. Searching anyway and quietly using a checkout somewhere
  # else would be answering a different question than the one asked.
  if (( DEST_EXPLICIT )); then
    _is_checkout "$DEST" && { printf '%s' "$DEST"; return 0; }
    return 1
  fi

  # 1. Run from inside one. The tool is the repository root now, so $PWD is
  # already the answer — the old two-level walk out of tools/crossrev is gone.
  _is_checkout "$PWD" && { printf '%s' "$PWD"; return 0; }

  # 2. Already installed — follow the symlink back to its source. One level up
  # from bin/, not three: the checkout root holds bin/ directly.
  if p="$(command -v crossrev 2>/dev/null)"; then
    while [[ -L "$p" ]]; do
      local d; d="$(cd -P "$(dirname "$p")" && pwd)"
      p="$(readlink "$p")"; [[ "$p" != /* ]] && p="$d/$p"
    done
    p="$(cd -P "$(dirname "$p")/.." && pwd 2>/dev/null)" || p=""
    [[ -n "$p" ]] && _is_checkout "$p" && { printf '%s' "$p"; return 0; }
  fi

  # 3. Where this script would have put it.
  _is_checkout "$DEST" && { printf '%s' "$DEST"; return 0; }

  return 1
}

printf '\n  %scrossrev%s\n' "$_b" "$_r"

# --- get a checkout ----------------------------------------------------------

if SRC="$(find_checkout)"; then
  step "Source"
  ok "using the checkout already at $SRC"
  say "Nothing was cloned. Re-run with --dir to install from somewhere else."
else
  command -v git >/dev/null 2>&1 || die \
    "git is not installed, and crossrev runs from a checkout" \
    "Install git and run this again. On macOS: xcode-select --install"

  step "Clone"
  say "crossrev runs from a checkout rather than a copied binary, so this needs"
  say "somewhere to live. It stays there — deleting it uninstalls the tool."
  say ""
  say "  repository  $REPO"
  say "  ref         ${REF:-the default branch}"
  say "  into        $DEST"
  printf '\n'
  ask "Clone it there?" || die "nothing was cloned" \
    "Choose a different location with --dir <path>, or clone it yourself and re-run this from inside it."

  mkdir -p "$(dirname "$DEST")"
  # gh where it is available, because it already holds the credential for a
  # private repository and knows the right URL form. Plain git otherwise, which
  # is all a public repository needs — the same fallback that will make this work
  # for someone who has never installed gh.
  clone_args=(--quiet)
  [[ -n "$REF" ]] && clone_args+=(--branch "$REF")
  if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    gh repo clone "$REPO" "$DEST" -- "${clone_args[@]}" || die \
      "could not clone $REPO${REF:+ at $REF} with gh" \
      "Check you have access to it, and that the ref exists: gh repo view $REPO"
  else
    git clone "${clone_args[@]}" "https://github.com/$REPO.git" "$DEST" || die \
      "could not clone $REPO${REF:+ at $REF}" \
      "It is private, or the ref does not exist. Authenticate with gh and try again: gh auth login"
  fi
  SRC="$DEST"
  ok "cloned into $SRC"
fi

# --- hand over to the installer ---------------------------------------------
#
# install.sh owns the requirements check, the PATH entry and the skills offer,
# and it is the same script someone with a checkout runs by hand. Duplicating any
# of that here would be a second copy to keep in step.

[[ -x "$SRC/install.sh" ]] || die \
  "the checkout at $SRC has no install.sh" \
  "Either that is not a CrossRev source tree, or the ref you cloned predates it — try --ref with a tag that has it. Point --dir somewhere else, or delete $SRC and re-run."

exec "$SRC/install.sh" ${INSTALL_ARGS+"${INSTALL_ARGS[@]}"}
