# shellcheck shell=bash
#
# Restore the CROSSREV_* variables the suite set, for a stub whose parent handed
# it an environment allowlist rather than the whole one.
#
# Sourced by every stub except codex, which is a tripwire and reads nothing.
#
# # Why this file exists
#
# The stubs are configured through the environment: tests/stub/gh reads its
# route table from CROSSREV_GH_ROUTES, the harness stubs read their payload from
# CROSSREV_REVIEW_PAYLOAD, and so on. bin/crossrev hands every child the whole
# environment it holds, because a shell function runs in the shell that called
# it, so a stub it starts already carries all of them and this file changes
# nothing on that path.
#
# The native binary hands each child an exact allowlist (Inherit, in
# internal/exec — the ADR 0001 boundary). A test-only name is not on it and must
# not be: an allowlist that carried one would be a production boundary widened
# for a fixture. So tests/harness.sh snapshots the names into a file first, and
# this reads them back.
#
# # The one rule
#
# A variable already set wins. The environment the stub was started with is the
# more specific fact, so a per-case `CROSSREV_X=y "$CROSSREV" …` prefix still
# means what it says, and a stale snapshot cannot override a fresh value.
#
# The file is written by tests/harness.sh with `printf %q`, one `NAME=value` per
# line, so a value carrying a space, a quote or a newline survives and no line
# is ever split. eval is over this shell's own generated text and nothing else.

_stub_env_file() { printf '%s/crossrev-stub.env' "${XDG_CONFIG_HOME:-$HOME/.config}"; }

_stub_env_load() {
  local file name line
  file="$(_stub_env_file)"
  [[ -r "$file" ]] || return 0
  while IFS= read -r line; do
    # The `export ` prefix comes off before the name is read back. Left on, the
    # name is `export CROSSREV_GH_LOG`, `${!name}` is empty for every line, and
    # the already-set rule below never fires — so a stale snapshot silently
    # replaced a value the caller had just set.
    name="${line#export }"
    name="${name%%=*}"
    [[ -n "$name" && "$name" != "$line" ]] || continue
    [[ -n "${!name:-}" ]] && continue
    eval "export $line"
  done <"$file"
}

_stub_env_load
