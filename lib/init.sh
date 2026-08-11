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
INIT_RUNNER="github-hosted"
INIT_NEEDS_REFRESHER=0
INIT_WORKFLOWS=""
INIT_SECRETS=""

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

  # Which harnesses are reachable in CI is a property of the runner rather than
  # of the config, so this is settled before anything else — a pairing the runner
  # cannot serve should fail here, with the token lifetime named, rather than at
  # the first API call with an authentication error.
  INIT_RUNNER="$(cfg_get '.runner')"; [[ -n "$INIT_RUNNER" ]] || INIT_RUNNER="github-hosted"
  # A typo here is the worst kind of wrong. Rendering treats anything unrecognised
  # as hosted, while the refresher derivation matches the exact string — so
  # `runner: github_hosted` would emit hosted workflows with no refresher, and the
  # credential would expire ten days later with nothing pointing at the cause.
  case "$INIT_RUNNER" in
    github-hosted|self-hosted) : ;;
    *) ui_die "the config sets runner: $INIT_RUNNER, which revloop does not recognise" \
         "It must be exactly github-hosted or self-hosted. Anything else would be treated as hosted while behaving as neither, and the first sign would be a credential expiring weeks later." ;;
  esac
  _init_assert_runner_serves_pairing
  _init_resolve_refresher

  local max; max="$(cfg_get '.max_passes')"
  local i
  for (( i = 1; i <= max; i++ )); do
    INIT_PASS_LABELS="$INIT_PASS_LABELS revloop/pass-$i"
  done
  INIT_PASS_LABELS="${INIT_PASS_LABELS# }"
  INIT_FIXED_LABELS="revloop/awaiting-resolution revloop/awaiting-review revloop/converged revloop/halted revloop/stop"

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

  INIT_WORKFLOWS="review resolve watchdog"
  (( INIT_NEEDS_REFRESHER )) && INIT_WORKFLOWS="$INIT_WORKFLOWS token-refresh"

  local f t
  for t in $INIT_WORKFLOWS; do INIT_WRITES="$INIT_WRITES .github/workflows/revloop-$t.yml"; done
  INIT_WRITES="$INIT_WRITES .github/revloop.yml"
  for f in $INIT_WRITES; do
    [[ -e "$f" ]] && INIT_OVERWRITES="$INIT_OVERWRITES $f"
  done
  INIT_WRITES="${INIT_WRITES# }"; INIT_OVERWRITES="${INIT_OVERWRITES# }"

  INIT_SECRETS="$(_init_required_secrets)"
}

# ---------------------------------------------------------------------------
# What the runner can serve, and what that costs
# ---------------------------------------------------------------------------

_init_assert_runner_serves_pairing() {
  local leg harness endpoint reason
  for leg in reviewer resolver; do
    harness="$(cfg_get ".$leg.harness")"
    endpoint="$(cfg_get ".$leg.endpoint")"
    # An endpoint means a static token in a secret, which never rotates and so
    # never cares what kind of runner it is on. It still has to resolve: an
    # endpoint named nowhere is a hard failure, and this is where it should
    # surface rather than in the middle of assembling a secret list.
    if [[ -n "$endpoint" && "$endpoint" != "null" ]]; then
      cfg_endpoint "$endpoint" >/dev/null
      # Endpoints are Anthropic-compatible and only the claude adapter speaks
      # them; the others refuse at invocation. Caught here, that is a config
      # error before anything is installed. Caught there, it is a workflow that
      # installs cleanly, fires on the first pull request, and dies every time.
      [[ "$harness" == "claude" ]] || ui_die \
        "the $leg names the endpoint '$endpoint' but runs on '$harness', which cannot use one" \
        "Named endpoints are Anthropic-compatible and reached through the claude adapter. Use harness: claude with endpoint: $endpoint, or drop the endpoint for this leg."
      continue
    fi
    reason="$(preflight_pairing_supported "$INIT_RUNNER" "$harness")" && continue
    ui_die "the $leg is configured to run $harness by subscription, and a $INIT_RUNNER runner cannot serve that" \
      "$reason. Two fixes: set runner: self-hosted in the config, where every harness refreshes its own credential on disk; or name a different harness for this leg."
  done
}

