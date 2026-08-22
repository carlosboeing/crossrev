#!/usr/bin/env bash
#
# Config, endpoint and backlog-discovery tests.
#
# All of this is deterministic logic whose failure is silent: a config layer
# that quietly loses a value, an endpoint that falls back to the wrong vendor,
# a backlog destination that writes somewhere nobody asked for. None of it needs a network, a
# harness or a pull request.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CROSSREV="$HERE/../bin/crossrev"

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
out="$($CROSSREV config show)"
is "no config: mode is local"             "$(jq -r .mode <<<"$out")"              "local"
is "no config: codex reviews"             "$(jq -r .reviewer.harness <<<"$out")"  "codex"
is "no config: claude resolves"          "$(jq -r .resolver.harness <<<"$out")" "claude"
is "no config: no endpoints are defined"  "$(jq -r '.endpoints|length' <<<"$out")" "0"
is "no config: nothing demands an API key" "$(jq -r '[.endpoints[]?.token_env]|length' <<<"$out")" "0"
is "no config: three passes per cycle"     "$(jq -r .policy.max_passes_per_cycle <<<"$out")" "3"
is "no config: medium is the minimum fix severity" "$(jq -r .policy.min_fix_severity <<<"$out")" "medium"
is "no config: automation hints are enabled" "$(jq -r .enable_automation_hint <<<"$out")" "true"

# --- template/default drift -----------------------------------------------
#
# Compare both directions. Defaults missing from the template contradict what
# init teaches an operator; template-only keys are worse, because the code can
# stop reading one while the hand-written file keeps promising it works.
flatten_config() {
  jq -r '
    def exempt($k):
      $k == "mode"
      or ($k | startswith("endpoints."))
      or ($k | startswith("reviewer."))
      or ($k | startswith("resolver."))
      or $k == "backlog.destination"
      or $k == "backlog.repository.path"
      or ($k | startswith("backlog.github_issues.labels"));
    paths(scalars | true) as $p
    | ($p | map(tostring) | join(".")) as $k
    | select(exempt($k) | not)
    | [$k, (getpath($p) | tojson)] | @tsv
  ' <<<"$1" | sort
}

defaults="$(bash -c 'source "$1"; source "$2"; _cfg_defaults' _ "$HERE/../lib/harnesses.sh" "$HERE/../lib/config.sh")"
template="$(yq -o=json -I=0 '.' "$HERE/../templates/crossrev.yml")"
flat_defaults="$(flatten_config "$defaults")"
flat_template="$(flatten_config "$template")"
is "the drift comparison covers twelve behavior leaves in both documents" \
  "$(printf '%s\n%s' "$(wc -l <<<"$flat_defaults" | tr -d ' ')" "$(wc -l <<<"$flat_template" | tr -d ' ')" | paste -sd / -)" "12/12"
is "every non-exempt default exists in the template with the same value" \
  "$(comm -23 <(printf '%s\n' "$flat_defaults") <(printf '%s\n' "$flat_template"))" ""
is "every non-exempt template leaf is implemented by the defaults" \
  "$(comm -13 <(printf '%s\n' "$flat_defaults") <(printf '%s\n' "$flat_template"))" ""

# --- version ---------------------------------------------------------------
d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
printf 'version: 99\n' > .github/crossrev.yml
err="$($CROSSREV config show 2>&1 >/dev/null)"; rc=$?
is  "version mismatch exits non-zero" "$rc" "1"
has "version mismatch names both versions" "$err" "declares version 99"

# --- the fixing threshold --------------------------------------------------
#
# A typo here used to read as a clean review: an unrecognised threshold ranks
# zero, zero meets nothing, so no finding counted as actionable and the pass
# reported converged with the bug still on the pull request. The value is
# refused at load instead, where the message can name it.
d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
printf 'version: 1\npolicy:\n  min_fix_severity: medum\n' > .github/crossrev.yml
err="$($CROSSREV config show 2>&1 >/dev/null)"; rc=$?
is  "a misspelt min_fix_severity exits non-zero" "$rc" "1"
has "and the error names the bad value"    "$err" "policy.min_fix_severity is 'medum'"
has "and says what the valid values are"   "$err" "high, medium or low"

