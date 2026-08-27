# shellcheck shell=bash
# lib/config.sh — two-layer configuration, endpoint and backlog resolution.
#
# Two files, because endpoints and policy live at different layers. Policy is
# repo-specific and belongs in the repo. Endpoint URLs are cross-project, and
# some of them — Ollama on a home server, OmniRoute on localhost — are
# meaningless on a GitHub-hosted runner. Committing those asserts something
# false for half the places the config is read.
#
# Config is YAML, so everything here goes through yq. jq reads JSON only.

# The three layers, kept separately as well as merged: `init` needs to know what
# the repository itself declared versus what it inherited, and the tests assert
# on the merge rather than on the result of it.
# shellcheck disable=SC2034  # read by init and by the test suite
CFG_REPO=""      # repository policy, as JSON
# shellcheck disable=SC2034  # read by init and by the test suite
CFG_OPERATOR=""  # operator file, as JSON
CFG_MERGED=""    # the two merged, as JSON

CROSSREV_CONFIG_VERSION=1

_cfg_operator_path() { printf '%s/crossrev/config.yml' "${XDG_CONFIG_HOME:-$HOME/.config}"; }

# Read a YAML file into JSON, or emit {} when it is absent.
#
# Returns 1 without a message when the file will not parse, and the caller
# refuses. The refusal cannot live here: every caller reads this through a
# command substitution, and ui_die ends with `exit 1`, which inside `$( )` ends
# only that subshell. lib/github.sh states the rule twice and lib/run.sh states
# it in its own header. bin/crossrev sets -e, but bash suppresses that for the
# whole body of a function invoked as part of an || list, and ctx_load is invoked
# exactly that way at lib/run.sh:944, 1763 and 2921. So the caller carried on
# with an empty string, jq refused it, and the next assertion blamed a key
# nobody had set.
_cfg_yaml_to_json() {
  [[ -f "$1" ]] || { printf '{}'; return 0; }
  yq -o=json -I=0 '.' "$1" 2>/dev/null || return 1
}

# Refuse a file that will not parse. Called outside the substitution that reads
# it, so the exit ends the run rather than a subshell.
_cfg_refuse_unparsable() {
  ui_die "could not parse $1" \
    "It must be valid YAML. Check it with: yq '.' $1"
}

# Read a path from a git revision rather than the working tree.
#
# Policy comes from the base revision, never the PR head. Read from the head, a
# pull request could raise max_passes_per_cycle, repoint an endpoint at a server it
# controls and harvest every prompt, or ship a REVIEW.md saying to return
# converged. A PR that legitimately changes review policy therefore takes effect
# when it merges, which is the correct order: the new policy is reviewed under
# the old one.
cfg_show_at_base() {
  local base_sha="$1" path="$2"
  git show "$base_sha:$path" 2>/dev/null || return 1
}

# Read YAML text into JSON, or emit {} when there is none.
#
# Returns 1 without a message when the text will not parse, for the reason
# _cfg_yaml_to_json gives above: the caller reads this through a command
# substitution, so the refusal has to happen outside it.
#
# Empty text is not a parse failure. A file that exists at the base revision
# and holds nothing states no policy, which is the same answer as no file.
_cfg_yaml_text_to_json() {
  local text="$1"
  [[ -n "$text" ]] || { printf '{}'; return 0; }
  printf '%s' "$text" | yq -o=json -I=0 '.' 2>/dev/null || return 1
}

# Refuse a base-revision file that will not parse.
#
# Separate from _cfg_refuse_unparsable because the hint has to read the
# revision rather than the working tree. The file on disk may parse, may differ
# from what the base revision holds, or may not be there at all, so `yq '.'
# .github/crossrev.yml` would check the wrong bytes.
_cfg_refuse_unparsable_at_base() {
  local path="$1" base_sha="$2"
  ui_die "could not parse $path at base revision $base_sha" \
    "It must be valid YAML. Check it with: git show $base_sha:$path | yq '.'"
}