# Derived, never asked.
#
# The refresher exists for exactly one situation: a harness whose credential
# rotates, authenticating by subscription, on an ephemeral runner. Change any one
# of those three and it disappears — so asking about it would be asking someone
# to restate a consequence of a pairing they already chose. A repository that
# does not need it never hears of it, and no second App is created.
_init_resolve_refresher() {
  local leg
  INIT_NEEDS_REFRESHER=0
  for leg in reviewer resolver; do
    if preflight_needs_refresher "$INIT_RUNNER" "$(cfg_get ".$leg.harness")" "$(cfg_get ".$leg.endpoint")"; then
      INIT_NEEDS_REFRESHER=1
    fi
  done
}

# The secrets this configuration actually needs, one name per line.
#
# Derived rather than fixed, because half of them exist only for one runner or
# one pairing. A hosted runner needs a credential for each subscription harness;
# a self-hosted one needs none of them, since the machine is already logged in.
_init_required_secrets() {
  printf 'APP_ID\n'
  printf 'APP_PRIVATE_KEY\n'
  printf 'REVLOOP_SOURCE_KEY\n'

  local leg harness endpoint ep tok seen=""
  for leg in reviewer resolver; do
    harness="$(cfg_get ".$leg.harness")"
    endpoint="$(cfg_get ".$leg.endpoint")"
    if [[ -n "$endpoint" && "$endpoint" != "null" ]]; then
      # An endpoint carries its own token variable, so the secret it needs is
      # whatever the endpoint block names — Kimi, Ollama or anything else. Naming
      # the variable rather than a vendor is what keeps this from going stale
      # every time the pairing changes.
      ep="$(cfg_endpoint "$endpoint")" || continue
      read -r _ tok <<<"$ep"
      [[ "$seen" == *" $tok "* ]] || { printf '%s\n' "$tok"; seen="$seen $tok "; }
      continue
    fi
    [[ "$INIT_RUNNER" == "self-hosted" ]] && continue
    case "$harness" in
      claude) [[ "$seen" == *" CLAUDE_CODE_OAUTH_TOKEN "* ]] || { printf 'CLAUDE_CODE_OAUTH_TOKEN\n'; seen="$seen CLAUDE_CODE_OAUTH_TOKEN "; } ;;
      codex)  [[ "$seen" == *" REVLOOP_CODEX_AUTH "* ]] || { printf 'REVLOOP_CODEX_AUTH\n'; seen="$seen REVLOOP_CODEX_AUTH "; } ;;
    esac
  done

  if (( INIT_NEEDS_REFRESHER )); then
    printf 'REVLOOP_REFRESH_APP_ID\n'
    printf 'REVLOOP_REFRESH_APP_PRIVATE_KEY\n'
  fi
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

  ui_line ""
  ui_line "runner            $INIT_RUNNER"
  local leg harness endpoint
  for leg in reviewer resolver; do
    harness="$(cfg_get ".$leg.harness")"
    endpoint="$(cfg_get ".$leg.endpoint")"
    if [[ -n "$endpoint" && "$endpoint" != "null" ]]; then
      ui_line "                  $leg: $harness via the '$endpoint' endpoint, a static token"
    else
      ui_line "                  $leg: $harness by subscription"
    fi
  done

  if (( INIT_NEEDS_REFRESHER )); then
    local refresher_meta; refresher_meta="$(_auth_meta "$INIT_OWNER" refresher)"
    ui_line ""
    ui_line "refresher App     needed — codex authenticates by subscription on an"
    ui_line "                  ephemeral runner, and its credential rotates, so one"
    ui_line "                  scheduled job refreshes it and the legs only read"
    if [[ -f "$refresher_meta" ]]; then
      ui_line "                  reuse \"$(jq -r .name "$refresher_meta")\" (id $(jq -r .id "$refresher_meta"))"
    else
      ui_line "                  one more browser approval, for an App carrying"
      ui_line "                  secrets:write and nothing else"
    fi
  fi

  local scope="repository"
  [[ "$INIT_OWNER_TYPE" == "organization" ]] && scope="organisation"
  ui_line ""
  ui_line "secrets           checked at $scope level, and set only where revloop has the value"
  local s
  while read -r s; do
    [[ -n "$s" ]] || continue
    if _init_secret_exists "$s"; then
      ui_line "                  $s — already set"
    else
      ui_line "                  $s — MISSING $(_init_secret_note "$s")"
    fi
  done <<<"$INIT_SECRETS"

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
    REVLOOP_REFRESH_APP_ID|REVLOOP_REFRESH_APP_PRIVATE_KEY)
      if [[ -f "$(_auth_meta "$INIT_OWNER" refresher)" ]]; then printf -- '— revloop will set it'
      else printf -- '— revloop will register the refresher App and set it'; fi ;;
    CLAUDE_CODE_OAUTH_TOKEN)
      printf -- '— revloop runs `claude setup-token` and captures the output; the token is never printed' ;;
    REVLOOP_CODEX_AUTH)
      printf -- '— seed once from a machine with a browser: `codex login`, then `gh secret set REVLOOP_CODEX_AUTH < ~/.codex/auth.json`' ;;
    REVLOOP_SOURCE_KEY)
      printf -- '— a read-only deploy key on %s; see the note after this plan' "$INIT_SOURCE_REPO" ;;
    *)
      printf -- '— the token an endpoint in the config names; set it yourself with `gh secret set %s`' "$1" ;;
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

  # The refresher App, when and only when the pairing needs one. Registering it
  # defensively would leave a private key carrying secrets:write sitting unused
  # in an organisation, which is precisely the credential nobody remembers to
  # rotate.
  if (( INIT_NEEDS_REFRESHER )); then
    # One credential, one repository, one writer. Concurrency groups do not span
    # repositories, so an organisation-level copy of the rotating credential
    # means every repository that reads it also refreshes it — and the first one
    # to refresh invalidates it for all the others, permanently.
    if [[ "$INIT_OWNER_TYPE" == "organization" ]] \
       && gh secret list --org "$INIT_OWNER" 2>/dev/null | grep -q '^REVLOOP_CODEX_AUTH\b'; then
      ui_warn "REVLOOP_CODEX_AUTH exists as an organisation secret on $INIT_OWNER" \
        "The refresher writes a repository secret, which takes precedence — so this repository will work and the organisation copy will go stale, breaking every other repository reading it. Each repository needs its own credential, seeded with its own \`codex login\`. Delete the organisation-level copy."
    fi

    local rmeta; rmeta="$(_auth_meta "$INIT_OWNER" refresher)"
    if [[ ! -f "$rmeta" ]]; then
      ui_gap
      ui_line "codex authenticates by subscription on an ephemeral runner, so one"
      ui_line "scheduled job has to refresh its credential. That job needs an App"
      ui_line "of its own carrying secrets:write — never the loop's App, which the"
      ui_line "review jobs use on attacker-controlled text."
      # Registering an App means approving it in a browser, so --yes cannot cover
      # it: a blanket yes must not stand in for an approval nobody is present to
      # give. Scripted runs name the command instead of pretending to run it.
      if [[ "${REVLOOP_ASSUME_YES:-0}" == "1" ]] || ! _ui_input_source >/dev/null 2>&1; then
        ui_no "no refresher App is registered for $INIT_OWNER, and registering one needs a browser"
        ui_next "revloop auth login --owner $INIT_OWNER --role refresher"
      elif ui_confirm "Register the refresher App for $INIT_OWNER?"; then
        auth_login --owner "$INIT_OWNER" --role refresher
        rmeta="$(_auth_meta "$INIT_OWNER" refresher)"
      fi
    fi
    if [[ -f "$rmeta" ]]; then
      local rpem; rpem="$(_auth_pem "$INIT_OWNER" refresher)"
      _init_secret_set REVLOOP_REFRESH_APP_ID "$(jq -r .id "$rmeta")" repo \
        || unfinished="$unfinished REVLOOP_REFRESH_APP_ID"
      if [[ -f "$rpem" ]]; then
        _init_secret_set REVLOOP_REFRESH_APP_PRIVATE_KEY "$(cat "$rpem")" repo \
          || unfinished="$unfinished REVLOOP_REFRESH_APP_PRIVATE_KEY"
      else
        ui_no "REVLOOP_REFRESH_APP_PRIVATE_KEY — the key file is missing at $rpem"
        unfinished="$unfinished REVLOOP_REFRESH_APP_PRIVATE_KEY"
      fi
    else
      unfinished="$unfinished REVLOOP_REFRESH_APP_ID REVLOOP_REFRESH_APP_PRIVATE_KEY"
    fi
  fi

  local s
  while read -r s; do
    case "$s" in ""|APP_ID|APP_PRIVATE_KEY|REVLOOP_REFRESH_APP_*) continue ;; esac
    if _init_secret_exists "$s"; then
      ui_ok "$s — already set"
      continue
    fi
    if [[ "$s" == "CLAUDE_CODE_OAUTH_TOKEN" ]] && _init_set_claude_token; then
      continue
    fi
    ui_no "$s — not set, and revloop does not have the value to set it"
    unfinished="$unfinished $s"
  done <<<"$INIT_SECRETS"

  # --- files ---------------------------------------------------------------
  ui_section "Files"
  mkdir -p .github/workflows
  local t name
  for t in $INIT_WORKFLOWS; do
    name=".github/workflows/revloop-$t.yml"
    _init_render_workflow "$ROOT/templates/revloop-$t.yml" >"$name"
    ui_ok "wrote $name"
  done
  # A pairing that stopped needing the refresher leaves its workflow behind,
  # still on a cron, still failing on a secret nobody sets any more. Saying so
  # beats a scheduled job that emails a failure every twelve hours.
  if (( INIT_NEEDS_REFRESHER == 0 )) && [[ -e .github/workflows/revloop-token-refresh.yml ]]; then
    ui_warn "this configuration needs no refresher, but .github/workflows/revloop-token-refresh.yml is still there" \
      "It stays on its schedule and fails every run once the credential it reads is gone. Delete it, and remove the refresher App's secrets, if the pairing is not going back."
  fi

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
        ui_no "CLAUDE_CODE_OAUTH_TOKEN — a leg runs on Claude, so it cannot authenticate"
        ui_line "   claude setup-token"
        ui_line "   gh secret set CLAUDE_CODE_OAUTH_TOKEN $(_init_secret_scope_flag)"
        ui_line ""
        ui_line "   That token is valid for a year and the command will not show it"
        ui_line "   again, so put it in the secret in the same sitting. Re-run"
        ui_line "   \`revloop init\` from a terminal and it does both, and records the"
        ui_line "   date so \`revloop auth status\` can warn as the year closes." ;;
      REVLOOP_CODEX_AUTH)
        ui_no "REVLOOP_CODEX_AUTH — a leg runs on Codex, so it cannot authenticate"
        ui_line "   codex login          # on a machine with a browser"
        # --repo unconditionally, never _init_secret_scope_flag. On an org-owned
        # repository that helper prints --org, and this is the one secret that
        # must never be organisation-scoped — the same misconfiguration init
        # warns about a few lines above. An instruction someone copies verbatim
        # is not the place to be inconsistent with your own warning.
        ui_line "   gh secret set REVLOOP_CODEX_AUTH --repo $INIT_REPO < ~/.codex/auth.json"
        ui_line ""
        ui_line "   Repository-scoped, not organisation-scoped, even on an org."
        ui_line "   Concurrency groups do not span repositories, so an org-level"
        ui_line "   copy is refreshed by every repository reading it and the first"
        ui_line "   one to refresh invalidates it for all the rest."
        ui_line ""
        ui_line "   Seeded once. From then on the refresher workflow is the only"
        ui_line "   thing that writes it, because using a refresh token consumes it"
        ui_line "   and a second writer kills the chain for everyone." ;;
      REVLOOP_REFRESH_APP_ID|REVLOOP_REFRESH_APP_PRIVATE_KEY)
        ui_no "$s — without the refresher App, codex's credential expires and stays expired"
        ui_line "   revloop auth login --owner $INIT_OWNER --role refresher"
        ui_line "   revloop init --upgrade" ;;
      APP_ID|APP_PRIVATE_KEY)
        : ;;   # already explained above
      *)
        ui_no "$s — an endpoint in the config names it, and nothing sets it"
        ui_line "   gh secret set $s $(_init_secret_scope_flag)" ;;
    esac
  done
  ui_end "The workflows are installed but will fail at the first missing secret, before any review runs."
  return 0
}

