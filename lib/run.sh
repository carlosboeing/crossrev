# shellcheck shell=bash
# lib/run.sh — the two legs, and the drivers over them.
#
# Everything that writes to a pull request goes through here. The decisions are
# in legs.sh, the GitHub calls are in github.sh, and the judgement is in the
# skills; this file is the sequencing, and the sequencing is where idempotency
# lives.
#
# Three mechanisms, in the order they execute:
#
#   1. Claim before work.  The leg's first write is a marker comment with
#      state "started". A second invocation finding an unfinished claim for the
#      same (pr, pass, leg) enters recovery instead of starting fresh.
#   2. Reconcile, do not redo — and the ledger IS the write. Every outward write
#      carries its finding id in its own body, so the record and the thing it
#      records land in one HTTP call. A ledger written afterwards has a window
#      that duplicates comments; this closes it by construction.
#   3. Complete last.  The claim is edited to "complete" only once every write
#      has landed. A marker without completion is what the watchdog looks for.
#
# One structural rule runs through the whole file: anything that can call ui_die
# is called directly, never inside a command substitution. `exit` inside `$( )`
# ends the subshell and lets the caller carry on with an empty string, which is
# how a fatal error becomes a silent one.

REVLOOP_INTERRUPTED=0
REVLOOP_LOCK=""
REVLOOP_SANDBOXED=""
REVLOOP_RESUME_HINT=""

# ---------------------------------------------------------------------------
# Interruption, locking and cleanup
# ---------------------------------------------------------------------------
#
# Ctrl-C is the expected way to stop a local run, so a leg finishes the write in
# flight and leaves a clean claim rather than dying between posting a comment and
# recording it.

run_trap_install() {
  trap 'REVLOOP_INTERRUPTED=1' INT TERM
  trap run_cleanup EXIT
}

run_cleanup() {
  [[ -n "$REVLOOP_SANDBOXED" ]] && sandbox_restore "$REVLOOP_SANDBOXED"
  [[ -n "$REVLOOP_LOCK" && -f "$REVLOOP_LOCK" ]] && rm -f "$REVLOOP_LOCK"
  # A restored credential dies with the process that borrowed it, on the fatal
  # paths as much as the clean one. Leaving it on disk is how a second job finds
  # a copy of a token that only one holder may refresh.
  cred_discard
  return 0
}

# Called between outward writes. Nothing is half-written when this returns.
run_checkpoint() {
  (( REVLOOP_INTERRUPTED == 0 )) && return 0
  ui_warn "interrupted after the last completed write" \
    "The claim marker on the pull request records how far this got, so nothing is duplicated on the way back in. Resume with: ${REVLOOP_RESUME_HINT:-revloop status --pr <n>}"
  exit 130
}

# Automated mode uses one concurrency group per pull request. Locally, two
# terminals against the same PR would interleave writes, so take a lock and name
# the holder rather than failing opaquely.
run_lock_acquire() {
  local pr="$1" mode="$2" gitdir lock holder pid
  [[ "$mode" == "event-driven" ]] && return 0
  gitdir="$(git rev-parse --git-dir 2>/dev/null)" || return 0
  mkdir -p "$gitdir/revloop"
  lock="$gitdir/revloop/pr-$pr.lock"
  if [[ -f "$lock" ]]; then
    holder="$(cat "$lock" 2>/dev/null)"
    pid="${holder%% *}"
    # Our own lock, from an earlier leg in this same process. `revloop run`
    # drives review and resolve one after the other, so the second leg re-enters
    # here holding what the first one took — a live PID that passes the check
    # below and reads as a collision with itself. Keep the lock for the whole
    # run rather than releasing it between legs: dropping it mid-loop would open
    # a window for a second terminal to start a pass halfway through this one.
    if [[ "$pid" == "$$" ]]; then return 0; fi
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      ui_die "another revloop run already holds pull request $pr — $holder" \
        "Two runs writing the same pull request would interleave comments and replies. Wait for it to finish, or stop that process."
    fi
    ui_warn "a previous run left a lock on pull request $pr held by $holder, which is no longer running" \
      "Taking it over. If that run was killed mid-write, its claim marker records how far it got and this run posts only the difference."
    rm -f "$lock"
  fi
  printf '%s on %s since %s\n' "$$" "$(hostname 2>/dev/null || echo local)" \
    "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" >"$lock"
  REVLOOP_LOCK="$lock"
}

# ---------------------------------------------------------------------------
# Shared context
# ---------------------------------------------------------------------------

CTX_REPO=""; CTX_PR=""; CTX_HEAD_SHA=""; CTX_BASE_SHA=""
CTX_HEAD_BRANCH=""; CTX_DEFAULT_BRANCH=""; CTX_CHANGED=0
CTX_TITLE=""; CTX_BODY=""; CTX_LABELS=""
CTX_MODE=""; CTX_AUTHOR=""; CTX_MARKERS="[]"; CTX_MAX_PASSES=3
CTX_SINK="none"; CTX_IDENTITY_LABEL="revloop-review"; CTX_SINK_LABELS=""
CTX_FIX_AT="medium"

ctx_load() {
  local pr="$1" repo="${2:-}" pr_json want
  preflight_require_yq

  if [[ -z "$repo" ]]; then
    repo="$(gh_repo_slug)"
    [[ -n "$repo" ]] || ui_die "could not work out which repository this is" \
      "Run revloop from a checkout with a GitHub remote, or pass --repo owner/name."
  fi

  pr_json="$(gh_pr_json "$repo" "$pr")"
  [[ -n "$pr_json" ]] || ui_die "could not read $repo#$pr" \
    "Check the number, and that \`gh auth status\` passes for that repository."

  # Fork pull requests fail closed. GitHub withholds secrets from them, so the
  # loop would run unauthenticated rather than not at all — and a fork's head
  # branch is not ours to push to.
  [[ "$(jq -r .isCrossRepository <<<"$pr_json")" == "false" ]] || ui_die \
    "$repo#$pr comes from a fork" \
    "revloop does not run on fork pull requests: GitHub withholds secrets from them, and the head branch is not this repository's to push to. Review it by hand."

  [[ "$(jq -r .state <<<"$pr_json")" == "OPEN" ]] || ui_die \
    "$repo#$pr is not open" \
    "revloop only runs on open pull requests. Reopen it, or pick another number."

  CTX_REPO="$repo"
  CTX_PR="$pr"
  CTX_TITLE="$(jq -r .title <<<"$pr_json")"
  CTX_BODY="$(jq -r '.body // ""' <<<"$pr_json")"
  CTX_HEAD_SHA="$(jq -r .headRefOid <<<"$pr_json")"
  CTX_BASE_SHA="$(jq -r .baseRefOid <<<"$pr_json")"
  CTX_HEAD_BRANCH="$(jq -r .headRefName <<<"$pr_json")"
  CTX_CHANGED="$(jq -r '.changedFiles // 0' <<<"$pr_json")"
  CTX_LABELS="$(jq -r '[.labels[].name] | join(" ")' <<<"$pr_json")"
  CTX_DEFAULT_BRANCH="$(gh_default_branch "$repo")"

  # Policy comes from the base revision, never the pull request head. Read from
  # the head, a branch could raise max_passes, repoint an endpoint at a server it
  # controls and harvest every prompt, or ship a REVIEW.md saying to return
  # converged. A branch that legitimately changes review policy therefore takes
  # effect when it merges, which is the correct order.
  cfg_load "$CTX_BASE_SHA"
  CTX_MODE="$(cfg_get '.mode')"
  CTX_MAX_PASSES="$(cfg_get '.max_passes')"
  CTX_FIX_AT="$(cfg_get '.resolver.fix_at')"

  want="$(cfg_get '.persist.defects')"
  CTX_SINK="$(cfg_resolve_sink "$CTX_BASE_SHA" "$want")"
  CTX_IDENTITY_LABEL="$(run_sink_field "$want" identity_label "revloop-review")"
  CTX_SINK_LABELS="$(run_sink_field "$want" labels "")"

  CTX_AUTHOR="$(state_trusted_author "$CTX_MODE")"
  [[ -n "$CTX_AUTHOR" ]] || ui_die "could not resolve whose markers to trust on $repo#$pr" \
    "Pass numbering, revision detection and the daily cap all read from the trusted author. Run: gh auth login"
  CTX_MARKERS="$(state_markers "$CTX_PR" "$CTX_REPO" "$CTX_AUTHOR")"
}

# A sink's own settings, by name or by finding the first one of a usable type.
run_sink_field() {
  local want="$1" field="$2" default="$3" name="$1" out
  if [[ -z "$want" || "$want" == "auto" || "$want" == "none" ]]; then
    name="$(jq -r 'first((.sinks // {} | to_entries[] | select(.value.type == "github_issue") | .key)) // empty' <<<"$CFG_MERGED")"
  fi
  [[ -n "$name" ]] || { printf '%s' "$default"; return 0; }
  out="$(jq -r --arg n "$name" --arg f "$field" '
    (.sinks[$n][$f]) as $v
    | if $v == null then ""
      elif ($v | type) == "array" then ($v | join(" "))
      else ($v | tostring) end' <<<"$CFG_MERGED")"
  if [[ -n "$out" ]]; then printf '%s' "$out"; else printf '%s' "$default"; fi
}

# Labels are load-bearing for the event chain and cosmetic without it, so the
# consequence of a missing one differs by mode. In event-driven mode a label that
# cannot be applied stalls the loop, which is fatal. Locally one process drives
# both legs, so it is a warning — otherwise every local run against a repository
# that never ran `init` would die after posting a perfectly good review.
run_label_add() {
  local label="$1"
  if [[ "$CTX_MODE" == "event-driven" ]]; then
    state_label_add "$CTX_PR" "$CTX_REPO" "$label"
  else
    gh api --method POST "repos/$CTX_REPO/issues/$CTX_PR/labels" -f "labels[]=$label" >/dev/null 2>&1 \
      || ui_warn "could not apply the label '$label' to $CTX_REPO#$CTX_PR" \
           "Locally that is cosmetic, because this process drives both legs itself. In automated mode it would stall the chain, which is what \`revloop init\` creates the labels for."
  fi
  return 0
}

# ---------------------------------------------------------------------------
# Severity, and what the threshold makes of it
# ---------------------------------------------------------------------------

