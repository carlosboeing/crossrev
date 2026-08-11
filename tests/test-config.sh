#!/usr/bin/env bash
#
# Config, endpoint and sink-discovery tests.
#
# All of this is deterministic logic whose failure is silent: a config layer
# that quietly loses a value, an endpoint that falls back to the wrong vendor,
# a sink that writes somewhere nobody asked for. None of it needs a network, a
# harness or a pull request.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REVLOOP="$HERE/../bin/revloop"

pass=0 fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }
has()   { [[ "$2" == *"$3"* ]] && ok "$1" || notok "$1" "contains '$3'" "$2"; }

# Each case gets a clean repo, and an operator config path that is not yours —
# a test that reads the developer's real ~/.config would pass or fail depending
# on whose machine it ran on.
new_repo() {
  local d; d="$(mktemp -d)"
  ( cd "$d" && git init -q . && git commit -q --allow-empty -m init )
  printf '%s' "$d"
}
XDG_CONFIG_HOME="$(mktemp -d)"; export XDG_CONFIG_HOME

# --- defaults --------------------------------------------------------------
d="$(new_repo)"; cd "$d" || exit 1
out="$($REVLOOP config show)"
is "no config: mode is single-run"        "$(jq -r .mode <<<"$out")"              "single-run"
is "no config: codex reviews"             "$(jq -r .reviewer.harness <<<"$out")"  "codex"
is "no config: claude addresses"          "$(jq -r .addresser.harness <<<"$out")" "claude"
is "no config: no endpoints are defined"  "$(jq -r '.endpoints|length' <<<"$out")" "0"
is "no config: nothing demands an API key" "$(jq -r '[.endpoints[]?.token_env]|length' <<<"$out")" "0"

# --- version ---------------------------------------------------------------
d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
printf 'version: 99\n' > .github/revloop.yml
err="$($REVLOOP config show 2>&1 >/dev/null)"; rc=$?
is  "version mismatch exits non-zero" "$rc" "1"
has "version mismatch names both versions" "$err" "declares version 99"

# --- precedence ------------------------------------------------------------
d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
printf 'version: 1\nmax_passes: 5\nreviewer:\n  harness: claude\n' > .github/revloop.yml
printf 'version: 1\nmax_passes: 9\n' > .revloop.yml
out="$($REVLOOP config show)"
is ".github/revloop.yml wins over .revloop.yml" "$(jq -r .max_passes <<<"$out")" "5"
is "unset keys keep their default"              "$(jq -r .caps.runs_per_day <<<"$out")" "12"

# --- endpoint merge --------------------------------------------------------
d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
printf 'version: 1\nendpoints:\n  kimi:\n    base_url: https://public.example/\n    token_env: KIMI_API_KEY\n' > .github/revloop.yml
mkdir -p "$XDG_CONFIG_HOME/revloop"
printf 'version: 1\nendpoints:\n  kimi:\n    base_url: http://mine.local/\n    token_env: KIMI_API_KEY\n' > "$XDG_CONFIG_HOME/revloop/config.yml"
out="$($REVLOOP config show)"
is "operator endpoint wins for the same name" "$(jq -r '.endpoints.kimi.base_url' <<<"$out")" "http://mine.local/"
rm -f "$XDG_CONFIG_HOME/revloop/config.yml"

# --- sink discovery --------------------------------------------------------
d="$(new_repo)"; cd "$d" || exit 1
is "nothing declared or installed resolves to none" "$($REVLOOP config sink)" "  deferred work would go to: none"

printf '# x\n\n## Project Map\n\n- **Tracker**: GitHub Issues\n' > AGENTS.md
is "Project Map GitHub Issues routes to the issue sink" "$($REVLOOP config sink)" "  deferred work would go to: issues"

printf '# x\n\n## Project Map\n\n- **Tracker**: none\n' > AGENTS.md
is "Project Map none stops probing" "$($REVLOOP config sink)" "  deferred work would go to: none"

rm AGENTS.md; touch backlog.config.yml
is "an installed Backlog.md is used in its own format" "$($REVLOOP config sink)" "  deferred work would go to: file backlog/tasks"

rm backlog.config.yml; touch TODO.md
is "a root TODO.md is used" "$($REVLOOP config sink)" "  deferred work would go to: file TODO.md"

rm TODO.md; mkdir -p docs; touch docs/ROADMAP.md
is "docs/ROADMAP.md is the last sniffed convention" "$($REVLOOP config sink)" "  deferred work would go to: file docs/ROADMAP.md"

# Tier 1 beats tier 2: a declaration is not overridden by a sniff.
printf '# x\n\n## Project Map\n\n- **Tracker**: GitHub Issues\n' > AGENTS.md
is "a declaration beats a sniffed convention" "$($REVLOOP config sink)" "  deferred work would go to: issues"

# The bare-`none` case above passes with docs/ROADMAP.md absent, so it cannot
# catch a gloss being read as part of the value. This one can: a sniffable
# convention is on disk, so a `none` that fails to match resolves to it instead.
printf '# x\n\n## Project Map\n\n- **Tracker**: none (ROADMAP.md is the single source of truth for "what next" — no separate tracker)\n' > AGENTS.md
is "a glossed none still stops probing" "$($REVLOOP config sink)" "  deferred work would go to: none"

printf '# x\n\n## Project Map\n\n- **Tracker**: docs/BACKLOG.md (newest at the top)\n' > AGENTS.md
is "a glossed path keeps the path and drops the gloss" "$($REVLOOP config sink)" "  deferred work would go to: file docs/BACKLOG.md"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