# Render one workflow template for the configured runner.
#
# Two mechanisms, and the split is deliberate. Scalars are substituted; whole
# steps and env entries are fenced, because a hosted runner installs the harness
# per run and passes credentials in as secrets, while a self-hosted one has both
# already and passing them would be the mistake — a secret restored over a
# working on-disk login is how you get a job authenticating as something nobody
# intended.
#
#   # revloop:only <runner>
#   ...lines kept only for that runner...
#   # revloop:end
# The install line for the harnesses this configuration actually names.
#
# Installing only Claude is worse than failing: `run_resolve_leg` falls back to
# whatever harness *is* present, warns in one line nobody reads in a CI log, and
# the loop runs both legs on one model. It completes normally, and the
# cross-model property the whole design exists for is gone with no error
# anywhere. Neither harness is on GitHub's runner images, so what gets installed
# has to follow the pairing.
_init_harness_install_line() {
  local leg harness endpoint seen="" out=""
  for leg in reviewer resolver; do
    harness="$(cfg_get ".$leg.harness")"
    # A leg on an endpoint still runs through the claude binary.
    endpoint="$(cfg_get ".$leg.endpoint")"
    [[ -n "$endpoint" && "$endpoint" != "null" ]] && harness="claude"
    [[ "$seen" == *" $harness "* ]] && continue
    seen="$seen $harness "
    # agy cannot appear here: it has no unattended installer, which is why the
    # pairing check refuses it on a hosted runner before rendering happens.
    case "$harness" in
      claude) out="$out          npm install -g @anthropic-ai/claude-code"$'\n' ;;
      codex)  out="$out          npm install -g @openai/codex"$'\n' ;;
    esac
  done
  # Indented to sit inside the template's `run: |` block, and trailing newline
  # trimmed so the substitution does not leave a blank line behind.
  printf '%s' "${out%$'\n'}"
}

