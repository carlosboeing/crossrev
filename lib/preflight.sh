# shellcheck shell=bash
# lib/preflight.sh — dependency checks.
#
# Every message names what is missing AND how to install it, because an error
# that names a problem without naming a fix has done half its job.

# How to install a given tool, phrased for the platform we are actually on.
_install_hint() {
  local tool="$1"
  case "$(uname -s)" in
    Darwin)
      case "$tool" in
        gh)     echo "brew install gh" ;;
        jq)     echo "brew install jq" ;;
        yq)     echo "brew install yq" ;;
        git)    echo "xcode-select --install" ;;
        claude) echo "https://claude.com/claude-code" ;;
        codex)  echo "npm install -g @openai/codex" ;;
        agy)    echo "https://antigravity.google" ;;
        kimi)   echo "https://github.com/MoonshotAI/kimi-code" ;;
        *)      echo "install $tool" ;;
      esac ;;
    *)
      case "$tool" in
        gh)     echo "https://github.com/cli/cli#installation" ;;
        jq)     echo "https://jqlang.github.io/jq/download/" ;;
        yq)     echo "https://github.com/mikefarah/yq#install" ;;
        git)    echo "your package manager, e.g. apt install git" ;;
        claude) echo "https://claude.com/claude-code" ;;
        codex)  echo "npm install -g @openai/codex" ;;
        agy)    echo "https://antigravity.google" ;;
        kimi)   echo "https://github.com/MoonshotAI/kimi-code" ;;
        *)      echo "install $tool" ;;
      esac ;;
  esac
}

# "<tool> <version>", or non-zero if the tool is not installed.
#
# Every CLI reports itself differently — "git version 2.50.1 (Apple Git-155)",
# "jq-1.8.1", "codex-cli 0.147.0", and claude and kimi print a bare number with
# no name at all. Pulling out the first version-shaped token and prefixing the
# tool name gives one readable format, and satisfies the first output rule:
# name the thing.
_tool_version() {
  command -v "$1" >/dev/null 2>&1 || return 1
  local raw ver
  raw="$("$1" --version 2>&1 | head -1)"
  ver="$(printf '%s' "$raw" | grep -oE 'v?[0-9]+\.[0-9]+[0-9A-Za-z.+-]*' | head -1)"
  printf '%s %s' "$1" "${ver:-$raw}"
}

