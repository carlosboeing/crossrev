#!/usr/bin/env bash
#
# Run the native Go test suite across all packages.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

export GOTOOLCHAIN="go1.27.0"

# -race, always. Several tests exist to say that a concurrent path is guarded,
# and a comment saying "run under -race" describes a run nobody performs unless
# the flag is here. Measured on this machine: about 7 seconds without it and
# about 31 with, both against a warm build cache.
go test -race ./... "$@"
