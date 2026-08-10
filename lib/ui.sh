# shellcheck shell=bash
# lib/ui.sh — output voice.
#
# The design sets six rules for everything revloop prints. They live here rather
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
  ui_die "revloop needs to ask you something, but no terminal is attached" \
    "Run this in a terminal directly. Editor-embedded and captured shells often have no controlling terminal, which is what this is."
}

# Ask before an outward-facing action — rule 6. The caller explains first; this
# only collects the answer. Defaults to no, so a stray newline cannot approve
# something. Honours --yes via REVLOOP_ASSUME_YES.
ui_confirm() {
  if [[ "${REVLOOP_ASSUME_YES:-0}" == "1" ]]; then
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
