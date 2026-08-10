# shellcheck shell=bash
# lib/init.sh — `revloop init`, the upgrade from local to automated.
#
# This is the most consequential command revloop has: it registers a GitHub
# identity, writes organisation secrets, and adds files to a repository. So it
# prints an itemised plan naming every path, secret and label, the resolved
# destination for deferred work and where that resolution came from, flags
# anything it would overwrite, and asks once.
#
# The gate is not politeness. It is the difference between a tool people trust
# with a second repository and one they run once.

INIT_DRY_RUN=0
INIT_UPGRADE=0
INIT_REPO=""
INIT_OWNER=""
INIT_OWNER_TYPE="user"

# Everything the plan needs, resolved before anything is printed.
INIT_SOURCE_REPO=""; INIT_SOURCE_SHA=""; INIT_SOURCE_REF=""
INIT_PASS_LABELS=""; INIT_FIXED_LABELS=""; INIT_SINK_LABELS=""
INIT_SINK_RESOLVED=""; INIT_SINK_ORIGIN=""
INIT_WRITES=""; INIT_OVERWRITES=""

readonly INIT_LABEL_COLOUR="5319e7"

cmd_init() {
  local yes=0
  while (( $# )); do
    case "$1" in
      --owner)   INIT_OWNER="${2:?--owner needs a value}"; shift 2 ;;
      --repo)    INIT_REPO="${2:?--repo needs a value}"; shift 2 ;;
      --dry-run) INIT_DRY_RUN=1; shift ;;
      --upgrade) INIT_UPGRADE=1; shift ;;
      --yes|-y)  yes=1; shift ;;
      *) ui_die "unknown option for init: $1" \
           "Usage: revloop init [--owner <owner>] [--upgrade] [--dry-run] [--yes]" ;;
    esac
  done

  preflight_require_yq
  _init_resolve
  _init_print_plan

  if (( INIT_DRY_RUN )); then
    ui_end "Nothing was changed — --dry-run prints the plan and stops."
    return 0
  fi

  if (( yes )); then
    # shellcheck disable=SC2034  # read by ui_confirm
    REVLOOP_ASSUME_YES=1
  fi
  ui_confirm "Proceed?" || { ui_end "Nothing was changed."; return 0; }

  _init_execute
}

# ---------------------------------------------------------------------------
# Resolution
# ---------------------------------------------------------------------------

