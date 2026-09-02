#!/usr/bin/env bash
#
# The snapshot the native wrapper writes: who may read it, and what it holds.
#
# tests/harness.sh wraps the native binary in a shell script that copies every
# exported CROSSREV_* name into $XDG_CONFIG_HOME/crossrev-stub.env, because the
# binary hands each child an exact allowlist and a test-only name is not on it.
# tests/stub/_stub-env.sh reads the file back.
#
# Two properties of that file are worth holding, and neither is visible from any
# other suite:
#
#   - It is created under umask 077, so it is readable only by its owner. It was
#     created under the process umask, which on a developer's machine is 022,
#     and the file came out world-readable.
#   - It carries no credential. A machine with CROSSREV_CODEX_AUTH,
#     CROSSREV_GROK_AUTH or CROSSREV_OPENCODE_AUTH exported — which is every
#     machine that has ever run an automated leg locally — wrote the real value
#     into it. No stub reads a name ending _AUTH: measured with
#     `grep -rn AUTH tests/stub/`, which answers only the comment headers.
#
# This suite calls the wrapper directly with a target that prints the file
# rather than running crossrev, so it says nothing about either implementation
# and needs no binary.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=harness.sh
source "$HERE/harness.sh"

# The ambient umask a developer's shell has, so a wrapper that does not set its
# own leaves a world-readable file and this suite says so.
umask 022

# A target that reports the snapshot instead of running anything. It runs while
# the file exists, which is the only moment it does: the wrapper removes it as
# soon as the target returns.
reader_dir="$(mktemp -d)"
cat >"$reader_dir/reader" <<'READER'
#!/usr/bin/env bash
set -uo pipefail
file="${XDG_CONFIG_HOME:-$HOME/.config}/crossrev-stub.env"
# GNU stat first, BSD stat second: the suite runs on both.
stat -c '%a' "$file" 2>/dev/null || stat -f '%Lp' "$file"
cat "$file"
READER
chmod +x "$reader_dir/reader"

# The names a stub does read, and the three credentials it never does. The
# values are obvious fakes; a test that printed a real one would be the leak it
# is testing for.
export CROSSREV_GH_ROUTES="$reader_dir/routes"
export CROSSREV_CODEX_AUTH="not-a-real-codex-credential"
export CROSSREV_GROK_AUTH="not-a-real-grok-credential"
export CROSSREV_OPENCODE_AUTH="not-a-real-opencode-credential"

wrapper="$(_stub_env_wrapper "$reader_dir/reader")"
out="$("$wrapper")"
mode="$(printf '%s\n' "$out" | head -1)"
body="$(printf '%s\n' "$out" | tail -n +2)"

is "the snapshot is readable only by its owner" "$mode" "600"

has "and it still carries a name a stub reads" "$body" "CROSSREV_GH_ROUTES"

hasnt "the codex credential is not in it"     "$body" "CROSSREV_CODEX_AUTH"
hasnt "nor the grok one"                      "$body" "CROSSREV_GROK_AUTH"
hasnt "nor the opencode one"                  "$body" "CROSSREV_OPENCODE_AUTH"
hasnt "and no value of one either"            "$body" "not-a-real-codex-credential"
hasnt "not the grok value"                    "$body" "not-a-real-grok-credential"
hasnt "not the opencode value"                "$body" "not-a-real-opencode-credential"

# The exclusion is by suffix rather than by the three names that ship today, so
# a harness added to lib/harnesses.json with its own secret is covered without
# anyone remembering to come back here.
export CROSSREV_UNSHIPPED_AUTH="not-a-real-credential-for-a-harness-that-does-not-exist"
out="$("$wrapper")"
hasnt "a credential for a harness that does not ship yet is out too" \
  "$(printf '%s\n' "$out" | tail -n +2)" "CROSSREV_UNSHIPPED_AUTH"

# The file exists for the invocation and no longer, which is what stops a stub
# invoked directly afterwards from reading a stale value back.
snapshot="$XDG_CONFIG_HOME/crossrev-stub.env"
is "and the snapshot does not outlive the run" "$([[ -e "$snapshot" ]] && printf yes || printf no)" "no"

rm -rf "$reader_dir"
finish
