#!/usr/bin/env bash
#
# Build the native Go crossrev binary.
#
# Usage:
#   scripts/build-native.sh <output-path>
#
# Requires an explicit output path.

set -euo pipefail

if [[ $# -lt 1 ]] || [[ -z "${1:-}" ]]; then
  printf 'Usage: %s <output-path>\n' "$0" >&2
  exit 1
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

OUT="$1"
mkdir -p "$(dirname "$OUT")"

export GOTOOLCHAIN="go1.27.0"
go build -trimpath -o "$OUT" ./cmd/crossrev
