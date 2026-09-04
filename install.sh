#!/usr/bin/env bash
#
# install.sh — put crossrev on your PATH.
#
# PATH is all this owns, permanently. Skills are installed by the `skills` CLI,
# which already knows 76 agents, offers project or global scope, symlinks rather
# than copies, and prints an itemised summary before acting. Reimplementing a
# fraction of that in bash would be strictly worse and would drift from a tool
# someone else maintains.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ ! -f "$HERE/lib/ui.sh" ]]; then
  echo "error  install.sh must run from a checkout of the repository." >&2
  echo "       Clone it first, then run install.sh from there." >&2
  exit 1
fi

if [[ ! -f "$HERE/scripts/build-native.sh" ]]; then
  echo "error  install.sh builds the binary with scripts/build-native.sh, which is missing." >&2
  echo "       Either that is not a CrossRev source tree, or the ref you cloned predates it." >&2
  exit 1
fi

command -v go >/dev/null 2>&1 || {
  echo "error  install.sh builds CrossRev from source, and go is not installed." >&2
  echo "       Install Go 1.21 or newer (go.mod fetches the pinned toolchain itself), then run this again." >&2
  exit 1
}

# shellcheck source=lib/ui.sh
source "$HERE/lib/ui.sh"
# shellcheck source=lib/preflight.sh
source "$HERE/lib/preflight.sh"

BIN_DIR="${CROSSREV_BIN_DIR:-$HOME/.local/bin}"
# empty = ask, 1 = install them, 0 = do not
WANT_SKILLS=""

while (( $# )); do
  case "$1" in
    # ui_confirm reads this, so exporting it is the whole of --yes.
    --yes|-y) export CROSSREV_ASSUME_YES=1; shift ;;
    --bin-dir) BIN_DIR="${2:?--bin-dir needs a path}"; shift 2 ;;
    --skills)    WANT_SKILLS=1; shift ;;
    --no-skills) WANT_SKILLS=0; shift ;;
    --help|-h)
      echo "usage: install.sh [--yes] [--bin-dir <dir>] [--skills | --no-skills]"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
done

printf '\n  %scrossrev%s\n' "$(tput bold 2>/dev/null || true)" "$(tput sgr0 2>/dev/null || true)"

ui_section "Source"
ui_line "$HERE"
ui_line "version $(tr -d '[:space:]' <"$HERE/VERSION")"

# Report requirements but do not refuse to install on a missing harness — you
# might be installing on a machine before setting the harnesses up. `doctor`
# and the legs themselves are where a missing dependency becomes fatal.
preflight_check harness || true

target="$BIN_DIR/crossrev"

# Build first, into a private directory, so a failed build leaves the
# installed copy alone rather than replacing it with half a binary.
build_tmp="$(mktemp -d)"
trap 'rm -rf "$build_tmp"' EXIT
bash "$HERE/scripts/build-native.sh" "$build_tmp/crossrev" || {
  echo "error  the build failed, and nothing was installed." >&2
  exit 1
}

replaced=""
if [[ -e "$target" || -L "$target" ]]; then
  if [[ -f "$target" ]] && ! [[ -L "$target" ]] && cmp -s "$build_tmp/crossrev" "$target"; then
    :  # already this binary, nothing to ask
  else
    existing="$(readlink "$target" 2>/dev/null || echo "$target")"
    ui_warn "$target already exists and is not this build" \
      "Continuing will replace it. It currently points at: $existing"
    ui_confirm "Replace it?" || { ui_say "Left it alone. Nothing was installed."; exit 1; }
    replaced="$existing"
  fi
fi

mkdir -p "$BIN_DIR"
# Copy rather than symlink, so the installed tool keeps working after the
# checkout moves on: the binary carries everything it needs. Rebuild and
# re-run this script to pick up a newer checkout.
#
# Remove first rather than copying over: every install the old script made is
# a symlink, and cp follows one, writing the binary through the link onto the
# checkout's own entrypoint instead of replacing the link.
rm -f "$target"
cp -f "$build_tmp/crossrev" "$target"
chmod +x "$target"
trap - EXIT
rm -rf "$build_tmp"

ui_section "Installed"
# Rule 5: verify rather than assert. A symlink that resolves to nothing runs as
# "command not found" later, a long way from here.
if [[ -x "$target" ]]; then
  ui_ok "$target"
  # Say what moved, not just what landed. The prompt above is easy to accept and
  # easier to skip with --yes, and the failure it leads to is silent: `crossrev`
  # keeps working while running a different build than the one you think, so
  # the next `git pull` in the old checkout changes nothing and there is no
  # error to explain why.
  if [[ -n "$replaced" ]]; then
    ui_line "   replaced $replaced"
    ui_line "   \`crossrev\` now runs the build from $HERE"
  fi
