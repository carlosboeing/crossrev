#!/usr/bin/env bash
# tests/test-check-changelog.sh — the changelog gate as a black box.
#
# scripts/check-changelog.sh reads the shipped set from `npm pack --dry-run
# --json`, and npm prints that JSON in two shapes: up to version 11 an array
# of pack results, from version 12 an object keyed by package name (npm/cli
# v12.0.0 release notes, tracked as npm/cli#9247; expo/expo#48091 is the same
# break in another tool). A gate that cannot read the packed list fails every
# pull request the moment the runner's npm moves, so both shapes are
# asserted, not only the one this machine's npm prints today.
#
# The gate is copied into a throwaway repository because it derives the
# repository root from its own path, and a stub npm on PATH serves whichever
# JSON shape the case chose, so the suite runs neither the real npm nor the
# network. Asserted per shape:
#   - a recorded change passes, and the gate says why
#   - a silent change fails, naming the unrecorded file
#   - a shape that yields no paths at all fails loudly rather than passing
#     as an empty shipped set

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tmproot.sh
source "$HERE/tmproot.sh"
ROOT="$HERE/.."

pass=0; fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" || notok "$1" "contains '$3'" "$2"; }

GATE="$ROOT/scripts/check-changelog.sh"

# One throwaway repository for the whole file, holding the two branches the
# cases run against: `recorded` changes a shipped file and writes the
# [Unreleased] entry; `silent` changes the same file and writes nothing.
work="$(mktemp -d)"
git -C "$work" init -q -b main
git -C "$work" config user.email crossrev@example.test
git -C "$work" config user.name "CrossRev Test"
mkdir -p "$work/scripts"
cp "$GATE" "$work/scripts/check-changelog.sh"

changelog_base() {
  cat <<'EOF'
# Changelog

## [Unreleased]

## [0.9.0]
- A prior release, so the [Unreleased] section has an edge to end at.
EOF
}
changelog_base >"$work/CHANGELOG.md"
printf 'readme, as shipped\n' >"$work/README.md"
git -C "$work" add -A
git -C "$work" commit -q -m base

git -C "$work" checkout -q -b recorded
printf 'readme, changed on the branch\n' >"$work/README.md"
changelog_base \
  | awk '{ print } /^## \[Unreleased\]$/ { print "- Changed the readme, recorded here." }' \
  >"$work/CHANGELOG.md"
git -C "$work" add -A
git -C "$work" commit -q -m recorded

git -C "$work" checkout -q -b silent main
printf 'readme, changed on the branch\n' >"$work/README.md"
git -C "$work" add -A
git -C "$work" commit -q -m silent

# The stub npm serves whichever pack output the case chose. The file lists
# hold what this package really packs where it matters here: README.md ships
# and CHANGELOG.md does not, so a change to README.md is a shipped change and
# the entry the `recorded` branch adds is never itself one.
stub="$(mktemp -d)"
cat >"$stub/npm" <<'STUB'
#!/usr/bin/env bash
cat "$NPM_PACK_OUTPUT"
STUB
chmod +x "$stub/npm"
files='[{"path":"README.md"},{"path":"LICENSE"},{"path":"VERSION"},{"path":"package.json"},{"path":"action.yml"},{"path":"install.sh"}]'
printf '[{"filename":"crossrev-ai-0.9.0.tgz","files":%s}]\n' "$files" >"$stub/npm11.json"
printf '{"crossrev-ai":{"filename":"crossrev-ai-0.9.0.tgz","files":%s}}\n' "$files" >"$stub/npm12.json"
printf '{}\n' >"$stub/npm12-empty.json"

# Runs the gate on one branch against one pack output, leaving the exit
# status in gate_rc and the whole output in gate_out. The stub directory goes
# first on PATH so `command -v npm` finds it before any real npm.
run_gate() { # branch pack-json
  git -C "$work" checkout -q "$1"
  if gate_out="$(PATH="$stub:$PATH" NPM_PACK_OUTPUT="$stub/$2" \
      bash "$work/scripts/check-changelog.sh" main 2>&1)"; then
    gate_rc=0
  else
    gate_rc=$?
  fi
}

run_gate recorded npm11.json
is "npm 11 array shape: a recorded change passes" "$gate_rc" "0"
has "npm 11 array shape: the gate reports the recorded entry" "$gate_out" "1 shipped file(s) changed, and [Unreleased] records it"

run_gate recorded npm12.json
is "npm 12 object shape: a recorded change passes" "$gate_rc" "0"
has "npm 12 object shape: the gate reports the recorded entry" "$gate_out" "1 shipped file(s) changed, and [Unreleased] records it"

run_gate silent npm11.json
is "npm 11 array shape: a silent change fails" "$gate_rc" "1"
has "npm 11 array shape: the failure names the unrecorded file" "$gate_out" "nothing was added under [Unreleased]"

run_gate silent npm12.json
is "npm 12 object shape: a silent change fails" "$gate_rc" "1"
has "npm 12 object shape: the failure names the unrecorded file" "$gate_out" "nothing was added under [Unreleased]"

run_gate recorded npm12-empty.json
is "a shape that yields no paths fails loudly" "$gate_rc" "1"
has "the loud failure names the packed list" "$gate_out" "could not read the packed file list from npm"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
