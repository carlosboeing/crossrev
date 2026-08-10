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

while (( $# )); do
  case "$1" in
    # ui_confirm reads this, so exporting it is the whole of --yes.
    --yes|-y) export REVLOOP_ASSUME_YES=1; shift ;;
    --bin-dir) BIN_DIR="${2:?--bin-dir needs a path}"; shift 2 ;;
    --help|-h)
      echo "usage: install.sh [--yes] [--bin-dir <dir>]"; exit 0 ;;
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

ui_gap
ui_line "revloop reads the two skills straight out of this checkout and reproduces"
ui_line "them into each prompt, so the loop already works. Install them only if you"
ui_line "want to invoke them by hand:"
ui_line ""
# Both details here were established by running the CLI, and both are easy to
# get wrong in a way that reports nothing rather than an error. The source must
# be this directory: from the repository root the CLI finds the standalone
# skills in skills/ and never walks into tools/revloop/skills/. And --skill
# takes ONE name per flag — a comma-separated list matches nothing and reports
# "No matching skills found" rather than complaining about the syntax.
ui_line "  npx skills@latest add $HERE \\"
ui_line "    --skill pr-review --skill pr-address"
ui_end "Then check everything:   revloop doctor"
