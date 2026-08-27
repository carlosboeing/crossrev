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
is "the drift comparison covers fourteen behavior leaves in both documents" \
  "$(printf '%s\n%s' "$(wc -l <<<"$flat_defaults" | tr -d ' ')" "$(wc -l <<<"$flat_template" | tr -d ' ')" | paste -sd / -)" "14/14"
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

# --- the run record ----------------------------------------------------------
#
# `false` must survive the read: jq's `//` treats false as empty, so a lenient
# read of the default would report the key unset and refuse every config.
d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
printf 'version: 1\n' > .github/crossrev.yml
out="$($CROSSREV config show)"; rc=$?
is "no config: the log defaults load without error" "$rc" "0"
is "no config: retention defaults to 14 days"       "$(jq -r .logs.retention_days <<<"$out")" "14"
is "no config: transcripts default to failure-only" "$(jq -r .logs.keep_transcripts <<<"$out")" "false"

for bad in 0 -1 fortnight; do
  d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
  printf 'version: 1\nlogs:\n  retention_days: %s\n' "$bad" > .github/crossrev.yml
  err="$($CROSSREV config show 2>&1 >/dev/null)"; rc=$?
  is  "logs.retention_days $bad exits non-zero" "$rc" "1"
  has "and the error names the bad value"       "$err" "logs.retention_days is '$bad'"
done

d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
printf 'version: 1\nlogs:\n  keep_transcripts: maybe\n' > .github/crossrev.yml
err="$($CROSSREV config show 2>&1 >/dev/null)"; rc=$?
is  "a non-boolean keep_transcripts exits non-zero" "$rc" "1"
has "and the error names the bad value"             "$err" "logs.keep_transcripts is 'maybe'"
has "and says what the valid values are"            "$err" "true or false"

d="$(new_repo)"; cd "$d" || exit 1; mkdir -p .github
printf 'version: 1\nlogs:\n  retention_days: 30\n  keep_transcripts: true\n' > .github/crossrev.yml
out="$($CROSSREV config show)"
is "logs.retention_days 30 is accepted"      "$(jq -r .logs.retention_days <<<"$out")" "30"
is "logs.keep_transcripts true is accepted"  "$(jq -r .logs.keep_transcripts <<<"$out")" "true"

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

# --- the backlog path guard -------------------------------------------------
#
# The value ends in a file write, so it is bounded rather than trusted. The
# guard used to resolve it with python3 and fall back to raw concatenation
# whenever that call failed, which let `../` traversal through on every host
# where python3 is absent — and python3 appears in none of CrossRev's
# dependency lists.
source "$HERE/../lib/ui.sh"
# shellcheck source=../lib/config.sh
source "$HERE/../lib/config.sh"

d="$(new_repo)"; cd "$d" || exit 1

cfg_assert_path_inside_repo "backlog/tasks" 2>/dev/null \
  && ok "a plain relative path is allowed" \
  || notok "a plain relative path is allowed" "exit 0" "exit $?"

cfg_assert_path_inside_repo "sub/../sibling" 2>/dev/null \
  && ok "a path that re-enters and stays inside is allowed" \
  || notok "a path that re-enters and stays inside is allowed" "exit 0" "exit $?"

cfg_assert_path_inside_repo "." 2>/dev/null \
  && ok "the checkout root itself is allowed" \
  || notok "the checkout root itself is allowed" "exit 0" "exit $?"

err="$(cfg_assert_path_inside_repo "../../etc" 2>&1 >/dev/null)"; rc=$?
is  "traversal above the checkout exits non-zero" "$rc" "1"
has "and names the configured path"               "$err" "'../../etc'"

err="$(cfg_assert_path_inside_repo "/etc" 2>&1 >/dev/null)"; rc=$?
is "an absolute path exits non-zero" "$rc" "1"

err="$(cfg_assert_path_inside_repo "sub/../../outside" 2>&1 >/dev/null)"; rc=$?
is "traversal that re-enters and then leaves exits non-zero" "$rc" "1"

