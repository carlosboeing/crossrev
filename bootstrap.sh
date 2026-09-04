#!/usr/bin/env bash
#
# bootstrap.sh — one command, from nothing to a working `crossrev`.
#
# This is the only file meant to be fetched and run directly. Everything else
# assumes a binary already exists; this is what fetches one.
#
# **It is deliberately self-contained.** It cannot source lib/ui.sh, because the
# whole reason it is running is that nothing is installed yet. So it carries
# its own thirty lines of prompting and says less than the rest of the tool
# does.
#
# **It is fetched, not cloned.** The repository is public, so raw.githubusercontent
# serves this file anonymously — no token, no gh, no credential of any kind:
#
#   curl -fsSL https://raw.githubusercontent.com/carlosboeing/crossrev/main/bootstrap.sh | bash
#
# Everything below assumes nothing but bash, curl and coreutils.
#
# **Every step it takes is optional.** Someone who already has a working binary
# should not be made to download again — so it compares against an existing one
# first and keeps it when the bytes match. It is safe to re-run.

set -euo pipefail

REPO="${CROSSREV_REPO:-carlosboeing/crossrev}"
DEST="${CROSSREV_BIN_DIR:-$HOME/.local/bin}"
# Empty means the latest release. A tag here is how you get a reproducible
# install — "what did I install?" has an answer only if the ref was not moving.
REF="${CROSSREV_REF:-}"
ASSUME_YES=0
DEST_EXPLICIT=0

while (( $# )); do
  case "$1" in
    --dir)  DEST="${2:?--dir needs a path}"; DEST_EXPLICIT=1; shift 2 ;;
    --repo) REPO="${2:?--repo needs owner/name}"; shift 2 ;;
    --ref)  REF="${2:?--ref needs a release tag}"; shift 2 ;;
    --yes|-y) ASSUME_YES=1; shift ;;
    --help|-h)
      echo "usage: bootstrap.sh [--dir <path>] [--repo owner/name] [--ref <tag>] [--yes]"
      exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
done

# --- the minimum UI this file needs -----------------------------------------
#
# Reading from /dev/tty rather than stdin is not defensive here, it is required:
# fetched and piped into bash, this script IS stdin, so `read` would consume the
# rest of itself. With no terminal at all there is nobody to ask, and the answer
# is to proceed only when --yes said so in advance.

if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
  _b=$'\033[1m'; _d=$'\033[2m'; _r=$'\033[0m'; _g=$'\033[32m'; _y=$'\033[33m'
else
  _b=''; _d=''; _r=''; _g=''; _y=''
fi

say()  { printf '  %s\n' "$1"; }
ok()   { printf '  %s✓%s %s\n' "$_g" "$_r" "$1"; }
step() { printf '\n%s◇  %s%s\n' "$_b" "$1" "$_r"; }
die()  { printf '\n%serror%s  %s\n       %s\n\n' "$_y" "$_r" "$1" "$2" >&2; exit 1; }

ask() {
  (( ASSUME_YES )) && return 0
  [[ -r /dev/tty ]] || die "there is no terminal to ask at, and this would install $DEST/crossrev" \
    "Re-run with --yes to accept, or --dir to choose somewhere else."
  local reply
  printf '%s◆  %s%s  [Y/n] ' "$_b" "$1" "$_r"
  read -r reply </dev/tty || return 1
  [[ -z "$reply" || "$reply" =~ ^[Yy] ]]
}

# --- which binary ------------------------------------------------------------
#
# The release builds two targets and nothing else, so anything else is refused
# by name before any download starts rather than mid-install with a 404.
# A pure function of its two arguments so the suite can drive it offline.

platform_asset() {
  case "$1/$2" in
    Darwin/arm64)  printf '%s' "crossrev-darwin-arm64" ;;
    Linux/x86_64)  printf '%s' "crossrev-linux-amd64" ;;
    *) return 1 ;;
  esac
}

ASSET="$(platform_asset "$(uname -s)" "$(uname -m)")" || die \
  "there is no CrossRev binary for $(uname -s)/$(uname -m)" \
  "This release ships macOS on Apple Silicon and Linux on 64-bit Intel/AMD only."

command -v curl >/dev/null 2>&1 || die \
  "curl is not installed, and this script downloads the binary with it" \
  "Install curl and run this again. On macOS: xcode-select --install"

# A checksum tool under either name. macOS ships shasum and no sha256sum;
# Linux ships sha256sum. Both check the same file format.
if command -v sha256sum >/dev/null 2>&1; then
  _sum() { sha256sum "$1"; }
elif command -v shasum >/dev/null 2>&1; then
  _sum() { shasum -a 256 "$1"; }
