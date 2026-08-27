#!/usr/bin/env bash
#
# Build the native Go crossrev binary.
#
# Usage:
#   scripts/build-native.sh [output-path]
#
# Defaults output to bin/crossrev-native if no argument is passed.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

OUT="${1:-bin/crossrev-native}"
mkdir -p "$(dirname "$OUT")"

export GOTOOLCHAIN="go1.27.0"
go build -trimpath -o "$OUT" ./cmd/crossrev