# Check the tools a given command actually needs.
#
# $1 is the requirement set:
#   core     git, gh (authenticated), jq, yq
#   harness  core plus at least one of claude, codex, kimi
#
# Prints a report and returns non-zero if anything required is missing, so the
# caller decides whether that is fatal. install.sh reports; a leg dies.
preflight_check() {
  local need="${1:-core}"
  local missing=()

  ui_section "Requirements"

  local t v
  for t in git gh jq yq; do
    if v="$(_tool_version "$t")"; then
      if [[ "$t" == "gh" ]]; then
        # Installed is not the same as usable. Rule 5: do not report success for
        # something unverified, and an unauthenticated gh fails at the first API
        # call rather than here.
        local who
        if who="$(gh api user --jq .login 2>/dev/null)"; then
          ui_ok "$v — authenticated as $who"
        else
          ui_no "gh — installed but not authenticated. Run: gh auth login"
          missing+=("gh-auth")
        fi
      else
        ui_ok "$v"
      fi
    else
      ui_no "$t — not found. Install with: $(_install_hint "$t")"
      missing+=("$t")
    fi
  done

  if [[ "$need" == "harness" ]]; then
    local found_harness=0
    # The three with adapters. Kimi is not on this list because it is not a
    # crossrev harness: it is reached through the claude adapter as a named
    # endpoint, so the `kimi` binary being present says nothing about whether
    # the loop can use it.
    for t in claude codex agy; do
      if v="$(_tool_version "$t")"; then
        ui_ok "$v"
        found_harness=1
      else
        ui_opt "$t — not found, optional"
      fi
    done
    if (( found_harness == 0 )); then
      ui_no "no harness CLI found — crossrev needs at least one of claude, codex or agy"
      missing+=("harness")
    fi
  fi

  (( ${#missing[@]} == 0 ))
}

# ---------------------------------------------------------------------------
# Which pairings the configured runner can actually serve
# ---------------------------------------------------------------------------
#
# Which harnesses are reachable in CI is a property of the runner, not of the
# config, because it comes down to whether a subscription credential can live in
# a repository secret. Saying so here — before anything is installed — beats
# failing at the first API call with an authentication error that reads like a
# wrong password.
#
# Measured lifetimes, read from installed credentials rather than documentation:
#
#   claude  `claude setup-token` issues a purpose-built token, 1 year
#   codex   OAuth access token, 10 days (iat to exp on a stored credential)
#   agy     OAuth access token, ~1 hour
#   kimi    OAuth access token, 15 minutes
#
# GitHub's scheduler has a five-minute floor and runs late under load, so it can
# keep a 10-day token warm comfortably and a 15-minute one not at all.

# Can this harness authenticate by subscription on this runner? Prints the
# reason when it cannot.
preflight_pairing_supported() {
  local runner="$1" harness="$2"
  [[ "$runner" == "self-hosted" ]] && return 0
  case "$harness" in
    claude) return 0 ;;
    codex)  return 0 ;;   # 10-day token, kept warm by the refresher workflow
    agy)
      printf "Antigravity's subscription token lives about an hour, so keeping it warm means refreshing every half hour, roughly 48 scheduled runs a day"
      return 1 ;;
    kimi)
      printf "Kimi's subscription token lives 15 minutes, and a scheduler with a five-minute floor that runs late under load cannot stay ahead of it"
      return 1 ;;
    *)
      printf "crossrev has no adapter for '%s'" "$harness"
      return 1 ;;
  esac
}

# Does this pairing need the single-writer refresher?
#
# Only one situation does: a harness whose credential rotates, authenticating by
# subscription, on an ephemeral runner. Change any one of the three and it
# disappears — which is why it is derived rather than asked.
preflight_needs_refresher() {
  local runner="$1" harness="$2" endpoint="$3"
  [[ "$runner" == "github-hosted" ]] || return 1
  [[ -z "$endpoint" || "$endpoint" == "null" ]] || return 1   # static token, never rotates
  [[ "$harness" == "codex" ]]
}

# One line per leg, for `crossrev doctor` and the `init` plan.
preflight_report_pairings() {
  local runner="$1" leg reason harness endpoint
  ui_section "Pairings on runner: $runner"
  for leg in reviewer resolver; do
    harness="$(cfg_get ".$leg.harness")"
    endpoint="$(cfg_get ".$leg.endpoint")"
    if [[ -n "$endpoint" && "$endpoint" != "null" ]]; then
      ui_ok "$leg — $harness via the '$endpoint' endpoint, a static token in a secret"
    elif reason="$(preflight_pairing_supported "$runner" "$harness")"; then
      if preflight_needs_refresher "$runner" "$harness" "$endpoint"; then
        ui_ok "$leg — $harness by subscription, kept warm by the refresher workflow"
      else
        ui_ok "$leg — $harness by subscription"
      fi
    else
      ui_no "$leg — $harness by subscription cannot run on a $runner runner"
      ui_line "   $reason"
      ui_line "   Fixes: set runner: self-hosted, or name a different harness for this leg."
      return 1
    fi
  done
  return 0
}

# yq reads YAML; jq reads JSON. Both config layers are YAML, so yq is not
# optional and the check says why rather than just naming the binary.
preflight_require_yq() {
  command -v yq >/dev/null 2>&1 || ui_die \
    "yq is not installed, and crossrev's config files are YAML" \
    "jq cannot read YAML. Install it with: $(_install_hint yq)"
}
