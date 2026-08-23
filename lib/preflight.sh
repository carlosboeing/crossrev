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
        openssl) echo "already present on macOS; otherwise brew install openssl" ;;
        *)
          local hint; hint="$(harness_get "$tool" .install.hint)"
          [[ -n "$hint" ]] && echo "$hint" || echo "install $tool" ;;
      esac ;;
    *)
      case "$tool" in
        gh)     echo "https://github.com/cli/cli#installation" ;;
        jq)     echo "https://jqlang.github.io/jq/download/" ;;
        yq)     echo "https://github.com/mikefarah/yq#install" ;;
        git)    echo "your package manager, e.g. apt install git" ;;
        openssl) echo "your package manager, e.g. apt install openssl" ;;
        *)
          local hint; hint="$(harness_get "$tool" .install.hint)"
          [[ -n "$hint" ]] && echo "$hint" || echo "install $tool" ;;
      esac ;;
  esac
}

# "<tool> <version>", or non-zero if the tool is not installed or would not say.
#
# Every CLI reports itself differently — "git version 2.50.1 (Apple Git-155)",
# "jq-1.8.1", "codex-cli 0.147.0", and claude and kimi print a bare number with
# no name at all. Pulling out the first version-shaped token and prefixing the
# tool name gives one readable format, and satisfies the first output rule:
# name the thing.
#
# Two failures, because the fixes differ and the caller has to say which:
#
#   1  not installed — install it
#   2  installed, but nothing version-shaped came back — find out why
#
# The second used to be a success. stderr is folded into the capture so a tool
# that complains still gets read, and printing the complaint as the version made
# every existence check pass whatever the tool actually did. openssl was the
# instance that surfaced it, and it is why the probe is not one flag for
# everything: openssl's own subcommand is `openssl version`, --version came
# later, and the build on GitHub's hosted runners rejects it.
_tool_version() {
  command -v "$1" >/dev/null 2>&1 || return 1
  local raw ver
  case "$1" in
    openssl) raw="$(openssl version 2>&1 | head -1)" ;;
    *)       raw="$("$1" --version 2>&1 | head -1)" ;;
  esac
  ver="$(printf '%s' "$raw" | grep -oE 'v?[0-9]+\.[0-9]+[0-9A-Za-z.+-]*' | head -1)"
  [[ -n "$ver" ]] || return 2
  printf '%s %s' "$1" "$ver"
}

