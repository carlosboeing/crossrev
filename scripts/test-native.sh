#!/usr/bin/env bash
#
# Run the native Go test suite across all packages.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

export GOTOOLCHAIN="go1.27.0"
go test ./... "$@"
