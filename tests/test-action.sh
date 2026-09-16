#!/usr/bin/env bash
#
# The composite action's contract with the CLI.
#
# action.yml assembles an argument list and hands it to bin/crossrev, so every
# input it forwards has to be an option the named leg accepts. The two halves
# live in different files and different languages, nothing connected them, and
# the failure is total rather than partial: a leg dies on its catch-all arm
# before doing anything at all. That is how `--trigger` shipped forwarded to a
# resolve leg that could not parse it, blocking every automated pass.
#
# The flags are read off action.yml rather than listed here, because a new input
# added to the forwarding step is the exact change that would bring this back.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

ACTION="$HERE/../action.yml"

# Held as words rather than arrays, and split on purpose: bash 3.2 ships without
# mapfile, and macOS is a supported platform.

# The flags the forwarding step appends.
FLAGS="$(grep -oE 'args\+=\(--[a-z-]+' "$ACTION" | sed 's/^args+=(//' | sort -u | tr '\n' ' ')"

# The legs the `leg` input says it takes, read off its own description so a leg
# added there without being taught the flags fails here rather than in a
# consumer's repository.
LEGS="$(yq -r '.inputs.leg.description' "$ACTION" |
  sed 's/.*— //; s/\.$//; s/,//g; s/ or / /' | tr -d '\n')"

# Both lists are derived, so a derivation that quietly returned nothing would
# make every assertion below vacuous.
has "the forwarded flags are read off action.yml" " $FLAGS" " --trigger "
has "and the legs are read off the leg input"     " $LEGS " " resolve "

# A flag the step appends with a value in the same expression takes one.
takes_value() { grep -qE "args\+=\($1 \"" "$ACTION"; }

# What a real invocation carries. A flag added since carries a placeholder,
# because the argument loop rejects an unknown option before it looks at any
# value — and a new flag that needs a real one should fail loudly here.
flag_value() {
  case "$1" in
    --pr)      printf '%s' "$FIX_PR" ;;
    --harness) printf 'claude' ;;
    --trigger) printf 'automatic' ;;
    *)         printf 'x' ;;
  esac
}

# Every flag at once, which is what the action sends when every input is set.
args=()
for f in $FLAGS; do
  args+=("$f")
  takes_value "$f" && args+=("$(flag_value "$f")")
done

# No routes at all, so each leg dies at the first read — after its argument loop
# and before anything is written, a model is run, or a pull request is touched.
fixture_repo; stub_reset

for leg in $LEGS; do
  # auth-refresh is deliberately outside this loop. The action does not send
  # it the forwarded flags — it builds `auth refresh --harness --repo` itself
  # — so "does this leg accept every forwarded flag" is not a question about
  # it. And it cannot merely be left in: `crossrev auth-refresh` is not a
  # command, so the binary answers `unknown command`, the assertion below
  # looks for `unknown option`, and the row passes having checked nothing.
  # The real coverage for this leg is the auth refresh invocation further down.
  [[ "$leg" == "auth-refresh" ]] && continue
  out="$("$CROSSREV" "$leg" "${args[@]}" 2>&1)"
  hasnt "the $leg leg takes every flag the action forwards" \
    "$out" "unknown option for $leg"
done

is "and nothing was written finding that out" "$(count 'method POST')" "0"

