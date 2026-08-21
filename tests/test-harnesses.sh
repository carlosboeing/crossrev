#!/usr/bin/env bash
#
# tests/test-harnesses.sh — tests for the harness descriptor and its validator.
#
# Covers descriptor loading, query helpers, filesystem-matching rules, and the
# ten validation failure cases.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"

# shellcheck source=tests/harness.sh
source "$HERE/harness.sh"

# shellcheck source=lib/ui.sh
source "$ROOT/lib/ui.sh"
# shellcheck source=lib/harnesses.sh
source "$ROOT/lib/harnesses.sh"

# --- shipped descriptor ---------------------------------------------------
unset CROSSREV_HARNESS_FILE
HARNESS_JSON=""
harness_load
is "the shipped descriptor loads cleanly" "$?" 0

names="$(harness_names | paste -sd, -)"
is "harness_names prints claude,codex,agy,grok in order" "$names" "claude,codex,agy,grok"

is "harness_get codex secret" "$(harness_get codex .credential.secret)" "CROSSREV_CODEX_AUTH"
is "harness_get agy secret is empty" "$(harness_get agy .credential.secret)" ""

store_agy="$(harness_get agy .credential.store)"
has "harness_get agy store names antigravity-oauth-token" "$store_agy" "antigravity-oauth-token"
hasnt "harness_get agy store does not contain oauth_creds" "$store_agy" "oauth_creds"

is "harness_get agy archetype is C" "$(harness_get agy .credential.archetype)" "C"
is "harness_get agy provenance is measured" "$(harness_get agy .credential.provenance)" "measured"

is "harness_get grok archetype is C" "$(harness_get grok .credential.archetype)" "C"
is "harness_get grok provenance is measured" "$(harness_get grok .credential.provenance)" "measured"
is "harness_get grok secret" "$(harness_get grok .credential.secret)" "CROSSREV_GROK_AUTH"
is "harness_get grok staging env is GROK_HOME" "$(harness_get grok .credential.staging.env)" "GROK_HOME"
is "harness_get_json grok quarantine is .grok" "$(harness_get_json grok .quarantine)" '[".grok"]'
hasnt "quarantine_shared no longer lists .grok" "$(harness_field .quarantine_shared)" ".grok"

is "harness_field endpoint_host is claude" "$(harness_field .endpoint_host)" "claude"

reason="$(harness_not_driven kimi)"
is "harness_not_driven kimi exits 0" "$?" 0
has "harness_not_driven kimi prints reason" "$reason" "reached through the claude adapter"

! harness_not_driven claude >/dev/null 2>&1
is "harness_not_driven claude exits 1" "$?" 0

# Adapter file correspondence
missing_adapter=0
while IFS= read -r n; do
  [[ -r "$ROOT/lib/adapters/$n.sh" ]] || missing_adapter=1
done < <(harness_names)
is "every driven harness has a file at lib/adapters/<name>.sh" "$missing_adapter" 0

orphan_adapter=0
for f in "$ROOT"/lib/adapters/*.sh; do
  n="$(basename "$f" .sh)"
  harness_known "$n" || orphan_adapter=1
done
is "every lib/adapters/*.sh has a descriptor entry" "$orphan_adapter" 0

# Generated runner guidance follows the same credential conditions as preflight.
readme_harness_table="$(sed -n '/<!-- crossrev:harness-table:start -->/,/<!-- crossrev:harness-table:end -->/p' "$ROOT/README.md")"
credentials_harness_table="$(sed -n '/<!-- crossrev:harness-table:start -->/,/<!-- crossrev:harness-table:end -->/p' "$ROOT/docs/credentials.md")"
has "generated README says seedable grok survives a hosted runner" "$readme_harness_table" '| `grok` | `~/.grok/auth.json` | 6 hours | Yes, by self-refreshing |'
has "generated README keeps unseedable agy off hosted runners" "$readme_harness_table" '| 56 minutes | No, CrossRev cannot seed into a hosted runner yet |'
has "generated credentials say seedable grok works on hosted runners" "$credentials_harness_table" '| `grok` | `~/.grok/auth.json` | 6 hours | Works, by self-refreshing |'
has "generated credentials keep unseedable agy on self-hosted runners" "$credentials_harness_table" '| 56 minutes | Use a self-hosted runner |'

# --- validation contract (10 rejection cases) ------------------------------
valid_json="$(<"$ROOT/lib/harnesses.json")"

test_validation_reject() {
  local label="$1" bad_json="$2" expected_snippet="$3"
  local tmp msg
  tmp="$(mktemp)"
  printf '%s' "$bad_json" >"$tmp"
  msg="$(harness_validate "$bad_json")"
  rm -f "$tmp"
  has "$label" "$msg" "$expected_snippet"
}

# 1. wrong version
bad="$(jq '.version = 2' <<<"$valid_json")"
test_validation_reject "rejects wrong version" "$bad" "reads version 1"

# 2. harnesses given as object rather than array
bad="$(jq '.harnesses = {}' <<<"$valid_json")"
test_validation_reject "rejects harnesses object" "$bad" "must be arrays"

# 3. empty harness array
bad="$(jq '.harnesses = []' <<<"$valid_json")"
test_validation_reject "rejects empty harness array" "$bad" "names no harnesses"

# 4. duplicate harness name
bad="$(jq '.harnesses += [.harnesses[0]]' <<<"$valid_json")"
test_validation_reject "rejects duplicate harness name" "$bad" "appears more than once"

# 5. name outside [a-z][a-z0-9-]*
bad="$(jq '.harnesses[0].name = "Claude_Code"' <<<"$valid_json")"
test_validation_reject "rejects bad harness name pattern" "$bad" "is not [a-z][a-z0-9-]*"

# 6. not_driven name collision or duplication
bad="$(jq '.not_driven += [{name: "claude", reason: "dup"}]' <<<"$valid_json")"
test_validation_reject "rejects not_driven collision with driven harness" "$bad" "is also a driven harness"

# 7. environment variable name outside [A-Z_][A-Z0-9_]*
bad="$(jq '.harnesses[0].credential.secret = "bad-secret-name"' <<<"$valid_json")"
test_validation_reject "rejects invalid env var name" "$bad" "is not [A-Z_][A-Z0-9_]*"

# 8. out-of-range enum
bad="$(jq '.harnesses[0].credential.archetype = "Z"' <<<"$valid_json")"
test_validation_reject "rejects out-of-range archetype" "$bad" "out-of-range"

# 9. path absolute or containing ..
bad="$(jq '.harnesses[0].quarantine += ["../etc/passwd"]' <<<"$valid_json")"
test_validation_reject "rejects path with .." "$bad" "contains a .. segment"

# 10. installer whose command omits the version it claims to pin
bad="$(jq '.harnesses[0].install.pinned_version = "9.9.9"' <<<"$valid_json")"
test_validation_reject "rejects installer command omitting pinned version" "$bad" "pinned version its command does not carry"

finish