# Resolution may not lean on python3: it is not a dependency anywhere, and a
# fallback that fires whenever it is missing turns the guard off entirely.
no_py_bin="$(mktemp -d)"
ln -s "$(command -v git)" "$no_py_bin/git"
ln -s "$(command -v jq)" "$no_py_bin/jq"
BASH_ABS="$(command -v bash)"
repo_lib="$(cd "$HERE/.." && pwd)"
err="$(PATH="$no_py_bin" "$BASH_ABS" -c '
  source "$1/lib/ui.sh"; source "$1/lib/config.sh"
  cfg_assert_path_inside_repo "../../etc"' _ "$repo_lib" 2>&1)"; rc=$?
is  "traversal is refused with nothing but git and jq on PATH" "$rc" "1"
has "and the refusal says why"                                 "$err" "outside the repository"

PATH="$no_py_bin" "$BASH_ABS" -c '
  source "$1/lib/ui.sh"; source "$1/lib/config.sh"
  cfg_assert_path_inside_repo "backlog/tasks"' _ "$repo_lib" 2>/dev/null \
  && ok "and a plain path is still accepted there" \
  || notok "and a plain path is still accepted there" "exit 0" "exit $?"

# --- a fatal inside a command substitution stops the caller ------------------
#
# ui_die ends with `exit 1`, which inside `$( )` ends only that subshell. The
# caller then carries on with whatever the substitution captured. bin/crossrev
# sets -e, but bash suppresses it for the whole body of a function invoked as
# part of an || list, and ctx_load is invoked exactly that way at lib/run.sh:944,
# 1763 and 2921. So the guard set -e appears to give these paths is not there.
#
# Each case below reproduces that shape: a function called with || that reaches
# the substitution. One refusal must print, and nothing after it.
repo_lib="$(cd "$HERE/.." && pwd)"

guarded() { # <xdg_home> <repo_dir> <body>
  XDG_CONFIG_HOME="$1" "$BASH_ABS" -c '
    set -euo pipefail
    cd "$2"
    source "$1/lib/ui.sh"; source "$1/lib/harnesses.sh"; source "$1/lib/config.sh"
    source "$1/lib/github.sh" 2>/dev/null || true
    # Captured first: inside a function, $3 means that function third argument.
    body="$3"
    guarded_caller() { eval "$body"; }
    guarded_caller || true
  ' _ "$repo_lib" "$2" "$3" 2>&1
}

# 1. The repository config, at lib/config.sh:112.
d="$(new_repo)"; mkdir -p "$d/.github"
printf 'version: 1\npolicy:\n  - not\n  a mapping: [unclosed\n' > "$d/.github/crossrev.yml"
xdg="$(mktemp -d)"
err="$(guarded "$xdg" "$d" 'cfg_load ""')"
has  "unparsable repository config: the refusal names the file" "$err" "could not parse"
is   "unparsable repository config: nothing else is reported" \
     "$(grep -c 'error ' <<<"$err")" "1"
is   "unparsable repository config: jq is never handed the empty string" \
     "$(grep -c 'jq:' <<<"$err")" "0"
is   "unparsable repository config: no unrelated key is blamed" \
     "$(grep -c 'min_fix_severity' <<<"$err")" "0"

# 2. The .crossrev.yml fallback, at lib/config.sh:113.
d="$(new_repo)"
printf 'version: 1\npolicy:\n  - not\n  a mapping: [unclosed\n' > "$d/.crossrev.yml"
xdg="$(mktemp -d)"
err="$(guarded "$xdg" "$d" 'cfg_load ""')"
has "unparsable .crossrev.yml: the refusal names the file" "$err" ".crossrev.yml"
is  "unparsable .crossrev.yml: nothing else is reported" \
    "$(grep -c 'error ' <<<"$err")" "1"

# 3. The operator config, at lib/config.sh:118. This is the one a review, resolve
#    or cycle run reaches, because cfg_load reads it whether or not a base
#    revision was given.
d="$(new_repo)"
xdg="$(mktemp -d)"; mkdir -p "$xdg/crossrev"
printf 'version: 1\npolicy:\n  - not\n  a mapping: [unclosed\n' > "$xdg/crossrev/config.yml"
err="$(guarded "$xdg" "$d" 'cfg_load ""')"
has "unparsable operator config: the refusal names the file" "$err" "could not parse"
is  "unparsable operator config: nothing else is reported" \
    "$(grep -c 'error ' <<<"$err")" "1"