# Defaults with no config file anywhere.
#
# Deliberately not what `init` writes. The CI starter names a specific pairing;
# a local user who has never heard of it would otherwise be told to set an API
# key before their first review, contradicting the promise that
# `crossrev review --pr 42` just works. So: no endpoints, two different local
# harnesses, local mode, and nothing persisted anywhere uninvited.
_cfg_defaults() {
  if ! declare -F harness_field >/dev/null 2>&1; then
    local here; here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    # shellcheck source=lib/harnesses.sh
    [[ -f "$here/harnesses.sh" ]] && source "$here/harnesses.sh"
  fi
  local rev res
  rev="$(harness_field .default_pairing.reviewer)"
  res="$(harness_field .default_pairing.resolver)"
  jq -cn --arg rev "$rev" --arg res "$res" '{
    version: 1,
    mode: "local",
    runner: "github-hosted",
    policy: {
      min_fix_severity: "medium",
      max_passes_per_cycle: 3,
      max_files_changed_per_pr: 200,
      max_prs_per_day: 25
    },
    git: { hooks: "skip" },
    logs: { retention_days: 14, keep_transcripts: false },
    endpoints: {},
    backlog: {
      destination: "auto",
      github_issues: {
        labels: [],
        tracking_label: "crossrev-review",
        create_missing_labels: true,
        comment_on_existing_issue: false
      },
      repository: { layout: "folder", path: null }
    },
    reviewer: { harness: $rev, model: null, effort: null, endpoint: null },
    resolver: { harness: $res, model: null, effort: null, endpoint: null },
    enable_automation_hint: true
  }'
}

# Load configuration.
#
# $1 — base SHA to read repo policy from, or empty to read the working tree.
#      Legs always pass a base SHA. `init` and `doctor` read the working tree,
#      because there is no pull request in play.
cfg_load() {
  local base_sha="${1:-}"
  local repo_json operator_json

  if [[ -n "$base_sha" ]]; then
    # Which file was found has to be tracked separately. cfg_show_at_base
    # returns 1 for a path that is not at that revision, and an empty string is
    # also what a file that exists and holds nothing returns, so the text alone
    # cannot say whether there was a file to parse.
    local text found=""
    if   text="$(cfg_show_at_base "$base_sha" ".github/crossrev.yml")"; then found=".github/crossrev.yml"
    elif text="$(cfg_show_at_base "$base_sha" ".crossrev.yml")";        then found=".crossrev.yml"
    fi
    if [[ -n "$found" ]]; then
      repo_json="$(_cfg_yaml_text_to_json "$text")" \
        || _cfg_refuse_unparsable_at_base "$found" "$base_sha"
    else
      repo_json='{}'
    fi
  else
    if   [[ -f .github/crossrev.yml ]]; then
      repo_json="$(_cfg_yaml_to_json .github/crossrev.yml)" || _cfg_refuse_unparsable .github/crossrev.yml
    elif [[ -f .crossrev.yml ]]; then
      repo_json="$(_cfg_yaml_to_json .crossrev.yml)" || _cfg_refuse_unparsable .crossrev.yml
    else repo_json='{}'
    fi
  fi

  local operator_path; operator_path="$(_cfg_operator_path)"
  operator_json="$(_cfg_yaml_to_json "$operator_path")" || _cfg_refuse_unparsable "$operator_path"

  cfg_check_version "$repo_json" "$(basename "${base_sha:+base revision }").github/crossrev.yml"
  cfg_check_version "$operator_json" "$(_cfg_operator_path)"

  CFG_REPO="$repo_json"
  CFG_OPERATOR="$operator_json"

  # Repo policy over defaults; then endpoints merged by name with the operator
  # file winning, so a repo can declare a public endpoint while you point the
  # same name at your own instance locally without touching the repo.
  CFG_MERGED="$(jq -cn \
    --argjson d "$(_cfg_defaults)" \
    --argjson r "$repo_json" \
    --argjson o "$operator_json" '
      ($d * $r) as $base
      | $base
      | .endpoints = (($base.endpoints // {}) * ($o.endpoints // {}))
    ')"

  cfg_assert_min_fix_severity
  cfg_assert_max_passes_per_cycle
  cfg_assert_git_hooks
  cfg_assert_logs
  cfg_assert_backlog
}

# Refuse a backlog destination or layout CrossRev does not recognise.
#
# The refusal lives here rather than in cfg_resolve_backlog for the reason
# _cfg_yaml_to_json gives above: every caller reads that function through a
# command substitution, where ui_die would end the subshell and let the caller
# continue on an empty resolution. cfg_load runs before any of them resolve, so
# a bad value is named here, once, outside a substitution.
cfg_assert_backlog() {
  local want layout
  want="$(jq -r '.backlog.destination // "auto"' <<<"$CFG_MERGED")"
  case "$want" in
    none|""|github_issues|repository|auto) : ;;
    *) ui_die "backlog.destination is '$want', which CrossRev does not recognise" \
         "Set it to github_issues, repository, none or auto in the repository config." ;;
  esac

  layout="$(jq -r '.backlog.repository.layout // "folder"' <<<"$CFG_MERGED")"
  case "$layout" in
    folder|file) : ;;
    *) ui_die "backlog.repository.layout is '$layout', which is not folder or file" \
         "Set it to folder or file in the repository config." ;;
  esac
}

