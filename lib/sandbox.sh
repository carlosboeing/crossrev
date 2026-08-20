# shellcheck shell=bash
# lib/sandbox.sh — neutralise repository-provided harness configuration.
#
# A pull request branch does not only contain content to review. It contains
# files that configure the thing reviewing it: settings, instruction files,
# hooks, MCP server definitions, agents. A hook is arbitrary code execution
# before the model ever sees a token.
#
# Two mechanisms were available and only one of them is usable.
#
# Claude Code's `--bare` skips hooks, plugin sync, auto-memory and CLAUDE.md
# auto-discovery, which is exactly right — but it also refuses subscription
# auth: "Anthropic auth is strictly ANTHROPIC_API_KEY or apiKeyHelper via
# --settings (OAuth and keychain are never read)". Verified by running it with
# no API key present, which fails with "Not logged in". So on Claude Code you
# can have project-config isolation or subscription billing, not both, and the
# design's headline is that it runs on subscriptions.
#
# Codex requires persisted trust before running a hook, and exposes
# `--dangerously-bypass-hook-trust` to skip that check. crossrev never passes it.
#
# So sanitising the checkout is the mechanism, and the flags are defence in
# depth where they are free. It is also harness-agnostic, which matters: a flag
# that changes name in the next release fails open, whereas a file that is not
# there cannot be read by anything.
#
# Quarantined rather than deleted, for a reason that is easy to miss: a pull
# request that ADDS a hook is exactly the pull request a reviewer should be
# flagging. The diff still carries the text, and the files stay readable at a
# path no harness auto-loads.

if ! declare -F harness_load >/dev/null 2>&1; then
  # shellcheck source=lib/harnesses.sh
  source "${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}/harnesses.sh"
fi

CROSSREV_QUARANTINE=".crossrev-quarantine"

# Every path a harness is known to load from a working directory.
#
# Deliberately over-broad and deliberately not exhaustive — this list is a
# best-effort layer, not the thing standing between an injected hook and the
# App token. That is the credential separation: the agent process holds no
# GitHub credential at all, so an injection that reaches tool use still cannot
# post as the App, push a commit, or read a secret.
_sandbox_paths() {
  harness_load
  jq -r '([ .harnesses[].quarantine[]? ] + (.quarantine_shared // [])) | unique[]' <<<"$HARNESS_JSON"
}

# Move repository-provided harness configuration out of the way.
# Prints one line per quarantined path.
sandbox_quarantine() {
  local root="${1:-.}" moved=0 p
  mkdir -p "$root/$CROSSREV_QUARANTINE"

  while IFS= read -r p; do
    # `test -e` matches case-insensitively on macOS, so it cannot tell CLAUDE.md
    # from claude.md and the mv below would rename the user's file. find is
    # case-sensitive on both BSD and GNU, which is what makes listing every
    # spelling safe.
    [[ -n "$(find "$root/$(dirname "$p")" -maxdepth 1 -name "$(basename "$p")" 2>/dev/null)" ]] || continue
    mkdir -p "$root/$CROSSREV_QUARANTINE/$(dirname "$p")"
    mv "$root/$p" "$root/$CROSSREV_QUARANTINE/$p"
    printf 'quarantined %s\n' "$p"
    moved=$((moved+1))
  done < <(_sandbox_paths)

  # An empty quarantine directory is itself a repository-provided path the
  # harness might notice, and it is noise in `git status`.
  rmdir "$root/$CROSSREV_QUARANTINE" 2>/dev/null || true
  return 0
}

# Put everything back, so the checkout is the PR's again before anything is
# committed. Without this the resolver would commit the quarantine.
sandbox_restore() {
  local root="${1:-.}" q="${1:-.}/$CROSSREV_QUARANTINE" p clobbered=""
  [[ -d "$q" ]] || return 0
  while IFS= read -r p; do
    [[ -n "$(find "$q/$(dirname "$p")" -maxdepth 1 -name "$(basename "$p")" 2>/dev/null)" ]] || continue
    mkdir -p "$root/$(dirname "$p")"
    # Anything sitting at this path now was written blind: the quarantine moved
    # the real file away before the harness started, so the agent never read it.
    # Discarding that write is the correct outcome — letting a pull request's own
    # instructions survive the quarantine is precisely what the quarantine
    # exists to stop — but it must not be silent. A finding the resolver
    # "fixed" by writing here is reported as fixed, lands in no commit, and the
    # "reported fixes but changed no files" guard stays quiet because other
    # files did change.
    [[ -n "$(find "$root/$(dirname "$p")" -maxdepth 1 -name "$(basename "$p")" 2>/dev/null)" ]] && clobbered="$clobbered $p"
    rm -rf "${root:?}/$p"
    mv "$q/$p" "$root/$p"
  done < <(_sandbox_paths)
  rm -rf "$q"
  [[ -n "$clobbered" ]] && ui_warn \
    "the harness wrote to quarantined path(s):$clobbered" \
    "Those writes were discarded when the checkout was restored, so any finding reported as fixed by editing them is not fixed and is in no commit. Check those findings by hand."
  return 0
}

# Arguments that harden a harness invocation without costing the billing model.
# Empty for claude: --bare is the only isolation flag and it disables OAuth.
sandbox_args_for() {
  local harness="$1"
  harness_get "$harness" '.sandbox_args // [] | join(" ")'
}
