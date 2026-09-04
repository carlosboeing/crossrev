#!/usr/bin/env bash
# tests/test-bootstrap.sh — the binary download, its digest check, and the install.
#
# bootstrap.sh fetches a release asset rather than cloning a checkout, so a full
# run needs the network and no suite does one. What runs offline: the platform
# mapping and the digest check as extracted functions, plus static assertions
# about the install — atomic, explicit-destination, safe replacement — and the
# sentence that says what the digest proves.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tmproot.sh
source "$HERE/tmproot.sh"
ROOT="$HERE/.."

pass=0; fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" \
            || notok "$1" "bootstrap.sh contains '$3'" "no such string in bootstrap.sh"; }
hasnt() { [[ "$2" != *"$3"* ]] && ok "$1" \
            || notok "$1" "bootstrap.sh does not contain '$3'" \
                     "found at line(s) $(grep -nF -- "$3" "$ROOT/bootstrap.sh" | cut -d: -f1 | tr '\n' ' ')"; }

src="$(cat "$ROOT/bootstrap.sh")"

# The two pure functions, lifted out of bootstrap.sh.
#
# Sourcing the script whole would start a download, so they are extracted
# instead, each as a /^name() {/,/^}/ range. _sum and die arrive as stubs: the
# script selects its checksum tool by probing the machine, which a range cannot
# carry, and the real die exits, which is what the refusal cases assert.
extract_functions() {
  sed -n '/^platform_asset() {/,/^}/p; /^verified_digest() {/,/^}/p' "$ROOT/bootstrap.sh"
}
_sum() { shasum -a 256 "$1" 2>/dev/null || sha256sum "$1"; }
die() { printf 'DIED %s' "$1"; exit 3; }

platform() {
  ( eval "$(extract_functions)" 2>/dev/null
    declare -F platform_asset >/dev/null || { printf '__extraction_failed__'; exit 0; }
    platform_asset "$1" "$2" || printf '__refused__'; )
}

# 1. The two targets the release builds.
is "macOS on Apple Silicon downloads the darwin asset" \
  "$(platform Darwin arm64)" "crossrev-darwin-arm64"
is "Linux on 64-bit Intel downloads the linux asset" \
  "$(platform Linux x86_64)" "crossrev-linux-amd64"

# 2. Anything else is refused by name, before any download.
is "Windows is refused" "$(platform MINGW64_NT x86_64)" "__refused__"
is "Linux on ARM is refused" "$(platform Linux aarch64)" "__refused__"
is "macOS on Intel is refused" "$(platform Darwin x86_64)" "__refused__"

# A digest check, run with fixture files.
#
# Each case runs in a subshell: verified_digest dies on a mismatch, and a die
# in the suite's own shell would end the suite rather than fail one assertion.
check() {
  local d; d="$(mktemp -d)"
  printf 'payload\n' >"$d/asset"
  ( eval "$(extract_functions)" 2>/dev/null
    if [[ "$1" == "mismatch" ]]; then
      printf 'deadbeef  crossrev-linux-amd64\n' >"$d/checksums.txt"
    else
      printf 'deadbeef  some-other-file\n' >"$d/checksums.txt"
    fi
    verified_digest "$d/asset" "$d/checksums.txt" "crossrev-linux-amd64" || printf '__refused__'; )
}

# The honest version: a matching digest must print the digest from
# checksums.txt and exit 0.
d="$(mktemp -d)"
printf 'payload\n' >"$d/asset"
_sum "$d/asset" | awk '{ print $1"  crossrev-linux-amd64" }' >"$d/sums"
out="$( (eval "$(extract_functions)" 2>/dev/null; verified_digest "$d/asset" "$d/sums" "crossrev-linux-amd64"); echo "exit=$?")"
is "a matching digest prints the expected digest and exits 0" "${out//$'\n'/}" "$(awk '{ print $1 }' "$d/sums")exit=0"

# 4. Anything else dies rather than installing.
is "a mismatched digest dies" "$(check mismatch)" "DIED the downloaded crossrev-linux-amd64 does not match its digest, so it was not installed"
is "a checksums.txt with no line for the asset dies" "$(check missing)" "DIED checksums.txt names no digest for crossrev-linux-amd64"

# 5. The install: atomic, explicit, safe.
has "the download lands in a private directory inside the destination" "$src" 'mktemp -d "$DEST/.crossrev-tmp.'
has "and is renamed over the destination" "$src" 'mv -f "$TMP/$ASSET" "$target"'
has "an explicit --dir is honoured" "$src" '--dir)  DEST='
has "an identical binary is kept without asking" "$src" 'cmp -s "$TMP/$ASSET" "$target"'
has "anything else asks before it is replaced" "$src" 'ask "Replace $target?"'

# 6. What the check proves, in the script's own words — and what it must never
# claim. A substituted release substitutes checksums.txt with the binary, so
# the digest proves the transfer arrived intact and nothing about the publisher.
has "the check is stated as transfer integrity" "$src" "covers the transfer, not the publisher"
has "and disclaims authorship" "$src" "says nothing about who put it there"
hasnt "no signature language" "$src" "sign"
hasnt "no attestation language" "$src" "attest"
hasnt "no authenticity language" "$src" "authentic"

# 7. What is gone: no clone, no checkout, no library.
hasnt "nothing clones anymore" "$src" "git clone"
# A sourcing command, not the header comment explaining there is none.
if grep -qE '^[[:space:]]*source ' "$ROOT/bootstrap.sh"; then
  notok "nothing sources anything (it is self-contained)" "no source command" \
    "found at line(s) $(grep -nE '^[[:space:]]*source ' "$ROOT/bootstrap.sh" | cut -d: -f1 | tr '\n' ' ')"
else
  ok "nothing sources anything (it is self-contained)"
fi
hasnt "nothing execs the old installer" "$src" "install.sh"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
