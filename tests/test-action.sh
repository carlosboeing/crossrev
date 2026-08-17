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
  out="$("$CROSSREV" "$leg" "${args[@]}" 2>&1)"
  hasnt "the $leg leg takes every flag the action forwards" \
    "$out" "unknown option for $leg"
done

is "and nothing was written finding that out" "$(count 'method POST')" "0"

finish