_init_resolve() {
  [[ -n "$INIT_REPO" ]] || INIT_REPO="$(gh_repo_slug)"
  [[ -n "$INIT_REPO" ]] || ui_die "could not work out which repository to set up" \
    "Run revloop init from a checkout with a GitHub remote, or pass --repo owner/name."

  # Detected, not asked. The repository's own owner is the trust boundary the
  # App's private key should sit on — the org for an org repo, your account for a
  # personal one.
  [[ -n "$INIT_OWNER" ]] || INIT_OWNER="${INIT_REPO%%/*}"
  INIT_OWNER_TYPE="$(gh api "users/$INIT_OWNER" --jq '.type' 2>/dev/null | tr '[:upper:]' '[:lower:]')"
  [[ "$INIT_OWNER_TYPE" == "organization" ]] || INIT_OWNER_TYPE="user"

  # Config from the working tree: there is no pull request in play, and this is
  # the config `init` is about to write.
  cfg_load ""

  local max; max="$(cfg_get '.max_passes')"
  local i
  for (( i = 1; i <= max; i++ )); do
    INIT_PASS_LABELS="$INIT_PASS_LABELS revloop/pass-$i"
  done
  INIT_PASS_LABELS="${INIT_PASS_LABELS# }"
  INIT_FIXED_LABELS="revloop/awaiting-address revloop/awaiting-review revloop/converged revloop/halted revloop/stop"

  # `auto` is a bootstrap convenience, not a runtime mode: resolve it once here
  # and write the concrete answer into the generated config, so the committed file
  # states plainly where deferred work goes.
  local want; want="$(cfg_get '.persist.defects')"
  INIT_SINK_RESOLVED="$(cfg_resolve_sink "" "$want")"
  if [[ "$want" == "auto" ]]; then
    if cfg_project_map_tracker "" >/dev/null; then
      INIT_SINK_ORIGIN="resolved from the ## Project Map section's Tracker field"
    else
      INIT_SINK_ORIGIN="resolved by sniffing for a convention already in use"
    fi
  else
    INIT_SINK_ORIGIN="named in the repository config as '$want'"
  fi

  if [[ "$INIT_SINK_RESOLVED" == "issues" ]]; then
    local identity extra
    identity="$(run_sink_field "$want" identity_label "revloop-review")"
    extra="$(run_sink_field "$want" labels "")"
    INIT_SINK_LABELS="$(printf '%s %s' "$identity" "$extra" | tr -s ' ' | sed 's/ *$//')"
  fi

  # Where the workflows fetch revloop from, pinned by SHA. A tag only looks
  # immutable: `git tag -f` plus a force push moves it, and the failure mode is a
  # repository whose review behaviour changes with nothing in its own history to
  # show for it.
  INIT_SOURCE_REPO="$(git -C "$ROOT" remote get-url origin 2>/dev/null \
    | sed -E 's#^.*github\.com[:/]##; s#\.git$##')" || INIT_SOURCE_REPO=""
  INIT_SOURCE_SHA="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null)" || INIT_SOURCE_SHA=""
  INIT_SOURCE_REF="$(git -C "$ROOT" describe --tags --exact-match 2>/dev/null)" \
    || INIT_SOURCE_REF="v$(revloop_version)"

  [[ -n "$INIT_SOURCE_REPO" && -n "$INIT_SOURCE_SHA" ]] || ui_die \
    "could not work out which commit of revloop to pin the workflows to" \
    "init generates workflows that check revloop out at a 40-character SHA. Run it from a git checkout of the repository revloop lives in."

  local f
  for f in .github/workflows/revloop-review.yml .github/workflows/revloop-address.yml \
           .github/workflows/revloop-watchdog.yml .github/revloop.yml; do
    INIT_WRITES="$INIT_WRITES $f"
    [[ -e "$f" ]] && INIT_OVERWRITES="$INIT_OVERWRITES $f"
  done
  INIT_WRITES="${INIT_WRITES# }"; INIT_OVERWRITES="${INIT_OVERWRITES# }"
}

# ---------------------------------------------------------------------------
# The plan
# ---------------------------------------------------------------------------