# The run record lives under the state directory, and both keys shape what
# survives there. Read leniently, `retention_days: fourteen` would sweep by a
# default nobody stated and `keep_transcripts: yes` would read as false while
# the config says keep — a typo landing silently in the direction that loses
# evidence. Refuse the values where they can still be named.
cfg_assert_logs() {
  local days keep
  days="$(jq -r '.logs.retention_days // empty' <<<"$CFG_MERGED")"
  if [[ "$days" =~ ^[0-9]+$ ]] && (( days > 0 )); then :; else
    ui_die "logs.retention_days is '${days:-unset}', which is not a whole number of days above zero" \
      "It bounds how long run logs and kept transcripts stay under the state directory before a sweep removes them. Set it to 1 or more in the repository config, or remove it to take the default of 14."
  fi
  # `// empty` would collapse the legitimate default: jq's alternative operator
  # treats false as empty, so `keep_transcripts: false` must be read explicitly.
  keep="$(jq -r '.logs.keep_transcripts | if . == null then empty else tostring end' <<<"$CFG_MERGED")"
  case "$keep" in
    true|false) return 0 ;;
    *) ui_die "logs.keep_transcripts is '${keep:-unset}', which is not true or false" \
         "It decides whether harness transcripts survive a successful leg rather than only a failed one. Set it to true or false in the repository config, or remove it to take the default of false." ;;
  esac
}

# An unrecognised threshold must not be representable.
#
# Left to the ranking table it ranks zero, and zero meets nothing — so
# `min_fix_severity: medum` counts no finding as actionable, the pass reports converged,
# and the cycle stops with a high-severity finding sitting on the pull request.
# A typo would look exactly like a clean review. Refuse the value instead.
cfg_assert_min_fix_severity() {
  local min_fix_severity
  min_fix_severity="$(jq -r '.policy.min_fix_severity // empty' <<<"$CFG_MERGED")"
  case "$min_fix_severity" in
    high|medium|low) return 0 ;;
    *) ui_die "policy.min_fix_severity is '${min_fix_severity:-unset}', which is not one of high, medium or low" \
         "It names the lowest severity the resolve leg may change code for unattended. Set it to high, medium or low in the repository config, or remove it to take the default of medium." ;;
  esac
}

# A pass bound is a count of passes, and zero is not one.
#
# Zero is already spoken for inside the orchestrator: `leg_review` passes 0 into
# `legs_should_continue` as its own sentinel for "no pass bound applies to this
# invocation", which is what lets a person ask for one attended pass past the
# bound. An operator writing 0 or -1 lands on that sentinel from the other
# direction, and the two readers then disagree about what it means — the
# automatic loop takes it as no bound at all and keeps starting passes, while
# `cmd_cycle` compares the pass number directly and stops before the first one.
# Neither is what somebody typing zero into a cap expects, and the automatic
# reading is the expensive one. Refuse the value where it can still be named.
cfg_assert_max_passes_per_cycle() {
  local max
  max="$(jq -r '.policy.max_passes_per_cycle // empty' <<<"$CFG_MERGED")"
  if [[ "$max" =~ ^[0-9]+$ ]] && (( max > 0 )); then return 0; fi
  ui_die "policy.max_passes_per_cycle is '${max:-unset}', which is not a whole number of passes above zero" \
    "It bounds how many passes the loop runs by itself before a person has to ask for another, so the smallest meaningful value is 1. Set it to 1 or more in the repository config, or remove it to take the default of 3. To stop crossrev reviewing a repository at all, remove its workflows rather than setting the bound to zero."
}

