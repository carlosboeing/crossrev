#!/usr/bin/env bash
#
# Toolchain verification: verify Go 1.27.0 toolchain identification,
# Darwin CGO probe compilation, linker ad-hoc signing on arm64,
# manual repeatable ad-hoc signing on amd64, and binary reproducibility.

set -euo pipefail

export GOTOOLCHAIN="go1.27.0"

raw_version="$(go version)"
go_version="$(go env GOVERSION 2>/dev/null || true)"
host_os="$(go env GOHOSTOS 2>/dev/null || true)"
host_arch="$(go env GOHOSTARCH 2>/dev/null || true)"

if [[ "$go_version" != "go1.27.0" ]]; then
  printf 'Error: expected Go version go1.27.0, got "%s"\n' "$go_version" >&2
  printf 'Refusing beta, release candidate, or mismatched Go toolchain.\n' >&2
  exit 1
fi

if [[ "$go_version" =~ (beta|rc) ]]; then
  printf 'Error: refusing beta or release candidate: %s\n' "$go_version" >&2
  exit 1
fi

if [[ "$host_os" != "darwin" ]]; then
  printf 'Error: Darwin toolchain verification must run on macOS (darwin), host is %s\n' "$host_os" >&2
  exit 1
fi

printf 'Go toolchain verification\n'
printf '  version:      %s\n' "$raw_version"
printf '  GOVERSION:    %s\n' "$go_version"
printf '  GOHOSTOS:     %s\n' "$host_os"
printf '  GOHOSTARCH:   %s\n' "$host_arch"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

cat << 'PROBE_EOF' > "$workdir/main.go"
package main

import "C"
import "fmt"

func main() {
	fmt.Println("CrossRev native toolchain verification probe")
}
PROBE_EOF

# arm64 builds
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -trimpath -o "$workdir/probe_arm64_1" "$workdir/main.go"
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -trimpath -o "$workdir/probe_arm64_2" "$workdir/main.go"

lipo_arm64="$(lipo -info "$workdir/probe_arm64_1")"
if ! [[ "$lipo_arm64" =~ arm64 ]]; then
  printf 'Error: lipo failed to confirm arm64 architecture: %s\n' "$lipo_arm64" >&2
  exit 1
fi
printf '  arm64 lipo:   confirmed arm64 architecture\n'

cs_arm64="$(codesign -dv "$workdir/probe_arm64_1" 2>&1 || true)"
if ! [[ "$cs_arm64" =~ Signature=adhoc ]] || ! [[ "$cs_arm64" =~ flags=.*adhoc ]]; then
  printf 'Error: arm64 binary missing linker ad-hoc signature:\n%s\n' "$cs_arm64" >&2
  exit 1
fi
printf '  arm64 sign:   confirmed linker ad-hoc signature\n'

sha_arm64_1="$(shasum -a 256 "$workdir/probe_arm64_1" | awk '{print $1}')"
sha_arm64_2="$(shasum -a 256 "$workdir/probe_arm64_2" | awk '{print $1}')"
if [[ "$sha_arm64_1" != "$sha_arm64_2" ]] || [[ -z "$sha_arm64_1" ]]; then
  printf 'Error: arm64 builds not reproducible:\n  build 1: %s\n  build 2: %s\n' "$sha_arm64_1" "$sha_arm64_2" >&2
  exit 1
fi
printf '  arm64 digest: match (%s)\n' "$sha_arm64_1"

# amd64 builds
CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 CC="clang -arch x86_64" go build -trimpath -o "$workdir/probe_amd64_1" "$workdir/main.go"
CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 CC="clang -arch x86_64" go build -trimpath -o "$workdir/probe_amd64_2" "$workdir/main.go"

lipo_amd64="$(lipo -info "$workdir/probe_amd64_1")"
if ! [[ "$lipo_amd64" =~ x86_64 ]]; then
  printf 'Error: lipo failed to confirm x86_64 architecture: %s\n' "$lipo_amd64" >&2
  exit 1
fi
printf '  amd64 lipo:   confirmed x86_64 architecture\n'

cs_amd64_pre="$(codesign -dv "$workdir/probe_amd64_1" 2>&1 || true)"
if [[ "$cs_amd64_pre" =~ Signature=adhoc ]]; then
  printf 'Error: amd64 binary was unexpectedly signed before explicit codesign step (expected initial unsigned binary):\n%s\n' "$cs_amd64_pre" >&2
  exit 1
fi
printf '  amd64 pre:    confirmed initial unsigned binary\n'

codesign -s - -i crossrev -f "$workdir/probe_amd64_1"
codesign -s - -i crossrev -f "$workdir/probe_amd64_2"

cs_amd64_post="$(codesign -dv "$workdir/probe_amd64_1" 2>&1 || true)"
if ! [[ "$cs_amd64_post" =~ Signature=adhoc ]] || ! [[ "$cs_amd64_post" =~ flags=.*adhoc ]]; then
  printf 'Error: amd64 binary missing explicit ad-hoc signature:\n%s\n' "$cs_amd64_post" >&2
  exit 1
fi
printf '  amd64 sign:   confirmed explicit ad-hoc signature\n'

sha_amd64_1="$(shasum -a 256 "$workdir/probe_amd64_1" | awk '{print $1}')"
sha_amd64_2="$(shasum -a 256 "$workdir/probe_amd64_2" | awk '{print $1}')"
if [[ "$sha_amd64_1" != "$sha_amd64_2" ]] || [[ -z "$sha_amd64_1" ]]; then
  printf 'Error: amd64 builds not reproducible:\n  build 1: %s\n  build 2: %s\n' "$sha_amd64_1" "$sha_amd64_2" >&2
  exit 1
fi
printf '  amd64 digest: match (%s)\n' "$sha_amd64_1"

printf 'toolchain verification clean: Go 1.27.0 Darwin probe verified\n'