# Check the tools a given command actually needs.
#
# $1 is the requirement set:
#   core     git, gh (authenticated), jq, yq, openssl
#   harness  core plus at least one of claude, codex, kimi
#
# openssl is core because the leg path reaches it: credentials.sh decodes a
# restored credential with it, and auth.sh signs the App JWT with it. A runner
# without it fails mid-leg on `command not found`, a long way from the cause.
#
# Prints a report and returns non-zero if anything required is missing, so the
# caller decides whether that is fatal. install.sh reports; a leg dies.
preflight_check() {
  local need="${1:-core}"
  local missing=()

  ui_section "Requirements"

  local t v rc
  for t in git gh jq yq openssl; do
    # `|| rc=$?` rather than `; rc=$?`: the composite action sources this under
    # `set -e`, where a bare assignment from a failing substitution ends the step.
    rc=0; v="$(_tool_version "$t")" || rc=$?
    if (( rc == 0 )); then
      if [[ "$t" == "gh" ]]; then
        # Installed is not the same as usable. Rule 5: do not report success for
        # something unverified, and an unauthenticated gh fails at the first API
        # call rather than here.
        #
        # Which endpoint proves it depends on what kind of token gh holds, and
        # both kinds are ordinary here: a person runs the local path with a user
        # token, and automated mode authenticates as a GitHub App installation on
        # every run. `GET /user` answers only the first — an installation token is
        # scoped to the installation, not to a user, so asking it for a user is a
        # 403 on a credential that is working perfectly.
        #
        # So each is asked for identity at the endpoint that suits it, cheapest
        # first, and `rate_limit` — which every token type can reach — settles the
        # case where neither answers. Reaching none of the three is the only thing
        # that means unauthenticated.
        local who
        if who="$(gh api user --jq .login 2>/dev/null)" && [[ -n "$who" ]]; then
          ui_ok "$v — authenticated as $who"
        elif gh api installation/repositories --jq .total_count >/dev/null 2>&1; then
          ui_ok "$v — authenticated as a GitHub App installation"
        elif gh api rate_limit >/dev/null 2>&1; then
          ui_ok "$v — authenticated"
        elif [[ -n "${GITHUB_ACTIONS:-}" ]]; then
          # Rule 4: name the next action. A runner cannot log in interactively,
          # and nothing it could do to gh would help — the credential it was
          # handed is the thing to look at.
          ui_no "gh — installed, but the token it was given was refused. Check the app-token the workflow passes, and that the App is still installed on this repository."
          missing+=("gh-auth")
        else
          ui_no "gh — installed but not authenticated. Run: gh auth login"
          missing+=("gh-auth")
        fi
      else
        ui_ok "$v"
      fi
    elif (( rc == 2 )); then
      # Present but not answering. Installing it again is the one thing that
      # will not help, so the message says so rather than reaching for the hint.
      ui_no "$t — installed, but it did not report a version. Check that it runs."
      missing+=("$t")
    else
      ui_no "$t — not found. Install with: $(_install_hint "$t")"
      missing+=("$t")
    fi
  done

  if [[ "$need" == "harness" ]]; then
    local found_harness=0
    if ! command -v jq >/dev/null 2>&1; then
      ui_opt "harness check skipped — install jq to probe installed harnesses"
    else
      local t binary rc v
      while IFS= read -r t; do
        binary="$(harness_get "$t" .binary)"
        [[ -n "$binary" ]] || binary="$t"
        rc=0; v="$(_tool_version "$binary")" || rc=$?
        if (( rc == 0 )); then
          ui_ok "$v"
          found_harness=1
        elif (( rc == 2 )); then
          # Deliberately not counted as a harness. A CLI that will not say what it
          # is has not been shown to work, and reporting "found" here means the
          # loop discovers it at the first model invocation instead.
          ui_opt "$t — installed, but it did not report a version"
        else
          ui_opt "$t — not found, optional"
        fi
      done < <(harness_names)
      if (( found_harness == 0 )); then
        ui_no "no harness CLI found — CrossRev needs at least one of $(harness_names_human)"
        missing+=("harness")
      fi
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

# Can this harness authenticate by subscription on this runner? Prints the
# reason when it cannot.
#
# The third argument is optional and names a leg in the descriptor's vocabulary
# — review or resolve. A harness that lists its legs is refused for the others
# here, so doctor reports the limit for automated mode rather than leaving it
# to be discovered by a failing job; without the argument the answer stays the
# credential-only question it always was.
preflight_pairing_supported() {
  local runner="$1" harness="$2" leg="${3:-}"

  # A descriptor fact, not a runner fact: self-hosted skips the credential
  # checks below because the machine already holds the login, but a harness
  # that does not serve this leg is refused on every runner.
  if [[ -n "$leg" ]] && ! harness_serves_leg "$harness" "$leg"; then
    printf "%s is review-only, and cannot serve the %s leg" \
      "$(harness_get "$harness" .product_name)" "$leg"
    return 1
  fi

  [[ "$runner" == "self-hosted" ]] && return 0

  if ! harness_known "$harness"; then
    local not_driven
    if not_driven="$(harness_not_driven "$harness")"; then
      printf "CrossRev has no adapter for '%s' (%s)" "$harness" "$not_driven"
    else
      printf "CrossRev has no adapter for '%s'" "$harness"
    fi
    return 1
  fi

  local arch prov secret seconds p_name refresher
  arch="$(harness_get "$harness" .credential.archetype)"
  prov="$(harness_get "$harness" .credential.provenance)"
  secret="$(harness_get "$harness" .credential.secret)"
  seconds="$(harness_get "$harness" .credential.access_token_seconds)"
  p_name="$(harness_get "$harness" .product_name)"
  refresher="$(harness_get "$harness" .credential.refresher)"

  [[ "$arch" == "A" ]] && return 0
  if [[ "$arch" == "B" && "$refresher" == "true" ]]; then
    return 0
  fi
  if [[ "$arch" == "C" && "$prov" == "measured" && -n "$secret" ]]; then
    return 0
  fi

  local mins=$(( seconds / 60 ))
  printf "%s's subscription token lives about %d minutes, and CrossRev has no way to seed it into a hosted runner yet" "$p_name" "$mins"
  return 1
}

# Which secret carries a harness's subscription credential in automated mode.
preflight_harness_secret() {
  local s
  s="$(harness_get "$1" .credential.secret)"
  [[ -n "$s" ]] || return 1
  printf '%s' "$s"
}

# Is this a runner where a credential can only arrive as a secret?
#
# GitHub sets RUNNER_ENVIRONMENT, and it is the only signal that separates the
# three environments CrossRev runs in. GITHUB_ACTIONS does not: it is true on a
# self-hosted runner too, where the harness is logged in on disk and no secret is
# expected — which is why the templates filter those env lines out of the
# workflow they generate for one. The values are GitHub's and happen to be the
# same two the `runner:` config key already uses.
preflight_hosted_runner() { [[ "${RUNNER_ENVIRONMENT:-}" == "github-hosted" ]]; }

# Does this pairing need the single-writer refresher?
#
# Only one situation does: a harness whose credential rotates, authenticating by
# subscription, on an ephemeral runner. Change any one of the three and it
# disappears — which is why it is derived rather than asked.
preflight_needs_refresher() {
  local runner="$1" harness="$2" endpoint="$3"
  [[ "$runner" == "github-hosted" ]] || return 1
  [[ -z "$endpoint" || "$endpoint" == "null" ]] || return 1   # static token, never rotates
  [[ "$(harness_get "$harness" .credential.refresher)" == "true" ]]
}

# One line per leg, for `crossrev doctor` and the `init` plan.
preflight_report_pairings() {
  local runner="$1" leg reason harness endpoint
  ui_section "Pairings on runner: $runner"
  for leg in reviewer resolver; do
    harness="$(cfg_get ".$leg.harness")"
    endpoint="$(cfg_get ".$leg.endpoint")"
    # The loop names the config keys; preflight_pairing_supported speaks the
    # descriptor's vocabulary.
    local leg_name="resolve"
    [[ "$leg" == "reviewer" ]] && leg_name="review"
    if [[ -n "$endpoint" && "$endpoint" != "null" ]]; then
      ui_ok "$leg — $harness via the '$endpoint' endpoint, a static token in a secret"
    elif reason="$(preflight_pairing_supported "$runner" "$harness" "$leg_name")"; then
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

# Check for a stranded quarantine directory left behind by a killed run.
#
# sandbox_restore removes .crossrev-quarantine/ when a run completes normally.
# A run killed by SIGKILL, a machine sleeping, or a crash before restore leaves
# the quarantine sitting in the tree and the real instruction files looking
# deleted in git status.
preflight_check_quarantine() {
  local q=".crossrev-quarantine"
  [[ -d "$q" ]] || return 0
  local paths=() p
  while IFS= read -r p; do
    [[ -n "$p" ]] && paths+=("$p")
  done < <(find "$q" -mindepth 1 2>/dev/null | sed "s|^$q/||" | sort)
  ui_no "stranded quarantine found at $q"
  if (( ${#paths[@]} > 0 )); then
    ui_line "   Files inside: ${paths[*]}"
  fi
  ui_line "   A previous run died before restoring the checkout. Move them back to restore your files."
  return 1
}

# Report tool-owned worktrees left behind by failed resolve runs.
#
# A clean resolve run removes its worktree; a failed run leaves it behind so
# the uncommitted edits and reflog can be inspected. Accumulation is reported
# here so leftover worktrees are discoverable rather than silent.
preflight_report_worktrees() {
  local base="${XDG_STATE_HOME:-$HOME/.local/state}/crossrev/worktrees"
  [[ -d "$base" ]] || return 0
  local wts=() wt
  while IFS= read -r wt; do
    [[ -d "$wt" ]] && wts+=("$wt")
  done < <(find "$base" -mindepth 2 -maxdepth 2 -type d 2>/dev/null | sort)
  (( ${#wts[@]} > 0 )) || return 0
  ui_section "Tool-owned worktrees"
  for wt in "${wts[@]}"; do
    ui_opt "$wt"
  done
  ui_line "   Left behind by failed resolve runs. Safe to remove if no run is in progress."
  return 0
}