_init_render_workflow() {
  local template="$1" runs_on refresh_scope
  if [[ "$INIT_RUNNER" == "self-hosted" ]]; then
    # Two labels, not one: `self-hosted` alone matches every self-hosted runner
    # the owner has, including ones set up for something else entirely.
    runs_on="[self-hosted, revloop]"
  else
    runs_on="ubuntu-latest"
  fi
  # Always repository-scoped. An organisation-level rotating credential would be
  # refreshed by every repository reading it, and concurrency groups do not span
  # repositories — so "one writer" would quietly become several, and the first to
  # refresh would invalidate the rest.
  refresh_scope="--repo $INIT_REPO"

  # The install block is several lines, and neither `sed s///` nor `awk -v` can
  # carry a newline in a replacement — awk rejects the assignment outright with
  # "newline in string". The environment can, and ENVIRON reads it back intact.
  #
  # A temp file was the first fix and was worse: this runs once per workflow, so
  # a RETURN trap cleans up on the ordinary path and leaks a file on every other
  # one. A file that never exists needs no trap.
  REVLOOP_HARNESS_INSTALL="$(_init_harness_install_line)" awk '
    index($0, "__HARNESS_INSTALL__") {
      n = split(ENVIRON["REVLOOP_HARNESS_INSTALL"], a, "\n")
      for (i = 1; i <= n; i++) print a[i]
      next
    }
    { print }' "$template" \
    | sed -e "s#__SOURCE_REPO__#$INIT_SOURCE_REPO#g" \
      -e "s#__SOURCE_SHA__#$INIT_SOURCE_SHA#g" \
      -e "s#__SOURCE_REF__#$INIT_SOURCE_REF#g" \
      -e "s#__RUNS_ON__#$runs_on#g" \
      -e "s#__REFRESH_SCOPE__#$refresh_scope#g" \
    | awk -v want="$INIT_RUNNER" '
        /^[[:space:]]*# revloop:only / { skip = ($3 != want); next }
        /^[[:space:]]*# revloop:end/   { skip = 0; next }
        !skip'
}

