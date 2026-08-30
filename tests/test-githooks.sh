#!/usr/bin/env bash
#
# scripts/githooks/pre-commit as a black box: a real repository, a real staged
# commit, and the hook's own exit status.
#
# The hook is the only privacy check that runs on every commit, and until this
# file existed nothing tested it. That gap is not theoretical — the exemption
# below was first written as a narrowing of what the hook denies, which silently
# stopped catching three ordinary commercial sentences. A green suite said
# nothing either way.
#
# Two sets, and both matter. A phrase that must commit proves the hook does not
# cry wolf; a phrase that must be refused proves it still guards. A gate people
# learn to bypass stops catching what it was written for, and a gate that catches
# nothing is worse.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
source "$HERE/harness.sh"

HOOK="$ROOT/scripts/githooks/pre-commit"

# One throwaway repository for the whole file. The hook reads the index, so a
# real `git add` is the only honest way to drive it.
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
git -C "$work" init -q
git -C "$work" config user.email crossrev@example.test
git -C "$work" config user.name "CrossRev Test"
mkdir -p "$work/scripts/githooks"
cp "$HOOK" "$work/scripts/githooks/pre-commit"
chmod +x "$work/scripts/githooks/pre-commit"
git -C "$work" add -A
git -C "$work" commit -q -m "the hook itself"

# verdict prints `accepted` or `refused` for one added line.
#
# The file is named .md rather than anything under the hook's own skip list, so
# the scan actually runs on it.
verdict() { # text
  printf '%s\n' "$1" > "$work/note.md"
  git -C "$work" add note.md
  if git -C "$work" -c core.hooksPath=scripts/githooks commit -q -m probe >/dev/null 2>&1; then
    git -C "$work" reset -q --soft HEAD~1
    git -C "$work" reset -q HEAD note.md
    printf 'accepted'
  else
    git -C "$work" reset -q HEAD note.md
    printf 'refused'
  fi
}

# --- the vendor-rate sense, which must commit --------------------------------
#
# Every line here is a real one from this repository. CrossRev prices nothing:
# it reads a vendored table of vendor token rates to tell an operator what a leg
# cost them on their own subscription.

is "a module boundary rule naming pricing commits" \
  "$(verdict '# ever reads a credential or talks to a network; pricing reads the committed')" "accepted"
is "table pricing commits" \
  "$(verdict '# Table pricing. Rates are per-token dollars upstream; the arithmetic scales')" "accepted"
is "the extract script commits" \
  "$(verdict '# The extract holds the models CrossRev can name and the fields pricing needs:')" "accepted"
is "the architecture sentence commits" \
  "$(verdict 'table pricing against the vendored extract at `lib/prices.json`')" "accepted"
is "a test name commits" \
  "$(verdict 'is "pricing the probe buckets reproduces its own cost" \')" "accepted"
is "a Go filename commits" \
  "$(verdict '// pricing.go — billing mode, table costs and the two presentation helpers')" "accepted"
is "a quotation of the boundary rule commits" \
  "$(verdict '// that reads it", and lib/usage.sh:15 says pricing "reads the committed extract')" "accepted"
is "a rate-refusal rationale commits" \
  "$(verdict 'why: "gpt-5.5 lists no cache-write rate, and pricing those tokens at zero would understate the leg"')" "accepted"
is "a cross-reference to the priced estimate commits" \
  "$(verdict '// and, when no harness cost survived, a table-priced estimate — pricing.go.')" "accepted"

# --- the commercial sense, which must be refused -----------------------------
#
# The last three were missed by a first attempt that narrowed the term instead
# of exempting the safe sense. The first of them evades the money branch too,
# because a single digit is not two and `per year` is not `per month`.

is "our pricing is refused" \
  "$(verdict 'Our pricing will be usage-based once the hosted tier ships.')" "refused"
is "a pricing page is refused" \
  "$(verdict 'We need a pricing page before the beta.')" "refused"
is "a pricing model is refused" \
  "$(verdict 'We should revisit the pricing model next quarter.')" "refused"
is "a concrete price at a single digit per year is refused" \
  "$(verdict 'Set pricing at $9 per year for the first cohort.')" "refused"
is "subscription pricing is refused" \
  "$(verdict 'Review subscription pricing before launch.')" "refused"
is "a bare pricing sentence is refused" \
  "$(verdict 'Pricing changes next quarter.')" "refused"

# --- the exemption strips a phrase, it does not skip a line ------------------
#
# The difference matters: a sentence carrying a safe phrase beside a real leak
# must still be refused, or the exemption becomes a way to smuggle one.

is "a safe phrase does not carry a workbench path past the scan" \
  "$(verdict 'Table pricing is described in .workbench/notes/rates.md')" "refused"
is "a safe phrase does not carry a hosted tier past the scan" \
  "$(verdict 'Table pricing feeds the hosted tier margin.')" "refused"
is "a safe phrase does not carry a monetisation note past the scan" \
  "$(verdict 'Table pricing is the first step toward monetisation.')" "refused"

# --- the other terms still guard --------------------------------------------

is "a workbench path is refused" \
  "$(verdict 'See .workbench/3-plans/the-plan.md for the rest.')" "refused"
is "a per-seat figure is refused" \
  "$(verdict 'Twelve per seat, billed annually.')" "refused"
is "a two-digit money figure is refused" \
  "$(verdict 'That run cost $40 of somebody else quota.')" "refused"
is "a shell positional parameter is not money" \
  "$(verdict 'printf "%s" "$1" | tr A-Z a-z')" "accepted"

# --- the gitlink check ------------------------------------------------------

mkdir -p "$work/.workbench"
git -C "$work" init -q "$work/.workbench"
git -C "$work" -C .workbench config user.email crossrev@example.test 2>/dev/null || true
( cd "$work/.workbench" && git config user.email crossrev@example.test && git config user.name t \
    && : > seed && git add seed && git commit -q -m seed )
git -C "$work" add -f .workbench 2>/dev/null
if git -C "$work" -c core.hooksPath=scripts/githooks commit -q -m probe >/dev/null 2>&1; then
  gitlink=accepted; git -C "$work" reset -q --soft HEAD~1
else
  gitlink=refused
fi
git -C "$work" reset -q HEAD .workbench 2>/dev/null
is "the workbench staged as a gitlink is refused" "$gitlink" "refused"

finish