d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
printf 'version: 1\nresolver:\n  harness: claude\n' > .github/crossrev.yml
out="$($CROSSREV config show)"; rc=$?
is "a resolver block keeps the policy default"         "$(jq -r .policy.min_fix_severity <<<"$out")" "medium"
is "and loading it is not an error"                    "$rc" "0"

for level in high medium low; do
  d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
  printf 'version: 1\npolicy:\n  min_fix_severity: %s\n' "$level" > .github/crossrev.yml
  is "min_fix_severity $level is accepted" "$($CROSSREV config show | jq -r .policy.min_fix_severity)" "$level"
done

# --- the git hooks switch --------------------------------------------------
#
# Read leniently, a typo falls through to whichever branch is not `run`, so a
# repository that asked to keep its hooks commits without them and nothing says
# so. Refused at load, where the message can still name the key. See ADR 0017.
d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
printf 'version: 1\ngit:\n  hooks: skipp\n' > .github/crossrev.yml
err="$($CROSSREV config show 2>&1 >/dev/null)"; rc=$?
is  "a misspelt git.hooks exits non-zero"  "$rc" "1"
has "and the error names the bad value"    "$err" "git.hooks is 'skipp'"
has "and says what the valid values are"   "$err" "skip or run"

is "no config: the resolver's commit skips this repository's hooks" \
  "$(cd "$(new_repo)" && $CROSSREV config show | jq -r .git.hooks)" "skip"

for setting in skip run; do
  d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
  printf 'version: 1\ngit:\n  hooks: %s\n' "$setting" > .github/crossrev.yml
  is "git.hooks $setting is accepted" "$($CROSSREV config show | jq -r .git.hooks)" "$setting"
done

# --- the pass bound --------------------------------------------------------
#
# Zero is the orchestrator's own sentinel for "no pass bound applies to this
# invocation", which is what lets a person ask for one attended pass past the
# bound. An operator writing zero into the cap lands on it from the other side,
# and the two readers then disagree: the automatic loop reads no bound and keeps
# starting passes, while `cmd_cycle` compares the pass number and stops before
# the first one. Refused at load, where the message can still name the key.
for bad in 0 -1 three; do
  d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
  printf 'version: 1\npolicy:\n  max_passes_per_cycle: %s\n' "$bad" > .github/crossrev.yml
  err="$($CROSSREV config show 2>&1 >/dev/null)"; rc=$?
  is  "max_passes_per_cycle $bad exits non-zero"   "$rc" "1"
  has "and the error names the bad value"          "$err" "max_passes_per_cycle is '$bad'"
  has "and says the smallest value that works"     "$err" "Set it to 1 or more"
done

d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
printf 'version: 1\npolicy:\n  max_passes_per_cycle: 1\n' > .github/crossrev.yml
is "a single pass per cycle is accepted" \
  "$($CROSSREV config show | jq -r .policy.max_passes_per_cycle)" "1"

# --- precedence ------------------------------------------------------------
d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
printf 'version: 1\npolicy:\n  max_passes_per_cycle: 5\nreviewer:\n  harness: claude\n' > .github/crossrev.yml
printf 'version: 1\npolicy:\n  max_passes_per_cycle: 9\n' > .crossrev.yml
out="$($CROSSREV config show)"
is ".github/crossrev.yml wins over .crossrev.yml" "$(jq -r .policy.max_passes_per_cycle <<<"$out")" "5"
is "unset keys keep their default"              "$(jq -r .policy.max_prs_per_day <<<"$out")" "25"