# Capture `claude setup-token` rather than asking for a paste.
#
# The command opens a browser for one authorisation and then prints the token to
# stdout, saying plainly that it will not show it again. Capturing it here closes
# the last place in the hosted setup where a credential would otherwise pass
# through a clipboard.
#
# The terminal still sees the command's own output, with anything token-shaped
# redacted on the way past: the URL and the prompts are what someone needs to
# complete the flow, and the token is not.
_init_set_claude_token() {
  command -v claude >/dev/null 2>&1 || return 1
  _ui_input_source >/dev/null 2>&1 || return 1   # no terminal, no browser flow

  ui_gap
  ui_line "CLAUDE_CODE_OAUTH_TOKEN is missing, and both legs need it to authenticate."
  ui_line "\`claude setup-token\` opens a browser once and prints a token valid for a"
  ui_line "year. revloop captures it straight into the secret — it is never printed"
  ui_line "here, never written to a file, and never shown again by anything."
  ui_confirm "Run \`claude setup-token\` now?" || return 1

  local raw token; raw="$(mktemp)"
  chmod 600 "$raw"
  # EXIT as well as RETURN. A RETURN trap does not fire when the process is cut
  # short — a Ctrl-C or a SIGTERM between `tee` writing the token and this
  # function returning would leave a year-long credential sitting in a temp file.
  # Both are set because the ordinary path returns normally and should clean up
  # then rather than at the end of the run.
  # shellcheck disable=SC2064  # expand now, not at trap time
  trap "rm -f '$raw'" RETURN EXIT

  # tee keeps the raw copy; sed redacts what reaches the terminal.
  #
  # `|| true` is load-bearing under `set -o pipefail`: a failed authorisation
  # would otherwise abort init entirely, halfway through, having already written
  # labels and secrets. The check below is what decides whether this worked.
  claude setup-token 2>&1 | tee "$raw" \
    | sed -E 's/(sk-ant-[A-Za-z0-9_-]{6})[A-Za-z0-9_-]+/\1…[captured by revloop, not shown]/g' \
    || true

  token="$(grep -oE 'sk-ant-[A-Za-z0-9_-]{20,}' "$raw" | tail -1)"
  if [[ -z "$token" ]]; then
    ui_warn "\`claude setup-token\` finished without printing a token revloop could recognise" \
      "The secret is not set, so CI cannot authenticate yet. Run it by hand and set the secret: claude setup-token, then gh secret set CLAUDE_CODE_OAUTH_TOKEN $(_init_secret_scope_flag)"
    return 1
  fi

  _init_secret_set CLAUDE_CODE_OAUTH_TOKEN "$token" || return 1
  # The one-year clock starts now, and this is the only moment the date exists:
  # the token cannot be read back, so nothing later can work out when it was
  # issued. Without this the first sign of expiry is a CI failure.
  auth_token_record "$INIT_REPO" CLAUDE_CODE_OAUTH_TOKEN 365
  ui_line "   expires in 365 days — \`revloop auth status\` warns as that closes"
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
#
# $3 is "repo" to force repository scope, and two secrets require it.
#
# **The refresher App's private key must never be an organisation secret with
# `--visibility all`.** That key can rewrite repository secrets, and org-wide
# visibility hands it to every workflow in the organisation — including this
# design's own review job, which checks out a pull request branch and runs a
# model over a diff. A prompt injection that reaches tool use in that job could
# read the key from its environment and mint a token that overwrites secrets. The
# whole argument for a second App is that its permission is unreachable from
# untrusted text; org-wide visibility would hand it straight back.
#
# The rotating credential is repository-scoped for a different reason: one
# credential refreshed by one writer, and concurrency groups do not span
# repositories.
_init_secret_set() {
  local name="$1" value="$2" force="${3:-}"
  if [[ "$force" == "repo" ]]; then
    if printf '%s' "$value" | gh secret set "$name" --repo "$INIT_REPO" >/dev/null 2>&1; then
      ui_ok "$name — set on $INIT_REPO, repository-scoped on purpose"
      return 0
    fi
    ui_no "$name — could not be set on $INIT_REPO"
    return 1
  fi
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