# Two values, and a third must not be representable.
#
# The failure mode a typo would cause is silent in the expensive direction. Read
# leniently, `hooks: skipp` falls through to whichever branch is not `run`, so a
# repository that meant to keep its hooks running keeps committing without them
# and nothing ever says so. Refuse the value where it can still be named.
#
# See ADR 0017 for why the default is skip.
cfg_assert_git_hooks() {
  local hooks
  hooks="$(jq -r '.git.hooks // empty' <<<"$CFG_MERGED")"
  case "$hooks" in
    skip|run) return 0 ;;
    *) ui_die "git.hooks is '${hooks:-unset}', which is not one of skip or run" \
         "It decides whether the resolver's commit and push run the repository's own git hooks. Set it to skip or run in the repository config, or remove it to take the default of skip." ;;
  esac
}

# A version key that is present and not 1 is a refusal, not a warning. The whole
# point of the key is that a future shape can be rejected by an old binary.
cfg_check_version() {
  local json="$1" where="$2" v
  v="$(jq -r '.version // empty' <<<"$json")"
  [[ -z "$v" ]] && return 0
  [[ "$v" == "$CROSSREV_CONFIG_VERSION" ]] && return 0
  ui_die "$where declares version $v, and this crossrev understands version $CROSSREV_CONFIG_VERSION" \
    "Upgrade crossrev, or set version: $CROSSREV_CONFIG_VERSION in that file if it really is the current shape."
}

cfg_get() { jq -r "$1 // empty" <<<"$CFG_MERGED"; }
cfg_get_json() { jq -c "$1 // null" <<<"$CFG_MERGED"; }

# Resolve a named endpoint to "<base_url> <token_env>".
#
# An unresolved name is a hard failure, never a fallback. If `endpoint: ollama`
# does not resolve because the run is on a runner that cannot see it, the leg
# stops and says so. Falling back to Anthropic would mean running Claude while
# the config says Ollama — the same silent substitution the divergence guard
# exists to catch, arriving through a different door.
cfg_endpoint() {
  local name="$1" ep
  [[ -n "$name" && "$name" != "null" ]] || return 1
  ep="$(jq -c --arg n "$name" '.endpoints[$n] // empty' <<<"$CFG_MERGED")"
  [[ -n "$ep" ]] || ui_die \
    "the endpoint '$name' is named in the config but defined nowhere" \
    "Define it under endpoints: in the repository config, or in $(_cfg_operator_path) if it is machine-local. crossrev will not silently fall back to the vendor's own API."
  local url tok
  url="$(jq -r '.base_url // empty' <<<"$ep")"
  tok="$(jq -r '.token_env // empty' <<<"$ep")"
  [[ -n "$url" ]] || ui_die "the endpoint '$name' has no base_url" "Add base_url: to its definition."
  [[ -n "$tok" ]] || ui_die "the endpoint '$name' has no token_env" "Add token_env: naming the environment variable that carries its token. Ollama's docs use ANTHROPIC_AUTH_TOKEN where Kimi's use ANTHROPIC_API_KEY, which is why the name is not assumed."
  printf '%s %s' "$url" "$tok"
}

# ---------------------------------------------------------------------------
# Where deferred work goes
# ---------------------------------------------------------------------------
#
# Inventing a folder in someone else's repository is the wrong default, so
# crossrev does not. Three tiers, first hit wins, and the last one only fires
# when a repository backlog was explicitly asked for.

