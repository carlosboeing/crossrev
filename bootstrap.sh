#!/usr/bin/env bash
#
# bootstrap.sh — one command, from nothing to a working `revloop`.
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
# **How it gets here does not change what it does.** Today the repository is
# private, so it is fetched with `gh api`, which uses the authentication revloop
# already requires rather than a credential anyone has to be given:
#
#   gh api "repos/carlosboeing/claude-code-resources/contents/tools/revloop/bootstrap.sh" \
#     -H "Accept: application/vnd.github.raw" | bash
#
# When the repository is public, raw.githubusercontent.com starts serving it
# anonymously and the incantation becomes a plain curl with no token and no gh:
#
#   curl -fsSL https://raw.githubusercontent.com/<owner>/<repo>/main/tools/revloop/bootstrap.sh | bash
#
# Nothing in this file changes between those two. It is a script fetched by
# different means, not two installers — which is the point: single-shot now, and
# still single-shot the day the repository goes public.
#
# **Every step it takes is optional.** Someone who already has a checkout, or who
# cloned it by hand, should not be made to clone again — so it looks for an
# existing one first and uses it. It is safe to re-run.

set -euo pipefail

REPO="${REVLOOP_REPO:-carlosboeing/claude-code-resources}"
# Named for the tool rather than the repository it currently lives in, so the
# path survives the extraction into a repository of its own.
DEST="${REVLOOP_SRC_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/revloop}"
# Empty means the repository's default branch. A tag or SHA here is how you get
# a reproducible install — "what did I install?" has an answer only if the ref
# was not a moving branch.
REF="${REVLOOP_REF:-}"
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

find_checkout() {
  local p

  # An explicit --dir is an instruction about where to install from, so it is the
  # only place looked at. Searching anyway and quietly using a checkout somewhere
  # else would be answering a different question than the one asked — and the
  # message would still say "using the checkout at ...", making it look deliberate.
  if (( DEST_EXPLICIT )); then
    [[ -f "$DEST/tools/revloop/install.sh" ]] && { printf '%s' "$DEST"; return 0; }
    return 1
  fi

  # 1. Run from inside one. Normalised, because "$PWD/../.." is a correct path
  # that reads like a bug in every message that prints it afterwards.
  if [[ -f "$PWD/tools/revloop/install.sh" ]]; then printf '%s' "$PWD"; return 0; fi
  if [[ -f "$PWD/install.sh" && -f "$PWD/bin/revloop" ]]; then
    ( cd -P "$PWD/../.." && pwd ); return 0
  fi

  # 2. Already installed — follow the symlink back to its source.
  if p="$(command -v revloop 2>/dev/null)"; then
    while [[ -L "$p" ]]; do
      local d; d="$(cd -P "$(dirname "$p")" && pwd)"
      p="$(readlink "$p")"; [[ "$p" != /* ]] && p="$d/$p"
    done
    p="$(cd -P "$(dirname "$p")/../../.." && pwd 2>/dev/null)" || p=""
    [[ -n "$p" && -f "$p/tools/revloop/install.sh" ]] && { printf '%s' "$p"; return 0; }
  fi

  # 3. Where this script would have put it.
  [[ -f "$DEST/tools/revloop/install.sh" ]] && { printf '%s' "$DEST"; return 0; }

  return 1
}

printf '\n  %srevloop%s\n' "$_b" "$_r"

# --- get a checkout ----------------------------------------------------------

if SRC="$(find_checkout)"; then
  step "Source"
  ok "using the checkout already at $SRC"
  say "Nothing was cloned. Re-run with --dir to install from somewhere else."
else
  command -v git >/dev/null 2>&1 || die \
    "git is not installed, and revloop runs from a checkout" \
    "Install git and run this again. On macOS: xcode-select --install"

  step "Clone"
  say "revloop runs from a checkout rather than a copied binary, so this needs"
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

[[ -x "$SRC/tools/revloop/install.sh" ]] || die \
  "the checkout at $SRC has no tools/revloop/install.sh" \
  "Either that is not a revloop source tree, or the ref you cloned predates it — try --ref with a tag that has it. Point --dir somewhere else, or delete $SRC and re-run."

exec "$SRC/tools/revloop/install.sh" ${INSTALL_ARGS+"${INSTALL_ARGS[@]}"}