# --- endpoint merge --------------------------------------------------------
d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
printf 'version: 1\nendpoints:\n  kimi:\n    base_url: https://public.example/\n    token_env: KIMI_API_KEY\n' > .github/crossrev.yml
mkdir -p "$XDG_CONFIG_HOME/crossrev"
printf 'version: 1\nendpoints:\n  kimi:\n    base_url: http://mine.local/\n    token_env: KIMI_API_KEY\n' > "$XDG_CONFIG_HOME/crossrev/config.yml"
out="$($CROSSREV config show)"
is "operator endpoint wins for the same name" "$(jq -r '.endpoints.kimi.base_url' <<<"$out")" "http://mine.local/"
rm -f "$XDG_CONFIG_HOME/crossrev/config.yml"

# --- backlog discovery -----------------------------------------------------
d="$(new_repo)"; cd "$d" || exit 1
is "nothing declared or installed resolves to none" "$($CROSSREV config backlog)" "  deferred work would go to: none"

printf '# x\n\n## Project Map\n\n- **Tracker**: GitHub Issues\n' > AGENTS.md
is "Project Map GitHub Issues routes to GitHub issues" "$($CROSSREV config backlog)" "  deferred work would go to: github_issues"

printf '# x\n\n## Project Map\n\n- **Tracker**: none\n' > AGENTS.md
is "Project Map none stops probing" "$($CROSSREV config backlog)" "  deferred work would go to: none"

rm AGENTS.md; touch backlog.config.yml
is "an installed Backlog config uses the folder layout" "$($CROSSREV config backlog)" "  deferred work would go to: repository folder backlog/tasks"

rm backlog.config.yml; touch BACKLOG.md
is "a root BACKLOG.md uses the file layout" "$($CROSSREV config backlog)" "  deferred work would go to: repository file BACKLOG.md"

rm BACKLOG.md; touch TODO.md
is "a root TODO.md uses the file layout" "$($CROSSREV config backlog)" "  deferred work would go to: repository file TODO.md"

# Tier 1 beats tier 2: a declaration is not overridden by a sniff.
printf '# x\n\n## Project Map\n\n- **Tracker**: GitHub Issues\n' > AGENTS.md
is "a declaration beats a sniffed convention" "$($CROSSREV config backlog)" "  deferred work would go to: github_issues"

# The bare-`none` case above passes with TODO.md absent, so it cannot
# catch a gloss being read as part of the value. This one can: a sniffable
# convention is on disk, so a `none` that fails to match resolves to it instead.
printf '# x\n\n## Project Map\n\n- **Tracker**: none (ROADMAP.md is the single source of truth for "what next" — no separate tracker)\n' > AGENTS.md
is "a glossed none still stops probing" "$($CROSSREV config backlog)" "  deferred work would go to: none"

printf '# x\n\n## Project Map\n\n- **Tracker**: docs/BACKLOG.md (newest at the top)\n' > AGENTS.md
is "a glossed path keeps the path and infers its layout" "$($CROSSREV config backlog)" "  deferred work would go to: repository file docs/BACKLOG.md"

# A repository key is explicit only when it appears in the repository config,
# not when it arrives through the merged defaults. A stated layout constrains
# the candidates rather than reinterpreting a path that belongs to another one.
d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github; touch BACKLOG.md
printf 'version: 1\nbacklog:\n  destination: repository\n' >.github/crossrev.yml
is "with neither repository key stated the sniff decides both" \
  "$($CROSSREV config backlog)" "  deferred work would go to: repository file BACKLOG.md"

printf 'version: 1\nbacklog:\n  destination: repository\n  repository:\n    layout: folder\n' >.github/crossrev.yml
is "an explicit folder layout skips a file convention" \
  "$($CROSSREV config backlog)" "  deferred work would go to: repository folder .crossrev/backlog"

rm BACKLOG.md; mkdir -p backlog; touch backlog/config.yml
printf 'version: 1\nbacklog:\n  destination: repository\n  repository:\n    layout: file\n' >.github/crossrev.yml
is "an explicit file layout skips a folder convention" \
  "$($CROSSREV config backlog)" "  deferred work would go to: repository file .crossrev/backlog.md"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
