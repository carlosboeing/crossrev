# shellcheck shell=bash
# lib/ui.sh — output voice.
#
# The design sets six rules for everything crossrev prints. They live here rather
# than in each caller's memory:
#
#   1. Name the thing            — "created 5 labels on your-org/website", not "labels"
#   2. Give the reason           — nobody knows why labels matter until you say so
#   3. Warnings state the consequence, not the condition
#   4. Errors state the next action
#   5. Never report success for something unverified
#   6. Explain before acting outward
#
# Rules 1-2 are the caller's job. Rules 3-6 are shaped by the helpers below:
# warn() and die() both take a second argument, and it is not optional.

# Colour is off when stdout is not a terminal, and opt-out via NO_COLOR
# (https://no-color.org). Both matter: this runs in CI as often as in a shell.
if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
  _c_reset=$'\033[0m'; _c_dim=$'\033[2m'; _c_bold=$'\033[1m'
  _c_red=$'\033[31m'; _c_yellow=$'\033[33m'; _c_green=$'\033[32m'; _c_blue=$'\033[34m'
else
  _c_reset=''; _c_dim=''; _c_bold=''
  _c_red=''; _c_yellow=''; _c_green=''; _c_blue=''
fi

# Section heading. Opens a block; body lines are prefixed with a rule.
ui_section() { printf '\n%s◇  %s%s\n' "$_c_blue" "$1" "$_c_reset"; }

# Section heading whose subject carries a state word — "#3 — converged".
#
# The word answers the first question before the reader parses anything else,
# which is the whole job when they are checking a dozen pull requests in a row.
# $3 picks the colour: ok, bad, warn, or anything else for the neutral blue.
# $4 is an optional dim qualifier, such as "(retried once)".
ui_section_state() {
  local title="$1" word="$2" kind="$3" note="${4:-}" c
  case "$kind" in
    ok)   c="$_c_green" ;;
    bad)  c="$_c_red" ;;
    warn) c="$_c_yellow" ;;
    *)    c="$_c_blue" ;;
  esac
  printf '\n%s◇  %s%s — %s%s%s' "$_c_blue" "$title" "$_c_reset" "$c" "$word" "$_c_reset"
  [[ -n "$note" ]] && printf ' %s%s%s' "$_c_yellow" "$note" "$_c_reset"
  printf '\n'
}

# A heading inside a section, grouping the lines under it.
ui_head() { printf '%s│%s  %s%s%s\n' "$_c_dim" "$_c_reset" "$_c_bold" "$1" "$_c_reset"; }

# A leg line, with a gutter before the glyph so the pass number sits to its left.
# $2 is ok, no, run, or anything else for the dim circle.
#
# `run` is a leg that is working right now, and it is deliberately neither of the
# other two: a tick would read as finished and a cross as failed, and it is
# neither. Blue rather than green for the same reason — the outcome is not in
# yet, and only the two settled glyphs get a verdict colour.
ui_row() {
  local gutter="$1" kind="$2" text="$3" glyph
  case "$kind" in
    ok)  glyph="${_c_green}✓${_c_reset}" ;;
    no)  glyph="${_c_red}✗${_c_reset}" ;;
    run) glyph="${_c_blue}◐${_c_reset}" ;;
    *)   glyph="${_c_dim}○${_c_reset}" ;;
  esac
  printf '%s│%s  %s%s %s\n' "$_c_dim" "$_c_reset" "$gutter" "$glyph" "$text"
}

# A command the reader can type. No glyph: under a NEXT heading the arrow is
# redundant, and the line reads as something to copy rather than as narration.
ui_cmd() { printf '%s│%s  %s%s%s\n' "$_c_dim" "$_c_reset" "$_c_blue" "$1" "$_c_reset"; }

# Body line inside a section.
ui_line() { printf '%s│%s  %s\n' "$_c_dim" "$_c_reset" "$1"; }

# Blank rule, for spacing inside a section.
ui_gap() { printf '%s│%s\n' "$_c_dim" "$_c_reset"; }

# Terminal line, closing a block.
ui_end() { printf '%s└%s  %s\n\n' "$_c_dim" "$_c_reset" "$1"; }

# Verified success. Only call this once the thing is actually true — rule 5.
ui_ok() { printf '%s│%s  %s✓%s %s\n' "$_c_dim" "$_c_reset" "$_c_green" "$_c_reset" "$1"; }

# A thing that is absent or failed.
ui_no() { printf '%s│%s  %s✗%s %s\n' "$_c_dim" "$_c_reset" "$_c_red" "$_c_reset" "$1"; }

# A thing that is absent but optional.
ui_opt() { printf '%s│%s  %s○%s %s\n' "$_c_dim" "$_c_reset" "$_c_dim" "$_c_reset" "$1"; }

# Next action.
ui_next() { printf '%s│%s  %s→%s %s\n' "$_c_dim" "$_c_reset" "$_c_blue" "$_c_reset" "$1"; }

# Plain informational line outside a section.
ui_say() { printf '  %s\n' "$1"; }

# Warning. $1 is the condition, $2 is what it costs you — rule 3. Both required,
# because a warning that names a condition without its consequence is the thing
# this project keeps trying not to print.
ui_warn() {
  printf '\n%s⚠  %s%s\n' "$_c_yellow" "$1" "$_c_reset" >&2
  printf '   %s\n\n' "$2" >&2
}

# Error, then exit. $1 is what went wrong, $2 is what to do about it — rule 4.
ui_die() {
  printf '\n%serror%s  %s\n' "$_c_red" "$_c_reset" "$1" >&2
  printf '       %s\n\n' "$2" >&2
  exit 1
}

# Where to read an answer from.
#
# /dev/tty first, so prompting still works with the tool on the right-hand side
# of a pipe — `curl … | sh` is a supported install path and its stdin is the
# script. But /dev/tty is not always there: cron, a CI step, a container without
# a controlling terminal, and some editor-embedded shells all fail to open it.
# Falling back to stdin covers those, and having neither is worth a real message
# rather than bash's "Device not configured".
_ui_input_source() {
  if ( : </dev/tty ) 2>/dev/null; then printf '/dev/tty'
  elif [[ -t 0 ]]; then printf '/dev/stdin'
  else return 1
  fi
}

_ui_no_input() {
  ui_die "CrossRev needs to ask you something, but no terminal is attached" \
    "Run this in a terminal directly. Editor-embedded and captured shells often have no controlling terminal, which is what this is."
}

# Ask before an outward-facing action — rule 6. The caller explains first; this
# only collects the answer. Defaults to no, so a stray newline cannot approve
# something. Honours --yes via CROSSREV_ASSUME_YES.
ui_confirm() {
  if [[ "${CROSSREV_ASSUME_YES:-0}" == "1" ]]; then
    printf '%s◆  %s%s  yes (--yes)\n' "$_c_bold" "$1" "$_c_reset"
    return 0
  fi
  local reply src
  src="$(_ui_input_source)" || _ui_no_input
  printf '%s◆  %s%s  [y/N] ' "$_c_bold" "$1" "$_c_reset"
  read -r reply <"$src" || return 1
  [[ "$reply" =~ ^[Yy]([Ee][Ss])?$ ]]
}

# Read one value. Prompt goes to stderr so the value can be captured from stdout.
ui_prompt() {
  local reply src
  src="$(_ui_input_source)" || _ui_no_input
  printf '%s◆  %s%s › ' "$_c_bold" "$1" "$_c_reset" >&2
  read -r reply <"$src" || return 1
  printf '%s' "$reply"
}