else
  ui_no "$target — created, but does not resolve to an executable"
  exit 1
fi

if ! command -v crossrev >/dev/null 2>&1; then
  ui_gap
  ui_line "$BIN_DIR is not on your PATH, so typing \`crossrev\` will not find it."
  ui_line "Add this to your shell profile:"
  ui_line "  export PATH=\"$BIN_DIR:\$PATH\""
fi

# ---------------------------------------------------------------------------
# The two skills — offered, not printed, and not forced
# ---------------------------------------------------------------------------
#
# Printing a command for someone to copy is the thing this project argues
# against everywhere else, so this offers to run it. It stays an offer rather
# than becoming part of the install for two reasons that are not politeness.
#
# The loop does not need them. crossrev reads both skills out of this checkout
# and reproduces their text into each prompt, so installing them is for using
# them by hand in an ordinary session. Installing something unneeded by default
# is worse than asking.
#
# And this is the only step that wants Node. Everything above runs with git,
# bash and coreutils; making the whole install depend on npx for an optional
# extra would be a poor trade.
#
# The source is this directory. Root skills/ holds exactly pr-review and
# pr-resolve, so naming them with --skill would select everything the CLI would
# have found anyway — a filter that can only go stale.
#
# Hand off, rather than drive.
#
# The skills CLI runs its own flow for a human: it detects which harnesses are
# installed, asks about project versus global scope, and asks whether to symlink
# or copy. That flow is better than anything decided here, and it is the flow
# someone gets running the command by hand — so an installer that suppressed it
# with --yes would be quietly making three choices on their behalf and calling it
# convenience.
#
# It also detects when an *agent* is driving it and goes non-interactive by
# itself, which is why the scripted branch is a real branch rather than
# defensiveness: in that mode nobody is asked, and its default scope is project —
# meaning the clone this script runs from. Present in the repository you were
# only installing from, absent everywhere you work, silent about the difference.
# So the scripted path names the scope and the interactive one does not.
#
# $1 is "interactive" or "scripted".
_install_skills() {
  if [[ "$1" == "interactive" ]]; then
    npx skills@latest add "$HERE"
  else
    npx skills@latest add "$HERE" --global --yes
  fi
}

ui_gap
if [[ "$WANT_SKILLS" == "0" ]]; then
  ui_line "Skipped the skills (--no-skills). The loop does not need them:"
  ui_line "crossrev reproduces both into each prompt from this checkout."
elif ! command -v npx >/dev/null 2>&1; then
  ui_line "npx is not installed, so the two skills were not offered. Nothing is"
  ui_line "missing — crossrev reproduces both into each prompt from this checkout."
  ui_line "Install Node if you want them available by hand, then run:"
  ui_line "  npx skills@latest add $HERE --global"
elif [[ "$WANT_SKILLS" != "1" ]] && ! _ui_input_source >/dev/null 2>&1; then
  # No terminal to ask at — a script, a CI step, a container with no controlling
  # terminal. Skip and say so. Dying here would fail an install that has already
  # succeeded, over an optional extra nobody was asked about.
  ui_line "No terminal attached, so the two optional skills were not offered."
  ui_line "The loop is unaffected. Add them with --skills, or:"
  ui_line "  npx skills@latest add $HERE --global"
else
  ui_line "pr-review and pr-resolve can also be installed for your harnesses, so"
  ui_line "you can invoke them by hand outside the loop. The loop itself does not"
  ui_line "need them — it reproduces both into each prompt from this checkout."
  ui_line ""
  ui_line "The skills CLI takes it from here: it detects which harnesses you have"
  ui_line "and asks where to put them and whether to symlink. Its questions, not"
  ui_line "crossrev's."
  printf '\n'
  if [[ "$WANT_SKILLS" == "1" ]] || ui_confirm "Hand over to the skills CLI now?"; then
    # Interactive unless install.sh was itself told not to ask. --yes here means
    # "do not ask ME whether to install them", not "answer the skills CLI's
    # questions for someone" — those are different permissions and only the
    # scripted path has the second.
    mode=interactive
    [[ "${CROSSREV_ASSUME_YES:-0}" == "1" ]] && mode=scripted
    if _install_skills "$mode"; then
      ui_ok "the skills CLI finished"
    else
      ui_warn "the skills CLI did not finish" \
        "Nothing else is affected — the loop reads both skills out of this checkout regardless. Retry with: npx skills@latest add $HERE"
    fi
  else
    ui_say "Left them out. Add them later with --skills, or:"
    ui_say "  npx skills@latest add $HERE"
    # The remote shorthand needs no checkout at all, which is the one thing the
    # line above cannot do. It is a hint for elsewhere, not what this run used.
    ui_say "  npx skills@latest add carlosboeing/crossrev   # from anywhere"
  fi
fi
ui_end "Then check everything:   crossrev doctor"
