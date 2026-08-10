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
    for t in claude codex kimi; do
      if v="$(_tool_version "$t")"; then
        ui_ok "$v"
        found_harness=1
      else
        ui_opt "$t — not found, optional"
      fi
    done
    if (( found_harness == 0 )); then
      ui_no "no harness CLI found — revloop needs at least one of claude, codex or kimi"
      missing+=("harness")
    fi
  fi

  (( ${#missing[@]} == 0 ))
}

# yq reads YAML; jq reads JSON. Both config layers are YAML, so yq is not
# optional and the check says why rather than just naming the binary.
preflight_require_yq() {
  command -v yq >/dev/null 2>&1 || ui_die \
    "yq is not installed, and revloop's config files are YAML" \
    "jq cannot read YAML. Install it with: $(_install_hint yq)"
}