is  "unparsable operator config: no unrelated key is blamed" \
    "$(grep -c 'min_fix_severity' <<<"$err")" "0"

# 4. A refused backlog value, at lib/run.sh:304, lib/init.sh:115 and
#    bin/crossrev:159. The caller must not proceed on an empty resolution.
d="$(new_repo)"; mkdir -p "$d/.github"
printf 'version: 1\nbacklog:\n  destination: elsewhere\n' > "$d/.github/crossrev.yml"
xdg="$(mktemp -d)"
# The substitution is the shape that matters: lib/run.sh:304 captures the result.
err="$(guarded "$xdg" "$d" 'cfg_load ""; resolved="$(cfg_resolve_backlog "" "$(cfg_get ".backlog.destination")")"; echo "REACHED_PAST_THE_REFUSAL resolved=[$resolved]"')"
has "refused backlog destination: the refusal names the value" "$err" "elsewhere"
is  "refused backlog destination: the caller stops"            "$(grep -c 'REACHED_PAST_THE_REFUSAL' <<<"$err")" "0"

d="$(new_repo)"; mkdir -p "$d/.github"
printf 'version: 1\nbacklog:\n  destination: repository\n  repository:\n    layout: flat\n' > "$d/.github/crossrev.yml"
xdg="$(mktemp -d)"
err="$(guarded "$xdg" "$d" 'cfg_load ""; resolved="$(cfg_resolve_backlog "" repository)"; echo "REACHED_PAST_THE_REFUSAL resolved=[$resolved]"')"
has "refused backlog layout: the refusal names the value" "$err" "flat"
is  "refused backlog layout: the caller stops"            "$(grep -c 'REACHED_PAST_THE_REFUSAL' <<<"$err")" "0"


# --- the base revision's config, and a broken one there ---------------------
#
# Policy is read from the pull request's base revision, never its head
# (ADR 0003), so this is the path automated mode takes. A file that will not
# parse there used to fall back to {}: every value the repository stated
# reverted to a default, with exit 0 and nothing printed. min_fix_severity
# dropping from high to medium is the cost — it names the lowest severity the
# resolve leg may change code for unattended.
#
# Absent and empty must stay silent, because most repositories have no config
# at their base revision at all, and cfg_show_at_base cannot tell an absent
# file from a broken one on its own.