# Capitalise a word for display. Not `${x^}`: macOS ships bash 3.2, where that
# expansion is a syntax error rather than a no-op.
_ucfirst() {
  local s="$1"
  printf '%s%s' "$(printf '%s' "${s:0:1}" | tr '[:lower:]' '[:upper:]')" "${s:1}"
}

# How many findings the resolve leg may change code for: at or above `fix_at`,
# and not pre-existing. The verdict, the labels and the summary all read this
# one number.
#
# The rank table is taken from legs.sh rather than restated in jq. Two copies of
# an ordinal scale drift, and the drift is invisible from outside: a pull request
# that says nothing is outstanding while the resolve leg is still committing.
run_actionable() {
  local findings="$1" ranks bar
  bar="$(legs_severity_rank "$CTX_FIX_AT")"
  ranks="$(jq -cn --argjson h "$(legs_severity_rank high)" \
                  --argjson m "$(legs_severity_rank medium)" \
                  --argjson l "$(legs_severity_rank low)" \
             '{high:$h, medium:$m, low:$l}')"
  jq --argjson r "$ranks" --argjson bar "$bar" '
    [ .[] | select((.pre_existing // false) | not)
          | select($bar > 0 and (($r[.severity] // 0) >= $bar)) ] | length' <<<"$findings"
}

# Severity is a scale, so it gets coloured circles: they read as one, and they
# carry colour into the summary table, where a GitHub alert cannot reach because
# alerts are block-level and do not render inside a cell.
run_severity_emoji() {
  case "$1" in
    high)   printf '🔴' ;;
    medium) printf '🟠' ;;
    low)    printf '🔵' ;;
    *)      printf '⚪' ;;
  esac
}

# Category is a kind rather than a scale, so it gets a pictogram. These appear in
# the summary table, where the column header supplies the context a bare glyph
# would otherwise lack.
run_category_emoji() {
  case "$1" in
    correctness)     printf '🐛' ;;
    security)        printf '🔒' ;;
    performance)     printf '⚡' ;;
    maintainability) printf '🧹' ;;
    testing)         printf '🧪' ;;
    docs)            printf '📄' ;;
    *)               printf '❓' ;;
  esac
}

# `🔴 **High · Security**`, the prefix of an inline finding heading.
#
# One line, so it does not wrap in a narrow diff column, and the category stays a
# readable word rather than a pictogram the reader has to decode. Provenance is
# deliberately absent: `pre-existing` is not a fourth severity and must not read
# as one, so it goes in the summary table's *what* cell and in the note under
# this finding, both of which have room to say what it means.
#
# Decorates the RENDERED heading only. A finding's stable id derives from its
# title, so decorating the title field itself would re-post every finding as new
# on the next pass.
run_finding_label() {
  local f="$1" sev kind
  sev="$(jq -r '.severity // "?"' <<<"$f")"
  kind="$(jq -r '.category // "?"' <<<"$f")"
  printf '%s **%s · %s**' "$(run_severity_emoji "$sev")" \
    "$(_ucfirst "$sev")" "$(_ucfirst "$kind")"
}

# Text that is about to land in a Markdown table cell.
#
# A model-written title carrying a pipe splits the row into extra columns, and a
# newline ends the table outright — both of which are silently wrong rather than
# obviously broken, because the surrounding rows still render.
_md_cell() {
  printf '%s' "$1" | tr '\n' ' ' | sed 's/|/\\|/g'
}

run_pass_labels() {
  local pass="$1" next="$2" l
  for l in awaiting-review awaiting-resolution converged halted; do
    [[ "$l" == "$next" ]] && continue
    state_label_remove "$CTX_PR" "$CTX_REPO" "revloop/$l"
  done
  # A pass that got this far is a pass that did not stall, so the watchdog's
  # retry marker goes with the state it described. Left standing it is per-PR
  # rather than per-stall: a pull request that stalls on pass 1, recovers, and
  # stalls again on pass 3 is halted on the spot for having "already been
  # retried once", and someone has to remove the label by hand to restart it.
  state_label_remove "$CTX_PR" "$CTX_REPO" "revloop/watchdog-retried"
  run_label_add "revloop/pass-$pass"
  [[ -n "$next" ]] && run_label_add "revloop/$next"
  return 0
}

# ---------------------------------------------------------------------------
# Which harness runs a leg
# ---------------------------------------------------------------------------

LEG_HARNESS=""; LEG_MODEL=""; LEG_EFFORT=""; LEG_ENDPOINT=""

# Sets the four LEG_* globals rather than printing them, so the warnings and the
# fatal case below reach the caller instead of dying in a subshell.
#
# Includes the single-harness fallback: when only one harness is installed, run
# both legs on it and say so in one line naming the cost, rather than halting.
# The divergence guard stays quiet in that case by design — it catches a config
# that asked for two models and got one, and this config asked for one.
run_leg_settings() {
  local leg="$1" override="${2:-}" alt="" h
  LEG_HARNESS="$(cfg_get ".$leg.harness")"
  LEG_MODEL="$(cfg_get ".$leg.model")"
  LEG_EFFORT="$(cfg_get ".$leg.effort")"
  LEG_ENDPOINT="$(cfg_get ".$leg.endpoint")"
  if [[ -n "$override" ]]; then LEG_HARNESS="$override"; LEG_ENDPOINT=""; LEG_MODEL=""; fi

  # A harness needs an adapter, not just a binary on PATH — and this is checked
  # before the binary test precisely because being installed is what makes it
  # reachable. `kimi` is the live case: the CLI sits on plenty of machines, so
  # `command -v kimi` succeeds, the function returns here, and the dispatch
  # three steps later dies on `adapter_kimi: command not found` with nothing
  # pointing back at the configuration that asked for it. The fallback below was
  # taught not to *choose* a harness with no adapter; it could not help when one
  # was named outright.
  if ! declare -F "adapter_$LEG_HARNESS" >/dev/null 2>&1; then
    ui_die "there is no adapter for the harness '$LEG_HARNESS'" \
      "revloop drives claude, codex and agy directly. Kimi is reached through the claude adapter instead: define it under endpoints: and set $leg.endpoint, not $leg.harness."
  fi

  command -v "$LEG_HARNESS" >/dev/null 2>&1 && return 0

  # Only harnesses that have an adapter. `kimi` was in this list and should not
  # have been: it is reached through the claude adapter as an endpoint, so
  # falling back to it named a harness with no adapter_kimi behind it, and the
  # leg died on "command not found" rather than on the missing harness.
  for h in claude codex agy; do
    command -v "$h" >/dev/null 2>&1 && { alt="$h"; break; }
  done
  [[ -n "$alt" ]] || ui_die \
    "the $leg is configured to use '$LEG_HARNESS', which is not installed, and no other harness is either" \
    "Install one of claude, codex or agy. revloop needs at least one, and two different ones is what makes the cross-model check mean anything."

  ui_warn "'$LEG_HARNESS' is not installed, so the $leg runs on '$alt' instead" \
    "Both legs now run on the same harness, so a bug it misses while reviewing it also misses while resolving. Install $LEG_HARNESS to get the second lineage back."
  LEG_HARNESS="$alt"
  # A model id for the harness that was asked for is wrong for a different one.
  LEG_MODEL=""; LEG_ENDPOINT=""
  return 0
}

_nullable() { [[ "$1" == "null" ]] && printf '' || printf '%s' "$1"; }

# Invoke a harness against a prompt and a schema, with the checkout quarantined,
# and write the adapter's envelope to $1.
#
# Not a command substitution: the quarantine has to be recorded in the caller's
# own REVLOOP_SANDBOXED so that the EXIT trap restores the checkout if this dies.
# A quarantine leaked out of a subshell leaves someone's working tree mangled.
#
# Retries once when the harness does not constrain its own output, which is the
# only path where a mismatched object is expected rather than a bug.
run_invoke() {
  local out_file="$1" harness="$2" prompt_file="$3" schema_file="$4" workdir="$5"
  local model="$6" effort="$7" endpoint="$8" validator="$9"

  # Layer one of the divergence guard, and the specific failure it exists for:
  # these variables are process-scoped, so a leg that leaks them silently
  # redirects the other leg too and the loop completes normally with the
  # cross-model property gone and no error anywhere.
  legs_assert_env_clean

  # A credential restored from a secret is read, used and thrown away. The
  # scratch home is discarded whichever way this returns, including the fatal
  # paths below, so a harness that refreshes and writes back on its own writes
  # into a directory nothing reads again.
  cred_prepare "$harness"

  local attempts=1 i=1 payload problem
  validate_harness_is_schema_native "$harness" || attempts=2

  while (( i <= attempts )); do
    REVLOOP_SANDBOXED="$workdir"
    sandbox_quarantine "$workdir" >/dev/null
    # Adapter stderr is NOT discarded. Each adapter already captures the harness
    # CLI's own noise into a temp file, so what reaches here is revloop's own
    # messages — including a fatal one about an endpoint that does not resolve.
    # Swallowing those left the process exiting 1 with nothing printed.
    "adapter_$harness" "$prompt_file" "$schema_file" "$workdir" \
      "$model" "$effort" "$endpoint" >"$out_file" || true
    sandbox_restore "$workdir"
    REVLOOP_SANDBOXED=""

    [[ -s "$out_file" ]] || printf '%s' \
      '{"ok":false,"payload":null,"error":"the adapter returned nothing at all"}' >"$out_file"

    if [[ "$(jq -r '.ok // false' "$out_file")" != "true" ]]; then
      ui_die "the $harness harness failed: $(jq -r '.error // "no error reported"' "$out_file")" \
        "Nothing has been written to the pull request. Check the harness runs by hand: $harness --version"
    fi

    payload="$(jq -c '.payload' "$out_file")"
    if problem="$("$validator" "$payload")"; then cred_discard; return 0; fi

    if (( i < attempts )); then
      ui_warn "$harness returned an object that does not match the schema — $problem" \
        "That harness does not constrain its own output, so this is the expected failure rather than a bug. Retrying once; a second mismatch is fatal."
    else
      ui_die "$harness returned an object that does not match the schema — $problem" \
        "This harness validates output against the schema natively, so a mismatch is an adapter or harness bug rather than model drift. Nothing has been written to the pull request."
    fi
    i=$((i+1))
  done
}