else
  die "neither sha256sum nor shasum is installed, and the download is verified before it runs" \
    "Install coreutils or perl and run this again."
fi

# --- which release ------------------------------------------------------------

if [[ -n "$REF" ]]; then
  TAG="$REF"
else
  TAG="$(curl -fsSIL -o /dev/null -w '%{url_effective}' \
    "https://github.com/$REPO/releases/latest" 2>/dev/null)" || die \
    "could not work out the latest release of $REPO" \
    "Check the network, then re-run with an explicit tag: --ref v0.5.0"
  TAG="${TAG##*/}"
fi
[[ -n "$TAG" ]] || die \
  "could not work out the latest release of $REPO" \
  "Check the network, then re-run with an explicit tag: --ref v0.5.0"

printf '\n  %scrossrev%s\n' "$_b" "$_r"

step "Download"
say "  release   $REPO $TAG"
say "  asset     $ASSET"
say "  into      $DEST/crossrev"
printf '\n'

# An explicit --dir is an instruction about where to install, so nothing else
# is consulted. Otherwise the one existing binary worth keeping is the one
# already on PATH: downloading over the top of an identical file is the
# rudest thing this script could do, and replacing a different one without
# asking is the second rudest.
if (( ! DEST_EXPLICIT )) && p="$(command -v crossrev 2>/dev/null)"; then
  say "A copy is already on PATH at $p; the download is checked against it first."
fi

# verified_digest answers the digest checksums.txt names for the asset, after
# proving the downloaded file matches it. Anything else dies before the binary
# lands on PATH. A pure function of files the caller names so the suite can
# drive it offline; _sum is whatever checksum tool this machine has.

verified_digest() {
  local want got
  want="$(awk -v a="$3" '$2 == a { print $1; found=1 } END { if (!found) exit 1 }' \
    "$2")" || die \
    "checksums.txt names no digest for $3" \
    "Without that line the binary cannot be checked before it runs. Check the release."
  got="$(_sum "$1")"; got="${got%% *}"
  [[ "$got" == "$want" ]] || die \
    "the downloaded $3 does not match its digest, so it was not installed" \
    "Delete nothing: re-run this script for a fresh download, and report it if a second download disagrees too."
  printf '%s' "$want"
}

# --- download and verify -------------------------------------------------------
#
# Into a private directory inside the destination, so the rename that installs
# the binary stays on one filesystem and is atomic: an interrupted run leaves
# the old binary or nothing, never half a file. The digest is checked there,
# before anything lands on PATH.

mkdir -p "$DEST"
TMP="$(mktemp -d "$DEST/.crossrev-tmp.XXXXXX")"
chmod 700 "$TMP"
trap 'rm -rf "$TMP"' EXIT

BASE="https://github.com/$REPO/releases/download/$TAG"
curl -fsSL -o "$TMP/$ASSET" "$BASE/$ASSET" || die \
  "could not download $ASSET from $REPO release $TAG" \
  "Check the tag exists and carries that asset: $BASE/$ASSET"
curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt" || die \
  "could not download checksums.txt from $REPO release $TAG" \
  "Without it the binary cannot be checked before it runs. Check the release carries one."
ok "downloaded $ASSET and its checksums"

verified_digest "$TMP/$ASSET" "$TMP/checksums.txt" "$ASSET" >/dev/null
ok "the digest matches"
say "That check covers the transfer, not the publisher: checksums.txt comes"
say "from the same release as the binary, so it proves the file arrived"
say "intact and says nothing about who put it there."

chmod +x "$TMP/$ASSET"

# --- install --------------------------------------------------------------------

target="$DEST/crossrev"
if [[ -e "$target" || -L "$target" ]]; then
  if [[ -f "$target" ]] && cmp -s "$TMP/$ASSET" "$target"; then
    ok "$target is already this binary. Nothing was changed."
    exit 0
  fi
  ask "Replace $target?" || die "nothing was installed" \
    "Left the existing file alone. Re-run with --dir <path> to install beside it."
fi

mv -f "$TMP/$ASSET" "$target"
trap - EXIT
rm -rf "$TMP"

if [[ -x "$target" ]]; then
  ok "$target"
else
  die "$target was installed but does not run as an executable" \
    "Check the filesystem allows execution there, then re-run this script."
fi

if ! command -v crossrev >/dev/null 2>&1; then
  say "$DEST is not on your PATH, so typing \`crossrev\` will not find it."
  say "Add this to your shell profile:"
  say "  export PATH=\"$DEST:\$PATH\""
fi

say "Then check everything:   crossrev doctor"