# The per-leg preflight level. Regression one — every watchdog run on v0.6.x
# failing `no harness CLI found` before doing any work — shipped because
# nothing here asserted which level a leg asks for. The levels are read out of
# action.yml's own case statement, so a leg added to the map without being
# added here is visible as a missing assertion rather than as a silent pass.
level_for() {
  yq -r '.runs.steps[] | select(.run | test("doctor --level")) | .run' "$ACTION" |
    awk -v leg="$1" '
      /^[[:space:]]*[*a-z|-]+\)/ {
        pattern = $1; sub(/\)$/, "", pattern)
        n = split(pattern, alts, "|")
        match_here = 0
        for (i = 1; i <= n; i++) if (alts[i] == leg || alts[i] == "*") match_here = 1
      }
      match_here && /level=/ { sub(/.*level=/, ""); sub(/[";[:space:]].*$/, ""); print; exit }
    '
}

for leg in review resolve cycle; do
  is "the $leg leg asks for the harness preflight" "$(level_for "$leg")" "harness"
done
for leg in status watchdog auth-refresh; do
  is "the $leg leg asks for the core preflight" "$(level_for "$leg")" "core"
done

# An unknown leg asks for more, not less. A leg added to the input without
# being added to the map must fail its preflight rather than skip a check.
is "an unrecognised leg falls back to harness" "$(level_for "not-a-leg")" "harness"

# --- the inputs a workflow omits ---------------------------------------
#
# A forwarded flag is only half the contract. The other half is what the action
# sends when a workflow passes nothing, because two of the three generated
# workflows pass no `trigger:` at all and take this default instead.
#
# The draft rule is what rides on it. `ctx_load` refuses a draft pull request
# only when the trigger is `automatic`, so a default of `human` — or no default,
# which the forwarding step turns into no flag and the CLI turns into `human` —
# puts the resolve leg back to pushing fixes to a draft the review leg refuses
# to look at. That is what shipped at v0.2.0 and it was invisible: every other
# assertion in this file passes either way.
is "the trigger input defaults to automatic" \
  "$(yq -r '.inputs.trigger.default' "$ACTION")" "automatic"

for wf in resolve watchdog; do
  is "templates/crossrev-$wf.yml passes no trigger, so it takes that default" \
    "$(yq -r '[.jobs[].steps[].with.trigger] | map(select(. != null)) | length' \
       "$HERE/../templates/crossrev-$wf.yml")" "0"
done

# The review workflow is the exception, and has to be: it also fires on
# `issue_comment`, where a person typed `/crossrev review` and the caps and the
# draft rule should not apply.
has "templates/crossrev-review.yml chooses its own trigger per event" \
  "$(yq -r '[.jobs[].steps[].with.trigger] | join("")' "$HERE/../templates/crossrev-review.yml")" \
  "'automatic' || 'human'"

# auth-refresh does not take the forwarded flags, so it gets its own branch in
# the run step rather than the generic assembly. Without the branch the generic
# path appends --trigger and --no-tips, which `auth refresh`'s argument loop
# refuses outright (internal/cli/parse.go:478-500) — the leg would die at its
# first argument, on a credential with a ten-day clock on it.
refresh_branch="$(yq -r '.runs.steps[-1].run' "$ACTION")"
has "the run step branches on auth-refresh"      "$refresh_branch" "auth-refresh"
has "and builds the auth refresh command itself" "$refresh_branch" "auth refresh"
has "naming the harness"                         "$refresh_branch" "--harness"
has "and the repository explicitly"              "$refresh_branch" "--repo"
branch="${refresh_branch#*inputs.leg*auth-refresh}"
hasnt "without forwarding the trigger flag"      "${branch%%fi*}" "--trigger"

# `crossrev auth-refresh` is not a command (internal/cli/parse.go:117), so the
# leg loop above cannot say anything useful about this value: the binary dies
# with `unknown command`, the loop's assertion looks for `unknown option`, and
# a vacuous pass is what you get. The real command is exercised here instead.
fixture_repo; stub_reset
out="$("$CROSSREV" auth refresh --harness codex --repo acme/widget 2>&1)"
hasnt "auth refresh takes the two flags the action sends it" \
  "$out" "unknown option for auth refresh"

# --- the release the action downloads ----------------------------------
#
# The binary follows the action's own VERSION. Regression: resolving through
# the pin never worked — github.action_ref arrives empty, the empty ref fell
# through to the tag branch, and every consumer silently downloaded the
# latest release whatever the pin said. The v0.7.0 proof caught it red: legs
# pinned at the v0.6.2 commit ran the v0.7.0 binary the hour it published.
# The download step is read out of action.yml, so a resolution rewritten
# without these properties fails here rather than in a consumer's
# repository.
download_step="$(yq -r '.runs.steps[] | select(.run | test("gh release download")) | .run' "$ACTION")"
has "the download step reads the action's own VERSION" \
  "$download_step" "GITHUB_ACTION_PATH/VERSION"
has "and refuses an unversioned checkout" \
  "$download_step" "no VERSION beside the loaded action"
has "and refuses a version that names no release" \
  "$download_step" "which is not a release"
hasnt "without resolving through the pin context" \
  "$download_step" '${{ github.action_ref }}'

finish