# ---------------------------------------------------------------------------
# The review leg
# ---------------------------------------------------------------------------

leg_review() {
  local pr="" repo="" harness_override="" no_tips=0
  while (( $# )); do
    case "$1" in
      --pr)      pr="${2:-}"; shift 2 ;;
      --repo)    repo="${2:-}"; shift 2 ;;
      --harness) harness_override="${2:-}"; shift 2 ;;
      --no-tips) no_tips=1; shift ;;
      --pass)    shift 2 ;;   # accepted and ignored: the pass comes from the PR
      *) ui_die "unknown option for review: $1" \
           "Usage: revloop review --pr <number> [--harness claude|codex] [--no-tips]" ;;
    esac
  done
  [[ -n "$pr" ]] || ui_die "revloop review needs a pull request number" "Usage: revloop review --pr 42"

  run_trap_install
  ctx_load "$pr" "$repo"
  run_lock_acquire "$CTX_PR" "$CTX_MODE"
  REVLOOP_RESUME_HINT="revloop review --pr $CTX_PR"

  # A human's request outranks everything, including a healthy verdict.
  if grep -qw "revloop/stop" <<<"$CTX_LABELS"; then
    ui_say "revloop/stop is on $CTX_REPO#$CTX_PR, so this run stops without reviewing."
    ui_say "Remove the label to let the loop continue."
    return 0
  fi

  local current pass claim stale recovering=0
  current="$(state_current_review_pass "$CTX_MARKERS")"
  claim="$(state_open_claim "$CTX_MARKERS" "$current" review)" || claim=""

  if [[ -n "$claim" ]]; then
    if stale="$(state_claim_is_stale "$claim" "$CTX_HEAD_SHA")"; then
      ui_warn "abandoning the unfinished pass-$current review claim — $stale" \
        "Resuming it would reconcile against findings that no longer describe this code. Starting the pass again instead."
      pass="$current"
      claim="$(jq -c --argjson ts "$(date +%s)" --arg sha "$CTX_HEAD_SHA" \
        '.ts = $ts | .head_sha = $sha | .findings = [] | .verdict = null' <<<"$claim")"
      recovering=1   # reuse the comment, not the work
    else
      recovering=1
      pass="$current"
    fi
  elif state_is_new_revision "$CTX_MARKERS" "$CTX_HEAD_SHA"; then
    pass=$(( current + 1 ))
  else
    ui_say "$CTX_REPO#$CTX_PR is already reviewed at ${CTX_HEAD_SHA:0:7} — pass $current, and nothing has changed since."
    ui_say "Push a revision, or run: revloop resolve --pr $CTX_PR"
    return 0
  fi

  # Termination, asked as "should a pass after $((pass-1)) begin?". Pass 3 of a
  # max_passes of 3 is the last pass, not the one after which a fourth starts.
  local runs_today decision reason
  runs_today="$(state_runs_today "$CTX_MARKERS")"
  if ! decision="$(legs_should_continue "issues-remain" "$(( pass - 1 ))" "$CTX_MAX_PASSES" \
      false false "$runs_today" "$(cfg_get '.caps.runs_per_day')" \
      "$CTX_CHANGED" "$(cfg_get '.caps.max_files_changed')")"; then
    reason="${decision#* }"
    ui_say "not reviewing $CTX_REPO#$CTX_PR — $reason"
    gh_comment_create "$CTX_REPO" "$CTX_PR" \
"**revloop stopped before pass $pass** — $reason.