# Read the Tracker field out of a `## Project Map` section.
#
# The convention declares where project-tracking information lives so tools read
# it instead of guessing. Read from the base revision like every other policy:
# a PR that could edit it from the head could repoint where the loop writes.
cfg_project_map_tracker() {
  local base_sha="${1:-}" text=""
  local f
  for f in AGENTS.md CLAUDE.md GEMINI.md; do
    if [[ -n "$base_sha" ]]; then text="$(cfg_show_at_base "$base_sha" "$f" || true)"
    elif [[ -f "$f" ]];      then text="$(cat "$f")"
    fi
    [[ -n "$text" ]] || continue
    local tracker
    tracker="$(printf '%s' "$text" \
      | awk 'tolower($0) ~ /^##+[[:space:]]*project (map|context)/ {inmap=1; next}
             inmap && /^##[[:space:]]/ {inmap=0}
             inmap' \
      | sed -n 's/^[[:space:]]*-[[:space:]]*\*\*Tracker\*\*:[[:space:]]*//Ip' | head -1)"
    # Drop a parenthetical gloss before returning the value. Project Map fields
    # routinely carry one — `none (ROADMAP.md is the single source of truth for
    # "what's next")` is how the convention's own example reads — and an
    # unstripped gloss makes `none` stop matching `none`, so the caller falls
    # through to the sniff and picks a destination the repository just declared
    # it does not have. Nothing legitimate here holds a parenthesis: the value
    # is a path, a tracker name, or `none`.
    tracker="$(printf '%s' "$tracker" | sed 's/[[:space:]]*(.*$//; s/[[:space:]]*$//')"
    [[ -n "$tracker" ]] && { printf '%s' "$tracker"; return 0; }
  done
  return 1
}

