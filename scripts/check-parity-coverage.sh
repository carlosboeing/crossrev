#!/usr/bin/env bash
#
# Gate 1's ledger check: every original tests/test-*.sh suite is recorded in
# tests/parity-coverage.tsv exactly once, with a destination that exists.
#
# Usage:
#   scripts/check-parity-coverage.sh           # the ledger is complete
#   scripts/check-parity-coverage.sh --native  # plus: every black-box
#     destination is pure (no lib/*.sh sourcing, no bin/crossrev execution)
#     and actually runs in scripts/test-native.sh
#
# A row has four tab-separated fields: suite, class, behaviour, destinations.
# Class is go-test (proved by Go package tests) or black-box (proved by running
# the shell suite itself against one built binary). Mixed suites that do both
# are recorded under their Go proof; their CLI halves keep running in
# test-native.sh as regression, which is why the --native list check allows a
# SUITES entry that maps to a go-test row but never the reverse.

set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1

NATIVE=0
if (( $# )) && [[ "$1" == "--native" ]]; then
  NATIVE=1
  shift
fi
(( $# == 0 )) || { printf 'usage: %s [--native]\n' "$0" >&2; exit 2; }

TSV="tests/parity-coverage.tsv"
[[ -f "$TSV" ]] || { printf 'missing %s\n' "$TSV" >&2; exit 1; }

fail=0
refuse() { printf 'parity coverage: %s\n' "$1" >&2; fail=1; }

# suite -> "class<TAB>destinations", so a duplicate is a second write to a set key.
seen=""
row_of() { awk -F'\t' -v s="$1" '$1 == s { print $2"\t"$4; found=1 } END { if (!found) exit 1 }' "$TSV"; }

line=0
while IFS= read -r raw || [[ -n "$raw" ]]; do
  line=$((line+1))
  [[ "$raw" =~ ^#.*$ || -z "$raw" ]] && continue
  fields="$(awk -F'\t' '{ print NF }' <<<"$raw")"
  [[ "$fields" == "4" ]] || { refuse "line $line has $fields tab-separated fields, want 4"; continue; }
  suite="$(cut -f1 <<<"$raw")"
  class="$(cut -f2 <<<"$raw")"
  dests="$(cut -f4 <<<"$raw")"
  case " $seen " in
    *" $suite "*) refuse "duplicate row for $suite (line $line)"; continue ;;
  esac
  seen="$seen $suite"
  [[ "$suite" == test-*.sh ]] || { refuse "line $line names $suite, want tests/test-*.sh"; continue; }
  [[ "$class" == "go-test" || "$class" == "black-box" ]] \
    || { refuse "$suite has class $class, want go-test or black-box"; continue; }
  [[ -n "$dests" ]] || { refuse "$suite names no destination"; continue; }
  for dest in $dests; do
    [[ -e "$dest" ]] || { refuse "$suite names a destination that does not exist: $dest"; continue; }
    if [[ "$class" == "go-test" ]]; then
      [[ "$dest" == *_test.go ]] || refuse "$suite is a go-test row with a non-Go destination: $dest"
    else
      [[ "$dest" == tests/test-*.sh ]] || refuse "$suite is a black-box row with a destination outside tests/: $dest"
    fi
  done
done <"$TSV"

# A suite on disk with no row is behaviour the ledger lost track of.
for path in tests/test-*.sh; do
  suite="${path#tests/}"
  case " $seen " in
    *" $suite "*) ;;
    *) refuse "no row for $suite" ;;
  esac
done

if (( NATIVE )); then
  # The suites test-native.sh runs, read off its SUITES list rather than
  # repeated here: two lists of the same set drift, and this check failing is
  # how that drift surfaces.
  suites="$(sed -n '/^SUITES=(/,/^)/p' scripts/test-native.sh | grep -oE 'test-[a-z-]+\.sh' || true)"
  [[ -n "$suites" ]] || refuse "could not read the SUITES list out of scripts/test-native.sh"

  while IFS= read -r raw || [[ -n "$raw" ]]; do
    [[ "$raw" =~ ^#.*$ || -z "$raw" ]] && continue
    suite="$(cut -f1 <<<"$raw")"
    class="$(cut -f2 <<<"$raw")"
    dests="$(cut -f4 <<<"$raw")"
    [[ "$class" == "black-box" ]] || continue
    for dest in $dests; do
      # A native destination proves the binary, so it must not reach the shell
      # implementation: no sourcing lib/*.sh, no executing the checked-in
      # entrypoint. Full-line comments are stripped first; a literal inside a
      # trailing comment would need a reader, and none of these files has one
      # next to either pattern. Shared harness tests/harness.sh is not a
      # destination and is not checked: its bin/crossrev fallback only runs
      # when CROSSREV_TEST_BIN is unset, which test-native.sh never allows.
      code="$(grep -vE '^[[:space:]]*#' "$dest")"
      if grep -qE '(^|[;&|[:space:]\(])(source|\.)[[:space:]]+[^;&|]*lib/' <<<"$code"; then
        refuse "$dest sources lib/*.sh, so it cannot prove the binary"
      fi
      if grep -qE '\.\./bin/crossrev|\$ROOT/bin/crossrev|\$HERE/\.\./bin/crossrev|(^|[;&|[:space:]\(\)])bin/crossrev' <<<"$code"; then
        refuse "$dest executes bin/crossrev, so it cannot prove the binary"
      fi
      # Every mapped black-box suite runs against the one built binary.
      grep -qxF "$suite" <<<"$suites" \
        || refuse "$suite is mapped black-box but runs nowhere in scripts/test-native.sh"
    done
  done <"$TSV"

  # And nothing runs there unmapped: an unlisted suite would prove behaviour
  # the ledger attributes nowhere.
  while IFS= read -r name; do
    [[ -z "$name" ]] && continue
    row_of "$name" >/dev/null || refuse "$name runs in scripts/test-native.sh but is mapped nowhere"
  done <<<"$suites"
fi

(( fail == 0 )) || exit 1
if (( NATIVE )); then
  printf 'parity coverage holds, and every black-box suite is pure and runs against the binary\n'
else
  printf 'parity coverage holds: every suite is mapped once to a destination that exists\n'
fi
