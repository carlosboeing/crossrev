#!/usr/bin/env bash
#
# install.sh — put revloop on your PATH.
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
  echo "       Clone it first, then run tools/revloop/install.sh from there." >&2
  exit 1
fi

# shellcheck source=lib/ui.sh
source "$HERE/lib/ui.sh"
# shellcheck source=lib/preflight.sh
source "$HERE/lib/preflight.sh"

BIN_DIR="${REVLOOP_BIN_DIR:-$HOME/.local/bin}"
# empty = ask, 1 = install them, 0 = do not
WANT_SKILLS=""

while (( $# )); do
  case "$1" in
    # ui_confirm reads this, so exporting it is the whole of --yes.
    --yes|-y) export REVLOOP_ASSUME_YES=1; shift ;;
    --bin-dir) BIN_DIR="${2:?--bin-dir needs a path}"; shift 2 ;;
    --skills)    WANT_SKILLS=1; shift ;;
    --no-skills) WANT_SKILLS=0; shift ;;
    --help|-h)
      echo "usage: install.sh [--yes] [--bin-dir <dir>] [--skills | --no-skills]"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
done

printf '\n  %srevloop%s\n' "$(tput bold 2>/dev/null || true)" "$(tput sgr0 2>/dev/null || true)"

ui_section "Source"
ui_line "$HERE"
ui_line "version $(tr -d '[:space:]' <"$HERE/VERSION")"

# Report requirements but do not refuse to install on a missing harness — you
# might be installing on a machine before setting the harnesses up. `doctor`
# and the legs themselves are where a missing dependency becomes fatal.
preflight_check harness || true

target="$BIN_DIR/revloop"
if [[ -e "$target" || -L "$target" ]]; then
  existing="$(readlink "$target" 2>/dev/null || echo "$target")"
  if [[ "$existing" == "$HERE/bin/revloop" ]]; then
    :  # already pointing at this checkout, nothing to ask
  else
    ui_warn "$target already exists and points somewhere else" \
      "Continuing will replace it. It currently points at: $existing"
    ui_confirm "Replace it?" || { ui_say "Left it alone. Nothing was installed."; exit 1; }
  fi
fi

mkdir -p "$BIN_DIR"
# Symlink rather than copy, so editing the checkout takes effect immediately and
# there is one source of truth. Same conclusion the skills CLI reached.
ln -sf "$HERE/bin/revloop" "$target"

ui_section "Installed"
# Rule 5: verify rather than assert. A symlink that resolves to nothing runs as
# "command not found" later, a long way from here.
if [[ -x "$target" ]]; then
  ui_ok "$target"
else
  ui_no "$target — created, but does not resolve to an executable"
  exit 1
fi

if ! command -v revloop >/dev/null 2>&1; then
  ui_gap
  ui_line "$BIN_DIR is not on your PATH, so typing \`revloop\` will not find it."
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
# The loop does not need them. revloop reads both skills out of this checkout
# and reproduces their text into each prompt, so installing them is for using
# them by hand in an ordinary session. Installing something unneeded by default
# is worse than asking.
#
# And this is the only step that wants Node. Everything above runs with git,
# bash and coreutils; making the whole install depend on npx for an optional
# extra would be a poor trade.
#
# Two details in the command were established by running the CLI, and both fail
# by reporting nothing rather than erroring. The source must be THIS directory:
# from the repository root the CLI finds the standalone skills in skills/ and
# never walks into tools/revloop/skills/. And --skill takes one name per flag —
# a comma-separated list matches nothing and says "No matching skills found"
# rather than complaining about the syntax.
_install_skills() {
  # --global explicitly. Without it the CLI installs project-level, and since
  # this script runs from inside the clone, that means into the clone: present
  # for the repository you were only installing from, absent everywhere you
  # actually work, and silent about the difference.
  npx skills@latest add "$HERE" \
    --skill pr-review --skill pr-address \
    --global --yes
}

ui_gap
if [[ "$WANT_SKILLS" == "0" ]]; then
  ui_line "Skipped the skills (--no-skills). The loop does not need them:"
  ui_line "revloop reproduces both into each prompt from this checkout."
elif ! command -v npx >/dev/null 2>&1; then
  ui_line "npx is not installed, so the two skills were not offered. Nothing is"
  ui_line "missing — revloop reproduces both into each prompt from this checkout."
  ui_line "Install Node if you want them available by hand, then run:"
  ui_line "  npx skills@latest add $HERE --skill pr-review --skill pr-address --global"
elif [[ "$WANT_SKILLS" != "1" ]] && ! _ui_input_source >/dev/null 2>&1; then
  # No terminal to ask at — a script, a CI step, a container with no controlling
  # terminal. Skip and say so. Dying here would fail an install that has already
  # succeeded, over an optional extra nobody was asked about.
  ui_line "No terminal attached, so the two optional skills were not offered."
  ui_line "The loop is unaffected. Add them with --skills, or:"
  ui_line "  npx skills@latest add $HERE --skill pr-review --skill pr-address --global"
else
  ui_line "pr-review and pr-address can also be installed for your harnesses, so"
  ui_line "you can invoke them by hand outside the loop. The loop itself does not"
  ui_line "need them — it reproduces both into each prompt from this checkout."
  ui_line ""
  ui_line "This installs them globally, at user level, for the agents the skills"
  ui_line "CLI finds. It symlinks rather than copies, so this checkout stays the"
  ui_line "one source of truth."
  printf '\n'
  if [[ "$WANT_SKILLS" == "1" ]] || ui_confirm "Install the two skills now?"; then
    if _install_skills; then
      ui_ok "installed pr-review and pr-address"
    else
      ui_warn "the skills CLI did not finish" \
        "Nothing else is affected — the loop reads both skills out of this checkout regardless. Retry with: npx skills@latest add $HERE --skill pr-review --skill pr-address --global"
    fi
  else
    ui_say "Left them out. Add them later with --skills, or:"
    ui_say "  npx skills@latest add $HERE --skill pr-review --skill pr-address --global"
  fi
fi
ui_end "Then check everything:   revloop doctor"