# Resolve `backlog.destination` to a concrete destination.
#
# Prints "github_issues", "repository <layout> <path>", or "none".
#
# Every caller reads this through a command substitution, so the two ui_die arms
# below cannot refuse a configured value: the exit would end the subshell and the
# caller would carry on with an empty resolution. cfg_assert_backlog refuses both
# values from cfg_load instead, and all three callers load before they resolve.
# The arms stay as a guard against a caller passing a literal this does not know.
cfg_resolve_backlog() {
  local base_sha="${1:-}" want="${2:-auto}"

  case "$want" in
    none|"") printf 'none'; return 0 ;;
    github_issues) printf 'github_issues'; return 0 ;;
    repository)
      local layout path layout_stated=false
      layout="$(jq -r '.backlog.repository.layout // "folder"' <<<"$CFG_MERGED")"
      path="$(jq -r '.backlog.repository.path // empty' <<<"$CFG_MERGED")"
      case "$layout" in
        folder|file) : ;;
        *) ui_die "backlog.repository.layout is '$layout', which is not folder or file" \
             "Set it to folder or file in the repository config." ;;
      esac
      if [[ -n "$path" ]]; then
        printf 'repository %s %s' "$layout" "$path"
        return 0
      fi
      [[ "$(jq -r '.backlog.repository | has("layout")' <<<"$CFG_REPO")" == "true" ]] && layout_stated=true
      if [[ "$layout_stated" == "true" ]]; then
        _cfg_sniff_repository_backlog "$base_sha" explicit "$layout"
      else
        _cfg_sniff_repository_backlog "$base_sha" explicit any
      fi
      return 0 ;;
    auto) : ;;
    *) ui_die "backlog.destination is '$want', which CrossRev does not recognise" \
         "Set it to github_issues, repository, none or auto in the repository config." ;;
  esac

  # Tier 1 — the repository's own declaration.
  local tracker
  if tracker="$(cfg_project_map_tracker "$base_sha")"; then
    case "$(printf '%s' "$tracker" | tr '[:upper:]' '[:lower:]')" in
      none)            printf 'none'; return 0 ;;
      *github*issue*)  printf 'github_issues'; return 0 ;;
      # A URL is a hosted tracker, not a path. It has to be caught before the
      # slash arm below, which would otherwise turn
      # `https://linear.app/acme/team/ENG` into a directory of that name inside
      # the checkout — relative, so the inside-the-repo guard waves it through.
      # Same outcome as a bare `Linear`: somewhere real, nothing to write to.
      http://*|https://*) : ;;
      */*|*.md)
        if [[ "$tracker" == *.md ]]; then
          printf 'repository file %s' "$tracker"
        else
          printf 'repository folder %s' "$tracker"
        fi
        return 0 ;;
      *)               : ;;  # Linear, Jira and friends: nothing to write to yet
    esac
  fi

  # Tiers 2 and 3.
  _cfg_sniff_repository_backlog "$base_sha" auto any
}

# Sniff for a convention already in use. $2 is "explicit" when a repository
# destination was asked for, in which case tier 3 applies; "auto" falls to none
# rather than creating a location nobody asked for. $3 constrains the layout
# when the repository config stated one explicitly.
# Does a path exist, in the base revision when there is one, else on disk?
_cfg_path_exists() {
  local base_sha="$1" path="$2"
  if [[ -n "$base_sha" ]]; then git cat-file -e "$base_sha:$path" 2>/dev/null
  else [[ -e "$path" ]]; fi
}

_cfg_sniff_repository_backlog() {
  local base_sha="${1:-}" mode="${2:-auto}" layout="${3:-any}"
  if [[ "$layout" == "any" || "$layout" == "folder" ]]; then
    if _cfg_path_exists "$base_sha" backlog.config.yml \
       || _cfg_path_exists "$base_sha" backlog/config.yml \
       || _cfg_path_exists "$base_sha" .backlog/config.yml; then
      printf 'repository folder backlog/tasks'; return 0
    fi
  fi
  if [[ "$layout" == "any" || "$layout" == "file" ]]; then
    if _cfg_path_exists "$base_sha" BACKLOG.md; then printf 'repository file BACKLOG.md'; return 0; fi
    if _cfg_path_exists "$base_sha" TODO.md;    then printf 'repository file TODO.md';    return 0; fi
  fi
  if [[ "$mode" == "explicit" ]]; then
    if [[ "$layout" == "file" ]]; then printf 'repository file .crossrev/backlog.md'
    else printf 'repository folder .crossrev/backlog'
    fi
    return 0
  fi
  printf 'none'
}

# A backlog path is a write target, so it is bounded rather than trusted. Even read
# from the base revision it is a string that ends in a file write, so a `../`
# sequence or an absolute path must fail loudly instead of landing somewhere
# surprising. Same reasoning as the branch guard: the check is cheap and the
# failure it prevents is not.
#
# Resolution is lexical and uses jq alone. It used to call python3's normpath
# with a fallback to raw concatenation whenever that call failed — and since
# python3 is in no dependency list, on most hosts every call failed, the
# fallback fired, and `../` traversal matched the containment pattern below.
# Nothing here can fail into an allowed answer: jq missing or erroring
# resolves to "", which dies.
cfg_assert_path_inside_repo() {
  local path="$1" root resolved
  root="$(git rev-parse --show-toplevel 2>/dev/null)" || ui_die \
    "not inside a git repository" "Run crossrev from a checkout of the repository under review."
  [[ "$path" != /* ]] || ui_die \
    "the backlog path '$path' is absolute" "Backlog paths are repository-relative, so that crossrev cannot write outside the checkout."
  case "$path" in
    ""|".") resolved="$root" ;;
    *) resolved="$(jq -rn --arg c "$root/$path" '
         [ $c | split("/")[] | select(. != "" and . != ".") ]
         | reduce .[] as $part ({stack: [], escaped: false};
             if .escaped then .
             elif $part == ".." then
               if (.stack | length) > 0 then .stack |= .[:-1] else .escaped = true end
             else .stack += [$part] end)
         | if .escaped or (.stack | length) == 0 then ""
           else "/" + (.stack | join("/")) end')" ;;
  esac
  case "$resolved" in
    "$root"|"$root"/*) return 0 ;;
    "") ui_die "the backlog path '$path' escapes the repository" \
      "It climbs above the checkout root. Backlog paths must stay inside the repository." ;;
    *) ui_die "the backlog path '$path' resolves outside the repository" \
         "It resolves to $resolved. Backlog paths must stay inside the checkout." ;;
  esac
}
