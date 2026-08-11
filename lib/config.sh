# shellcheck shell=bash
# lib/config.sh — two-layer configuration, endpoint and sink resolution.
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

REVLOOP_CONFIG_VERSION=1

_cfg_operator_path() { printf '%s/revloop/config.yml' "${XDG_CONFIG_HOME:-$HOME/.config}"; }

# Read a YAML file into JSON, or emit {} when it is absent.
_cfg_yaml_to_json() {
  [[ -f "$1" ]] || { printf '{}'; return 0; }
  yq -o=json -I=0 '.' "$1" 2>/dev/null || ui_die \
    "could not parse $1" \
    "It must be valid YAML. Check it with: yq '.' $1"
}

# Read a path from a git revision rather than the working tree.
#
# Policy comes from the base revision, never the PR head. Read from the head, a
# pull request could raise max_passes, repoint an endpoint at a server it
# controls and harvest every prompt, or ship a REVIEW.md saying to return
# converged. A PR that legitimately changes review policy therefore takes effect
# when it merges, which is the correct order: the new policy is reviewed under
# the old one.
cfg_show_at_base() {
  local base_sha="$1" path="$2"
  git show "$base_sha:$path" 2>/dev/null || return 1
}

_cfg_yaml_text_to_json() {
  local text="$1"
  [[ -n "$text" ]] || { printf '{}'; return 0; }
  printf '%s' "$text" | yq -o=json -I=0 '.' 2>/dev/null || printf '{}'
}

# Defaults with no config file anywhere.
#
# Deliberately not what `init` writes. The CI starter names a specific pairing;
# a local user who has never heard of it would otherwise be told to set an API
# key before their first review, contradicting the promise that
# `revloop review --pr 42` just works. So: no endpoints, two different local
# harnesses, single-run, and nothing persisted anywhere uninvited.
_cfg_defaults() {
  jq -cn '{
    version: 1,
    mode: "single-run",
    max_passes: 3,
    endpoints: {},
    sinks: {},
    persist: { defects: "auto", escalated: "none" },
    reviewer: { harness: "codex",  model: null, effort: null, endpoint: null },
    resolver: { harness: "claude", model: null, effort: null, endpoint: null,
                fix_at: "medium" },
    caps: { runs_per_day: 12, max_files_changed: 200 },
    runner: "github-hosted"
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
    local text
    text="$(cfg_show_at_base "$base_sha" ".github/revloop.yml" \
         || cfg_show_at_base "$base_sha" ".revloop.yml" || true)"
    repo_json="$(_cfg_yaml_text_to_json "$text")"
  else
    if   [[ -f .github/revloop.yml ]]; then repo_json="$(_cfg_yaml_to_json .github/revloop.yml)"
    elif [[ -f .revloop.yml ]];        then repo_json="$(_cfg_yaml_to_json .revloop.yml)"
    else repo_json='{}'
    fi
  fi

  operator_json="$(_cfg_yaml_to_json "$(_cfg_operator_path)")"

  cfg_check_version "$repo_json" "$(basename "${base_sha:+base revision }").github/revloop.yml"
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

  cfg_assert_fix_at
}

# An unrecognised threshold must not be representable.
#
# Left to the ranking table it ranks zero, and zero meets nothing — so
# `fix_at: medum` counts no finding as actionable, the pass reports converged,
# and the cycle stops with a high-severity finding sitting on the pull request.
# A typo would look exactly like a clean review. Refuse the value instead.
cfg_assert_fix_at() {
  local fix_at; fix_at="$(jq -r '.resolver.fix_at // empty' <<<"$CFG_MERGED")"
  case "$fix_at" in
    high|medium|low) return 0 ;;
    *) ui_die "resolver.fix_at is '${fix_at:-unset}', which is not one of high, medium or low" \
         "It names the lowest severity the resolve leg may change code for unattended. Set it to high, medium or low in the repository config, or remove it to take the default of medium." ;;
  esac
}