_init_print_plan() {
  local meta app_line=""
  meta="$(_auth_meta "$INIT_OWNER")"
  if [[ -f "$meta" ]]; then
    app_line="reuse \"$(jq -r .name "$meta")\" (id $(jq -r .id "$meta"), owner $INIT_OWNER)"
  else
    app_line="none registered for $INIT_OWNER — run \`revloop auth login\` first"
  fi

  ui_section "Plan for $INIT_REPO"
  ui_line ""
  ui_line "GitHub App        $app_line"
  ui_line "source pin        $INIT_SOURCE_REPO @ ${INIT_SOURCE_SHA:0:40}"
  ui_line "                  ($INIT_SOURCE_REF — the SHA is the pin, the tag is a comment)"

  local scope="repository"
  [[ "$INIT_OWNER_TYPE" == "organization" ]] && scope="organisation"
  ui_line ""
  ui_line "secrets           checked at $scope level, and set only where revloop has the value"
  local s
  for s in APP_ID APP_PRIVATE_KEY CLAUDE_CODE_OAUTH_TOKEN REVLOOP_SOURCE_KEY; do
    if _init_secret_exists "$s"; then
      ui_line "                  $s — already set"
    else
      ui_line "                  $s — MISSING $(_init_secret_note "$s")"
    fi
  done

  ui_line ""
  local pass_count fixed_count
  pass_count="$(wc -w <<<"$INIT_PASS_LABELS" | tr -d ' ')"
  fixed_count="$(wc -w <<<"$INIT_FIXED_LABELS" | tr -d ' ')"
  ui_line "labels            $(( pass_count + fixed_count )) for the loop:"
  _init_label_inventory "$INIT_PASS_LABELS $INIT_FIXED_LABELS"
  if [[ -n "$INIT_SINK_LABELS" ]]; then
    ui_line "                  $(wc -w <<<"$INIT_SINK_LABELS" | tr -d ' ') for filed issues:"
    _init_label_inventory "$INIT_SINK_LABELS"
  fi
  ui_line "                  the chain is label-driven and gh refuses to apply a label"
  ui_line "                  that does not exist, so a repository without them posts its"
  ui_line "                  first review and then dies looking healthy"

  ui_line ""
  ui_line "deferred work     $INIT_SINK_RESOLVED"
  ui_line "                  $INIT_SINK_ORIGIN"

  ui_line ""
  local f first=1
  for f in $INIT_WRITES; do
    if (( first )); then ui_line "write             $f"; first=0
    else ui_line "                  $f"; fi
  done

  ui_line ""
  if [[ -n "$INIT_OVERWRITES" ]]; then
    ui_line "overwrites        $INIT_OVERWRITES"
  else
    ui_line "overwrites        none"
  fi

  if ! _init_branch_protected; then
    ui_warn "branch protection is off on $(gh_default_branch "$INIT_REPO") in $INIT_REPO" \
      "The orchestrator's own push guard would be the only thing stopping a bad push. It asserts the target equals the pull request's head branch and is not the default branch, but branch protection is the backstop behind it."
  fi
}

_init_label_inventory() {
  local l state
  for l in $1; do
    if gh_label_exists "$INIT_REPO" "$l"; then state="exists"; else state="create"; fi
    ui_line "                    $state  $l"
  done
}

_init_secret_note() {
  case "$1" in
    APP_ID|APP_PRIVATE_KEY)
      if [[ -f "$(_auth_meta "$INIT_OWNER")" ]]; then printf -- '— revloop will set it'
      else printf -- '— run `revloop auth login` first'; fi ;;
    CLAUDE_CODE_OAUTH_TOKEN)
      printf -- '— set it yourself for now: `claude setup-token`, then `gh secret set`' ;;
    REVLOOP_SOURCE_KEY)
      printf -- '— a read-only deploy key on %s; see the note after this plan' "$INIT_SOURCE_REPO" ;;
  esac
}

_init_secret_exists() {
  local name="$1"
  if [[ "$INIT_OWNER_TYPE" == "organization" ]] \
     && gh secret list --org "$INIT_OWNER" 2>/dev/null | grep -q "^$name\b"; then
    return 0
  fi
  gh secret list --repo "$INIT_REPO" 2>/dev/null | grep -q "^$name\b"
}

_init_branch_protected() {
  local branch; branch="$(gh_default_branch "$INIT_REPO")"
  gh api "repos/$INIT_REPO/branches/$branch/protection" >/dev/null 2>&1
}

# ---------------------------------------------------------------------------
# Execution
# ---------------------------------------------------------------------------

