#!/usr/bin/env bash
#
# Build the native Go CrossRev binary.
#
# Usage:
#   scripts/build-binary.sh <target> <output-path>
#
# <target> is darwin-arm64, linux-amd64, or host. The per-target flags live
# here rather than in the workflow, so that the binary CI publishes and the
# binary a developer installs are produced the same way. That the release
# workflow once carried its own `go build` lines is how the version-agreement
# gate came to verify a build nobody ships.

set -euo pipefail

if [[ $# -lt 2 ]] || [[ -z "${1:-}" ]] || [[ -z "${2:-}" ]]; then
  printf 'Usage: %s <target> <output-path>\n' "$0" >&2
  exit 1
fi

TARGET="$1"
OUT="$2"

case "$TARGET" in
  # CGO stays on: a CGO darwin/arm64 build leaves the linker's ad-hoc
  # signature on the Mach-O binary, which the release workflow asserts
  # (docs/adrs/0018-go-native-parity-contract.md). No explicit signing
  # step: the target that needed one is deferred.
  darwin-arm64) export CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 ;;
  # Static: the binary installs onto machines whose libc is unknown, so it
  # carries none.
  linux-amd64)  export CGO_ENABLED=0 GOOS=linux GOARCH=amd64 ;;
  host)         ;;
  *) printf 'unknown target: %s (want darwin-arm64, linux-amd64 or host)\n' "$TARGET" >&2; exit 1 ;;
esac

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

mkdir -p "$(dirname "$OUT")"

export GOTOOLCHAIN="go1.27.0"
go build -trimpath -o "$OUT" ./cmd/crossrev