new_base_repo() { # <relpath> <content> [<relpath> <content> ...] -> "<dir> <sha>"
  local d; d="$(mktemp -d)"
  (
    cd "$d" || exit 1
    git init -q .
    git config user.email t@example.com
    git config user.name t
    while (( $# >= 2 )); do
      mkdir -p "$(dirname "$1")"
      printf '%s' "$2" > "$1"
      shift 2
    done
    git add -A
    git commit -q --allow-empty -m base
  ) >/dev/null 2>&1
  printf '%s %s' "$d" "$(git -C "$d" rev-parse HEAD)"
}

# The exit status, which `guarded` deliberately swallows. Run the plain shape
# here: what matters for this case is that the process stops non-zero.
base_status() { # <xdg_home> <repo_dir> <base_sha> -> exit code
  XDG_CONFIG_HOME="$1" "$BASH_ABS" -c '
    set -euo pipefail
    cd "$2"
    source "$1/lib/ui.sh"; source "$1/lib/harnesses.sh"; source "$1/lib/config.sh"
    cfg_load "$3"
  ' _ "$repo_lib" "$2" "$3" >/dev/null 2>&1
  printf '%s' "$?"
}

good_policy='version: 1
policy:
  max_passes_per_cycle: 7
  min_fix_severity: high
'
bad_policy='version: 1
policy:
  max_passes_per_cycle: 7
  min_fix_severity: high
  - broken: [unclosed
'
report='cfg_load "%s"; echo "REACHED max=$(cfg_get .policy.max_passes_per_cycle) min=$(cfg_get .policy.min_fix_severity)"'

# 1. No config at the base revision. Today's behaviour exactly: defaults, no word.
read -r d sha <<<"$(new_base_repo)"
xdg="$(mktemp -d)"
# shellcheck disable=SC2059  # report is a format string by design
err="$(guarded "$xdg" "$d" "$(printf "$report" "$sha")")"
has "no config at the base revision: the caller runs on"    "$err" "REACHED"
has "no config at the base revision: defaults apply"        "$err" "max=3 min=medium"
is  "no config at the base revision: nothing is reported"   "$(grep -c 'error ' <<<"$err")" "0"

# 2. Present but empty at the base revision. git show returns exit 0 and no
#    bytes, which must read as no policy rather than as a parse failure.
read -r d sha <<<"$(new_base_repo .github/crossrev.yml '')"
xdg="$(mktemp -d)"
# shellcheck disable=SC2059
err="$(guarded "$xdg" "$d" "$(printf "$report" "$sha")")"
has "empty config at the base revision: the caller runs on"  "$err" "REACHED"
has "empty config at the base revision: defaults apply"      "$err" "max=3 min=medium"
is  "empty config at the base revision: nothing is reported" "$(grep -c 'error ' <<<"$err")" "0"

# 3. Valid at the base revision. The stated policy is what applies.
read -r d sha <<<"$(new_base_repo .github/crossrev.yml "$good_policy")"
xdg="$(mktemp -d)"
# shellcheck disable=SC2059
err="$(guarded "$xdg" "$d" "$(printf "$report" "$sha")")"
has "valid config at the base revision: the stated policy applies" "$err" "max=7 min=high"

# 4. Malformed at the base revision. One refusal, and the caller stops.
read -r d sha <<<"$(new_base_repo .github/crossrev.yml "$bad_policy")"
xdg="$(mktemp -d)"
# shellcheck disable=SC2059
err="$(guarded "$xdg" "$d" "$(printf "$report" "$sha")")"
has "unparsable base config: the refusal names the file"     "$err" ".github/crossrev.yml"
has "unparsable base config: the refusal names the revision" "$err" "base revision $sha"
is  "unparsable base config: the caller stops"               "$(grep -c 'REACHED' <<<"$err")" "0"
is  "unparsable base config: nothing else is reported"       "$(grep -c 'error ' <<<"$err")" "1"
is  "unparsable base config: jq is never handed the empty string" \
    "$(grep -c 'jq:' <<<"$err")" "0"
is  "unparsable base config: no unrelated key is blamed" \
    "$(grep -c 'min_fix_severity is' <<<"$err")" "0"
is  "unparsable base config: the run exits non-zero"         "$(base_status "$xdg" "$d" "$sha")" "1"

# 5. The .crossrev.yml fallback at the base revision, refused the same way.
read -r d sha <<<"$(new_base_repo .crossrev.yml "$bad_policy")"
xdg="$(mktemp -d)"
# shellcheck disable=SC2059
err="$(guarded "$xdg" "$d" "$(printf "$report" "$sha")")"
has "unparsable base .crossrev.yml: the refusal names the file" "$err" ".crossrev.yml"
is  "unparsable base .crossrev.yml: the caller stops"           "$(grep -c 'REACHED' <<<"$err")" "0"
is  "unparsable base .crossrev.yml: nothing else is reported"   "$(grep -c 'error ' <<<"$err")" "1"

# 6. Precedence at the base revision: .github/crossrev.yml is read and
#    .crossrev.yml is not, whether the first one parses or not. A broken
#    .github file must not fall through to the other file's policy.
read -r d sha <<<"$(new_base_repo .github/crossrev.yml "$good_policy" .crossrev.yml 'version: 1
policy:
  max_passes_per_cycle: 5
  min_fix_severity: low
')"
xdg="$(mktemp -d)"
# shellcheck disable=SC2059
err="$(guarded "$xdg" "$d" "$(printf "$report" "$sha")")"
has "base revision precedence: .github/crossrev.yml wins" "$err" "max=7 min=high"

read -r d sha <<<"$(new_base_repo .github/crossrev.yml "$bad_policy" .crossrev.yml 'version: 1
policy:
  max_passes_per_cycle: 5
'
)"
xdg="$(mktemp -d)"
# shellcheck disable=SC2059
err="$(guarded "$xdg" "$d" "$(printf "$report" "$sha")")"
has "broken .github at the base revision: it is named, not skipped" "$err" ".github/crossrev.yml"
is  "broken .github at the base revision: the other file is not read" \
    "$(grep -c 'max=5' <<<"$err")" "0"

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