No review ran, so nothing here is a judgement about the code. Raising the cap in \`.github/revloop.yml\` and pushing a revision would start it again." >/dev/null
    run_pass_labels "$(( pass > 1 ? pass - 1 : 1 ))" halted
    return 0
  fi

  run_leg_settings reviewer "$harness_override"
  local harness="$LEG_HARNESS" model effort endpoint
  model="$(_nullable "$LEG_MODEL")"
  effort="$(_nullable "$LEG_EFFORT")"
  endpoint="$(_nullable "$LEG_ENDPOINT")"

  printf '\n  Reviewing %s#%s — pass %s of %s\n' "$CTX_REPO" "$CTX_PR" "$pass" "$CTX_MAX_PASSES"
  printf '  Reviewer: %s%s%s\n' "$harness" "${model:+, $model}" "${effort:+, $effort effort}"

  # --- claim before work ---------------------------------------------------
  local comment_id marker
  if (( recovering )); then
    comment_id="$(jq -r '.comment_id' <<<"$claim")"
    marker="$claim"
    ui_say "Resuming pass $pass — the previous attempt recorded $(jq -r '(.findings // []) | length' <<<"$marker") finding(s)."
  else
    marker="$(jq -cn --argjson p "$pass" --arg sha "$CTX_HEAD_SHA" \
      --arg r "${GITHUB_RUN_ID:-local-$$}" --argjson ts "$(date +%s)" \
      --arg h "$harness" --arg m "$model" \
      '{v:1, leg:"review", pass:$p, state:"started", ts:$ts, run_id:$r,
        head_sha:$sha, harness:$h, model:(if $m == "" then null else $m end),
        model_reported:null, verdict:null, findings:[]}')"
    comment_id="$(gh_comment_create "$CTX_REPO" "$CTX_PR" \
"**revloop — reviewing, pass $pass of $CTX_MAX_PASSES**

Reading the diff and any earlier review threads. This comment becomes the pass summary when the review finishes.$(state_marker_encode "$marker")")"
    [[ -n "$comment_id" ]] || ui_die "the claim comment did not post on $CTX_REPO#$CTX_PR" \
      "The claim is what makes a retry safe, so revloop stops rather than reviewing without one."
    marker="$(jq -c --argjson id "$comment_id" '. + {comment_id: $id}' <<<"$marker")"
  fi
  run_checkpoint

  # --- the agent, unless a previous attempt already recorded its output -----
  local findings verdict model_reported
  if [[ "$(jq -r '(.findings // []) | length' <<<"$marker")" != "0" \
        && "$(jq -r '.verdict // "null"' <<<"$marker")" != "null" ]]; then
    ui_say "The previous attempt already recorded its findings, so the review is not run again."
    findings="$(jq -c '.findings' <<<"$marker")"
    verdict="$(jq -r '.verdict' <<<"$marker")"
    model_reported="$(jq -r '.model_reported // "null"' <<<"$marker")"
  else
    local tmp diff_file prompt_file review_md envelope_file exclude meta prior threads payload
    tmp="$(mktemp -d)"
    diff_file="$tmp/diff"; prompt_file="$tmp/prompt"
    review_md="$tmp/review.md"; envelope_file="$tmp/envelope"

    exclude=""
    [[ "$CTX_SINK" == file\ * ]] && exclude="^(${CTX_SINK#file }|\\.revloop/)"
    gh_pr_diff "$CTX_REPO" "$CTX_PR" "$exclude" >"$diff_file"

    cfg_show_at_base "$CTX_BASE_SHA" "REVIEW.md" >"$review_md" 2>/dev/null || : >"$review_md"

    meta="$(jq -cn --arg repo "$CTX_REPO" --argjson pr "$CTX_PR" --argjson pass "$pass" \
      --argjson max "$CTX_MAX_PASSES" --arg sha "$CTX_HEAD_SHA" --arg fa "$CTX_FIX_AT" \
      --arg t "$CTX_TITLE" --arg b "$CTX_BODY" \
      '{repo:$repo, pr:$pr, pass:$pass, max_passes:$max, head_sha:$sha, fix_at:$fa,
        title:$t, body:$b}')"
    prior="$(jq -c '[.[] | select(.leg == "review") | (.findings // [])[]]' <<<"$CTX_MARKERS")"
    threads="$(gh_review_threads "$CTX_REPO" "$CTX_PR")"

    prompt_review "$prompt_file" "$ROOT/skills/pr-review/SKILL.md" "$diff_file" \
      "$meta" "$prior" "$threads" "$review_md"

    ui_say "Reading the diff and any prior review threads."
    run_invoke "$envelope_file" "$harness" "$prompt_file" \
      "$ROOT/schemas/findings.schema.json" "$(pwd)" \
      "$model" "$effort" "$endpoint" validate_findings

    payload="$(jq -c .payload "$envelope_file")"
    model_reported="$(jq -r '.model_reported // "null"' "$envelope_file")"
    verdict="$(jq -r .verdict <<<"$payload")"

    # Stable ids, so "already posted" is a set-membership test rather than a
    # guess. The anchor lets a finding still be matched after the line moves.
    local raw enriched="[]" i n f id anchor
    raw="$(jq -c '[.findings[]]' <<<"$payload")"
    n="$(jq 'length' <<<"$raw")"
    for (( i = 0; i < n; i++ )); do
      f="$(jq -c ".[$i]" <<<"$raw")"
      anchor="$(state_anchor "$(jq -r .path <<<"$f")" "$(jq -r .line <<<"$f")")"
      id="$(state_finding_id "$(jq -r .path <<<"$f")" "$(jq -r .title <<<"$f")" "$anchor")"
      enriched="$(jq -c --argjson f "$f" --arg id "$id" --arg a "$anchor" \
        '. + [$f + {id:$id, anchor:$a, thread_id:null, disposition:null, tracked_as:null}]' \
        <<<"$enriched")"
    done
    findings="$enriched"
    rm -rf "$tmp"

    # Record findings into the claim BEFORE posting any of them. A crash after
    # this point needs no second model invocation to recover, which is the
    # difference between a cheap retry and an expensive one.
    marker="$(jq -c --argjson f "$findings" --arg v "$verdict" --arg mr "$model_reported" \
      '.findings = $f | .verdict = $v
       | .model_reported = (if $mr == "null" then null else $mr end)' <<<"$marker")"
    gh_comment_edit "$CTX_REPO" "$comment_id" \
"**revloop — reviewing, pass $pass of $CTX_MAX_PASSES**

Findings recorded; posting them now.$(state_marker_encode "$(jq -c 'del(.comment_id)' <<<"$marker")")"
  fi
  run_checkpoint

  # --- inline comments, reconciled against what already landed -------------
  local already posted=0 skipped=0 n i f id
  already="$(state_posted_finding_ids "$CTX_PR" "$CTX_REPO" "$CTX_AUTHOR" review)" || already=""

  n="$(jq 'length' <<<"$findings")"
  local high medium low pre actionable
  high="$(jq '[.[] | select(.severity == "high")] | length' <<<"$findings")"
  medium="$(jq '[.[] | select(.severity == "medium")] | length' <<<"$findings")"
  low="$(jq '[.[] | select(.severity == "low")] | length' <<<"$findings")"
  pre="$(jq '[.[] | select(.pre_existing // false)] | length' <<<"$findings")"
  actionable="$(run_actionable "$findings")"

  if (( n > 0 )); then
    ui_say "Found $n issue(s) — $high high, $medium medium, $low low, of which $pre pre-existing."
    ui_say "$actionable at or above fix_at ($CTX_FIX_AT); the rest are reported and left alone."
    ui_say "Posting them as inline comments on the lines they affect."
  fi

  for (( i = 0; i < n; i++ )); do
    f="$(jq -c ".[$i]" <<<"$findings")"
    id="$(jq -r .id <<<"$f")"
    if grep -qx -- "$id" <<<"$already"; then skipped=$(( skipped + 1 )); continue; fi
    gh_review_comment_create "$CTX_REPO" "$CTX_PR" "$CTX_HEAD_SHA" \
      "$(jq -r .path <<<"$f")" "$(jq -r .line <<<"$f")" "$(jq -r '.side // "RIGHT"' <<<"$f")" \
      "$(_review_comment_body "$f" "$pass" "$harness" "$model")" >/dev/null
    posted=$(( posted + 1 ))
    run_checkpoint
  done

  (( posted > 0 ))  && ui_ok "posted $posted inline comment(s)"
  (( skipped > 0 )) && ui_say "$skipped finding(s) were already on the pull request from an earlier attempt, so they were not posted twice."

  # Thread ids come from GraphQL, matched by the finding marker in each comment
  # body rather than by path and line, so a moved line does not lose the link.
  local threads_now
  threads_now="$(gh_review_threads "$CTX_REPO" "$CTX_PR")"
  findings="$(jq -c --argjson t "$threads_now" '
    [ .[] as $f | $f + {thread_id: ([$t[] | select(.finding_ids | index($f.id)) | .id] | first // $f.thread_id)} ]' \
    <<<"$findings")"
  marker="$(jq -c --argjson f "$findings" '.findings = $f' <<<"$marker")"

  # --- the summary comment -------------------------------------------------
  local summary_body
  summary_body="$(_review_summary_body "$findings" "$marker")"
  gh_comment_edit "$CTX_REPO" "$comment_id" \
    "$summary_body$(state_marker_encode "$(jq -c 'del(.comment_id)' <<<"$marker")")"
  ui_ok "posted a summary comment"
  run_checkpoint

  # --- complete last -------------------------------------------------------
  marker="$(jq -c '.state = "complete"' <<<"$marker")"
  gh_comment_edit "$CTX_REPO" "$comment_id" \
    "$summary_body$(state_marker_encode "$(jq -c 'del(.comment_id)' <<<"$marker")")"

  local next
  case "$verdict" in
    converged) next="converged" ;;
    blocked)   next="halted" ;;
    *) if (( actionable > 0 )); then next="awaiting-resolution"; else next="converged"; fi ;;
  esac
  run_pass_labels "$pass" "$next"

  printf '  → verdict: %s\n\n' "$verdict"
  if [[ "$next" == "awaiting-resolution" ]]; then
    ui_say "Nothing was changed in your working tree. To act on these:"
    ui_say "  revloop resolve --pr $CTX_PR"
    printf '\n'
  else
    (( no_tips )) || run_upgrade_nudge
  fi
  return 0
}

# A review comment must explain itself to a collaborator who has never heard of
# revloop — which model, which pass of how many, and what happens next.
#
# The note under each finding says what will happen to it, which is a question
# about the threshold rather than about the severity alone. Saying "minor, not
# blocking" while the resolve leg is about to rewrite that line would be the
# comment contradicting the commit.
_review_comment_body() {
  local f="$1" pass="$2" harness="$3" model="$4" note
  if [[ "$(jq -r '.pre_existing // false' <<<"$f")" == "true" ]]; then
    note="A real bug, but this pull request did not introduce it, so it is reported here and never fixed here."
  elif legs_should_fix "$(jq -r .severity <<<"$f")" "$CTX_FIX_AT" false; then
    note="At or above this repository's \`fix_at\` ($CTX_FIX_AT), so the resolve leg may change code for it."
  else
    note="Below this repository's \`fix_at\` ($CTX_FIX_AT), so it is reported and left to a human."
  fi
  printf '%s — %s\n\n%s\n\n**Fix:** %s\n\n<sub>%s · revloop pass %s, reviewed by %s%s. A second agent now verifies this point and either fixes it, defers it, or explains why it is wrong.</sub>%s' \
    "$(run_finding_label "$f")" "$(jq -r .title <<<"$f")" \
    "$(jq -r .why <<<"$f")" "$(jq -r .fix <<<"$f")" \
    "$note" "$pass" "$harness" "${model:+ ($model)}" \
    "$(state_finding_marker "$(jq -r .id <<<"$f")" "$pass" review)"
}

# The summary table. Severity and category carry emoji, and provenance sits in
# the *what* cell rather than beside the severity — `pre-existing` is not a
# fourth severity and must not read as one.
#
# Rendered row by row rather than in one jq pass, because the emoji come from the
# two functions above and a second copy of either mapping inside a jq program is
# a copy that drifts.
_findings_table() {
  local findings="$1" n i f sev kind pre
  n="$(jq 'length' <<<"$findings")"
  printf '| Severity | Category | Where | What |\n|---|---|---|---|\n'
  for (( i = 0; i < n; i++ )); do
    f="$(jq -c ".[$i]" <<<"$findings")"
    sev="$(jq -r '.severity // "?"' <<<"$f")"
    kind="$(jq -r '.category // "?"' <<<"$f")"
    pre=""
    [[ "$(jq -r '.pre_existing // false' <<<"$f")" == "true" ]] && pre=' <sub>· pre-existing</sub>'
    printf '| %s %s | %s %s | `%s:%s` | %s%s |\n' \
      "$(run_severity_emoji "$sev")" "$(_ucfirst "$sev")" \
      "$(run_category_emoji "$kind")" "$(_ucfirst "$kind")" \
      "$(jq -r .path <<<"$f")" "$(jq -r .line <<<"$f")" \
      "$(_md_cell "$(jq -r .title <<<"$f")")" "$pre"
  done
  printf '\n'
}

# The review leg's summary comment.
#
# Takes the marker rather than eight positional arguments, because the resolve
# leg re-renders this comment from the marker alone when it fills in the
# dispositions. Everything the comment says about the run — harness, model,
# effort, endpoint, timing, tokens — therefore has to live on the marker, and
# passing the marker is what keeps the two renderings honest about that.
_review_summary_body() {
  local findings="$1" marker="$2" n actionable verdict pass harness model reported
  n="$(jq 'length' <<<"$findings")"
  actionable="$(run_actionable "$findings")"
  verdict="$(jq -r '.verdict // "issues-remain"' <<<"$marker")"
  pass="$(jq -r '.pass // 1' <<<"$marker")"
  harness="$(jq -r '.harness // "?"' <<<"$marker")"
  model="$(jq -r '.model // ""' <<<"$marker")"
  reported="$(jq -r '.model_reported // ""' <<<"$marker")"

  printf '## revloop review — pass %s of %s\n\n' "$pass" "$CTX_MAX_PASSES"
  printf 'Reviewed by `%s`%s' "$harness" "${model:+ using \`$model\`}"
  [[ -n "$reported" && "$reported" != "null" ]] && printf ', answered by `%s`' "$reported"
  printf '. Verdict: **%s**.\n\n' "$verdict"

  if (( n == 0 )); then
    printf 'No findings. Low-severity and pre-existing issues would be listed here too, so this is an empty review rather than a filtered one.\n\n'
  else
    _findings_table "$findings"
  fi

  case "$verdict" in
    converged)
      printf 'Nothing at or above `fix_at` (%s) remains, so the loop stops here. Findings below the threshold, and pre-existing ones, are reported but cannot keep the loop alive — a loop that cannot converge because of a naming quibble is one nobody leaves switched on.\n' "$CTX_FIX_AT" ;;
    blocked)
      printf 'The review could not be completed, so the loop halts and a human is needed.\n' ;;
    *)
      printf '**Next:** a second agent verifies every finding above against the codebase and either fixes it, skips it, defers it, or explains why it is wrong. It may change code for the %s at or above `fix_at` (%s); the rest are verified and reported, never silently dropped.\n' "$actionable" "$CTX_FIX_AT" ;;
  esac
}

# ---------------------------------------------------------------------------
# The resolve leg
# ---------------------------------------------------------------------------

leg_resolve() {
  local pr="" repo="" harness_override="" no_tips=0
  while (( $# )); do
    case "$1" in
      --pr)      pr="${2:-}"; shift 2 ;;
      --repo)    repo="${2:-}"; shift 2 ;;
      --harness) harness_override="${2:-}"; shift 2 ;;
      --no-tips) no_tips=1; shift ;;
      --pass)    shift 2 ;;
      *) ui_die "unknown option for resolve: $1" \
           "Usage: revloop resolve --pr <number> [--harness claude|codex]" ;;
    esac
  done
  [[ -n "$pr" ]] || ui_die "revloop resolve needs a pull request number" "Usage: revloop resolve --pr 42"

  run_trap_install
  ctx_load "$pr" "$repo"
  run_lock_acquire "$CTX_PR" "$CTX_MODE"
  REVLOOP_RESUME_HINT="revloop resolve --pr $CTX_PR"

  if grep -qw "revloop/stop" <<<"$CTX_LABELS"; then
    ui_say "revloop/stop is on $CTX_REPO#$CTX_PR, so nothing is resolved."
    ui_say "Remove the label to let the loop continue."
    return 0
  fi

  local pass review_marker findings
  pass="$(state_current_review_pass "$CTX_MARKERS")"
  (( pass > 0 )) || ui_die "$CTX_REPO#$CTX_PR has no review to resolve" \
    "The resolve leg acts on a review leg's findings. Run: revloop review --pr $CTX_PR"

  review_marker="$(state_marker_for "$CTX_MARKERS" "$pass" review)"
  [[ "$(jq -r '.state // ""' <<<"$review_marker")" == "complete" ]] || ui_die \
    "the pass-$pass review on $CTX_REPO#$CTX_PR did not finish" \
    "Resolving a half-posted review would reply to findings the reviewer may not have finished recording. Re-run: revloop review --pr $CTX_PR"

  if state_current_pass_complete "$CTX_MARKERS" "$pass" resolve; then
    ui_say "pass $pass of $CTX_REPO#$CTX_PR is already resolved."
    ui_say "Push a revision, or run: revloop review --pr $CTX_PR"
    return 0
  fi

  findings="$(jq -c '.findings // []' <<<"$review_marker")"
  if [[ "$(jq 'length' <<<"$findings")" == "0" ]]; then
    ui_say "pass $pass found nothing to resolve on $CTX_REPO#$CTX_PR."
    run_pass_labels "$pass" converged
    return 0
  fi

  # The push guard runs before anything is invoked, not before the push. Finding
  # out after a model has run that the checkout is on the wrong branch wastes the
  # invocation and leaves changes in a tree nobody expected them in.
  local origin_repo
  origin_repo="$(git remote get-url origin 2>/dev/null | sed -E 's#^.*github\.com[:/]##; s#\.git$##')" || origin_repo=""
  legs_assert_push_target "$(git rev-parse --abbrev-ref HEAD)" "$CTX_HEAD_BRANCH" \
    "$CTX_DEFAULT_BRANCH" "$CTX_REPO" "${origin_repo:-$CTX_REPO}"

  run_leg_settings resolver "$harness_override"
  local harness="$LEG_HARNESS" model effort endpoint
  model="$(_nullable "$LEG_MODEL")"
  effort="$(_nullable "$LEG_EFFORT")"
  endpoint="$(_nullable "$LEG_ENDPOINT")"

  printf '\n  Resolving %s#%s — pass %s of %s\n' "$CTX_REPO" "$CTX_PR" "$pass" "$CTX_MAX_PASSES"
  printf '  Resolver: %s%s%s\n' "$harness" "${model:+, $model}" "${effort:+, $effort effort}"

  local claim comment_id marker stale
  claim="$(state_open_claim "$CTX_MARKERS" "$pass" resolve)" || claim=""
  if [[ -n "$claim" ]]; then
    comment_id="$(jq -r '.comment_id' <<<"$claim")"
    if stale="$(state_claim_is_stale "$claim" "$CTX_HEAD_SHA")"; then
      ui_warn "abandoning the unfinished pass-$pass resolve claim — $stale" \
        "Resuming it would reconcile replies against a revision that has moved. Starting the pass again instead."
      marker="$(jq -c --argjson ts "$(date +%s)" --arg sha "$CTX_HEAD_SHA" \
        '.ts = $ts | .head_sha = $sha | .dispositions = [] | .commit_sha = null' <<<"$claim")"
    else
      marker="$claim"
      ui_say "Resuming pass $pass — the previous attempt recorded $(jq -r '(.dispositions // []) | length' <<<"$marker") disposition(s)."
    fi
  else
    marker="$(jq -cn --argjson p "$pass" --arg sha "$CTX_HEAD_SHA" \
      --arg r "${GITHUB_RUN_ID:-local-$$}" --argjson ts "$(date +%s)" \
      --arg h "$harness" --arg m "$model" \
      '{v:1, leg:"resolve", pass:$p, state:"started", ts:$ts, run_id:$r, head_sha:$sha,
        harness:$h, model:(if $m == "" then null else $m end), model_reported:null,
        blocked:false, blocked_reason:null, commit_sha:null, summary:"", dispositions:[]}')"
    comment_id="$(gh_comment_create "$CTX_REPO" "$CTX_PR" \
"**revloop — resolving pass $pass of $CTX_MAX_PASSES**

Verifying each finding against the codebase. This comment becomes the pass summary when the resolve leg finishes.$(state_marker_encode "$marker")")"
    [[ -n "$comment_id" ]] || ui_die "the claim comment did not post on $CTX_REPO#$CTX_PR" \
      "The claim is what makes a retry safe, so revloop stops rather than resolving without one."
  fi
  marker="$(jq -c --argjson id "$comment_id" '. + {comment_id: $id}' <<<"$marker")"
  run_checkpoint

  local threads dispositions blocked blocked_reason summary model_reported
  threads="$(gh_review_threads "$CTX_REPO" "$CTX_PR")"

  if [[ "$(jq -r '(.dispositions // []) | length' <<<"$marker")" != "0" ]]; then
    ui_say "The previous attempt already recorded its dispositions, so the resolver is not run again."
    dispositions="$(jq -c '.dispositions' <<<"$marker")"
    summary="$(jq -r '.summary // ""' <<<"$marker")"
    blocked="$(jq -r '.blocked // false' <<<"$marker")"
    blocked_reason="$(jq -r '.blocked_reason // "null"' <<<"$marker")"
    model_reported="$(jq -r '.model_reported // "null"' <<<"$marker")"
  else
    local candidates enriched prior_dispositions
    candidates="$(_resolve_dedupe_candidates "$findings")"

    prior_dispositions="$(jq -c '[.[] | select(.leg == "resolve") | (.dispositions // [])[]]' <<<"$CTX_MARKERS")"

    # Each finding is handed to the model with the orchestrator's own answer to
    # "may you change code for this", rather than the threshold and a rule to
    # apply. Two readings of one policy is one reading too many: the label the
    # reviewer already posted says which findings are fixable, and a model that
    # ranks them differently contradicts a comment sitting on the pull request.
    enriched="$(jq -c --argjson prior "$prior_dispositions" '
      [ .[] as $f
        | $f + {prior_disposition: ([$prior[] | select(.finding_id == $f.id) | .disposition] | last // null)} ]' \
      <<<"$findings")"
    local n_e i_e f_e may enriched_out="[]"
    n_e="$(jq 'length' <<<"$enriched")"
    for (( i_e = 0; i_e < n_e; i_e++ )); do
      f_e="$(jq -c ".[$i_e]" <<<"$enriched")"
      may=false
      legs_should_fix "$(jq -r .severity <<<"$f_e")" "$CTX_FIX_AT" \
        "$(jq -r '.pre_existing // false' <<<"$f_e")" && may=true
      enriched_out="$(jq -c --argjson f "$f_e" --argjson m "$may" \
        '. + [$f + {may_fix: $m}]' <<<"$enriched_out")"
    done
    enriched="$enriched_out"

    local tmp diff_file prompt_file envelope_file exclude meta payload
    tmp="$(mktemp -d)"
    diff_file="$tmp/diff"; prompt_file="$tmp/prompt"; envelope_file="$tmp/envelope"
    exclude=""
    [[ "$CTX_SINK" == file\ * ]] && exclude="^(${CTX_SINK#file }|\\.revloop/)"
    gh_pr_diff "$CTX_REPO" "$CTX_PR" "$exclude" >"$diff_file"

    meta="$(jq -cn --arg repo "$CTX_REPO" --argjson pr "$CTX_PR" --argjson pass "$pass" \
      --argjson max "$CTX_MAX_PASSES" --arg sha "$CTX_HEAD_SHA" --arg fa "$CTX_FIX_AT" \
      --arg sink "$CTX_SINK" \
      '{repo:$repo, pr:$pr, pass:$pass, max_passes:$max, head_sha:$sha, fix_at:$fa, sink:$sink}')"

    prompt_resolve "$prompt_file" "$ROOT/skills/pr-resolve/SKILL.md" "$diff_file" \
      "$meta" "$enriched" "$threads" "$candidates"

    ui_say "Verifying each finding against the codebase."
    run_invoke "$envelope_file" "$harness" "$prompt_file" \
      "$ROOT/schemas/resolve.schema.json" "$(pwd)" \
      "$model" "$effort" "$endpoint" validate_resolve

    payload="$(jq -c .payload "$envelope_file")"
    model_reported="$(jq -r '.model_reported // "null"' "$envelope_file")"
    dispositions="$(jq -c '.dispositions' <<<"$payload")"
    summary="$(jq -r '.summary' <<<"$payload")"
    blocked="$(jq -r '.blocked // false' <<<"$payload")"
    blocked_reason="$(jq -r '.blocked_reason // "null"' <<<"$payload")"
    rm -rf "$tmp"

    marker="$(jq -c --argjson d "$dispositions" --arg w "$summary" --argjson b "$blocked" \
      --arg br "$blocked_reason" --arg mr "$model_reported" '
      .dispositions = $d | .summary = $w | .blocked = $b
      | .blocked_reason = (if $br == "null" then null else $br end)
      | .model_reported = (if $mr == "null" then null else $mr end)' <<<"$marker")"
    gh_comment_edit "$CTX_REPO" "$comment_id" \
"**revloop — resolving pass $pass of $CTX_MAX_PASSES**

Dispositions recorded; committing and replying now.$(state_marker_encode "$(jq -c 'del(.comment_id)' <<<"$marker")")"
  fi
  run_checkpoint

  # Layer two of the divergence guard: compare answering models, where both
  # harnesses report one. Absence is not a halt — that would disqualify the codex
  # adapter for a field Codex does not emit.
  local configured
  configured="$(legs_configured_difference \
    "$(cfg_get '.reviewer.harness')"  "$(cfg_get '.reviewer.endpoint')"  "$(cfg_get '.reviewer.model')" \
    "$(cfg_get '.resolver.harness')" "$(cfg_get '.resolver.endpoint')" "$(cfg_get '.resolver.model')")"
  legs_assert_models_diverged "$configured" \
    "$(jq -r '.model_reported // "null"' <<<"$review_marker")" "${model_reported:-null}"

  # --- pass one: persist deferred work, before anything is committed -------
  #
  # Every record a deferral produces is written here, and the commit follows it.
  # The ordering is load-bearing for the file sink and irrelevant to the issue
  # one: a file sink writes into the working tree, so a commit that ran first
  # left that write uncommitted, and on an ephemeral runner it died with the
  # container — while its thread resolved, because `tracked` was non-empty. The
  # issue sink is indifferent, since it writes to GitHub rather than the tree.
  #
  # Replies and resolution are pass two, below the commit. Persist still happens
  # before resolve, which is the invariant that matters: a thread resolved
  # against a write that did not land is exactly how work disappears.
  local filed=0 matched=0 deferred_lines="" sink_wrote=0
  local n i d id disp tracked dup existing
  n="$(jq 'length' <<<"$dispositions")"
  for (( i = 0; i < n; i++ )); do
    d="$(jq -c ".[$i]" <<<"$dispositions")"
    id="$(jq -r .finding_id <<<"$d")"
    [[ "$(jq -r .disposition <<<"$d")" == "deferred" ]] || continue

    tracked=""; existing=""
    if [[ "$CTX_SINK" == "issues" ]]; then
      # Tier 1: exact, against revloop's own issues. Deterministic, no model,
      # no false positives — this is what stops three pull requests touching
      # one legacy bug filing it three times.
      existing="$(gh_issue_by_finding "$CTX_REPO" "$CTX_IDENTITY_LABEL" "$id")" || existing=""
    fi
    dup="$(jq -r '.duplicate_of // ""' <<<"$d")"
    if [[ -n "$existing" ]]; then
      tracked="$CTX_REPO#$existing"
      matched=$(( matched + 1 ))
      deferred_lines="$deferred_lines
- \`$id\` — already tracked as #$existing, so nothing was filed"
    elif [[ -n "$dup" && "$dup" != "null" ]]; then
      tracked="$CTX_REPO#$dup"
      matched=$(( matched + 1 ))
      deferred_lines="$deferred_lines
- \`$id\` — matches the existing issue #$dup, so nothing was filed"
      if [[ "$(run_sink_field "$(cfg_get '.persist.defects')" comment_on_match false)" == "true" ]]; then
        gh_issue_comment "$CTX_REPO" "$dup" \
          "Seen again while reviewing $CTX_REPO#$CTX_PR (revloop pass $pass).$(state_finding_marker "$id" "$pass" resolve)"
      fi
    else
      tracked="$(_resolve_persist "$d" "$id" "$pass")" || tracked=""
      if [[ -n "$tracked" ]]; then
        filed=$(( filed + 1 ))
        # Only a file sink puts something in the tree for the commit to carry.
        [[ "$CTX_SINK" == file\ * ]] && sink_wrote=1
        deferred_lines="$deferred_lines
- \`$id\` — filed as ${tracked/#"$CTX_REPO"/}"
      else
        deferred_lines="$deferred_lines
- \`$id\` — **not persisted anywhere**, so its thread stays open rather than resolving against a write that did not land"
      fi
    fi

    # Carried on the record itself rather than in a parallel array, so pass two
    # reads one thing and a crash between the passes leaves no orphan state to
    # reconcile.
    dispositions="$(jq -c --arg id "$id" --arg t "$tracked" \
      'map(if .finding_id == $id then . + {revloop_tracked: $t} else . end)' <<<"$dispositions")"
  done
  run_checkpoint

  # --- the commit, then its SHA -------------------------------------------
  #
  # Guarded on more than fixes. A pass that defers everything and fixes nothing
  # still has a file sink's write sitting in the tree, and a `fixed_count > 0`
  # test left exactly that case uncommitted.
  local commit_sha fixed_count commit_msg
  commit_sha="$(jq -r '.commit_sha // ""' <<<"$marker")"
  fixed_count="$(jq '[.[] | select(.disposition == "fixed")] | length' <<<"$dispositions")"
  if [[ -n "$commit_sha" && "$commit_sha" != "null" ]]; then
    ui_say "The previous attempt already pushed ${commit_sha:0:7}, so the fix step is skipped."
  elif (( fixed_count > 0 || sink_wrote )); then
    commit_sha=""
    if (( fixed_count > 0 )); then
      commit_msg="fix: resolve revloop review findings (pass $pass)

$(jq -r '[.[] | select(.disposition == "fixed") | "- " + .finding_id] | join("\n")' <<<"$dispositions")"
    else
      commit_msg="chore: record deferred revloop findings (pass $pass)

$(jq -r '[.[] | select(.disposition == "deferred") | "- " + .finding_id] | join("\n")' <<<"$dispositions")"
    fi
    commit_sha="$(gh_commit_and_push "$CTX_HEAD_BRANCH" "$commit_msg" "$CTX_HEAD_SHA")"
    if [[ -n "$commit_sha" ]]; then
      ui_ok "pushed ${commit_sha:0:7} to $CTX_HEAD_BRANCH"
      # Recorded immediately after the push, because the window between the two
      # is the one crash boundary comments cannot dedupe away.
      marker="$(jq -c --arg s "$commit_sha" '.commit_sha = $s' <<<"$marker")"
      gh_comment_edit "$CTX_REPO" "$comment_id" \
"**revloop — resolving pass $pass of $CTX_MAX_PASSES**

Pushed \`${commit_sha:0:7}\`; replying to each thread now.$(state_marker_encode "$(jq -c 'del(.comment_id)' <<<"$marker")")"
    elif (( fixed_count > 0 )); then
      # Only meaningful when fixes were claimed. A deferral-only pass whose sink
      # write produced no diff is not a broken promise about the code.
      ui_warn "the resolver reported $fixed_count fix(es) but changed no files" \
        "The replies below will claim a fix that is not in the diff. Treat those dispositions as unverified and read the thread before merging."
    fi
  fi
  run_checkpoint

  # --- pass two: reply and resolve ----------------------------------------
  local already resolved_n=0 escalated=0
  local thread_id root_id should_resolve reply_body
  already="$(state_posted_finding_ids "$CTX_PR" "$CTX_REPO" "$CTX_AUTHOR" resolve)" || already=""

  for (( i = 0; i < n; i++ )); do
    d="$(jq -c ".[$i]" <<<"$dispositions")"
    id="$(jq -r .finding_id <<<"$d")"
    disp="$(jq -r .disposition <<<"$d")"
    tracked="$(jq -r '.revloop_tracked // ""' <<<"$d")"

    thread_id="$(jq -r --arg id "$id" '[.[] | select(.finding_ids | index($id))] | first | .id // ""' <<<"$threads")"
    root_id="$(jq -r --arg id "$id" '[.[] | select(.finding_ids | index($id))] | first | .root_comment_id // ""' <<<"$threads")"

    # The reply carries its own finding marker, which is what stops a retry
    # replying twice.
    if ! grep -qx -- "$id" <<<"$already"; then
      reply_body="$(_resolve_reply_body "$d" "$tracked" "$pass" "$harness" "$model")"
      if [[ -n "$root_id" && "$root_id" != "null" ]]; then
        gh_review_reply "$CTX_REPO" "$CTX_PR" "$root_id" "$reply_body" || true
      else
        gh_comment_create "$CTX_REPO" "$CTX_PR" "$reply_body" >/dev/null
      fi
      run_checkpoint
    fi

    # Resolution, per the disposition's own rule. `deferred` resolves only once
    # persisted; with nothing persisted the thread stays open, which is the
    # honest behaviour — resolving a thread whose content lands nowhere is how
    # work disappears.
    should_resolve=0
    case "$disp" in
      fixed|skipped|rebutted) should_resolve=1 ;;
      deferred)  [[ -n "$tracked" ]] && should_resolve=1 ;;
      escalated) escalated=$(( escalated + 1 )) ;;
    esac
    if (( should_resolve )) && [[ -n "$thread_id" && "$thread_id" != "null" ]]; then
      gh_thread_resolve "$thread_id" && resolved_n=$(( resolved_n + 1 )) || true
    fi

    # Fill the disposition into the review leg's marker. One record per finding
    # rather than two that can disagree.
    findings="$(jq -c --arg id "$id" --arg disp "$disp" --arg tracked "$tracked" '
      map(if .id == $id
          then .disposition = $disp
               | .tracked_as = (if $tracked == "" then null else $tracked end)
          else . end)' <<<"$findings")"
    run_checkpoint
  done

  (( filed > 0 ))      && ui_ok "filed $filed issue(s) for deferred work"
  (( matched > 0 ))    && ui_say "$matched deferred finding(s) already had an issue, so nothing was filed for them."
  (( resolved_n > 0 )) && ui_ok "resolved $resolved_n thread(s)"

  # The review marker is edited rather than copied, so the finding list and its
  # dispositions cannot drift apart.
  local review_comment_id updated_review
  review_comment_id="$(jq -r '.comment_id' <<<"$review_marker")"
  updated_review="$(jq -c --argjson f "$findings" 'del(.comment_id) | .findings = $f' <<<"$review_marker")"
  gh_comment_edit "$CTX_REPO" "$review_comment_id" \
    "$(_review_summary_body "$findings" "$review_marker")$(state_marker_encode "$updated_review")"
  run_checkpoint

  # --- the summary comment, then completion --------------------------------------
  local summary_body
  summary_body="$(_resolve_summary_body "$dispositions" "$findings" "$deferred_lines" "$marker")"
  gh_comment_edit "$CTX_REPO" "$comment_id" \
    "$summary_body$(state_marker_encode "$(jq -c 'del(.comment_id)' <<<"$marker")")"
  ui_ok "posted a summary comment"
  run_checkpoint

  marker="$(jq -c '.state = "complete"' <<<"$marker")"
  gh_comment_edit "$CTX_REPO" "$comment_id" \
    "$summary_body$(state_marker_encode "$(jq -c 'del(.comment_id)' <<<"$marker")")"

  local next="awaiting-review"
  if [[ "$blocked" == "true" ]] || (( escalated > 0 )); then next="halted"; fi
  run_pass_labels "$pass" "$next"
  if (( escalated > 0 )); then
    run_label_add "revloop/stop"
    ui_say "$escalated finding(s) need a human decision, so revloop/stop is applied and the loop halts."
  fi

  if [[ "$blocked" == "true" ]]; then
    printf '  → blocked: %s\n\n' "$blocked_reason"
  else
    printf '  → resolved pass %s\n\n' "$pass"
    if [[ "$next" == "awaiting-review" ]]; then
      ui_say "To look again with the reviewer:"
      ui_say "  revloop review --pr $CTX_PR"
      printf '\n'
    fi
  fi
  if (( no_tips == 0 )) && [[ "$next" != "awaiting-review" ]]; then run_upgrade_nudge; fi
  return 0
}

# Tier 2 dedupe, the retrieval half: the orchestrator searches, the model judges.
#
# Keyed on the file path, since an issue about a bug in a file almost always names
# it. Capped, and the cap is announced rather than silent — a truncated candidate
# set that reads as "nothing matched" is how a duplicate gets filed.
_resolve_dedupe_candidates() {
  local findings="$1" out="{}" searched=0 limit=10 n i f cand
  [[ "$CTX_SINK" == "issues" ]] || { printf '%s' "$out"; return 0; }

  n="$(jq 'length' <<<"$findings")"
  for (( i = 0; i < n; i++ )); do
    if (( searched >= limit )); then
      ui_warn "only the first $limit finding(s) were searched for existing issues, out of $n" \
        "Anything past that is judged with no candidates, so a duplicate could be filed. Its thread still carries the finding either way."
      break
    fi
    f="$(jq -c ".[$i]" <<<"$findings")"
    searched=$(( searched + 1 ))
    cand="$(gh_issue_candidates "$CTX_REPO" "$(jq -r .path <<<"$f")" "")"
    [[ "$(jq 'length' <<<"$cand")" == "0" ]] && continue
    out="$(jq -c --arg id "$(jq -r .id <<<"$f")" --argjson c "$cand" '. + {($id): $c}' <<<"$out")"
  done
  printf '%s' "$out"
}

# File one deferred defect to the resolved sink. Prints where it landed, or
# nothing when the write did not happen.
_resolve_persist() {
  local d="$1" id="$2" pass="$3" persist title body dir target n
  persist="$(jq -c '.persist // null' <<<"$d")"
  [[ "$persist" != "null" ]] || return 1

  title="$(jq -r .title <<<"$persist")"
  body="$(jq -r .body <<<"$persist")

---
Found by revloop while reviewing $CTX_REPO#$CTX_PR (pass $pass). Verified against the codebase before filing: one model raised it, a second confirmed it is real, and it was left out of that pull request deliberately rather than missed.$(state_finding_marker "$id" "$pass" resolve)"

  case "$CTX_SINK" in
    issues)
      n="$(gh_issue_create "$CTX_REPO" "$title" "$body" "$CTX_IDENTITY_LABEL $CTX_SINK_LABELS")" || return 1
      [[ -n "$n" ]] || return 1
      printf '%s#%s' "$CTX_REPO" "$n" ;;
    file\ *)
      dir="${CTX_SINK#file }"
      cfg_assert_path_inside_repo "$dir"
      if [[ "$dir" == *.md ]]; then
        # An existing markdown convention is appended to, because that is what
        # the convention is.
        printf '\n## %s\n\n%s\n' "$title" "$body" >>"$dir"
        printf '%s' "$dir"
      else
        # One file per finding, never an append: two concurrent pull requests
        # appending to one markdown file conflict on merge every time.
        mkdir -p "$dir"
        target="$dir/$id.md"
        printf '# %s\n\n%s\n' "$title" "$body" >"$target"
        printf '%s' "$target"
      fi ;;
    *)
      return 1 ;;
  esac
}

_resolve_reply_body() {
  local d="$1" tracked="$2" pass="$3" harness="$4" model="$5" disp lead
  disp="$(jq -r .disposition <<<"$d")"
  case "$disp" in
    fixed)     lead="**Fixed.**" ;;
    skipped)   lead="**Skipped.**" ;;
    deferred)  lead="**Deferred.**" ;;
    rebutted)  lead="**Not changing this.**" ;;
    escalated) lead="**This needs a human decision.**" ;;
    *)         lead="**$disp.**" ;;
  esac
  printf '%s %s\n' "$lead" "$(jq -r .reply <<<"$d")"
  [[ -n "$tracked" ]] && printf '\nTracked outside this pull request as %s, so it survives the merge.\n' "$tracked"
  printf '\n<sub>revloop pass %s, verified by %s%s. Every finding is verified whatever its severity — severity governs what happens afterwards, not whether the check happens.</sub>%s' \
    "$pass" "$harness" "${model:+ ($model)}" \
    "$(state_finding_marker "$(jq -r .finding_id <<<"$d")" "$pass" resolve)"
}

# The disposition table, with the severity emoji on each finding.
#
# The finding is named by its title rather than by its id, because the id means
# nothing to a collaborator reading the pull request — but the id is what the
# dispositions are keyed on, so it stays in the cell, small, for anyone matching
# a row against a thread. The reasoning is deliberately not a column: the only
# text revloop holds is the model's full reply, which belongs in the thread it
# was written for and would not survive a table cell.
_dispositions_table() {
  local dispositions="$1" findings="$2" n i d id f sev title
  n="$(jq 'length' <<<"$dispositions")"
  printf '| Finding | Disposition |\n|---|---|\n'
  for (( i = 0; i < n; i++ )); do
    d="$(jq -c ".[$i]" <<<"$dispositions")"
    id="$(jq -r .finding_id <<<"$d")"
    f="$(jq -c --arg id "$id" 'map(select(.id == $id)) | first // {}' <<<"$findings")"
    sev="$(jq -r '.severity // "?"' <<<"$f")"
    title="$(jq -r '.title // ""' <<<"$f")"
    [[ -n "$title" ]] || title="$id"
    printf '| %s %s <sub>`%s`</sub> | %s |\n' \
      "$(run_severity_emoji "$sev")" "$(_md_cell "$title")" "$id" \
      "$(jq -r .disposition <<<"$d")"
  done
  printf '\n'
}

# The resolve leg's summary comment. Takes the marker for the same reason the
# review leg's does — everything it reports about the run lives there.
_resolve_summary_body() {
  local dispositions="$1" findings="$2" deferred_lines="$3" marker="$4"
  local summary pass harness model reported commit blocked blocked_reason
  summary="$(jq -r '.summary // ""' <<<"$marker")"
  pass="$(jq -r '.pass // 1' <<<"$marker")"
  harness="$(jq -r '.harness // "?"' <<<"$marker")"
  model="$(jq -r '.model // ""' <<<"$marker")"
  reported="$(jq -r '.model_reported // ""' <<<"$marker")"
  commit="$(jq -r '.commit_sha // ""' <<<"$marker")"
  blocked="$(jq -r '.blocked // false' <<<"$marker")"
  blocked_reason="$(jq -r '.blocked_reason // ""' <<<"$marker")"

  printf '## revloop resolved pass %s of %s\n\n' "$pass" "$CTX_MAX_PASSES"
  printf 'Verified by `%s`%s' "$harness" "${model:+ using \`$model\`}"
  [[ -n "$reported" && "$reported" != "null" ]] && printf ', answered by `%s`' "$reported"
  printf '. '
  if [[ -n "$commit" && "$commit" != "null" ]]; then
    printf 'Fixes pushed as `%s`.\n\n' "${commit:0:7}"
  else
    printf 'No code changed this pass.\n\n'
  fi

  printf '%s\n\n' "$summary"

  _dispositions_table "$dispositions" "$findings"

  if [[ -n "$deferred_lines" ]]; then
    printf '## Deferred work filed\n'
    printf '%s\n\n' "$deferred_lines"
    printf 'An unresolved thread on a merged pull request is visible in no GitHub view, which is why a deferred finding goes somewhere durable before its thread is resolved.\n\n'
  fi

  if [[ "$blocked" == "true" ]]; then
    printf '**Blocked:** %s\n\nThe loop halts here and needs a human.\n' "$blocked_reason"
  fi
}

# ---------------------------------------------------------------------------
# The drivers
# ---------------------------------------------------------------------------

# single-run mode: one process calls the legs in sequence. Because state lives on
# the pull request rather than in process memory, this is a thin driver over the
# same legs the workflows invoke, and the two modes can be A/B tested on real
# pull requests without touching leg code.
cmd_cycle() {
  local pr="" repo="" no_tips=0
  local -a args=()
  while (( $# )); do
    case "$1" in
      --pr)      pr="${2:-}"; args+=(--pr "${2:-}"); shift 2 ;;
      --repo)    repo="${2:-}"; args+=(--repo "${2:-}"); shift 2 ;;
      --harness) args+=(--harness "${2:-}"); shift 2 ;;
      --no-tips) no_tips=1; shift ;;
      *) ui_die "unknown option for cycle: $1" "Usage: revloop cycle --pr <number> [--no-tips]" ;;
    esac
  done
  [[ -n "$pr" ]] || ui_die "revloop cycle needs a pull request number" "Usage: revloop cycle --pr 42"

  ctx_load "$pr" "$repo"
  local max="$CTX_MAX_PASSES" i pass marker rmarker verdict actionable
  ui_say "Cycling $CTX_REPO#$CTX_PR, up to $max passes. Ctrl-C is safe — each leg finishes the write in flight."

  for (( i = 1; i <= max; i++ )); do
    leg_review "${args[@]}" --no-tips || return 1

    # Re-read: the review leg wrote the state this decision reads.
    ctx_load "$pr" "$repo"
    pass="$(state_current_review_pass "$CTX_MARKERS")"
    marker="$(state_marker_for "$CTX_MARKERS" "$pass" review)"
    verdict="$(jq -r '.verdict // "blocked"' <<<"$marker")"
    actionable="$(run_actionable "$(jq -c '.findings // []' <<<"$marker")")"

    if [[ "$verdict" == "blocked" ]]; then
      ui_end "Halted after pass $pass — the reviewer could not complete."
      return 0
    fi
    if [[ "$verdict" == "converged" ]] || (( actionable == 0 )); then
      ui_end "Converged after pass $pass — nothing at or above fix_at ($CTX_FIX_AT) remains."
      (( no_tips )) || run_upgrade_nudge
      return 0
    fi

    leg_resolve "${args[@]}" --no-tips || return 1

    ctx_load "$pr" "$repo"
    rmarker="$(state_marker_for "$CTX_MARKERS" "$pass" resolve)"
    if [[ "$(jq -r '.blocked // false' <<<"$rmarker")" == "true" ]]; then
      ui_end "Halted after pass $pass — the resolver reported blocked."
      return 0
    fi
    if grep -qw "revloop/stop" <<<"$CTX_LABELS"; then
      ui_end "Halted after pass $pass — a point needs a human decision, so revloop/stop is applied."
      return 0
    fi
  done

  ui_end "Reached max_passes ($max) on $CTX_REPO#$CTX_PR without converging. Every finding and reply is on the pull request."
  (( no_tips )) || run_upgrade_nudge
  return 0
}

# Position AND interruption, because a stuck local run with no watchdog is only
# recoverable if something tells you it is stuck.
cmd_status() {
  local pr="" repo=""
  while (( $# )); do
    case "$1" in
      --pr)   pr="${2:-}"; shift 2 ;;
      --repo) repo="${2:-}"; shift 2 ;;
      *) ui_die "unknown option for status: $1" "Usage: revloop status --pr <number>" ;;
    esac
  done
  [[ -n "$pr" ]] || ui_die "revloop status needs a pull request number" "Usage: revloop status --pr 42"

  ctx_load "$pr" "$repo"

  ui_section "$CTX_REPO#$CTX_PR"
  ui_line "head        ${CTX_HEAD_SHA:0:7} on $CTX_HEAD_BRANCH, $CTX_CHANGED file(s) changed"
  ui_line "mode        $CTX_MODE, trusting markers written by $CTX_AUTHOR"
  ui_line "labels      ${CTX_LABELS:-none}"
  ui_line "deferred    $CTX_SINK"

  local pass total leg m st stale m_review
  pass="$(state_current_review_pass "$CTX_MARKERS")"
  total="$(jq 'length' <<<"$CTX_MARKERS")"
  if (( pass == 0 )); then
    ui_line "passes      none yet"
    ui_gap
    ui_next "revloop review --pr $CTX_PR"
    ui_end "Nothing has run on this pull request."
    return 0
  fi

  ui_line "passes      $pass of $CTX_MAX_PASSES, from $total trusted marker(s)"
  ui_gap

  # Was a resolve leg ever owed this pass? A converged review is the reason the
  # loop stopped and a blocked one hands over to a human, so in both cases the
  # answer is no. Everything below asks this before it asks whether that leg ran
  # — the two questions look alike and only one of them is about work outstanding.
  local m_review review_state review_verdict resolve_due=1
  m_review="$(state_marker_for "$CTX_MARKERS" "$pass" review)"
  review_state="$(jq -r '.state // ""' <<<"$m_review")"
  review_verdict="$(jq -r '.verdict // ""' <<<"$m_review")"
  [[ "$review_state" == "complete" ]] \
    && [[ "$review_verdict" == "converged" || "$review_verdict" == "blocked" ]] \
    && resolve_due=0

  for leg in review resolve; do
    m="$(state_marker_for "$CTX_MARKERS" "$pass" "$leg")"
    if [[ -z "$m" ]]; then
      # "has not run" reads as a step still outstanding. When no resolve leg was
      # due, that is the difference between a finished loop and one that looks
      # abandoned halfway.
      if [[ "$leg" == "resolve" ]] && (( resolve_due == 0 )); then
        ui_opt "resolve — not needed, the review $review_verdict"
      else
        ui_opt "$leg — has not run this pass"
      fi
      continue
    fi
    st="$(jq -r '.state' <<<"$m")"
    if [[ "$st" == "complete" ]]; then
      ui_ok "$leg — complete, $(jq -r '.verdict // (if .blocked then "blocked" else "done" end)' <<<"$m")"
    elif stale="$(state_claim_is_stale "$m" "$CTX_HEAD_SHA")"; then
      ui_no "$leg — interrupted, and stale: $stale. A re-run abandons it and starts the pass again."
    else
      ui_no "$leg — interrupted mid-flight. A re-run resumes it and posts only what is missing."
    fi
  done

  ui_gap
  if [[ "$review_state" != "complete" ]]; then
    ui_next "revloop review --pr $CTX_PR"
  elif (( resolve_due )) && ! state_current_pass_complete "$CTX_MARKERS" "$pass" resolve; then
    ui_next "revloop resolve --pr $CTX_PR"
  elif state_is_new_revision "$CTX_MARKERS" "$CTX_HEAD_SHA"; then
    ui_next "revloop review --pr $CTX_PR"
  elif [[ "$review_verdict" == "converged" ]]; then
    ui_next "nothing — the loop converged on pass $pass"
  elif [[ "$review_verdict" == "blocked" ]]; then
    ui_next "nothing automatic — the reviewer reported blocked, so what happens next is a human's call"
  else
    ui_next "nothing — this revision is reviewed and resolved"
  fi
  ui_end "State is read from the pull request itself, so this is the same view a workflow gets."
}

# ---------------------------------------------------------------------------
# The watchdog
# ---------------------------------------------------------------------------
#
# Event-driven mode's failure mode is silence: a dropped label event and a
# converged pull request look identical from outside. So something has to go
# looking, and it retries once before giving up — a dropped event is fixed by
# re-firing it, and re-applying a label GitHub already holds fires nothing, which
# is why the retry removes it first.
cmd_watchdog() {
  local repo="" timeout=1800
  while (( $# )); do
    case "$1" in
      --repo)    repo="${2:-}"; shift 2 ;;
      --timeout) timeout="${2:-1800}"; shift 2 ;;
      *) ui_die "unknown option for watchdog: $1" "Usage: revloop watchdog [--repo owner/name] [--timeout <seconds>]" ;;
    esac
  done

  if [[ -z "$repo" ]]; then
    repo="$(gh_repo_slug)"
    [[ -n "$repo" ]] || ui_die "could not work out which repository to watch" \
      "Run the watchdog from a checkout with a GitHub remote, or pass --repo owner/name."
  fi

  local stuck now checked=0 retried=0 halted=0
  now="$(date +%s)"
  stuck="$(gh api "repos/$repo/pulls?state=open&per_page=100" \
    --jq '[.[] | select([.labels[].name] | any(startswith("revloop/awaiting-")))
           | {number, labels: [.labels[].name], head: .head.sha}]' 2>/dev/null)" || stuck="[]"

  ui_section "revloop watchdog on $repo"

  local n i pr labels head author markers marker age leg
  n="$(jq 'length' <<<"$stuck")"
  for (( i = 0; i < n; i++ )); do
    pr="$(jq -r ".[$i].number" <<<"$stuck")"
    labels="$(jq -r ".[$i].labels | join(\" \")" <<<"$stuck")"
    head="$(jq -r ".[$i].head" <<<"$stuck")"
    checked=$(( checked + 1 ))

    grep -qw "revloop/stop" <<<"$labels" && continue

    if [[ "$labels" == *"revloop/awaiting-resolution"* ]]; then leg="resolve"; else leg="review"; fi

    author="$(state_trusted_author event-driven)"
    markers="$(state_markers "$pr" "$repo" "$author")"
    marker="$(jq -c '[.[]] | last // empty' <<<"$markers")"
    if [[ -z "$marker" ]]; then
      ui_no "#$pr — waiting on the $leg leg with no marker at all, so it never started"
      _watchdog_retry "$repo" "$pr" "$labels" "$leg" "$head" && retried=$(( retried + 1 )) || halted=$(( halted + 1 ))
      continue
    fi

    age=$(( now - $(jq -r '.ts // 0' <<<"$marker") ))
    if (( age < timeout )); then
      ui_opt "#$pr — waiting on the $leg leg, $(( age / 60 )) minute(s) in, inside the $(( timeout / 60 ))-minute timeout"
      continue
    fi

    ui_no "#$pr — waiting on the $leg leg for $(( age / 60 )) minutes, past the $(( timeout / 60 ))-minute timeout"
    if _watchdog_retry "$repo" "$pr" "$labels" "$leg" "$head"; then
      retried=$(( retried + 1 ))
    else
      halted=$(( halted + 1 ))
    fi
  done

  ui_gap
  ui_line "checked $checked pull request(s) waiting on a leg — retried $retried, halted $halted"
  ui_end "A pass that never fired and a pass that converged look identical from outside, which is why this runs on a schedule."
}

# One retry, then halt. A second failure is not a dropped event.
_watchdog_retry() {
  local repo="$1" pr="$2" labels="$3" leg="$4" head="$5"
  local label; label="$(legs_awaiting_label "$leg")"

  if grep -qw "revloop/watchdog-retried" <<<"$labels"; then
    state_label_remove "$pr" "$repo" "$label"
    gh_label_ensure "$repo" "revloop/halted" "$(legs_label_colour revloop/halted)" "revloop stopped" >/dev/null
    state_label_add "$pr" "$repo" "revloop/halted"
    gh_comment_create "$repo" "$pr" \
"**revloop halted** — the $leg leg was already retried once and is still not finishing.

The last marker on this pull request records how far it got. Nothing here is a judgement about the code: the loop stopped, it did not converge.

To look yourself: \`revloop status --pr $pr\`. To restart it, remove \`revloop/halted\` and \`revloop/watchdog-retried\`, then apply \`$label\`." >/dev/null
    ui_line "   halted — it had already been retried once"
    return 1
  fi

  # Re-applying a label GitHub already holds fires no event, so the retry has to
  # remove it first.
  gh_label_ensure "$repo" "revloop/watchdog-retried" \
    "$(legs_label_colour revloop/watchdog-retried)" "revloop retried this leg once" >/dev/null
  state_label_add "$pr" "$repo" "revloop/watchdog-retried"
  state_label_remove "$pr" "$repo" "$label"
  state_label_add "$pr" "$repo" "$label"
  ui_line "   retried by re-firing $label at ${head:0:7}"
  return 0
}

# ---------------------------------------------------------------------------
# The upgrade nudge
# ---------------------------------------------------------------------------
#
# Only where it applies: a repository already running GitHub Actions but with no
# revloop workflows. A repository with no CI at all gets nothing, because the
# suggestion would be noise. Rarely: only on a run that reached a terminal state,
# so at most once per pull request. Suppressible by flag and by config.
run_upgrade_nudge() {
  [[ "${REVLOOP_NO_TIPS:-0}" == "1" ]] && return 0
  [[ "$(cfg_get '.tips')" == "false" ]] && return 0
  [[ -d .github/workflows ]] || return 0
  compgen -G ".github/workflows/revloop-*.yml" >/dev/null 2>&1 && return 0

  cat <<'EOF'
  Tip: this repo already runs GitHub Actions. `revloop init` would run this
  loop automatically on every pull request — review, fixes, re-review — and
  takes about a minute to set up. Silence this with `--no-tips`.

EOF
}