_init_execute() {
  local unfinished=""

  # --- labels --------------------------------------------------------------
  local created=0 existed=0 l state
  for l in $INIT_PASS_LABELS $INIT_FIXED_LABELS; do
    state="$(gh_label_ensure "$INIT_REPO" "$l" "$INIT_LABEL_COLOUR" "revloop loop state")"
    [[ "$state" == "created" ]] && created=$(( created + 1 )) || existed=$(( existed + 1 ))
  done
  ui_section "Labels"
  ui_ok "created $created and found $existed already on $INIT_REPO for the loop"

  if [[ -n "$INIT_SINK_LABELS" ]]; then
    local can_create missing=""
    can_create="$(run_sink_field "$(cfg_get '.persist.defects')" create_labels true)"
    created=0; existed=0
    for l in $INIT_SINK_LABELS; do
      if gh_label_exists "$INIT_REPO" "$l"; then existed=$(( existed + 1 )); continue; fi
      if [[ "$can_create" == "false" ]]; then missing="$missing $l"; continue; fi
      gh_label_ensure "$INIT_REPO" "$l" "d4c5f9" "filed by revloop" >/dev/null
      created=$(( created + 1 ))
    done
    if [[ -n "$missing" ]]; then
      # A repository that governs its own label set is one where inventing labels
      # is worse than stopping.
      ui_die "the issue sink needs these labels and create_labels is false:$missing" \
        "gh refuses to apply a label that does not exist, so filing would die after the review had already posted. Create them by hand, or set create_labels: true in the sink."
    fi
    ui_ok "created $created and found $existed already for filed issues"
  fi

  # --- secrets -------------------------------------------------------------
  ui_section "Secrets"
  local meta pem
  meta="$(_auth_meta "$INIT_OWNER")"
  if [[ -f "$meta" ]]; then
    pem="$(_auth_pem "$INIT_OWNER")"
    _init_secret_set APP_ID "$(jq -r .id "$meta")" || unfinished="$unfinished APP_ID"
    if [[ -f "$pem" ]]; then
      _init_secret_set APP_PRIVATE_KEY "$(cat "$pem")" || unfinished="$unfinished APP_PRIVATE_KEY"
    else
      ui_no "APP_PRIVATE_KEY — the key file is missing at $pem"
      unfinished="$unfinished APP_PRIVATE_KEY"
    fi
  else
    ui_no "no App is registered for $INIT_OWNER, so APP_ID and APP_PRIVATE_KEY were not set"
    ui_next "revloop auth login --owner $INIT_OWNER"
    unfinished="$unfinished APP_ID APP_PRIVATE_KEY"
  fi

  local s
  for s in CLAUDE_CODE_OAUTH_TOKEN REVLOOP_SOURCE_KEY; do
    if _init_secret_exists "$s"; then
      ui_ok "$s — already set"
    else
      ui_no "$s — not set, and revloop does not have the value to set it"
      unfinished="$unfinished $s"
    fi
  done

  # --- files ---------------------------------------------------------------
  ui_section "Files"
  mkdir -p .github/workflows
  local t name
  for t in review address watchdog; do
    name=".github/workflows/revloop-$t.yml"
    sed -e "s#__SOURCE_REPO__#$INIT_SOURCE_REPO#g" \
        -e "s#__SOURCE_SHA__#$INIT_SOURCE_SHA#g" \
        -e "s#__SOURCE_REF__#$INIT_SOURCE_REF#g" \
        "$ROOT/templates/revloop-$t.yml" >"$name"
    ui_ok "wrote $name"
  done

  if (( INIT_UPGRADE )) && [[ -e .github/revloop.yml ]]; then
    # --upgrade regenerates workflows from the installed version, so drift across
    # repositories is handled by regeneration rather than hand-editing every copy.
    # It deliberately leaves the policy file alone.
    ui_say "left .github/revloop.yml alone — --upgrade regenerates workflows, not policy"
  else
    _init_write_config
    ui_ok "wrote .github/revloop.yml, with deferred work resolved to $INIT_SINK_RESOLVED"
  fi

  # --- what is not done ----------------------------------------------------
  ui_section "Still needed"
  if [[ -z "$unfinished" ]]; then
    ui_ok "nothing — open a pull request and the loop runs"
    ui_end "Watch it with: revloop status --pr <number>"
    return 0
  fi

  # Refusing to finish quietly. A missing source key fails at checkout before any
  # review runs, which is the good kind of failure — but only if someone knows.
  for s in $unfinished; do
    case "$s" in
      REVLOOP_SOURCE_KEY)
        ui_no "REVLOOP_SOURCE_KEY — the workflows cannot check revloop out without it"
        ui_line "   The App token is scoped to its own installation and cannot read"
        ui_line "   $INIT_SOURCE_REPO; the default workflow token is scoped to this"
        ui_line "   repository. So the source checkout needs a credential of its own."
        ui_line ""
        ui_line "   ssh-keygen -t ed25519 -C revloop-source -f /tmp/revloop-source -N ''"
        ui_line "   gh repo deploy-key add /tmp/revloop-source.pub --repo $INIT_SOURCE_REPO --title revloop-source"
        ui_line "   gh secret set REVLOOP_SOURCE_KEY $(_init_secret_scope_flag) </tmp/revloop-source"
        ui_line "   rm /tmp/revloop-source /tmp/revloop-source.pub"
        ui_line ""
        ui_line "   Read-only by default, so its blast radius if leaked is read access"
        ui_line "   to that one repository — no write, no user identity." ;;
      CLAUDE_CODE_OAUTH_TOKEN)
        ui_no "CLAUDE_CODE_OAUTH_TOKEN — both legs run on Claude, so neither can authenticate"
        ui_line "   claude setup-token"
        ui_line "   gh secret set CLAUDE_CODE_OAUTH_TOKEN $(_init_secret_scope_flag)"
        ui_line ""
        ui_line "   That token is valid for a year and the command will not show it"
        ui_line "   again, so put it in the secret in the same sitting. Note the date:"
        ui_line "   the first sign of expiry is a CI failure on a day nobody is looking."
        ui_line ""
        ui_line "   Capturing it automatically, and warning as the year closes, is on"
        ui_line "   the subscription-credentials amendment's task list." ;;
      APP_ID|APP_PRIVATE_KEY)
        : ;;   # already explained above
    esac
  done
  ui_end "The workflows are installed but will fail at the first missing secret, before any review runs."
  return 0
}