# A version key that is present and not 1 is a refusal, not a warning. The whole
# point of the key is that a future shape can be rejected by an old binary.
cfg_check_version() {
  local json="$1" where="$2" v
  v="$(jq -r '.version // empty' <<<"$json")"
  [[ -z "$v" ]] && return 0
  [[ "$v" == "$REVLOOP_CONFIG_VERSION" ]] && return 0
  ui_die "$where declares version $v, and this revloop understands version $REVLOOP_CONFIG_VERSION" \
    "Upgrade revloop, or set version: $REVLOOP_CONFIG_VERSION in that file if it really is the current shape."
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
    "Define it under endpoints: in the repository config, or in $(_cfg_operator_path) if it is machine-local. revloop will not silently fall back to the vendor's own API."
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
# revloop does not. Three tiers, first hit wins, and the last one only fires
# when a file sink was explicitly asked for.

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

# Resolve `persist.defects: auto` to a concrete destination.
#
# Prints "<kind> <detail>": "issues", "file <path>", or "none".
cfg_resolve_sink() {
  local base_sha="${1:-}" want="${2:-auto}"

  case "$want" in
    none|"") printf 'none'; return 0 ;;
    auto) : ;;
    *)
      local kind; kind="$(jq -r --arg n "$want" '.sinks[$n].type // empty' <<<"$CFG_MERGED")"
      [[ -n "$kind" ]] || ui_die \
        "persist names the sink '$want', which is defined nowhere" \
        "Define it under sinks: in the repository config, or set persist.defects to none."
      case "$kind" in
        github_issue) printf 'issues'; return 0 ;;
        file)
          local p; p="$(jq -r --arg n "$want" '.sinks[$n].path // "auto"' <<<"$CFG_MERGED")"
          [[ "$p" == "auto" ]] && { _cfg_sniff_file_sink "$base_sha" explicit; return 0; }
          printf 'file %s' "$p"; return 0 ;;
        *) ui_die "the sink '$want' has an unknown type '$kind'" "Supported types are github_issue and file." ;;
      esac ;;
  esac

  # Tier 1 — the repository's own declaration.
  local tracker
  if tracker="$(cfg_project_map_tracker "$base_sha")"; then
    case "$(printf '%s' "$tracker" | tr '[:upper:]' '[:lower:]')" in
      none)            printf 'none'; return 0 ;;
      *github*issue*)  printf 'issues'; return 0 ;;
      # A URL is a hosted tracker, not a path. It has to be caught before the
      # slash arm below, which would otherwise turn
      # `https://linear.app/acme/team/ENG` into a directory of that name inside
      # the checkout — relative, so the inside-the-repo guard waves it through.
      # Same outcome as a bare `Linear`: somewhere real, nothing to write to.
      http://*|https://*) : ;;
      */*|*.md)        printf 'file %s' "$tracker"; return 0 ;;
      *)               : ;;  # Linear, Jira and friends: nothing to write to yet
    esac
  fi

  # Tiers 2 and 3.
  _cfg_sniff_file_sink "$base_sha" auto
}

# Sniff for a convention already in use. $2 is "explicit" when a file sink was
# actually asked for, in which case tier 3 applies; "auto" falls to none rather
# than creating a directory nobody asked for.
# Does a path exist, in the base revision when there is one, else on disk?
_cfg_path_exists() {
  local base_sha="$1" path="$2"
  if [[ -n "$base_sha" ]]; then git cat-file -e "$base_sha:$path" 2>/dev/null
  else [[ -e "$path" ]]; fi
}

_cfg_sniff_file_sink() {
  local base_sha="${1:-}" mode="${2:-auto}"
  if _cfg_path_exists "$base_sha" backlog.config.yml \
     || _cfg_path_exists "$base_sha" backlog/config.yml \
     || _cfg_path_exists "$base_sha" .backlog/config.yml; then
    printf 'file backlog/tasks'; return 0
  fi
  if _cfg_path_exists "$base_sha" TODO.md;         then printf 'file TODO.md';         return 0; fi
  if _cfg_path_exists "$base_sha" docs/ROADMAP.md; then printf 'file docs/ROADMAP.md'; return 0; fi
  if [[ "$mode" == "explicit" ]]; then printf 'file .revloop/backlog'; return 0; fi
  printf 'none'
}

# A sink path is a write target, so it is bounded rather than trusted. Even read
# from the base revision it is a string that ends in a file write, so a `../`
# sequence or an absolute path must fail loudly instead of landing somewhere
# surprising. Same reasoning as the branch guard: the check is cheap and the
# failure it prevents is not.
cfg_assert_path_inside_repo() {
  local path="$1" root resolved
  root="$(git rev-parse --show-toplevel 2>/dev/null)" || ui_die \
    "not inside a git repository" "Run revloop from a checkout of the repository under review."
  [[ "$path" != /* ]] || ui_die \
    "the sink path '$path' is absolute" "Sink paths are repository-relative, so that revloop cannot write outside the checkout."
  resolved="$(cd "$root" && python3 -c 'import os,sys; print(os.path.normpath(os.path.join(os.getcwd(), sys.argv[1])))' "$path" 2>/dev/null)" \
    || resolved="$root/$path"
  case "$resolved" in
    "$root"|"$root"/*) return 0 ;;
    *) ui_die "the sink path '$path' resolves outside the repository" \
         "It resolves to $resolved. Sink paths must stay inside the checkout." ;;
  esac
}
