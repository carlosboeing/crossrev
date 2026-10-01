#!/usr/bin/env bash
#
# Gate 1's ledger check: every original tests/test-*.sh suite is recorded in
# tests/parity-coverage.tsv exactly once, with a destination that exists.
#
# Usage: scripts/check-parity-coverage.sh
#
# A row has four tab-separated fields: suite, class, behaviour, destinations.
# Class is go-test (proved by Go package tests) or black-box (proved by running
# the shell suite itself against one built binary, which is what tests/run.sh
# builds and runs).

set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1

(( $# == 0 )) || { printf 'usage: %s\n' "$0" >&2; exit 2; }

TSV="tests/parity-coverage.tsv"
[[ -f "$TSV" ]] || { printf 'missing %s\n' "$TSV" >&2; exit 1; }

fail=0
refuse() { printf 'parity coverage: %s\n' "$1" >&2; fail=1; }

# suite -> destinations, so a duplicate is a second write to a set key.
seen=""

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


(( fail == 0 )) || exit 1
printf 'parity coverage holds: every suite is mapped once to a destination that exists\n'