_init_secret_scope_flag() {
  if [[ "$INIT_OWNER_TYPE" == "organization" ]]; then
    printf -- '--org %s' "$INIT_OWNER"
  else
    printf -- '--repo %s' "$INIT_REPO"
  fi
}

# Org level where the owner is an org, so later repositories in it need only
# config, labels and the App install.
_init_secret_set() {
  local name="$1" value="$2"
  if [[ "$INIT_OWNER_TYPE" == "organization" ]]; then
    if printf '%s' "$value" | gh secret set "$name" --org "$INIT_OWNER" --visibility all >/dev/null 2>&1; then
      ui_ok "$name — set at the $INIT_OWNER organisation level"
      return 0
    fi
    ui_warn "could not set $name at the $INIT_OWNER organisation level" \
      "That needs admin access to the organisation. Falling back to a repository secret, which works but has to be repeated for every repository."
  fi
  if printf '%s' "$value" | gh secret set "$name" --repo "$INIT_REPO" >/dev/null 2>&1; then
    ui_ok "$name — set on $INIT_REPO"
    return 0
  fi
  ui_no "$name — could not be set"
  return 1
}

# The generated config states plainly where deferred work goes, because `auto` is
# a bootstrap convenience rather than a runtime mode. Written with yq rather than
# sed, so a file sink adds a key to the existing `sinks` block instead of a second
# `sinks:` mapping alongside it.
_init_write_config() {
  local resolved="$INIT_SINK_RESOLVED" persist="none" path=""
  case "$resolved" in
    issues)  persist="issues" ;;
    file\ *) persist="backlog"; path="${resolved#file }" ;;
    *)       persist="none" ;;
  esac

  if [[ -n "$path" ]]; then
    yq ".persist.defects = \"backlog\"
        | .sinks.backlog.type = \"file\"
        | .sinks.backlog.path = \"$path\"" \
      "$ROOT/templates/revloop.yml" >.github/revloop.yml
  else
    yq ".persist.defects = \"$persist\"" "$ROOT/templates/revloop.yml" >.github/revloop.yml
  fi
}
