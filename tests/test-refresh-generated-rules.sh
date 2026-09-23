#!/usr/bin/env bash
# tests/test-refresh-generated-rules.sh — maintainer tool for upstream Linguist rules.
#
# scripts/refresh-generated-rules.sh inspects upstream Linguist's generated.rb
# and prints lockfile names and filename patterns that internal/intel/generated.go
# does not have.
#
# This test stubs an upstream generated.rb response and asserts:
#   - It shows the upstream commit SHA.
#   - It reports lockfiles and patterns absent from internal/intel/generated.go.
#   - It does not report lockfiles already present (package-lock.json).
#   - It does not rewrite Go files.
#   - It is never called during a normal runtime path (offline).

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tmproot.sh
source "$HERE/tmproot.sh"
ROOT="$HERE/.."

pass=0; fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" || notok "$1" "output contains '$3'" "$2"; }
hasnt() { [[ "$2" != *"$3"* ]] && ok "$1" || notok "$1" "output does not contain '$3'" "$2"; }

SCRIPT="$ROOT/scripts/refresh-generated-rules.sh"
GO_FILE="$ROOT/internal/intel/generated.go"

# 1. Script existence and executable permission
[[ -f "$SCRIPT" ]] && ok "scripts/refresh-generated-rules.sh exists" \
  || notok "scripts/refresh-generated-rules.sh exists" "file at $SCRIPT" "file not found"
[[ -x "$SCRIPT" ]] && ok "scripts/refresh-generated-rules.sh is executable" \
  || notok "scripts/refresh-generated-rules.sh is executable" "executable bit set" "not executable"

# 2. Stubbed upstream response
stub_dir="$(mktemp -d)"
stub_rb="$stub_dir/generated.rb"
stub_sha="767853446a7763ee1d1f04c8bdc4d2f92d834037"

cat >"$stub_rb" <<'EOF'
module Linguist
  class Generated
    def package_lock?
      !!name.match(/package-lock\.json/)
    end

    def bogus_lock?
      !!name.match(/bogus-new\.lock/)
    end

    def compiled_ts?
      !!name.match(/\.compiled\.ts/)
    end
  end
end
EOF

# Record mtime and hash of internal/intel/generated.go before running
hash_before="$(shasum -a 256 "$GO_FILE" 2>/dev/null || sha256sum "$GO_FILE" 2>/dev/null || cksum "$GO_FILE")"

# 3. Run script with stubbed file and commit
status=0
out="$(CROSSREV_LINGUIST_URL="file://$stub_rb" CROSSREV_LINGUIST_COMMIT="$stub_sha" bash "$SCRIPT" 2>&1)" || status=$?

[[ "$status" -eq 0 ]] && ok "the script exits 0" || notok "the script exits 0" "exit 0" "exit $status: $out"

# Assert upstream commit shown
has "output shows the upstream commit" "$out" "$stub_sha"

# Assert missing lockfile reported
has "reports bogus-new.lock as absent lockfile" "$out" "bogus-new.lock"

# Assert missing pattern reported
has "reports \\.compiled\\.ts as absent pattern" "$out" '\.compiled\.ts'

# Assert already-present lockfile is NOT reported
hasnt "does not report package-lock.json as absent" "$out" "package-lock.json"

# Assert Go file was not modified
hash_after="$(shasum -a 256 "$GO_FILE" 2>/dev/null || sha256sum "$GO_FILE" 2>/dev/null || cksum "$GO_FILE")"
[[ "$hash_before" == "$hash_after" ]] && ok "internal/intel/generated.go was not modified" \
  || notok "internal/intel/generated.go was not modified" "$hash_before" "$hash_after"

# 4. Verify crossrev runtime does not execute the maintainer script
if command -v git >/dev/null 2>&1; then
  # Comments explaining the maintainer script are expected, but no Go code should execute it
  ref="$(git -C "$ROOT" grep -n "refresh-generated-rules.sh" internal/ cmd/ 2>/dev/null | grep -v '^[a-zA-Z0-9_/.-]*:[0-9]*:[[:space:]]*//' || true)"
  [[ -z "$ref" ]] && ok "refresh-generated-rules.sh is not executed in runtime Go code" \
    || notok "refresh-generated-rules.sh is not executed in runtime Go code" "empty" "$ref"
fi

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
