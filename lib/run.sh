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

CROSSREV_INTERRUPTED=0
CROSSREV_LOCK=""
CROSSREV_SANDBOXED=""
CROSSREV_RESUME_HINT=""
CROSSREV_WORKTREE=""

_worktree_dir() {
  local repo="$1" pr="$2" slug
  slug="${repo//\//-}"
  printf '%s/crossrev/worktrees/%s/pr-%s' "${XDG_STATE_HOME:-$HOME/.local/state}" "$slug" "$pr"
}

# ---------------------------------------------------------------------------
# Interruption, locking and cleanup
# ---------------------------------------------------------------------------
#
# Ctrl-C is the expected way to stop a local run, so a leg finishes the write in
# flight and leaves a clean claim rather than dying between posting a comment and
# recording it.

run_trap_install() {
  trap 'CROSSREV_INTERRUPTED=1' INT TERM
  trap run_cleanup EXIT
}

run_cleanup() {
  local rc=$?
  [[ -n "$CROSSREV_SANDBOXED" ]] && sandbox_restore "$CROSSREV_SANDBOXED"
  [[ -n "$CROSSREV_LOCK" && -f "$CROSSREV_LOCK" ]] && rm -f "$CROSSREV_LOCK"
  if (( rc != 0 )) && [[ -n "$CROSSREV_WORKTREE" && -d "$CROSSREV_WORKTREE" ]]; then
    printf '  Worktree kept for debugging: %s\n' "$CROSSREV_WORKTREE" >&2
  fi
  # A restored credential dies with the process that borrowed it, on the fatal
  # paths as much as the clean one. Leaving it on disk is how a second job finds
  # a copy of a token that only one holder may refresh.
  cred_discard
  return "$rc"
}

# Called between outward writes. Nothing is half-written when this returns.
run_checkpoint() {
  (( CROSSREV_INTERRUPTED == 0 )) && return 0
  ui_warn "interrupted after the last completed write" \
    "The marker on the pull request records how far this got, so nothing is duplicated on the way back in. Resume with: ${CROSSREV_RESUME_HINT:-crossrev status --pr <n>}"
  exit 130
}

# Automated mode uses one concurrency group per pull request. Locally, two
# terminals against the same PR would interleave writes, so take a lock and name
# the holder rather than failing opaquely.
run_lock_acquire() {
  local pr="$1" mode="$2" gitdir lock holder pid
  [[ "$mode" == "automated" ]] && return 0
  gitdir="$(git rev-parse --git-dir 2>/dev/null)" || return 0
  mkdir -p "$gitdir/crossrev"
  lock="$gitdir/crossrev/pr-$pr.lock"
  if [[ -f "$lock" ]]; then
    holder="$(cat "$lock" 2>/dev/null)"
    pid="${holder%% *}"
    # Our own lock, from an earlier leg in this same process. `crossrev run`
    # drives review and resolve one after the other, so the second leg re-enters
    # here holding what the first one took — a live PID that passes the check
    # below and reads as a collision with itself. Keep the lock for the whole
    # run rather than releasing it between legs: dropping it mid-loop would open
    # a window for a second terminal to start a pass halfway through this one.
    if [[ "$pid" == "$$" ]]; then return 0; fi
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      ui_die "another CrossRev run already holds pull request $pr — $holder" \
        "Two runs writing the same pull request would interleave comments and replies. Wait for it to finish, or stop that process."
    fi
    ui_warn "a previous run left a lock on pull request $pr held by $holder, which is no longer running" \
      "Taking it over. If that run was killed mid-write, its marker records how far it got and this run posts only the difference."
    rm -f "$lock"
  fi
  printf '%s on %s since %s\n' "$$" "$(hostname 2>/dev/null || echo local)" \
    "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" >"$lock"
  CROSSREV_LOCK="$lock"
}

# ---------------------------------------------------------------------------
# Shared context
# ---------------------------------------------------------------------------

CTX_REPO=""; CTX_PR=""; CTX_HEAD_SHA=""; CTX_BASE_SHA=""
CTX_HEAD_BRANCH=""; CTX_DEFAULT_BRANCH=""; CTX_CHANGED=0
CTX_HEAD_REPO=""; CTX_MAINTAINER_CAN_MODIFY=false; CTX_IS_CROSS_REPOSITORY=false
CTX_TITLE=""; CTX_BODY=""; CTX_LABELS=""; CTX_URL=""
CTX_MODE=""; CTX_AUTHOR=""; CTX_MARKERS="[]"; CTX_MAX_PASSES_PER_CYCLE=3
CTX_BACKLOG="none"; CTX_BACKLOG_LAYOUT=""; CTX_BACKLOG_PATH=""
CTX_TRACKING_LABEL="crossrev-review"; CTX_BACKLOG_LABELS=""
CTX_MIN_FIX_SEVERITY="medium"

ctx_load() {
  local pr="$1" repo="${2:-}" trigger="${3:-human}" pr_json resolved
  preflight_require_yq

  if [[ -z "$repo" ]]; then
    repo="$(gh_repo_slug)"
    [[ -n "$repo" ]] || ui_die "could not work out which repository this is" \
      "Run crossrev from a checkout with a GitHub remote, or pass --repo owner/name."
  fi

  pr_json="$(gh_pr_json "$repo" "$pr")"
  [[ -n "$pr_json" ]] || ui_die "could not read $repo#$pr" \
    "Check the number, and that \`gh auth status\` passes for that repository."

  # Fork pull requests fail closed in automated mode: GitHub withholds secrets
  # from them, so the loop would run unauthenticated rather than not at all.
  # A local run uses the operator's own credentials, so the refusal is scoped
  # to automatic triggers.
  if [[ "$trigger" == "automatic" && "$(jq -r '.isCrossRepository' <<<"$pr_json")" != "false" ]]; then
    ui_die "$repo#$pr comes from a fork" \
      "crossrev does not run on fork pull requests: GitHub withholds secrets from them. Review it locally or by hand."
  fi

  [[ "$(jq -r .state <<<"$pr_json")" == "OPEN" ]] || ui_die \
    "$repo#$pr is not open" \
    "crossrev only runs on open pull requests. Reopen it, or pick another number."

  if [[ "$trigger" == "automatic" && "$(jq -r '.isDraft // false' <<<"$pr_json")" == "true" ]]; then
    ui_say "$repo#$pr is a draft pull request, so an automatic invocation does not review it."
    ui_say "Mark it ready for review, or ask for a review explicitly."
    return 2
  fi

  CTX_REPO="$repo"
  CTX_PR="$pr"
  CTX_TITLE="$(jq -r .title <<<"$pr_json")"
  # Already in the one `gh pr view --json` call every leg makes, so this costs no
  # extra round trip. `status` is the only reader: it names the pull request and
  # says where to open it, which today's output never does.
  CTX_URL="$(jq -r '.url // ""' <<<"$pr_json")"
  CTX_BODY="$(jq -r '.body // ""' <<<"$pr_json")"
  CTX_HEAD_SHA="$(jq -r .headRefOid <<<"$pr_json")"
  CTX_BASE_SHA="$(jq -r .baseRefOid <<<"$pr_json")"
  CTX_HEAD_BRANCH="$(jq -r .headRefName <<<"$pr_json")"
  CTX_CHANGED="$(jq -r '.changedFiles // 0' <<<"$pr_json")"
  CTX_LABELS="$(jq -r '[.labels[].name] | join(" ")' <<<"$pr_json")"
  CTX_DEFAULT_BRANCH="$(gh_default_branch "$repo")"
  # Only an explicit `isCrossRepository: false` means "this repository's own
  # branch". Absent metadata is recorded as unknown rather than left empty, so
  # the push guard reads it as a fork and demands a resolved head repository and
  # maintainer edits — an unknown push target must never inherit the permission
  # an upstream branch gets by default.
  CTX_IS_CROSS_REPOSITORY="$(jq -r 'if .isCrossRepository != null then (.isCrossRepository | tostring) else "unknown" end' <<<"$pr_json")"
  if [[ "$CTX_IS_CROSS_REPOSITORY" == "false" ]]; then
    CTX_HEAD_REPO="$repo"
    CTX_MAINTAINER_CAN_MODIFY="false"
  else
    CTX_HEAD_REPO="$(jq -r 'if .headRepositoryOwner.login and .headRepository.name then "\(.headRepositoryOwner.login)/\(.headRepository.name)" elif .headRepository.nameWithOwner then .headRepository.nameWithOwner else "" end' <<<"$pr_json")"
    CTX_MAINTAINER_CAN_MODIFY="$(jq -r 'if .maintainerCanModify != null then (.maintainerCanModify | tostring) else "" end' <<<"$pr_json")"
  fi

  # Policy comes from the base revision, never the pull request head. Read from
  # the head, a branch could raise max_passes_per_cycle, repoint an endpoint at a server it
  # controls and harvest every prompt, or ship a REVIEW.md saying to return
  # converged. A branch that legitimately changes review policy therefore takes
  # effect when it merges, which is the correct order.
  cfg_load "$CTX_BASE_SHA"
  CTX_MODE="$(cfg_get '.mode')"
  CTX_MAX_PASSES_PER_CYCLE="$(cfg_get '.policy.max_passes_per_cycle')"
  CTX_MIN_FIX_SEVERITY="$(cfg_get '.policy.min_fix_severity')"

  resolved="$(cfg_resolve_backlog "$CTX_BASE_SHA" "$(cfg_get '.backlog.destination')")"
  read -r CTX_BACKLOG CTX_BACKLOG_LAYOUT CTX_BACKLOG_PATH <<<"$resolved"
  CTX_TRACKING_LABEL="$(cfg_get '.backlog.github_issues.tracking_label')"
  CTX_BACKLOG_LABELS="$(jq -r '.backlog.github_issues.labels | join(" ")' <<<"$CFG_MERGED")"

  CTX_AUTHOR="$(state_trusted_author "$CTX_MODE")"
  [[ -n "$CTX_AUTHOR" ]] || ui_die "could not resolve whose markers to trust on $repo#$pr" \
    "Pass numbering, revision detection and the daily cap all read from the trusted author. Run: gh auth login"
  CTX_MARKERS="$(state_markers "$CTX_PR" "$CTX_REPO" "$CTX_AUTHOR")"
}

# Labels are load-bearing for the event chain and cosmetic without it, so the
# consequence of an application failure differs by mode. In automated mode a
# label that cannot be applied stalls the loop, which is fatal. Locally one process drives
# both legs, so it is a warning — otherwise every local run against a repository
# that never ran `init` would die after posting a perfectly good review.
run_label_add() {
  local label="$1"
  if [[ "$CTX_MODE" == "automated" ]]; then
    state_label_add "$CTX_PR" "$CTX_REPO" "$label"
  else
    gh api --method POST "repos/$CTX_REPO/issues/$CTX_PR/labels" -f "labels[]=$label" >/dev/null 2>&1 \
      || ui_warn "could not apply the label '$label' to $CTX_REPO#$CTX_PR" \
           "Locally that is cosmetic, because this process drives both legs itself. In automated mode it would stall the chain, which is what \`crossrev init\` creates the labels for."
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

# How many findings the resolve leg may change code for: at or above `min_fix_severity`,
# and not pre-existing. The verdict, the labels and the summary all read this
# one number.
#
# The rank table is taken from legs.sh rather than restated in jq. Two copies of
# an ordinal scale drift, and the drift is invisible from outside: a pull request
# that says nothing is outstanding while the resolve leg is still committing.
run_actionable() {
  local findings="$1" ranks bar
  bar="$(legs_severity_rank "$CTX_MIN_FIX_SEVERITY")"
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

# `🔴 [High · Security]`, the label on an inline finding heading.
#
# Brackets rather than bold, because the emphasis belongs on the defect. The
# heading carries the title, and a bold label beside a plain title puts the
# reader's eye on a two-word classification they scan past rather than on the
# thing that is wrong. With the bold moved off, the brackets are the only thing
# delimiting the label, so they are doing work rather than repeating it.
#
# The space after the closing bracket is load-bearing: `[text](url)` is a link
# only when the paren immediately follows, so a title beginning with `(` would
# otherwise swallow the label whole.
#
# One line, and the category stays a readable word rather than a pictogram the
# reader has to decode. Provenance is deliberately absent: `pre-existing` is not
# a fourth severity and must not read as one, so it goes in the summary table's
# *what* cell and in the note under this finding, both of which have room to say
# what it means.
#
# Decorates the RENDERED heading only. A finding's stable id derives from its
# title, so decorating the title field itself would re-post every finding as new
# on the next pass.
run_finding_label() {
  local f="$1" sev kind
  sev="$(jq -r '.severity // "?"' <<<"$f")"
  kind="$(jq -r '.category // "?"' <<<"$f")"
  printf '%s [%s · %s]' "$(run_severity_emoji "$sev")" \
    "$(_ucfirst "$sev")" "$(_ucfirst "$kind")"
}

# Model-written text flattened to one line.
#
# A newline in a heading ends the heading, and a newline in a table cell ends the
# table — both silently, because everything around them still renders.
#
# Every other control character goes with the newline, because a lone carriage
# return is a line ending by CommonMark's own definition and ends the row just as
# quietly, and an escape sequence renders nowhere useful. `_commit_line` strips
# the same span on the way into a commit body, for the same reason.
_one_line() {
  printf '%s' "$1" | tr '\000-\037\177' ' '
}

# Text that is about to land in a Markdown table cell.
#
# A pipe in a model-written title splits the row into extra columns, which is
# wrong in the same quiet way: the surrounding rows are unaffected.
_md_cell() {
  _one_line "$1" | sed 's/|/\\|/g'
}

run_pass_labels() {
  local pass="$1" next="$2" l
  for l in awaiting-review awaiting-resolution converged halted; do
    [[ "$l" == "$next" ]] && continue
    state_label_remove "$CTX_PR" "$CTX_REPO" "crossrev/$l"
  done
  # A pass that got this far is a pass that did not stall, so the watchdog's
  # retry marker goes with the state it described. Left standing it is per-PR
  # rather than per-stall: a pull request that stalls on pass 1, recovers, and
  # stalls again on pass 3 is halted on the spot for having "already been
  # retried once", and someone has to remove the label by hand to restart it.
  state_label_remove "$CTX_PR" "$CTX_REPO" "crossrev/watchdog-retried"
  # The pass label is mutually exclusive like the four above it, so it moves
  # rather than accumulates: the grey pill is *the* pass number, singular, and a
  # three-pass pull request carrying three of them makes the reader scan for the
  # highest. Which ones to take off is read from the labels actually on the pull
  # request rather than counted down from the cap, so a new revision that resets
  # the counter to 1 sheds the higher labels a finished cycle left behind, and a
  # label from a config whose cap has since been lowered still goes.
  # shellcheck disable=SC2086  # CTX_LABELS is a space-joined list; splitting is the point
  for l in $CTX_LABELS; do
    [[ "$l" == crossrev/pass-* && "$l" != "crossrev/pass-$pass" ]] || continue
    state_label_remove "$CTX_PR" "$CTX_REPO" "$l"
  done
  run_label_add "crossrev/pass-$pass"
  [[ -n "$next" ]] && run_label_add "crossrev/$next"
  return 0
}

# ---------------------------------------------------------------------------
# Which harness runs a leg
# ---------------------------------------------------------------------------

LEG_HARNESS=""; LEG_MODEL=""; LEG_EFFORT=""; LEG_ENDPOINT=""; LEG_WRITE="no"

# Sets the five LEG_* globals rather than printing them, so the warnings and the
# fatal case below reach the caller instead of dying in a subshell.
#
# Includes the single-harness fallback: when only one harness is installed, run
# both legs on it and say so in one line naming the cost, rather than halting.
# The divergence guard stays quiet in that case by design — it catches a config
# that asked for two models and got one, and this config asked for one.
run_leg_settings() {
  local leg="$1" override="${2:-}" alt="" h

  # Derived from the leg, and deliberately not configurable. Only the resolver
  # changes files; the reviewer has no reason to, and granting it write access
  # widens the blast radius of a prompt injection carried in a diff for nothing
  # in return. Set before the early returns below so every path carries it.
  LEG_WRITE=no
  [[ "$leg" == "resolver" ]] && LEG_WRITE=yes

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
      "crossrev drives claude, codex and agy directly. Kimi is reached through the claude adapter instead: define it under endpoints: and set $leg.endpoint, not $leg.harness."
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
    "Install one of claude, codex or agy. crossrev needs at least one, and two different ones is what makes the cross-model check mean anything."

  ui_warn "'$LEG_HARNESS' is not installed, so the $leg runs on '$alt' instead" \
    "Both legs now run on the same harness, so a bug it misses while reviewing it also misses while resolving. Install $LEG_HARNESS to get the second lineage back."
  LEG_HARNESS="$alt"
  # A model id for the harness that was asked for is wrong for a different one.
  LEG_MODEL=""; LEG_ENDPOINT=""
  return 0
}

_nullable() { [[ "$1" == "null" ]] && printf '' || printf '%s' "$1"; }

_pass_label() {
  local p="$1" m="$2"
  if (( p > m )); then
    printf 'pass %s (past the cycle cap of %s)' "$p" "$m"
  else
    printf 'pass %s of %s' "$p" "$m"
  fi
}

# Invoke a harness against a prompt and a schema, with the checkout quarantined,
# and write the adapter's envelope to $1.
#
# Not a command substitution: the quarantine has to be recorded in the caller's
# own CROSSREV_SANDBOXED so that the EXIT trap restores the checkout if this dies.
# A quarantine leaked out of a subshell leaves someone's working tree mangled.
#
# Retries once when the harness does not constrain its own output, which is the
# only path where a mismatched object is expected rather than a bug.
#
# ---------------------------------------------------------------------------
# The working tree across a retry
# ---------------------------------------------------------------------------
#
# The resolve harness edits files before it returns its answer, and a rejected
# answer is thrown away — its edits are not. So a retry that reuses the tree
# reads one the discarded attempt already changed: it applies a non-idempotent
# fix twice, or finds a finding already fixed and calls it skipped, and
# `git add -A` commits whatever is sitting there either way. The resolutions
# that get recorded then describe a tree nobody produced, which is the same
# class of silent divergence between record and reality that the numbering
# above exists to remove.
#
# So each attempt starts from the state captured before the first one.
#
# Captured into a temporary index rather than with `git stash`: stash rewrites
# the real index and pushes onto the user's stash list, and crossrev is routinely
# run in a checkout somebody else is also working in. Seeded from the real index
# so the stat cache still applies and this costs a `git status`, not a rehash of
# every file in the repository.
#
# Prints the tree object, or nothing when the state could not be captured.
_run_tree_capture() {
  local idx="$1" gitdir
  gitdir="$(git rev-parse --git-dir 2>/dev/null)" || return 1
  rm -f "$idx"
  cp "$gitdir/index" "$idx" 2>/dev/null || true
  GIT_INDEX_FILE="$idx" git add -A >/dev/null 2>&1 || return 1
  GIT_INDEX_FILE="$idx" git write-tree 2>/dev/null
}

# Put the working tree back to a captured state, or fail.
#
# Read into the capture's own index rather than the repository's, and that is
# what keeps someone's staging area out of this. The capture is a tree of
# everything that was there, staged and unstaged alike, so resetting the real
# index to it stages every unstaged change the run happened to find — and a pass
# that then fixes nothing, or dies later, hands the checkout back with a staging
# area crossrev invented. Reading into the temporary index puts the same files
# back and leaves the real one untouched.
#
# The files still get rewritten: the capture's stat cache was recorded before the
# attempt ran, so anything the attempt touched fails the stat check and is
# checked out again.
#
# Anything untracked afterwards was written by the attempt being discarded: the
# capture indexed every file that was there before it ran, so read-tree restores
# those and only the leftovers are left over. `ls-files` reads the capture for
# the same reason — a file that was untracked before crossrev started is in the
# capture but not in the real index, and asking the real index would delete it as
# though the attempt had written it. Ignored files are neither captured nor
# removed, which is right — they are not committed either.
_run_tree_restore() {
  local idx="$1" tree="$2" p top
  [[ -n "$tree" && -f "$idx" ]] || return 1
  top="$(git rev-parse --show-toplevel 2>/dev/null)" || return 1
  GIT_INDEX_FILE="$idx" git read-tree --reset -u "$tree" >/dev/null 2>&1 || return 1
  while IFS= read -r p; do
    rm -rf -- "${top:?}/$p"
  done < <(GIT_INDEX_FILE="$idx" git -C "$top" ls-files --others --exclude-standard)
  return 0
}

# Reset before a retry, or refuse to retry at all.
#
# Asking again on top of a discarded attempt's edits is worse than losing the
# pass: the accepted answer is then recorded against changes it never made, and
# the commit carries both. So a capture that was never taken, or a restore that
# will not apply, stops the leg instead of degrading it quietly.
_run_retry_reset() {
  local idx="$1" tree="$2" harness="$3" problem="$4"
  _run_tree_restore "$idx" "$tree" && return 0
  rm -f "$idx"
  ui_die "$harness needs asking again, and the working tree it already edited could not be put back — $problem" \
    "Retrying on top of a discarded attempt's edits would commit changes no accepted answer describes. Nothing has been written to the pull request; check \`git status\` in the checkout and re-run the leg."
}

# The way out when an answer is rejected and there is no attempt left to make.
#
# A retry restores; an exhausted budget used not to, which left the last rejected
# attempt's edits sitting in the checkout with nothing on the pull request to say
# so. The next run then captures them as its own baseline and commits them under
# resolutions that describe neither attempt — the divergence the capture exists
# to prevent, arriving one run later instead of one attempt later.
#
# A restore that will not apply is warned about rather than hidden: the leg is
# dying anyway, and the operator is the one who has to deal with what is left.
_run_invoke_abort() {
  local idx="$1" tree="$2"
  if [[ -n "$tree" ]] && ! _run_tree_restore "$idx" "$tree"; then
    ui_warn "the rejected attempt's edits could not be put back" \
      "They are still in the checkout, and a later run would capture them as its own baseline. Check \`git status\` before re-running the leg."
  fi
  rm -f "$idx"
}

# run_invoke <out> <harness> <prompt> <schema> <workdir> <model> <effort> <endpoint> <write> <validator> [expect]
#
# `write` is yes or no and rides alongside the other leg-derived settings, so
# each adapter decides how its own CLI expresses the capability. See
# run_leg_settings for where it comes from.
#
# `expect` is passed to the validator as its second argument, describing what
# the orchestrator supplied — see lib/validate.sh. Absent for a leg with nothing
# to compare against.
run_invoke() {
  local out_file="$1" harness="$2" prompt_file="$3" schema_file="$4" workdir="$5"
  local model="$6" effort="$7" endpoint="$8" write="$9"
  local validator="${10}" expect="${11:-}"

  # Layer one of the divergence guard, and the specific failure it exists for:
  # these variables are process-scoped, so a leg that leaks them silently
  # redirects the other leg too and the loop completes normally with the
  # cross-model property gone and no error anywhere.
  legs_assert_env_clean

  # A credential restored from a secret is read, used and thrown away. The
  # scratch home is discarded whichever way this returns, including the fatal
  # paths below, so a harness that refreshes and writes back on its own writes
  # into a directory nothing reads again.
  cred_prepare "$harness" "$endpoint"

  # Two budgets, because a shape failure and a semantic one mean opposite things
  # about who is at fault.
  #
  # Shape: every shipped harness constrains its output to the schema, so a
  # mismatch there is an adapter or harness bug and a second call reproduces it.
  # Only a harness that does not constrain output gets a retry, which is what
  # this has always done.
  #
  # Semantic: the shape is perfect and the content contradicts what the
  # orchestrator supplied — a finding number nothing was numbered with, one
  # answered twice, one left out. That is model drift by definition, no adapter
  # is involved, and it earns one more attempt rather than costing a pass that
  # has already been paid for.
  local shape_budget=1 semantic_budget=1 payload problem rc
  validate_harness_is_schema_native "$harness" || shape_budget=2

  # Taken before the first attempt, because that is the only moment the tree is
  # still the one a retry needs to start from. See _run_tree_capture.
  local snap_index snap_tree
  snap_index="$(mktemp -u)"
  snap_tree="$(_run_tree_capture "$snap_index")" || snap_tree=""

  while :; do
    CROSSREV_SANDBOXED="$workdir"
    sandbox_quarantine "$workdir" >/dev/null
    # Adapter stderr is NOT discarded. Each adapter already captures the harness
    # CLI's own noise into a temp file, so what reaches here is crossrev's own
    # messages — including a fatal one about an endpoint that does not resolve.
    # Swallowing those left the process exiting 1 with nothing printed.
    "adapter_$harness" "$prompt_file" "$schema_file" "$workdir" \
      "$model" "$effort" "$endpoint" "$write" >"$out_file" || true
    sandbox_restore "$workdir"
    CROSSREV_SANDBOXED=""

    [[ -s "$out_file" ]] || printf '%s' \
      '{"ok":false,"payload":null,"error":"the adapter returned nothing at all"}' >"$out_file"

    if [[ "$(jq -r '.ok // false' "$out_file")" != "true" ]]; then
      # The capture is dropped rather than applied. There is no rejected answer
      # here — the harness never got as far as one — so whatever it did write is
      # evidence of how it failed, and deleting that is not crossrev's call.
      rm -f "$snap_index"
      # `$harness --version` was the advice here and it tests the wrong thing: it
      # answers with no credential, so it succeeds on a runner that cannot
      # authenticate and sends the reader away satisfied while the leg keeps
      # failing. Same defect the gh check in preflight exists to avoid — installed
      # is not the same as usable. Authentication is what this fails on most
      # often, so it is what the advice names.
      ui_die "the $harness harness failed: $(jq -r '.error // "no error reported"' "$out_file")" \
        "Nothing has been written to the pull request. If the error above mentions authentication, a token or a 401, the harness is installed and cannot log in: on a GitHub-hosted runner its credential comes from a repository secret, so check \`gh secret list\`, and locally check the harness's own login. \`$harness --version\` cannot tell you either way — it answers without a credential."
    fi

    payload="$(jq -c '.payload' "$out_file")"
    # `|| rc=$?` rather than reading $? on the next line: this runs under
    # `set -e`, which kills the process on a bare failing assignment before any
    # branch below gets to look at the code.
    rc=0
    problem="$("$validator" "$payload" "$expect")" || rc=$?
    if (( rc == 0 )); then cred_discard; rm -f "$snap_index"; return 0; fi

    if (( rc == 2 )); then
      if (( semantic_budget > 0 )); then
        semantic_budget=$(( semantic_budget - 1 ))
        _run_retry_reset "$snap_index" "$snap_tree" "$harness" "$problem"
        ui_warn "$harness returned an answer that contradicts what it was given — $problem" \
          "The shape is right, so this is the model drifting rather than a bug in crossrev or the harness. Anything it edited has been put back, and it is being asked once more; a second one is fatal."
        continue
      fi
      _run_invoke_abort "$snap_index" "$snap_tree"
      ui_die "$harness twice returned an answer that contradicts what it was given — $problem" \
        "The shape was right both times, so the schema cannot catch this and crossrev will not guess which finding was meant. Nothing has been written to the pull request, and the edits both rejected attempts made have been put back. Re-run the leg, or try the other harness."
    fi

    shape_budget=$(( shape_budget - 1 ))
    if (( shape_budget > 0 )); then
      _run_retry_reset "$snap_index" "$snap_tree" "$harness" "$problem"
      ui_warn "$harness returned an object that does not match the schema — $problem" \
        "That harness does not constrain its own output, so this is the expected failure rather than a bug. Anything it edited has been put back, and it is being retried once; a second mismatch is fatal."
      continue
    fi
    _run_invoke_abort "$snap_index" "$snap_tree"
    ui_die "$harness returned an object that does not match the schema — $problem" \
      "This harness validates output against the schema natively, so a mismatch is an adapter or harness bug rather than model drift. Nothing has been written to the pull request, and the rejected attempt's edits have been put back."
  done
}

# ---------------------------------------------------------------------------
# The review leg
# ---------------------------------------------------------------------------

leg_review() {
  local pr="" repo="" harness_override="" trigger="human" no_tips=0 continuation=0
  while (( $# )); do
    case "$1" in
      --pr)      pr="${2:-}"; shift 2 ;;
      --repo)    repo="${2:-}"; shift 2 ;;
      --harness) harness_override="${2:-}"; shift 2 ;;
      --trigger) trigger="${2:-}"; shift 2 ;;
      --continuation) continuation=1; shift ;;
      --no-tips) no_tips=1; shift ;;
      --pass)    shift 2 ;;   # accepted and ignored: the pass comes from the PR
      *) ui_die "unknown option for review: $1" \
           "Usage: crossrev review --pr <number> [--harness claude|codex] [--no-tips]" ;;
    esac
  done
  [[ -n "$pr" ]] || ui_die "crossrev review needs a pull request number" "Usage: crossrev review --pr 42"
  case "$trigger" in
    human|automatic) ;;
    *) ui_die "unknown review trigger: $trigger" "Use --trigger human or --trigger automatic." ;;
  esac

  run_trap_install
  local load_rc
  ctx_load "$pr" "$repo" "$trigger" || {
    load_rc=$?
    (( load_rc == 2 )) && return 0
    return "$load_rc"
  }
  run_lock_acquire "$CTX_PR" "$CTX_MODE"
  CROSSREV_RESUME_HINT="crossrev review --pr $CTX_PR"

  # A human's request outranks everything, including a healthy verdict.
  if grep -qw "crossrev/stop" <<<"$CTX_LABELS"; then
    ui_say "crossrev/stop is on $CTX_REPO#$CTX_PR, so this run stops without reviewing."
    ui_say "Remove the label to let the loop continue."
    return 0
  fi

  local current pass claim stale recovering=0
  current="$(state_current_review_pass "$CTX_MARKERS")"
  claim="$(state_open_claim "$CTX_MARKERS" "$current" review)" || claim=""

  if [[ -n "$claim" ]]; then
    if stale="$(state_claim_is_stale "$claim" "$CTX_HEAD_SHA")"; then
      ui_warn "abandoning the unfinished pass-$current review — $stale" \
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
    ui_say "Push a revision, or run: crossrev resolve --pr $CTX_PR"
    return 0
  fi

  # Termination, asked as "should a pass after $((pass-1)) begin?". Pass 3 of a
  # max_passes_per_cycle of 3 is the last pass, not the one after which a fourth starts.
  local other_prs_today=0 max_passes_per_cycle=0 max_prs_per_day=0 files_changed=0 max_files_changed_per_pr=0
  local decision reason
  if [[ "$trigger" == "automatic" ]]; then
    max_passes_per_cycle="$CTX_MAX_PASSES_PER_CYCLE"
    max_prs_per_day="$(cfg_get '.policy.max_prs_per_day')"
    if (( max_prs_per_day > 0 )); then
      other_prs_today="$(state_prs_reviewed_today "$CTX_REPO" "$CTX_AUTHOR" "$(( $(date +%s) - 86400 ))" \
        "$max_prs_per_day" "$CTX_PR" "$CTX_MARKERS")"
    fi
    files_changed="$CTX_CHANGED"
    max_files_changed_per_pr="$(cfg_get '.policy.max_files_changed_per_pr')"
  elif (( continuation )); then
    max_passes_per_cycle="$CTX_MAX_PASSES_PER_CYCLE"
  fi
  if ! decision="$(legs_should_continue "issues-remain" "$(( pass - 1 ))" "$max_passes_per_cycle" \
      false false "$other_prs_today" "$max_prs_per_day" "$files_changed" "$max_files_changed_per_pr")"; then
    reason="${decision#* }"
    ui_say "not reviewing $CTX_REPO#$CTX_PR — $reason"
    # The refusal gets a marker as well as a comment and a label. Without one,
    # `status` has nothing to render the halt from and has to infer it from a
    # label plus the prose of a comment body — which is how a readable state
    # becomes a guessed one. `state: "declined"` keeps it out of pass numbering,
    # revision detection and the daily cap: it records a pass that did not run.
    gh_comment_create "$CTX_REPO" "$CTX_PR" \
"**crossrev stopped before pass $pass** — $reason.

No review ran, so nothing here is a judgement about the code. Raising the cap in \`.github/crossrev.yml\` and pushing a revision would start it again.$(state_marker_encode "$(jq -cn --argjson p "$pass" --arg sha "$CTX_HEAD_SHA" \
  --arg r "${GITHUB_RUN_ID:-local-$$}" --argjson ts "$(date +%s)" --arg why "$reason" \
  '{v:1, leg:"review", pass:$p, state:"declined", ts:$ts, done_ts:$ts, run_id:$r,
    head_sha:$sha, harness:null, model:null, effort:null, endpoint:null,
    model_reported:null, tokens:null, verdict:"declined", reason:$why, findings:[]}')")" >/dev/null
    run_pass_labels "$(( pass > 1 ? pass - 1 : 1 ))" halted
    return 0
  fi

  run_leg_settings reviewer "$harness_override"
  local harness="$LEG_HARNESS" model effort endpoint write="$LEG_WRITE"
  model="$(_nullable "$LEG_MODEL")"
  effort="$(_nullable "$LEG_EFFORT")"
  endpoint="$(_nullable "$LEG_ENDPOINT")"

  printf '\n  Reviewing %s#%s — %s\n' "$CTX_REPO" "$CTX_PR" "$(_pass_label "$pass" "$CTX_MAX_PASSES_PER_CYCLE")"
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
      --arg h "$harness" --arg m "$model" --arg e "$effort" --arg ep "$endpoint" \
      '{v:1, leg:"review", pass:$p, state:"started", ts:$ts, done_ts:null, run_id:$r,
        head_sha:$sha, harness:$h, model:(if $m == "" then null else $m end),
        effort:(if $e == "" then null else $e end),
        endpoint:(if $ep == "" then null else $ep end),
        model_reported:null, tokens:null, verdict:null, findings:[]}')"
    comment_id="$(gh_comment_create "$CTX_REPO" "$CTX_PR" \
"**crossrev — reviewing, $(_pass_label "$pass" "$CTX_MAX_PASSES_PER_CYCLE")**

Reading the diff and any earlier review threads. This comment becomes the pass summary when the review finishes.$(state_marker_encode "$marker")")"
    [[ -n "$comment_id" ]] || ui_die "the claim comment did not post on $CTX_REPO#$CTX_PR" \
      "The marker is what makes a retry safe, so crossrev stops rather than reviewing without one."
    marker="$(jq -c --argjson id "$comment_id" '. + {comment_id: $id}' <<<"$marker")"
  fi
  run_checkpoint

  # --- the agent, unless a previous attempt already recorded its output -----
  local findings verdict model_reported tokens
  if [[ "$(jq -r '(.findings // []) | length' <<<"$marker")" != "0" \
        && "$(jq -r '.verdict // "null"' <<<"$marker")" != "null" ]]; then
    ui_say "The previous attempt already recorded its findings, so the review is not run again."
    findings="$(jq -c '.findings' <<<"$marker")"
    verdict="$(jq -r '.verdict' <<<"$marker")"
    model_reported="$(jq -r '.model_reported // "null"' <<<"$marker")"
  else
    local tmp diff_file prompt_file review_md envelope_file meta prior threads payload
    local -a exclude
    tmp="$(mktemp -d)"
    diff_file="$tmp/diff"; prompt_file="$tmp/prompt"
    review_md="$tmp/review.md"; envelope_file="$tmp/envelope"

    exclude=()
    [[ "$CTX_BACKLOG" == "repository" ]] && exclude=("$CTX_BACKLOG_PATH" .crossrev)
    gh_pr_diff "$CTX_REPO" "$CTX_PR" "$CTX_BASE_SHA" "$CTX_HEAD_SHA" ${exclude[@]+"${exclude[@]}"} >"$diff_file"

    cfg_show_at_base "$CTX_BASE_SHA" "REVIEW.md" >"$review_md" 2>/dev/null || : >"$review_md"

    meta="$(jq -cn --arg repo "$CTX_REPO" --argjson pr "$CTX_PR" --argjson pass "$pass" \
      --argjson max "$CTX_MAX_PASSES_PER_CYCLE" --arg sha "$CTX_HEAD_SHA" --arg fa "$CTX_MIN_FIX_SEVERITY" \
      --arg t "$CTX_TITLE" --arg b "$CTX_BODY" \
      '{repo:$repo, pr:$pr, pass:$pass, max_passes_per_cycle:$max, head_sha:$sha, min_fix_severity:$fa,
        title:$t, body:$b}')"
    prior="$(jq -c '[.[] | select(.leg == "review") | (.findings // [])[]]' <<<"$CTX_MARKERS")"
    threads="$(gh_review_threads "$CTX_REPO" "$CTX_PR")"

    prompt_review "$prompt_file" "$ROOT/skills/pr-review/SKILL.md" "$diff_file" \
      "$meta" "$prior" "$threads" "$review_md"

    ui_say "Reading the diff and any prior review threads."
    run_invoke "$envelope_file" "$harness" "$prompt_file" \
      "$ROOT/schemas/findings.schema.json" "$(pwd)" \
      "$model" "$effort" "$endpoint" "$write" validate_findings

    payload="$(jq -c .payload "$envelope_file")"
    model_reported="$(jq -r '.model_reported // "null"' "$envelope_file")"
    tokens="$(jq -c '.tokens // null' "$envelope_file")"
    verdict="$(jq -r .verdict <<<"$payload")"

    # Stable ids, so "already posted" is a set-membership test rather than a
    # guess. The anchor lets a finding still be matched after the line moves.
    local raw enriched="[]" i n f id anchor moved
    raw="$(jq -c '[.findings[]]' <<<"$payload")"
    n="$(jq 'length' <<<"$raw")"
    for (( i = 0; i < n; i++ )); do
      f="$(jq -c ".[$i]" <<<"$raw")"
      moved="$(_review_anchor_to_diff "$f" "$diff_file")"
      if [[ -n "$moved" ]]; then
        ui_say "$(jq -r .path <<<"$f"):$(jq -r .line <<<"$f") ($(jq -r '.side // "RIGHT"' <<<"$f")) is not a line the diff shows; anchoring the finding to line $moved instead."
        f="$(jq -c --argjson l "$moved" '.line = $l' <<<"$f")"
      fi
      anchor="$(state_anchor "$(jq -r .path <<<"$f")" "$(jq -r .line <<<"$f")")"
      id="$(state_finding_id "$(jq -r .path <<<"$f")" "$(jq -r .title <<<"$f")" "$anchor")"
      enriched="$(jq -c --argjson f "$f" --arg id "$id" --arg a "$anchor" \
        '. + [$f + {id:$id, anchor:$a, thread_id:null, root_comment_id:null, resolution:null, tracked_as:null}]' \
        <<<"$enriched")"
    done
    findings="$enriched"
    rm -rf "$tmp"

    # Record findings into the claim BEFORE posting any of them. A crash after
    # this point needs no second model invocation to recover, which is the
    # difference between a cheap retry and an expensive one.
    marker="$(jq -c --argjson f "$findings" --arg v "$verdict" --arg mr "$model_reported" \
      --argjson tk "${tokens:-null}" \
      '.findings = $f | .verdict = $v | .tokens = $tk
       | .model_reported = (if $mr == "null" then null else $mr end)' <<<"$marker")"
    gh_comment_edit "$CTX_REPO" "$comment_id" \
"**crossrev — reviewing, $(_pass_label "$pass" "$CTX_MAX_PASSES_PER_CYCLE")**

Findings recorded; posting them now.$(state_marker_encode "$(jq -c 'del(.comment_id)' <<<"$marker")")"
  fi
  run_checkpoint

  # --- inline comments, reconciled against what already landed -------------
  local already posted=0 skipped=0 unanchored=0 n i f id landed unanchored_already
  already="$(state_posted_finding_ids "$CTX_PR" "$CTX_REPO" "$CTX_AUTHOR" review)" || already=""

  # Seeded from the pull request rather than from zero, for the same reason the
  # resolve leg seeds its unthreaded count: a run that stopped between a
  # fallback comment and the summary comes back with that comment already
  # posted, `already` skips it below, and a counter starting at zero would
  # report a clean pass over a degraded one. Where the finding's marker landed
  # is the record — an inline comment is a review comment, and a fallback is an
  # issue comment, written by the same call that posted it.
  unanchored_already="$(state_unthreaded_finding_ids \
    "$CTX_PR" "$CTX_REPO" "$CTX_AUTHOR" review "$pass")" || unanchored_already=""
  [[ -n "$unanchored_already" ]] && \
    unanchored="$(printf '%s\n' "$unanchored_already" | wc -l | tr -d ' ')"

  n="$(jq 'length' <<<"$findings")"
  local high medium low pre actionable
  high="$(jq '[.[] | select(.severity == "high")] | length' <<<"$findings")"
  medium="$(jq '[.[] | select(.severity == "medium")] | length' <<<"$findings")"
  low="$(jq '[.[] | select(.severity == "low")] | length' <<<"$findings")"
  pre="$(jq '[.[] | select(.pre_existing // false)] | length' <<<"$findings")"
  actionable="$(run_actionable "$findings")"

  if (( n > 0 )); then
    ui_say "Found $n issue(s) — $high high, $medium medium, $low low, of which $pre pre-existing."
    ui_say "$actionable at or above min_fix_severity ($CTX_MIN_FIX_SEVERITY); the rest are reported and left alone."
    ui_say "Posting them as inline comments on the lines they affect."
  fi

  for (( i = 0; i < n; i++ )); do
    f="$(jq -c ".[$i]" <<<"$findings")"
    id="$(jq -r .id <<<"$f")"
    if grep -qx -- "$id" <<<"$already"; then skipped=$(( skipped + 1 )); continue; fi
    # gh_review_comment_create says which of the two happened. Discarding that
    # is how the run came to claim a clean inline post over a degraded one — a
    # fallback may change how something lands and may never change whether the
    # run says it landed normally.
    landed="$(gh_review_comment_create "$CTX_REPO" "$CTX_PR" "$CTX_HEAD_SHA" \
      "$(jq -r .path <<<"$f")" "$(jq -r .line <<<"$f")" "$(jq -r '.side // "RIGHT"' <<<"$f")" \
      "$(_review_comment_body "$f" "$pass" "$harness" "$model")")"
    posted=$(( posted + 1 ))
    [[ "$landed" == "fallback" ]] && unanchored=$(( unanchored + 1 ))
    run_checkpoint
  done

  # "finding" rather than "inline", because a fallback is counted here too and
  # the warning below is what splits the two. Subtracting `unanchored` instead
  # would be wrong on the resume path: it is seeded from the pass's own markers
  # before the loop runs, so a run coming back after an interruption starts
  # non-zero over findings `already` skips, and the difference can reach zero
  # on a pass where inline comments did land.
  (( posted > 0 ))  && ui_ok "posted $posted finding comment(s)"
  (( skipped > 0 )) && ui_say "$skipped finding(s) were already on the pull request from an earlier attempt, so they were not posted twice."
  if (( unanchored > 0 )); then
    local finding_noun="findings"; (( unanchored == 1 )) && finding_noun="finding"
    ui_warn "$unanchored $finding_noun could not be anchored to a line and landed as top-level comments" \
      "Each one names the location it faults, so nothing is lost, but it sits at the top of the pull request rather than beside the code — and the resolve leg has no thread to reply into either."
  fi

  # Thread ids come from GraphQL, matched by the finding marker in each comment
  # body rather than by path and line, so a moved line does not lose the link.
  #
  # `root_comment_id` is carried beside `thread_id` because the two answer
  # different questions and only one of them is a URL. `thread_id` is the GraphQL
  # node the resolve leg resolves the thread through; `root_comment_id` is the
  # numeric id a `#r<id>` anchor is built from, which is how a person reaches the
  # conversation from a table or a commit message.
  local threads_now
  threads_now="$(gh_review_threads "$CTX_REPO" "$CTX_PR")"
  findings="$(jq -c --argjson t "$threads_now" '
    [ .[] as $f
      | [$t[] | select(.finding_ids | index($f.id))] | first as $m
      | $f + { thread_id: ($m.id // $f.thread_id),
               root_comment_id: ($m.root_comment_id // $f.root_comment_id) } ]' \
    <<<"$findings")"
  marker="$(jq -c --argjson f "$findings" '.findings = $f' <<<"$marker")"

  # --- the summary comment -------------------------------------------------
  #
  # Stamped before the body is rendered, because the run-details table reads the
  # elapsed time off the marker and the marker is what the resolve leg re-renders
  # this comment from. A stamp written after the render would leave the first copy
  # of the comment claiming a duration nothing recorded.
  local summary_body
  marker="$(jq -c --argjson t "$(date +%s)" --argjson u "$unanchored" \
    '.done_ts = $t | .unanchored = $u' <<<"$marker")"
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
  next="$(legs_pass_label "$verdict" "$actionable" "$(_markers_escalated "$CTX_MARKERS")")"
  if [[ "$verdict" == "converged" && "$next" != "converged" ]]; then
    local finding_noun="findings"; (( actionable == 1 )) && finding_noun="finding"
    ui_warn "the reviewer returned verdict '$verdict' alongside $actionable actionable $finding_noun" \
      "The actionable count outranks the verdict, so the pass is labelled '$next' to run the resolve leg."
  fi
  run_pass_labels "$pass" "$next"

  printf '  → verdict: %s\n\n' "$verdict"
  if [[ "$next" == "awaiting-resolution" ]]; then
    ui_say "Nothing was changed in your working tree. To act on these:"
    ui_say "  crossrev resolve --pr $CTX_PR"
    printf '\n'
  else
    (( no_tips )) || run_upgrade_nudge
  fi
  return 0
}

# A finding's line, checked against the diff it was found in and moved onto the
# nearest line GitHub will accept when the miss is small enough to be a miscount.
#
# GitHub takes a comment only on a line the diff actually shows, and a reviewer
# that counts lines under a `@@` header instead of reading the gutter lands just
# outside one. That is not a small failure downstream: the comment falls back to
# the top of the pull request, so no review thread exists, so the resolve leg's
# reply has nowhere to go either and falls back in turn. One miscounted number
# costs two orphaned comments.
#
# Called before the finding's id is derived, and that ordering is load-bearing.
# `state_finding_id` hashes a window of lines around the anchor, so a line
# corrected after the id was computed would be a different finding to the next
# pass, and every cross-pass resolution would come apart.
#
# A line the diff cannot place is left exactly as the reviewer wrote it. Posting
# still attempts it, because the diff is not the only reason GitHub refuses one,
# and the fallback that catches it is the same either way.
#
# Prints the line to move to, or nothing when the finding stays where it is. The
# caller narrates the move and applies it, so this writes nothing to the stream
# it answers on.
_review_anchor_to_diff() {
  local f="$1" diff_file="$2" moved
  moved="$(diff_anchor "$diff_file" \
    "$(jq -r .path <<<"$f")" "$(jq -r '.side // "RIGHT"' <<<"$f")" "$(jq -r .line <<<"$f")")"
  [[ -n "$moved" && "$moved" != "$(jq -r .line <<<"$f")" ]] && printf '%s' "$moved"
  return 0
}

# A review comment must explain itself to a collaborator who has never heard of
# crossrev — which model, which pass of how many, and what happens next.
#
# The note under each finding says what will happen to it, which is a question
# about the threshold rather than about the severity alone. Saying "minor, not
# blocking" while the resolve leg is about to rewrite that line would be the
# comment contradicting the commit.
_review_comment_body() {
  local f="$1" pass="$2" harness="$3" model="$4" note
  if [[ "$(jq -r '.pre_existing // false' <<<"$f")" == "true" ]]; then
    note="A real bug, but this pull request did not introduce it, so it is reported here and never fixed here."
  elif legs_should_fix "$(jq -r .severity <<<"$f")" "$CTX_MIN_FIX_SEVERITY" false; then
    note="At or above this repository's \`min_fix_severity\` ($CTX_MIN_FIX_SEVERITY), so the resolve leg may change code for it."
  else
    note="Below this repository's \`min_fix_severity\` ($CTX_MIN_FIX_SEVERITY), so it is reported and left to a human."
  fi
  # A heading rather than a bold run of prose. Without it the label, the title,
  # the consequence and the fix are all one weight, and a reader scanning a diff
  # column has no edge to stop at.
  printf '#### %s %s\n\n%s\n\n**Fix:** %s\n\n<sub>%s · crossrev pass %s, reviewed by %s%s. A second agent now verifies this point and either fixes it, defers it, or explains why it is wrong.</sub>%s' \
    "$(run_finding_label "$f")" "$(_one_line "$(jq -r .title <<<"$f")")" \
    "$(jq -r .why <<<"$f")" "$(jq -r .fix <<<"$f")" \
    "$note" "$pass" "$harness" "${model:+ ($model)}" \
    "$(state_finding_marker "$(jq -r .id <<<"$f")" "$pass" review)"
}

# A GitHub alert: a tinted callout with a coloured left border, an icon and a
# bold label. They work in pull request comments, not only in files.
#
# **Exactly one per summary comment.** Stacking callouts turns the comment into
# wallpaper and destroys the thing that made the first one work, which is why the
# per-finding severities live in the table where emoji carry the colour instead.
#
# It degrades to a plain blockquote wherever GitHub's stylesheet is absent —
# notification email, a third-party mirror, `gh pr view` in a terminal. Acceptable
# for one coarse signal at the top of a comment, and not acceptable for every
# finding, which is the other half of why emoji do the fine-grained work.
_alert() {
  local kind="$1" body="$2"
  printf '> [!%s]\n' "$kind"
  # Every line needs its own marker, or the alert ends at the first newline and
  # the rest of the sentence renders as ordinary prose underneath it.
  printf '%s\n' "$body" | sed 's/^/> /'
  printf '\n'
}

# "3m 12s", from two marker timestamps. An em dash when either is missing, which
# is what a marker written before this field existed looks like.
_elapsed() {
  local from="$1" to="$2" secs
  [[ "$from" =~ ^[0-9]+$ && "$to" =~ ^[0-9]+$ ]] || { printf '—'; return 0; }
  secs=$(( to - from ))
  (( secs < 0 )) && { printf '—'; return 0; }
  if (( secs < 60 )); then printf '%ss' "$secs"
  else printf '%sm %ss' "$(( secs / 60 ))" "$(( secs % 60 ))"; fi
}

# 41205 as 41,205. Nothing in bash groups digits, and an unpunctuated six-figure
# number is the one cell in the table a reader has to count.
#
# Done with parameter expansion rather than a sed loop: BSD sed rejects labels
# written on one line, so the obvious `:a; s/…/; ta` one-liner works on GNU and
# silently prints the number ungrouped here.
_thousands() {
  [[ "$1" =~ ^[0-9]+$ ]] || { printf '—'; return 0; }
  local n="$1" out=""
  while (( ${#n} > 3 )); do
    out=",${n: -3}$out"
    n="${n:0:${#n}-3}"
  done
  printf '%s%s' "$n" "$out"
}

# Whether two model names denote the same model: the one crossrev asked for, and
# the one the harness says answered.
#
# Not a string comparison, because a harness resolves an alias. Claude Code
# takes `opus` or `sonnet` and reports the canonical id it landed on, so
# comparing raw strings turns ordinary configuration into a bold warning that
# the cross-model pairing may have broken. That warning is the one line in the
# table that must never cry wolf: a reader who has seen it fire on a healthy run
# will skim past it on the run where it means something.
#
# Containment rather than an alias table, deliberately — an alias-to-id table
# goes stale for the same reason the price table does, and it would have to be
# maintained per harness. An alias is the family token its canonical id carries,
# `opus` inside `claude-opus-4-5-20251101`, and a date pin is that id with more
# of it, so one name inside the other is one model written at two precisions. A
# substitution shares no such token, which is the case worth shouting about.
_same_model() {
  local want got
  want="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
  got="$(printf '%s' "$2" | tr '[:upper:]' '[:lower:]')"
  [[ -n "$want" && -n "$got" ]] || return 1
  [[ "$got" == *"$want"* || "$want" == *"$got"* ]]
}

# The run details, one row for the comment's own leg and no other.
#
# **One row per leg, and only its own.** The review comment reports the review
# leg, the resolve comment the resolve leg. The two sit adjacent on the pull
# request, so nothing is lost by not duplicating — and duplication means that
# after a retry or a resume the two copies can disagree, with no rule saying
# which wins.
#
# **Harness, model and effort are one cell.** They describe one thing: which
# agent ran. A named endpoint adds a fourth segment, because which endpoint
# served the call matters as much as which model.
#
# **Unconditional.** It appears whatever the verdict, including converged and
# blocked. A pass that finds nothing still spends time and tokens, and that is
# exactly when you want to know.
_run_details() {
  local marker="$1" leg="$2" harness model reported effort endpoint agent gaps=""
  harness="$(jq -r '.harness // "?"' <<<"$marker")"
  model="$(jq -r '.model // ""' <<<"$marker")"
  reported="$(jq -r '.model_reported // ""' <<<"$marker")"
  effort="$(jq -r '.effort // ""' <<<"$marker")"
  endpoint="$(jq -r '.endpoint // ""' <<<"$marker")"

  agent="\`$harness\`"
  if [[ -n "$reported" && "$reported" != "null" ]]; then
    agent="$agent · \`$reported\`"
    # A genuine substitution is not a footnote. It may mean the cross-model
    # property broke, and it should be impossible to skim past. An alias
    # resolving to its own canonical id is not one, so it is not flagged as one.
    if [[ -n "$model" && "$model" != "null" ]] && ! _same_model "$model" "$reported"; then
      agent="$agent — **requested \`$model\`, a different model answered**"
    fi
  elif [[ -n "$model" && "$model" != "null" ]]; then
    agent="$agent · \`$model\`"
    gaps="$harness does not report which model answered, so the model above is the one crossrev requested."
  fi
  [[ -n "$effort" && "$effort" != "null" ]] && agent="$agent · $effort effort"
  [[ -n "$endpoint" && "$endpoint" != "null" && "$endpoint" != "vendor" ]] \
    && agent="$agent · via \`$endpoint\`"

  printf '**Run details**\n\n'
  printf '| Leg | Agent | Duration | Tokens |\n|---|---|---|---|\n'
  printf '| %s | %s | %s | %s |\n\n' "$leg" "$agent" \
    "$(_elapsed "$(jq -r '.ts // ""' <<<"$marker")" "$(jq -r '.done_ts // ""' <<<"$marker")")" \
    "$(_thousands "$(jq -r '.tokens // ""' <<<"$marker")")"

  # Named once, under the table, rather than annotated in every cell: the gap is
  # the same every pass, so repeating it is noise.
  #
  # Cost is deliberately absent rather than left as a blank column that reads as
  # zero, and the sentence says what crossrev knows rather than what the run cost.
  # A leg can be authenticated as a subscription, as a vendor API key, or as a
  # named Anthropic-compatible endpoint that charges per token — and crossrev is
  # handed no billing figure in any of them, so claiming there is nothing to pay
  # would be wrong on two of the three. Computing one from tokens times a price
  # table means maintaining a price table that goes stale.
  printf '<sub>%sNo cost is shown: crossrev is given no billing figure by the harness, whichever credential the leg ran on.</sub>\n\n' \
    "${gaps:+$gaps }"
}

# The summary table. Severity and category carry emoji, and provenance sits in
# the *what* cell rather than beside the severity — `pre-existing` is not a
# fourth severity and must not read as one.
#
# Rendered row by row rather than in one jq pass, because the emoji come from the
# two functions above and a second copy of either mapping inside a jq program is
# a copy that drifts.
_findings_table() {
  local findings="$1" sha="$2" n i f sev kind pre path line
  n="$(jq 'length' <<<"$findings")"
  printf '| Severity | Category | Finding | Location |\n|---|---|---|---|\n'
  for (( i = 0; i < n; i++ )); do
    f="$(jq -c ".[$i]" <<<"$findings")"
    sev="$(jq -r '.severity // "?"' <<<"$f")"
    kind="$(jq -r '.category // "?"' <<<"$f")"
    path="$(jq -r .path <<<"$f")"
    line="$(jq -r .line <<<"$f")"
    pre=""
    [[ "$(jq -r '.pre_existing // false' <<<"$f")" == "true" ]] && pre=' <sub>· pre-existing</sub>'
    # `&nbsp;` between glyph and word, because these are the narrowest columns
    # and an ordinary space is a break opportunity — the pair wraps onto two
    # lines and the emoji ends up orphaned above its own label.
    #
    # The finding comes before its location, because the reading order is
    # severity, then what was found, then where it is. The location has a column
    # of its own and carries the full path rather than a basename with the path
    # hidden in a hover title, so two files sharing a name are tellable apart
    # without one.
    printf '| %s&nbsp;%s | %s&nbsp;%s | %s%s | %s |\n' \
      "$(run_severity_emoji "$sev")" "$(_ucfirst "$sev")" \
      "$(run_category_emoji "$kind")" "$(_ucfirst "$kind")" \
      "$(_md_cell "$(jq -r .title <<<"$f")")" "$pre" \
      "$(_location_link "$path" "$line" "$(_blob_url "$sha" "$path" "$line")")"
  done
  printf '\n'
}

# A permalink to one line of one file at the revision that was reviewed.
#
# Pinned to the marker's head_sha rather than to the branch: the resolve leg
# re-renders this comment after pushing its fixes, and a branch-relative link
# would then point at code that has moved under the finding it describes.
_blob_url() {
  local sha="$1" path="$2" line="$3"
  printf 'https://github.com/%s/blob/%s/%s#L%s' "$CTX_REPO" "$sha" "$(_url_path "$path")" "$line"
}

# A repository path on its way into a link destination.
#
# Percent-encoded one segment at a time, so the separators survive and everything
# else is escaped. Three characters are the reason, and all three are legal in a
# path: a space ends an inline link's destination, a `)` closes it early and
# spills the rest of the URL into the cell around it, and a `|` splits the table
# row the link sits in.
#
# `@uri` covers all three. It is stricter than JavaScript's encodeURIComponent,
# which leaves the parentheses alone — a difference worth knowing, because the
# rule that holds for one does not hold for the other.
_url_path() {
  jq -rn --arg p "$1" '$p | split("/") | map(@uri) | join("/")'
}

# A link to the review thread a finding was raised in.
#
# The files-tab form rather than the conversation-tab one, and the difference was
# measured rather than assumed. `<pr>#discussion_r<id>` never scrolls on a first
# load: the timeline renders after the browser has already applied the fragment,
# so it works on a reload and not on a click, which is not a property a link in
# permanent history can rely on. `<pr>/files#r<id>` scrolls, and fails only for a
# comment GitHub has marked outdated because it could no longer re-anchor it onto
# the current diff.
_thread_url() {
  printf 'https://github.com/%s/pull/%s/files#r%s' "$CTX_REPO" "$CTX_PR" "$1"
}

# How a finding is named to a person: its location, linked.
#
# Never its id. The 16-character finding id is a correlation key for crossrev's
# own state. It lives in an HTML comment marker, so it renders invisible, and a
# browser find on the pull request matches only the row the reader started from.
# It is a dead end wherever it is printed.
#
# The path is a table cell like any other, and it gets the same escaping the
# title beside it gets. `src/a|b/x.ts` is a legal path on any POSIX filesystem,
# and the path on a finding is a model-written string that nothing upstream holds
# to more than "not empty" — so an unescaped pipe splits the row into an extra
# column, silently, with every row around it still rendering. The escape goes
# inside the code span because GFM resolves `\|` before the span is parsed: a
# pipe in backticks splits the row exactly as one outside them does.
_location_link() {
  local path="$1" line="$2" url="$3"
  [[ -n "$path" && "$path" != "null" ]] || { printf -- '—'; return 0; }
  printf '[`%s:%s`](%s)' "$(_md_cell "$path")" "$(_md_cell "$line")" "$url"
}

# The location cell for a finding the resolve leg reports on.
#
# Links the thread, because the reader's question there is what happened to this
# finding and the answer is the conversation. Falls back to the code permalink
# for a finding that never got a thread — the `unthreaded` case, an inline
# comment GitHub refused to anchor.
#
# The text comes from crossrev's own finding record and never from GitHub's:
# GitHub drops a comment's line to null once it goes outdated, which is exactly
# when the link stops working and the text has to carry the reader alone.
_finding_location() {
  local f="$1" sha="$2" path line root
  path="$(jq -r '.path // ""' <<<"$f")"
  line="$(jq -r '.line // ""' <<<"$f")"
  root="$(jq -r '.root_comment_id // ""' <<<"$f")"
  if [[ -n "$root" && "$root" != "null" ]]; then
    _location_link "$path" "$line" "$(_thread_url "$root")"
  else
    _location_link "$path" "$line" "$(_blob_url "$sha" "$path" "$line")"
  fi
}

# The review leg's summary comment.
#
# Takes the marker rather than eight positional arguments, because the resolve
# leg re-renders this comment from the marker alone when it fills in the
# resolutions. Everything the comment says about the run — harness, model,
# effort, endpoint, timing, tokens — therefore has to live on the marker, and
# passing the marker is what keeps the two renderings honest about that.
_review_summary_body() {
  local findings="$1" marker="$2" n actionable verdict pass
  n="$(jq 'length' <<<"$findings")"
  actionable="$(run_actionable "$findings")"
  verdict="$(jq -r '.verdict // "issues-remain"' <<<"$marker")"
  pass="$(jq -r '.pass // 1' <<<"$marker")"

  printf '## crossrev review — %s\n\n' "$(_pass_label "$pass" "$CTX_MAX_PASSES_PER_CYCLE")"

  # The alert is the one line in the comment that has to read as a sentence, so
  # it gets a real plural rather than the "(s)" the terminal output uses.
  local noun="findings"; (( n == 1 )) && noun="finding"

  # The verdict, in the one place a reader cannot skim past. It used to sit at the
  # foot of the comment as ordinary prose, below a table, which is the last place
  # anyone looks for the answer to "what happens now".
  case "$verdict" in
    converged)
      _alert TIP "$(printf '**Converged.** Nothing at or above `min_fix_severity` (%s) remains, so the loop stops here. Findings below the threshold, and pre-existing ones, are reported but cannot keep the loop alive — a loop that cannot converge because of a naming quibble is one nobody leaves switched on.' "$CTX_MIN_FIX_SEVERITY")" ;;
    blocked)
      _alert WARNING '**The review could not be completed.** The loop halts here and a human is needed. Nothing in this comment is a judgement about the code.' ;;
    *)
      _alert CAUTION "$(printf '**%s %s need resolving.** A second agent now verifies every finding below against the codebase and either fixes it, skips it, defers it, or explains why it is wrong. It may change code for the %s at or above `min_fix_severity` (%s); the rest are verified and reported, never silently dropped.' "$n" "$noun" "$actionable" "$CTX_MIN_FIX_SEVERITY")" ;;
  esac

  # Which agent ran is the run-details table's job now, and repeating the
  # answering model here is the duplication Decision 4 rejected — two renderings
  # of one fact that can disagree after a retry.
  printf 'Verdict: **%s**.\n\n' "$verdict"

  if (( n == 0 )); then
    printf 'No findings. Low-severity and pre-existing issues would be listed here too, so this is an empty review rather than a filtered one.\n\n'
  else
    _findings_table "$findings" "$(jq -r '.head_sha // ""' <<<"$marker")"
  fi

  # A finding that never reached its line is recorded here rather than only in
  # the run's own output, because the pull request is what anyone reads
  # afterwards and the run's output is gone. The same silence hid this on PR 14:
  # the table listed a finding at `CHANGELOG.md:14` and nothing said the comment
  # was sitting at the top of the pull request instead.
  local unanchored
  unanchored="$(jq -r '.unanchored // 0' <<<"$marker")"
  if (( unanchored == 1 )); then
    printf 'One finding could not be anchored to a line of the diff, so it is a top-level comment on this pull request naming its location instead. Its reply will land there too, because there is no review thread to put one in.\n\n'
  elif (( unanchored > 1 )); then
    printf '%s findings could not be anchored to a line of the diff, so they are top-level comments on this pull request naming their locations instead. Their replies will land there too, because there are no review threads to put them in.\n\n' "$unanchored"
  fi

  _run_details "$marker" review
}

# ---------------------------------------------------------------------------
# The resolve leg
# ---------------------------------------------------------------------------

leg_resolve() {
  local pr="" repo="" harness_override="" trigger="human" no_tips=0
  while (( $# )); do
    case "$1" in
      --pr)      pr="${2:-}"; shift 2 ;;
      --repo)    repo="${2:-}"; shift 2 ;;
      --harness) harness_override="${2:-}"; shift 2 ;;
      --trigger) trigger="${2:-}"; shift 2 ;;
      --no-tips) no_tips=1; shift ;;
      --pass)    shift 2 ;;
      *) ui_die "unknown option for resolve: $1" \
           "Usage: crossrev resolve --pr <number> [--harness claude|codex] [--trigger human|automatic]" ;;
    esac
  done
  [[ -n "$pr" ]] || ui_die "crossrev resolve needs a pull request number" "Usage: crossrev resolve --pr 42"
  case "$trigger" in
    human|automatic) ;;
    *) ui_die "unknown resolve trigger: $trigger" "Use --trigger human or --trigger automatic." ;;
  esac

  run_trap_install
  # Handed on rather than parsed and dropped: ctx_load declines a draft pull
  # request on an automatic invocation, and a resolve leg that always said
  # "human" would resolve findings on a draft the review leg refused to make.
  local load_rc
  ctx_load "$pr" "$repo" "$trigger" || {
    load_rc=$?
    (( load_rc == 2 )) && return 0
    return "$load_rc"
  }
  run_lock_acquire "$CTX_PR" "$CTX_MODE"
  CROSSREV_RESUME_HINT="crossrev resolve --pr $CTX_PR"

  if grep -qw "crossrev/stop" <<<"$CTX_LABELS"; then
    ui_say "crossrev/stop is on $CTX_REPO#$CTX_PR, so nothing is resolved."
    ui_say "Remove the label to let the loop continue."
    return 0
  fi

  local pass review_marker findings
  pass="$(state_current_review_pass "$CTX_MARKERS")"
  (( pass > 0 )) || ui_die "$CTX_REPO#$CTX_PR has no review to resolve" \
    "The resolve leg acts on a review leg's findings. Run: crossrev review --pr $CTX_PR"

  review_marker="$(state_marker_for "$CTX_MARKERS" "$pass" review)"
  [[ "$(jq -r '.state // ""' <<<"$review_marker")" == "complete" ]] || ui_die \
    "the pass-$pass review on $CTX_REPO#$CTX_PR did not finish" \
    "Resolving a half-posted review would reply to findings the reviewer may not have finished recording. Re-run: crossrev review --pr $CTX_PR"

  local redrive=""
  if state_current_pass_complete "$CTX_MARKERS" "$pass" resolve; then
    local done_marker
    done_marker="$(state_marker_for "$CTX_MARKERS" "$pass" resolve)"
    if legs_resolve_redrivable "$done_marker"; then
      redrive="$done_marker"
    else
      ui_say "pass $pass of $CTX_REPO#$CTX_PR is already resolved."
      ui_say "Push a revision, or run: crossrev review --pr $CTX_PR"
      return 0
    fi
  fi

  findings="$(jq -c '.findings // []' <<<"$review_marker")"
  if [[ "$(jq 'length' <<<"$findings")" == "0" ]]; then
    # An empty pass converges only when nothing is waiting on a human. While an
    # escalation stands, halted is the honest label — the same call the review
    # leg makes — and converged would contradict the threads still open.
    #
    # The verdict is read for the same reason `legs_pass_label` exempts it: a
    # reviewer that converges after a human settled the escalated thread is the
    # settlement being verified, and that is the one way out of the halt. Left
    # to the escalation count alone, running this leg over that pass would strip
    # the green label the review leg had just earned and halt it again.
    if [[ "$(jq -r '.verdict // ""' <<<"$review_marker")" != "converged" ]] \
       && (( $(_markers_escalated "$CTX_MARKERS") > 0 )); then
      ui_say "pass $pass raised nothing new on $CTX_REPO#$CTX_PR, and the escalated findings still need a human decision."
      run_pass_labels "$pass" halted
    else
      ui_say "pass $pass found nothing to resolve on $CTX_REPO#$CTX_PR."
      run_pass_labels "$pass" converged
    fi
    return 0
  fi

  # Resolve runs in a dedicated worktree so the operator's checkout is untouched.
  local orig_cwd wt_dir wt_err wt_cur_sha push_remote target_repo current_sha
  orig_cwd="$(pwd)"
  wt_dir="$(_worktree_dir "$CTX_REPO" "$CTX_PR")"
  CROSSREV_WORKTREE="$wt_dir"

  push_remote="$(git config "branch.${CTX_HEAD_BRANCH}.pushRemote" 2>/dev/null || true)"
  if [[ -z "$push_remote" ]]; then
    push_remote="$(git config "branch.${CTX_HEAD_BRANCH}.remote" 2>/dev/null || true)"
  fi
  if [[ -z "$push_remote" ]]; then
    push_remote="$(git config "remote.pushDefault" 2>/dev/null || true)"
  fi
  if [[ -z "$push_remote" ]]; then
    if git remote get-url origin >/dev/null 2>&1; then
      push_remote="origin"
    fi
  fi
  [[ -n "$push_remote" ]] || ui_die \
    "could not resolve the push remote for branch '$CTX_HEAD_BRANCH'" \
    "Check \`git remote -v\` in this checkout."

  legs_resolve_push_repo "$push_remote"
  target_repo="$LEGS_PUSH_REPO"
  [[ -n "$target_repo" ]] || ui_die \
    "could not read the URL for remote '$push_remote'" \
    "Check \`git remote -v\` in this checkout."

  if ! git cat-file -e "${CTX_HEAD_SHA}^{commit}" 2>/dev/null; then
    git fetch "$push_remote" "$CTX_HEAD_SHA" >/dev/null 2>&1 \
      || git fetch "$push_remote" "refs/pull/${CTX_PR}/head" >/dev/null 2>&1 \
      || git fetch "$push_remote" "$CTX_HEAD_BRANCH" >/dev/null 2>&1 \
      || git fetch "$push_remote" >/dev/null 2>&1 || true
    if ! git cat-file -e "${CTX_HEAD_SHA}^{commit}" 2>/dev/null; then
      ui_die "could not find revision '$CTX_HEAD_SHA' for $CTX_REPO#$CTX_PR" \
        "Fetching from remote '$push_remote' did not reach the pull request's head revision."
    fi
  fi

  if [[ -d "$wt_dir" ]]; then
    wt_cur_sha="$(git -C "$wt_dir" rev-parse HEAD 2>/dev/null || true)"
    if [[ -z "$wt_cur_sha" || "$wt_cur_sha" != "$CTX_HEAD_SHA" ]]; then
      rm -rf "$wt_dir"
      git worktree prune 2>/dev/null || true
    fi
  fi

  if [[ ! -d "$wt_dir" ]]; then
    mkdir -p "$(dirname "$wt_dir")"
    if ! wt_err="$(git worktree add --detach "$wt_dir" "$CTX_HEAD_SHA" 2>&1)"; then
      ui_die "could not create worktree for revision '$CTX_HEAD_SHA' at $wt_dir" "$wt_err"
    fi
  fi

  cd "$wt_dir" || ui_die "could not enter worktree at $wt_dir" "Check directory permissions."

  # The push guard runs before anything is invoked, not before the push. Finding
  # out after a model has run that the checkout is on the wrong revision wastes the
  # invocation and leaves changes in a tree nobody expected them in.
  current_sha="$(git rev-parse HEAD 2>/dev/null || true)"

  legs_assert_push_target "$current_sha" "$CTX_HEAD_SHA" "$CTX_HEAD_BRANCH" \
    "$CTX_DEFAULT_BRANCH" "$CTX_HEAD_REPO" "$target_repo" "$CTX_MAINTAINER_CAN_MODIFY" "$CTX_IS_CROSS_REPOSITORY"

  run_leg_settings resolver "$harness_override"
  local harness="$LEG_HARNESS" model effort endpoint write="$LEG_WRITE"
  model="$(_nullable "$LEG_MODEL")"
  effort="$(_nullable "$LEG_EFFORT")"
  endpoint="$(_nullable "$LEG_ENDPOINT")"

  printf '\n  Resolving %s#%s — %s\n' "$CTX_REPO" "$CTX_PR" "$(_pass_label "$pass" "$CTX_MAX_PASSES_PER_CYCLE")"
  printf '  Resolver: %s%s%s\n' "$harness" "${model:+, $model}" "${effort:+, $effort effort}"

  local claim comment_id marker stale
  claim="$(state_open_claim "$CTX_MARKERS" "$pass" resolve)" || claim=""
  if [[ -n "$redrive" ]]; then
    # A pass that ended blocked or escalated is complete but not settled, so
    # the claim is rebuilt from the finished marker rather than refused: the
    # resolutions and the block go back to empty, the clock and the revision
    # move to now, and the same comment carries the new attempt — editing it
    # keeps the marker history one record per pass, which is what every reader
    # above assumes.
    comment_id="$(jq -r '.comment_id' <<<"$redrive")"
    marker="$(jq -c --argjson ts "$(date +%s)" --arg sha "$CTX_HEAD_SHA" \
      --arg r "${GITHUB_RUN_ID:-local-$$}" \
      --arg h "$harness" --arg m "$model" --arg e "$effort" --arg ep "$endpoint" '
      .state = "started" | .ts = $ts | .done_ts = null | .head_sha = $sha | .run_id = $r
      | .harness = $h | .model = (if $m == "" then null else $m end)
      | .effort = (if $e == "" then null else $e end)
      | .endpoint = (if $ep == "" then null else $ep end)
      | .blocked = false | .blocked_reason = null | .commit_sha = null
      | .model_reported = null | .tokens = null | .summary = "" | .resolutions = []
      | .commit_subject = null
      | del(.unthreaded)' <<<"$redrive")"
    ui_say "Pass $pass's resolve leg ended without settling its findings — driving pass $pass again."
    gh_comment_edit "$CTX_REPO" "$comment_id" \
"**crossrev — resolving $(_pass_label "$pass" "$CTX_MAX_PASSES_PER_CYCLE")**

Driving the pass again: the previous attempt ended without settling its findings. Verifying each finding against the codebase. This comment becomes the pass summary when the resolve leg finishes.$(state_marker_encode "$(jq -c 'del(.comment_id)' <<<"$marker")")"
  elif [[ -n "$claim" ]]; then
    comment_id="$(jq -r '.comment_id' <<<"$claim")"
    if stale="$(state_claim_is_stale "$claim" "$CTX_HEAD_SHA")"; then
      ui_warn "abandoning the unfinished pass-$pass resolve — $stale" \
        "Resuming it would reconcile replies against a revision that has moved. Starting the pass again instead."
      marker="$(jq -c --argjson ts "$(date +%s)" --arg sha "$CTX_HEAD_SHA" \
        '.ts = $ts | .head_sha = $sha | .resolutions = [] | .commit_sha = null
         | .commit_subject = null' <<<"$claim")"
    else
      marker="$claim"
      ui_say "Resuming pass $pass — the previous attempt recorded $(jq -r '(.resolutions // []) | length' <<<"$marker") resolution(s)."
    fi
  else
    marker="$(jq -cn --argjson p "$pass" --arg sha "$CTX_HEAD_SHA" \
      --arg r "${GITHUB_RUN_ID:-local-$$}" --argjson ts "$(date +%s)" \
      --arg h "$harness" --arg m "$model" --arg e "$effort" --arg ep "$endpoint" \
      '{v:1, leg:"resolve", pass:$p, state:"started", ts:$ts, done_ts:null, run_id:$r,
        head_sha:$sha, harness:$h, model:(if $m == "" then null else $m end),
        effort:(if $e == "" then null else $e end),
        endpoint:(if $ep == "" then null else $ep end),
        model_reported:null, tokens:null,
        blocked:false, blocked_reason:null, commit_sha:null, commit_subject:null,
        summary:"", resolutions:[]}')"
    comment_id="$(gh_comment_create "$CTX_REPO" "$CTX_PR" \
"**crossrev — resolving $(_pass_label "$pass" "$CTX_MAX_PASSES_PER_CYCLE")**

Verifying each finding against the codebase. This comment becomes the pass summary when the resolve leg finishes.$(state_marker_encode "$marker")")"
    [[ -n "$comment_id" ]] || ui_die "the claim comment did not post on $CTX_REPO#$CTX_PR" \
      "The marker is what makes a retry safe, so crossrev stops rather than resolving without one."
  fi
  marker="$(jq -c --argjson id "$comment_id" '. + {comment_id: $id}' <<<"$marker")"
  run_checkpoint

  local threads resolutions blocked blocked_reason summary model_reported tokens
  threads="$(gh_review_threads "$CTX_REPO" "$CTX_PR")"

  # Backfilled rather than trusted from the review marker, because a pull request
  # reviewed before findings carried `root_comment_id` has none on record and its
  # summary table would fall back to the code permalink for every row. The review
  # leg sets it too; this covers what it could not.
  findings="$(jq -c --argjson t "$threads" '
    [ .[] as $f
      | [$t[] | select(.finding_ids | index($f.id))] | first as $m
      | $f + { root_comment_id: ($f.root_comment_id // $m.root_comment_id) } ]' \
    <<<"$findings")"

  if [[ "$(jq -r '(.resolutions // []) | length' <<<"$marker")" != "0" ]]; then
    ui_say "The previous attempt already recorded its resolutions, so the resolver is not run again."
    resolutions="$(jq -c '.resolutions' <<<"$marker")"
    # A claim written before the field was renamed carries `wrap_up`, and an
    # upgrade between recording the resolutions and finishing the pass lands
    # exactly here: the resolutions are found, the agent is not run again, and a
    # bare `.summary` read publishes an empty comment. Migrated on the MARKER
    # rather than read defensively into a local, because the summary comment is
    # rendered from the marker and a local would leave the published body empty.
    marker="$(jq -c '.summary = (.summary // .wrap_up // "") | del(.wrap_up)' <<<"$marker")"
    summary="$(jq -r '.summary' <<<"$marker")"
    blocked="$(jq -r '.blocked // false' <<<"$marker")"
    blocked_reason="$(jq -r '.blocked_reason // "null"' <<<"$marker")"
    model_reported="$(jq -r '.model_reported // "null"' <<<"$marker")"
  else
    local candidates enriched prior_resolutions
    candidates="$(_resolve_dedupe_candidates "$findings")"

    prior_resolutions="$(jq -c '[.[] | select(.leg == "resolve") | (.resolutions // [])[]]' <<<"$CTX_MARKERS")"

    # Each finding is handed to the model with the orchestrator's own answer to
    # "may you change code for this", rather than the threshold and a rule to
    # apply. Two readings of one policy is one reading too many: the label the
    # reviewer already posted says which findings are fixable, and a model that
    # ranks them differently contradicts a comment sitting on the pull request.
    enriched="$(jq -c --argjson prior "$prior_resolutions" '
      [ .[] as $f
        | $f + {prior_resolution: ([$prior[] | select(.finding_id == $f.id) | .resolution] | last // null)} ]' \
      <<<"$findings")"
    # Each finding also gets the number the prompt will show it under. The model
    # returns that number instead of the finding's 16-character id, because
    # copying a hash accurately is clerical work models are poor at — on PR 5 the
    # resolver mistyped all three, and every lookup keyed on them missed in
    # silence. Every shipped harness enforces "an integer" before crossrev sees
    # the payload, so the mistyped identifier stops being reachable rather than
    # merely being detected.
    local n_e i_e f_e may enriched_out="[]"
    n_e="$(jq 'length' <<<"$enriched")"
    for (( i_e = 0; i_e < n_e; i_e++ )); do
      f_e="$(jq -c ".[$i_e]" <<<"$enriched")"
      may=false
      legs_should_fix "$(jq -r .severity <<<"$f_e")" "$CTX_MIN_FIX_SEVERITY" \
        "$(jq -r '.pre_existing // false' <<<"$f_e")" && may=true
      enriched_out="$(jq -c --argjson f "$f_e" --argjson m "$may" --argjson n "$(( i_e + 1 ))" \
        '. + [$f + {may_fix: $m, number: $n}]' <<<"$enriched_out")"
    done
    enriched="$enriched_out"

    local tmp diff_file prompt_file envelope_file meta payload
    local -a exclude
    tmp="$(mktemp -d)"
    diff_file="$tmp/diff"; prompt_file="$tmp/prompt"; envelope_file="$tmp/envelope"
    exclude=()
    [[ "$CTX_BACKLOG" == "repository" ]] && exclude=("$CTX_BACKLOG_PATH" .crossrev)
    gh_pr_diff "$CTX_REPO" "$CTX_PR" "$CTX_BASE_SHA" "$CTX_HEAD_SHA" ${exclude[@]+"${exclude[@]}"} >"$diff_file"

    # `base_sha` and `crossrev_email` are here for the commit convention the
    # prompt shows: the subjects are sampled from the base revision, and the
    # leg's own past commits are excluded so it does not learn the generic
    # subject it is replacing.
    meta="$(jq -cn --arg repo "$CTX_REPO" --argjson pr "$CTX_PR" --argjson pass "$pass" \
      --argjson max "$CTX_MAX_PASSES_PER_CYCLE" --arg sha "$CTX_HEAD_SHA" --arg fa "$CTX_MIN_FIX_SEVERITY" \
      --arg backlog "$CTX_BACKLOG" --arg base "$CTX_BASE_SHA" \
      --arg mine "${CROSSREV_GIT_EMAIL:-crossrev@users.noreply.github.com}" \
      '{repo:$repo, pr:$pr, pass:$pass, max_passes_per_cycle:$max, head_sha:$sha, min_fix_severity:$fa,
        backlog:$backlog, base_sha:$base, crossrev_email:$mine}')"

    prompt_resolve "$prompt_file" "$ROOT/skills/pr-resolve/SKILL.md" "$diff_file" \
      "$meta" "$enriched" "$threads" "$candidates"

    # What the orchestrator supplied, so the validator can check the answer
    # against it: how many findings were numbered, and which issue numbers were
    # offered as duplicate candidates.
    local expect
    expect="$(jq -cn --argjson n "$n_e" --argjson c "$candidates" \
      '{findings: $n, candidates: [$c[]?[]?.number] | unique}')"

    ui_say "Verifying each finding against the codebase."
    run_invoke "$envelope_file" "$harness" "$prompt_file" \
      "$ROOT/schemas/resolve.schema.json" "$(pwd)" \
      "$model" "$effort" "$endpoint" "$write" validate_resolve "$expect"

    payload="$(jq -c .payload "$envelope_file")"
    model_reported="$(jq -r '.model_reported // "null"' "$envelope_file")"
    resolutions="$(jq -c '.resolutions' <<<"$payload")"

    # The number stops existing here. Everything below — the marker, the thread
    # lookup, the dedupe, the commit message, the resolution table — keeps
    # keying on the finding's id exactly as it did before, so a marker written
    # after this change is readable by the code that came before it and no
    # migration is needed for the ones already on live pull requests.
    #
    # Matched on the number each finding was rendered with rather than on array
    # position. The two agree today, and a mapping that depends on nobody ever
    # reordering that array is the kind of assumption this change exists to stop
    # making.
    resolutions="$(jq -c --argjson f "$enriched" '
      [ .[] as $d
        | $d + {finding_id: ([$f[] | select(.number == $d.finding_number) | .id] | first)}
        | del(.finding_number) ]' <<<"$resolutions")"
    summary="$(jq -r '.summary' <<<"$payload")"
    blocked="$(jq -r '.blocked // false' <<<"$payload")"
    blocked_reason="$(jq -r '.blocked_reason // "null"' <<<"$payload")"
    tokens="$(jq -c '.tokens // null' "$envelope_file")"
    # Onto the marker rather than into a local, for the reason every other field
    # here is: the commit happens after a checkpoint, so a run that dies between
    # the two comes back needing the subject the resolver wrote, and a local is
    # gone by then.
    rm -rf "$tmp"

    marker="$(jq -c --argjson d "$resolutions" --arg w "$summary" --argjson b "$blocked" \
      --arg br "$blocked_reason" --arg mr "$model_reported" --argjson tk "${tokens:-null}" \
      --argjson p "$payload" '
      .resolutions = $d | .summary = $w | .blocked = $b | .tokens = $tk
      | .commit_subject = ($p.commit_subject // null)
      | .blocked_reason = (if $br == "null" then null else $br end)
      | .model_reported = (if $mr == "null" then null else $mr end)' <<<"$marker")"
    gh_comment_edit "$CTX_REPO" "$comment_id" \
"**crossrev — resolving $(_pass_label "$pass" "$CTX_MAX_PASSES_PER_CYCLE")**

Resolutions recorded; committing and replying now.$(state_marker_encode "$(jq -c 'del(.comment_id)' <<<"$marker")")"
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
  # The ordering is load-bearing for the repository backlog and irrelevant to the
  # GitHub issues destination: a repository write in the working tree after a commit
  # left that write uncommitted, and on an ephemeral runner it died with the
  # container — while its thread resolved, because `tracked` was non-empty. The
  # GitHub issues are indifferent, since they are written outside the tree.
  #
  # Replies and resolution are pass two, below the commit. Persist still happens
  # before resolve, which is the invariant that matters: a thread resolved
  # against a write that did not land is exactly how work disappears.
  local filed=0 matched=0 deferred_lines="" backlog_wrote=0
  local n i d id disp tracked dup existing where
  n="$(jq 'length' <<<"$resolutions")"
  for (( i = 0; i < n; i++ )); do
    d="$(jq -c ".[$i]" <<<"$resolutions")"
    id="$(jq -r .finding_id <<<"$d")"
    [[ "$(jq -r .resolution <<<"$d")" == "deferred" ]] || continue

    # Each line leads with where the finding is, not with its id. The id named
    # nothing a reader could reach: it lives in an invisible marker, so it is not
    # even searchable on the page the list is printed on.
    where="$(_finding_location \
      "$(jq -c --arg id "$id" 'map(select(.id == $id)) | first // {}' <<<"$findings")" \
      "$(jq -r '.head_sha // ""' <<<"$marker")")"

    tracked=""; existing=""
    if [[ "$CTX_BACKLOG" == "github_issues" ]]; then
      # Tier 1: exact, against crossrev's own issues. Deterministic, no model,
      # no false positives — this is what stops three pull requests touching
      # one legacy bug filing it three times.
      existing="$(gh_issue_by_finding "$CTX_REPO" "$CTX_TRACKING_LABEL" "$id")" || existing=""
    fi
    dup="$(jq -r '.duplicate_of // ""' <<<"$d")"
    if [[ -n "$existing" ]]; then
      tracked="$CTX_REPO#$existing"
      matched=$(( matched + 1 ))
      deferred_lines="$deferred_lines
- $where — already tracked as #$existing, so nothing was filed"
    elif [[ -n "$dup" && "$dup" != "null" ]]; then
      tracked="$CTX_REPO#$dup"
      matched=$(( matched + 1 ))
      deferred_lines="$deferred_lines
- $where — matches the existing issue #$dup, so nothing was filed"
      if [[ "$(jq -r '.backlog.github_issues.comment_on_existing_issue' <<<"$CFG_MERGED")" == "true" ]]; then
        gh_issue_comment "$CTX_REPO" "$dup" \
          "Seen again while reviewing $CTX_REPO#$CTX_PR (crossrev pass $pass).$(state_finding_marker "$id" "$pass" resolve)"
      fi
    else
      tracked="$(_resolve_persist "$d" "$id" "$pass")" || tracked=""
      if [[ -n "$tracked" ]]; then
        filed=$(( filed + 1 ))
        # Only a repository backlog puts something in the tree for the commit to carry.
        [[ "$CTX_BACKLOG" == "repository" ]] && backlog_wrote=1
        deferred_lines="$deferred_lines
- $where — filed as ${tracked/#"$CTX_REPO"/}"
      else
        deferred_lines="$deferred_lines
- $where — **not persisted anywhere**, so its thread stays open rather than resolving against a write that did not land"
      fi
    fi

    # Carried on the record itself rather than in a parallel array, so pass two
    # reads one thing and a crash between the passes leaves no orphan state to
    # reconcile.
    #
    # With no backlog configured there is nothing to track into: the thread
    # staying open is the configuration, not a failure, so the field is left
    # off rather than recorded empty. An empty value asserts a landing that was
    # attempted and did not happen — the loop-end decision reads exactly that.
    if [[ "$CTX_BACKLOG" != "none" ]]; then
      resolutions="$(jq -c --arg id "$id" --arg t "$tracked" \
        'map(if .finding_id == $id then . + {crossrev_tracked: $t} else . end)' <<<"$resolutions")"
    fi
  done
  run_checkpoint

  # --- the commit, then its SHA -------------------------------------------
  #
  # Guarded on more than fixes. A pass that defers everything and fixes nothing
  # still has a repository backlog write sitting in the tree, and a `fixed_count > 0`
  # test left exactly that case uncommitted.
  local commit_sha fixed_count commit_msg
  commit_sha="$(jq -r '.commit_sha // ""' <<<"$marker")"
  fixed_count="$(jq '[.[] | select(.resolution == "fixed")] | length' <<<"$resolutions")"
  if [[ -n "$commit_sha" && "$commit_sha" != "null" ]]; then
    ui_say "The previous attempt already pushed ${commit_sha:0:7}, so the fix step is skipped."
  elif (( fixed_count > 0 || backlog_wrote )); then
    commit_sha=""
    local subject
    if (( fixed_count > 0 )); then
      # The model's subject, or the generic one. A subject that fails validation
      # is reported rather than swallowed: the commit still lands, and the run
      # says the message is not the one the resolver wrote.
      subject="$(jq -r '.commit_subject // ""' <<<"$marker")"
      if ! _commit_subject_ok "$subject" "$marker"; then
        [[ -z "$subject" || "$subject" == "null" ]] || ui_warn \
          "the resolver's commit subject was rejected, so the commit carries a generic one" \
          "A subject must be one line of at most 100 characters, with no control characters. The fix itself is unaffected."
        subject="fix: resolve crossrev review findings (pass $pass)"
      fi
      commit_msg="$subject

$(_commit_body "$resolutions" "$findings" fixed "$CTX_HEAD_SHA" "$pass")"
    else
      # A deferral-only pass keeps a fixed subject. "Record deferred findings" is
      # already an accurate description of what happened — no code changed — so a
      # second piece of model-authored text would widen what has to be validated
      # and say nothing new.
      commit_msg="chore: record deferred crossrev findings (pass $pass)

$(_commit_body "$resolutions" "$findings" deferred "$CTX_HEAD_SHA" "$pass")"
    fi
    commit_sha="$(gh_commit_and_push "$CTX_HEAD_BRANCH" "$commit_msg" "$CTX_HEAD_SHA" "$push_remote" "$CTX_HEAD_REPO")"
    if [[ -n "$commit_sha" ]]; then
      ui_ok "pushed ${commit_sha:0:7} to $CTX_HEAD_BRANCH"
      # Recorded immediately after the push, because the window between the two
      # is the one crash boundary comments cannot dedupe away.
      marker="$(jq -c --arg s "$commit_sha" '.commit_sha = $s' <<<"$marker")"
      gh_comment_edit "$CTX_REPO" "$comment_id" \
"**crossrev — resolving $(_pass_label "$pass" "$CTX_MAX_PASSES_PER_CYCLE")**

Pushed \`${commit_sha:0:7}\`; replying to each thread now.$(state_marker_encode "$(jq -c 'del(.comment_id)' <<<"$marker")")"
    elif (( fixed_count > 0 )); then
      # Only meaningful when fixes were claimed. A deferral-only pass whose backlog
      # write produced no diff is not a broken promise about the code.
      ui_warn "the resolver reported $fixed_count fix(es) but changed no files" \
        "The replies below will claim a fix that is not in the diff, so their threads stay open and the pass halts for a person. Treat those resolutions as unverified and read the thread before merging."
    fi
  fi
  run_checkpoint

  # --- pass two: reply and resolve ----------------------------------------
  #
  # `unthreaded` counts the replies that did not land under the finding they
  # answer. The top-level fallback exists for a real case — an inline comment
  # GitHub refused to anchor, so there is no thread to reply to — and losing it
  # would be worse than keeping it. What is not acceptable is that it used to
  # change where a reply landed without changing anything the run said about it,
  # so a pass that threaded none of its replies read exactly like one that
  # threaded all of them. A fallback may change how something lands and may never
  # change whether the run says it landed normally.
  local already resolved_n=0 escalated=0 unthreaded=0
  local thread_id root_id should_resolve reply_body unthreaded_already
  already="$(state_posted_finding_ids "$CTX_PR" "$CTX_REPO" "$CTX_AUTHOR" resolve)" || already=""

  # A re-driven pass answers its own findings again: the replies already on
  # those threads record the attempt that declined to decide, and a thread this
  # pass resolves cannot be left with that as its last word. The dedupe still
  # holds for every other pass — and for this pass's own replies on a resume,
  # which arrives here through the claim path with `redrive` empty.
  if [[ -n "$redrive" ]]; then
    already="$(grep -vxF -f <(jq -r '.[].id' <<<"$findings") <<<"$already" || true)"
  fi

  # Seeded from the pull request rather than from zero, because a run that
  # stopped between a fallback reply and the summary comment comes back with
  # that reply already posted — `already` skips it below, so the loop never
  # counts it, and the summary would report a clean pass over a degraded one.
  # The reply's own marker is the record, and it is only ever an issue comment
  # when the thread reply is what failed.
  unthreaded_already="$(state_unthreaded_finding_ids \
    "$CTX_PR" "$CTX_REPO" "$CTX_AUTHOR" resolve "$pass")" || unthreaded_already=""
  [[ -n "$unthreaded_already" ]] && \
    unthreaded="$(printf '%s\n' "$unthreaded_already" | wc -l | tr -d ' ')"

  for (( i = 0; i < n; i++ )); do
    d="$(jq -c ".[$i]" <<<"$resolutions")"
    id="$(jq -r .finding_id <<<"$d")"
    disp="$(jq -r .resolution <<<"$d")"
    tracked="$(jq -r '.crossrev_tracked // ""' <<<"$d")"

    thread_id="$(jq -r --arg id "$id" '[.[] | select(.finding_ids | index($id))] | first | .id // ""' <<<"$threads")"
    root_id="$(jq -r --arg id "$id" '[.[] | select(.finding_ids | index($id))] | first | .root_comment_id // ""' <<<"$threads")"

    # The reply carries its own finding marker, which is what stops a retry
    # replying twice.
    if ! grep -qx -- "$id" <<<"$already"; then
      reply_body="$(_resolve_reply_body "$d" "$tracked" "$pass" "$harness" "$model")"
      if [[ -n "$root_id" && "$root_id" != "null" ]]; then
        # gh_review_reply says why it failed; what it cannot do is make the run
        # account for it, so the `|| true` that used to discard the outcome
        # counts it instead and the reply is posted where it will at least be
        # read.
        if ! gh_review_reply "$CTX_REPO" "$CTX_PR" "$root_id" "$reply_body"; then
          unthreaded=$(( unthreaded + 1 ))
          gh_comment_create "$CTX_REPO" "$CTX_PR" "$reply_body" >/dev/null
        fi
      else
        unthreaded=$(( unthreaded + 1 ))
        ui_warn "no review thread was found for finding $id, so its reply is a top-level comment" \
          "The reply is on the pull request rather than under the code it answers. This is expected when GitHub refused to anchor the original inline comment, and unexpected otherwise."
        gh_comment_create "$CTX_REPO" "$CTX_PR" "$reply_body" >/dev/null
      fi
      run_checkpoint
    fi

    # Resolution, per the resolution's own rule. `deferred` resolves only once
    # persisted; with nothing persisted the thread stays open, which is the
    # honest behaviour — resolving a thread whose content lands nowhere is how
    # work disappears. `fixed` is the same rule read against the commit: with
    # nothing pushed, the reply claims a change the diff does not carry, and a
    # resolved thread puts a green tick over exactly the defect the reviewer
    # raised. The pass halts on the same fact, so the thread is what a person
    # reads when they come to see why.
    should_resolve=0
    case "$disp" in
      fixed)            [[ -n "$commit_sha" ]] && should_resolve=1 ;;
      skipped|disputed) should_resolve=1 ;;
      deferred)         [[ -n "$tracked" ]] && should_resolve=1 ;;
      escalated)        escalated=$(( escalated + 1 )) ;;
    esac
    if (( should_resolve )) && [[ -n "$thread_id" && "$thread_id" != "null" ]]; then
      gh_thread_resolve "$thread_id" && resolved_n=$(( resolved_n + 1 )) || true
    fi

    # Fill the resolution into the review leg's marker. One record per finding
    # rather than two that can disagree.
    findings="$(jq -c --arg id "$id" --arg disp "$disp" --arg tracked "$tracked" '
      map(if .id == $id
          then .resolution = $disp
               | .tracked_as = (if $tracked == "" then null else $tracked end)
          else . end)' <<<"$findings")"
    run_checkpoint
  done

  (( filed > 0 ))      && ui_ok "filed $filed issue(s) for deferred work"
  (( matched > 0 ))    && ui_say "$matched deferred finding(s) already had an issue, so nothing was filed for them."
  (( resolved_n > 0 )) && ui_ok "resolved $resolved_n thread(s)"
  if (( unthreaded > 0 )); then
    local reply_noun="replies"; (( unthreaded == 1 )) && reply_noun="reply"
    ui_warn "$unthreaded $reply_noun could not be threaded and landed as top-level comments" \
      "Each one names the finding it answers, so nothing is lost, but a reader following the diff will not see it beside the code."
  fi

  # The review marker is edited rather than copied, so the finding list and its
  # resolutions cannot drift apart.
  local review_comment_id updated_review
  review_comment_id="$(jq -r '.comment_id' <<<"$review_marker")"
  updated_review="$(jq -c --argjson f "$findings" 'del(.comment_id) | .findings = $f' <<<"$review_marker")"
  gh_comment_edit "$CTX_REPO" "$review_comment_id" \
    "$(_review_summary_body "$findings" "$review_marker")$(state_marker_encode "$updated_review")"
  run_checkpoint

  # --- the summary comment, then completion --------------------------------------
  #
  # The un-threaded count goes onto the marker rather than being passed as an
  # argument, for the same reason everything else the comment reports about the
  # run does: the marker is what the comment is re-rendered from, so a fact that
  # lives only in a local disappears on recovery.
  local summary_body
  # The resolutions go back onto the marker with the tracking each deferral
  # earned, because the marker is the record status reads the pass's ending
  # from — a deferral whose record never landed has to be visible there, or
  # the marker copy of the label decision cannot agree with the label.
  marker="$(jq -c --argjson t "$(date +%s)" --argjson u "$unthreaded" --argjson d "$resolutions" \
    '.done_ts = $t | .unthreaded = $u | .resolutions = $d' <<<"$marker")"
  summary_body="$(_resolve_summary_body "$resolutions" "$findings" "$deferred_lines" "$marker")"
  gh_comment_edit "$CTX_REPO" "$comment_id" \
    "$summary_body$(state_marker_encode "$(jq -c 'del(.comment_id)' <<<"$marker")")"
  ui_ok "posted a summary comment"
  run_checkpoint

  marker="$(jq -c '.state = "complete"' <<<"$marker")"
  gh_comment_edit "$CTX_REPO" "$comment_id" \
    "$summary_body$(state_marker_encode "$(jq -c 'del(.comment_id)' <<<"$marker")")"

  local next other_escalated
  # Escalations in OTHER passes stand too — the halt outlives the pass that
  # caused it. This pass's own marker is re-read below rather than trusted
  # from the load the leg started with, because a re-drive rewrites it.
  other_escalated="$(jq --argjson p "$pass" \
    '[.[] | select(.leg == "resolve" and .pass != $p) | (.resolutions // [])[] | select(.resolution == "escalated")] | length' \
    <<<"$CTX_MARKERS")"
  next="$(legs_resolve_pass_label "$marker" "$other_escalated")"
  run_pass_labels "$pass" "$next"
  if (( escalated > 0 )); then
    run_label_add "crossrev/stop"
    ui_say "$escalated finding(s) need a human decision, so crossrev/stop is applied and the loop halts."
  fi

  if [[ "$blocked" == "true" ]]; then
    printf '  → blocked: %s\n\n' "$blocked_reason"
  else
    printf '  → resolved pass %s\n\n' "$pass"
    if [[ "$next" == "awaiting-review" ]]; then
      ui_say "To look again with the reviewer:"
      ui_say "  crossrev review --pr $CTX_PR"
      printf '\n'
    elif [[ "$next" == "converged" ]]; then
      ui_say "Every finding settled without a change to the code, so the loop is done."
      printf '\n'
    elif [[ "$next" == "halted" ]] && legs_resolve_unpushed_fix "$marker"; then
      ui_say "A claimed fix reached no commit, so its thread stays open and the pass halts."
      printf '\n'
    fi
  fi
  if (( no_tips == 0 )) && [[ "$next" != "awaiting-review" ]]; then run_upgrade_nudge; fi

  cd "$orig_cwd" || true
  git worktree remove --force "$wt_dir" 2>/dev/null || rm -rf "$wt_dir"
  # One level, not `rmdir -p`. The `-p` form walks up removing every parent that
  # becomes empty, and stops only at the first non-empty one - which on a machine
  # where ~/.local holds nothing else means CrossRev deletes ~/.local/state and
  # ~/.local. Four of those levels are ours to remove and two are not.
  rmdir "$(dirname "$wt_dir")" 2>/dev/null || true
  CROSSREV_WORKTREE=""
  return 0
}

# Tier 2 dedupe, the retrieval half: the orchestrator searches, the model judges.
#
# Keyed on the file path, since an issue about a bug in a file almost always names
# it. Capped, and the cap is announced rather than silent — a truncated candidate
# set that reads as "nothing matched" is how a duplicate gets filed.
_resolve_dedupe_candidates() {
  local findings="$1" out="{}" searched=0 limit=10 n i f cand
  [[ "$CTX_BACKLOG" == "github_issues" ]] || { printf '%s' "$out"; return 0; }

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

# File one deferred defect to the resolved backlog. Prints where it landed, or
# nothing when the write did not happen.
_resolve_persist() {
  local d="$1" id="$2" pass="$3" persist title body dir target n
  persist="$(jq -c '.persist // null' <<<"$d")"
  [[ "$persist" != "null" ]] || return 1

  title="$(jq -r .title <<<"$persist")"
  body="$(jq -r .body <<<"$persist")

---
Found by crossrev while reviewing $CTX_REPO#$CTX_PR (pass $pass). Verified against the codebase before filing: one model raised it, a second confirmed it is real, and it was left out of that pull request deliberately rather than missed.$(state_finding_marker "$id" "$pass" resolve)"

  case "$CTX_BACKLOG" in
    github_issues)
      n="$(gh_issue_create "$CTX_REPO" "$title" "$body" "$CTX_TRACKING_LABEL $CTX_BACKLOG_LABELS")" || return 1
      [[ -n "$n" ]] || return 1
      printf '%s#%s' "$CTX_REPO" "$n" ;;
    repository)
      dir="$CTX_BACKLOG_PATH"
      cfg_assert_path_inside_repo "$dir"
      if [[ "$CTX_BACKLOG_LAYOUT" == "file" ]]; then
        # An existing markdown convention is appended to, because that is what
        # the convention is. The parent is created first: the fallback for a
        # stated `layout: file` with no path is `.crossrev/backlog.md`, whose
        # directory normally does not exist, and an append into a missing
        # directory fails without stopping the function. The path was printed
        # anyway, so the caller counted the finding tracked and resolved its
        # thread against a write that never landed — which is the one failure
        # persisting before resolving exists to prevent.
        mkdir -p "$(dirname "$dir")" || return 1
        printf '\n## %s\n\n%s\n' "$title" "$body" >>"$dir" || return 1
        printf '%s' "$dir"
      else
        # One file per finding, never an append: two concurrent pull requests
        # appending to one markdown file conflict on merge every time.
        mkdir -p "$dir" || return 1
        target="$dir/$id.md"
        printf '# %s\n\n%s\n' "$title" "$body" >"$target" || return 1
        printf '%s' "$target"
      fi ;;
    *)
      return 1 ;;
  esac
}

# Strip an opening the model wrote for itself.
#
# The reply's lead is presentation, so it belongs to the orchestrator — but
# nothing said so, and the resolve prompt hands the model "the conversation so
# far", which contains earlier replies opening with exactly these words. So the
# model reads the house style off the pull request, reproduces it, and crossrev
# prefixes its own on top: "**Fixed.** **Fixed.** Confirmed, and …".
#
# Three details it has to get right. The trailing period is required, or a reply
# legitimately beginning "Fixed the comparison" loses its first word. It repeats,
# because one pass over "**Fixed.** **Fixed.**" leaves a second copy for crossrev
# to double again. And it touches the first line only — `^` in sed anchors per
# line, so an unrestricted pass would eat a "Deferred." that opens a paragraph
# three screens down.
#
# It also catches the model opening with the WRONG word. The resolution is the
# orchestrator's own answer, so a reply that leads "Fixed." on a skipped finding
# has its lead replaced rather than stacked.
_strip_resolution_lead() {
  local text="$1" head rest prev
  head="${text%%$'\n'*}"
  rest=""
  [[ "$head" != "$text" ]] && rest="${text#*$'\n'}"
  prev=""
  while [[ "$head" != "$prev" ]]; do
    prev="$head"
    head="$(printf '%s' "$head" | sed -E 's/^[[:space:]]*(\*\*)?(Fixed|Skipped|Deferred|Not changing this|This needs a human decision)\.(\*\*)?[[:space:]]*//')"
  done
  if [[ -n "$rest" ]]; then printf '%s\n%s' "$head" "$rest"; else printf '%s' "$head"; fi
}

_resolve_reply_body() {
  local d="$1" tracked="$2" pass="$3" harness="$4" model="$5" disp lead
  disp="$(jq -r .resolution <<<"$d")"
  case "$disp" in
    fixed)     lead="**Fixed.**" ;;
    skipped)   lead="**Skipped.**" ;;
    deferred)  lead="**Deferred.**" ;;
    disputed)  lead="**Not changing this.**" ;;
    escalated) lead="**This needs a human decision.**" ;;
    *)         lead="**$disp.**" ;;
  esac
  printf '%s %s\n' "$lead" "$(_strip_resolution_lead "$(jq -r .reply <<<"$d")")"
  [[ -n "$tracked" ]] && printf '\nTracked outside this pull request as %s, so it survives the merge.\n' "$tracked"
  printf '\n<sub>crossrev pass %s, verified by %s%s. Every finding is verified whatever its severity — severity governs what happens afterwards, not whether the check happens.</sub>%s' \
    "$pass" "$harness" "${model:+ ($model)}" \
    "$(state_finding_marker "$(jq -r .finding_id <<<"$d")" "$pass" resolve)"
}

# "3 fixed, 1 skipped, 1 deferred" — only the resolutions that actually
# happened, in the order the five are defined, so a pass that fixed everything
# does not read as four zeroes.
_resolution_counts() {
  jq -r '
    ["fixed","skipped","deferred","disputed","escalated"] as $order
    | (group_by(.resolution) | map({key: .[0].resolution, value: length}) | from_entries) as $by
    | [ $order[] | select($by[.] != null) | "\($by[.]) \(.)" ]
    | if length == 0 then "Nothing to resolution."
      else join(", ") + "." end' <<<"$1"
}

# The resolution table, with the severity emoji on each finding.
#
# The finding is named by its title rather than by its id, because the id means
# nothing to a collaborator reading the pull request — but the id is what the
# resolutions are keyed on, so it stays in the cell, small, for anyone matching
# a row against a thread. The reasoning is deliberately not a column: the only
# text crossrev holds is the model's full reply, which belongs in the thread it
# was written for and would not survive a table cell.
_resolutions_table() {
  local resolutions="$1" findings="$2" sha="$3" n i d id f sev title
  n="$(jq 'length' <<<"$resolutions")"
  printf '| Severity | Finding | Location | Resolution |\n|---|---|---|---|\n'
  for (( i = 0; i < n; i++ )); do
    d="$(jq -c ".[$i]" <<<"$resolutions")"
    id="$(jq -r .finding_id <<<"$d")"
    f="$(jq -c --arg id "$id" 'map(select(.id == $id)) | first // {}' <<<"$findings")"
    sev="$(jq -r '.severity // "?"' <<<"$f")"
    title="$(jq -r '.title // ""' <<<"$f")"
    # The id appears here and nowhere else, and only when the review record has
    # no finding under it. That is a broken state rather than a normal one, and
    # the id is then the single handle anyone debugging it has.
    [[ -n "$title" ]] || title="finding \`$id\` is not in the review record"
    printf '| %s&nbsp;%s | %s | %s | %s |\n' \
      "$(run_severity_emoji "$sev")" "$(_ucfirst "$sev")" \
      "$(_md_cell "$title")" \
      "$(_finding_location "$f" "$sha")" \
      "$(jq -r .resolution <<<"$d")"
  done
  printf '\n'
}

# The subject line of a resolve leg's commit, or nothing.
#
# The model writes it, because only the model knows what the change did. That
# makes it the one piece of model-authored text that becomes permanent history,
# so it is validated rather than trusted, and every rule below rejects instead of
# repairing: a silently repaired subject is a subject nobody chose.
#
# A rejected subject is not fatal. The caller falls back to the generic message
# and says so — a bad commit subject is not worth failing a pass that fixed real
# defects.
_commit_subject_ok() {
  local s="$1" raw_json="${2:-}"
  [[ -n "$s" && "$s" != "null" ]] || return 1
  # If the marker or raw JSON was passed, check the JSON-encoded subject directly
  # before command substitution stripped NUL bytes.
  if [[ -n "$raw_json" ]]; then
    if jq -e '.commit_subject | if . == null then false else test("[\u0000-\u001f\u007f]") end' <<<"$raw_json" >/dev/null 2>&1; then
      return 1
    fi
  fi
  # A newline would turn the rest into a body the orchestrator did not compose.
  [[ "$s" != *$'\n'* ]] || return 1
  # Control characters reach `git log` and every terminal that renders it. DEL
  # is named beside the C0 range rather than left out of it: it sits above the
  # range at 0x7f, and `_commit_line` below already strips it from a body line.
  # A subject git records forever is no better a place for it than a body is.
  [[ "$s" != *[$'\x01'-$'\x1f'$'\x7f']* ]] || return 1
  # A safety net above every convention's own limit rather than a convention of
  # its own: the repository's subjects are what the model is told to match.
  (( ${#s} <= 100 )) || return 1
  # Marker prefixes are matched literally wherever crossrev reads its own state.
  [[ "$s" != *"$CROSSREV_MARKER_PREFIX"* ]] || return 1
  return 0
}

# Model-written text on its way into a commit message, flattened to one line.
#
# The body is the orchestrator's, but the titles and paths in it are the review
# leg's, and nothing upstream holds them to a line: the schema asks for one and a
# model returns what it returns. Raw, a newline in a title continues the body
# with lines nobody composed — including a second `Crossrev-pr:` trailer, which
# every tool that reads these commits parses as crossrev's own — and a control
# character reaches `git log` and every terminal that renders it, permanently.
#
# Flattened rather than rejected, unlike the subject beside it. The subject is a
# whole line the model chose, so repairing one publishes a subject nobody wrote;
# a title is a fragment the orchestrator is quoting, and the honest repair is to
# quote it on the one line it was asked for.
_commit_line() {
  printf '%s' "$1" | tr '\000-\037\177' ' '
}

# The body of a resolve leg's commit: what was settled, where, and how to reach
# the conversation that settled it.
#
# Composed by the orchestrator rather than by the model. It already holds the
# titles and the locations, so asking for text it would then have to validate
# buys nothing — and the reasoning belongs in the thread, which the body links.
_commit_body() {
  local resolutions="$1" findings="$2" want="$3" sha="$4" pass="$5"
  local n i r id f title path line root
  n="$(jq 'length' <<<"$resolutions")"
  for (( i = 0; i < n; i++ )); do
    r="$(jq -c ".[$i]" <<<"$resolutions")"
    [[ "$(jq -r .resolution <<<"$r")" == "$want" ]] || continue
    id="$(jq -r .finding_id <<<"$r")"
    f="$(jq -c --arg id "$id" 'map(select(.id == $id)) | first // {}' <<<"$findings")"
    title="$(jq -r '.title // ""' <<<"$f")"
    path="$(jq -r '.path // ""' <<<"$f")"
    line="$(jq -r '.line // ""' <<<"$f")"
    root="$(jq -r '.root_comment_id // ""' <<<"$f")"
    printf -- '- %s\n' "$(_commit_line "${title:-$id}")"
    [[ -n "$path" && "$path" != "null" ]] || continue
    # The path and the line go through it too. Both are the same model's text on
    # the same output line, and sanitising one of the three would be arbitrary.
    path="$(_commit_line "$path")"; line="$(_commit_line "$line")"
    if [[ -n "$root" && "$root" != "null" ]]; then
      printf '  %s:%s - %s\n' "$path" "$line" "$(_thread_url "$root")"
    else
      printf '  %s:%s - %s\n' "$path" "$line" "$(_blob_url "$sha" "$path" "$line")"
    fi
  done
  # Trailers rather than a sentence, so `git log --grep` and any tool that parses
  # them can read what the subject no longer says. The committer identity already
  # marks these as crossrev's; these say which pull request and which pass.
  printf '\nCrossrev-pr: %s#%s\nCrossrev-pass: %s\n' "$CTX_REPO" "$CTX_PR" "$pass"
}

# The resolve leg's summary comment. Takes the marker for the same reason the
# review leg's does — everything it reports about the run lives there.
_resolve_summary_body() {
  local resolutions="$1" findings="$2" deferred_lines="$3" marker="$4"
  local summary pass commit blocked blocked_reason
  summary="$(jq -r '.summary // ""' <<<"$marker")"
  pass="$(jq -r '.pass // 1' <<<"$marker")"
  commit="$(jq -r '.commit_sha // ""' <<<"$marker")"
  blocked="$(jq -r '.blocked // false' <<<"$marker")"
  blocked_reason="$(jq -r '.blocked_reason // ""' <<<"$marker")"

  printf '## crossrev resolved %s\n\n' "$(_pass_label "$pass" "$CTX_MAX_PASSES_PER_CYCLE")"

  local counts escalated noun
  counts="$(_resolution_counts "$resolutions")"
  escalated="$(jq '[.[] | select(.resolution == "escalated")] | length' <<<"$resolutions")"
  noun="findings"; (( escalated == 1 )) && noun="finding"
  if [[ "$blocked" == "true" ]]; then
    _alert WARNING "$(printf '**Blocked:** %s The loop halts here and needs a human. %s' \
      "$blocked_reason" "$counts")"
  elif (( escalated > 0 )); then
    _alert WARNING "$(printf '**%s %s need a human decision.** `crossrev/stop` is applied, so the loop halts until somebody removes it. %s' \
      "$escalated" "$noun" "$counts")"
  else
    _alert NOTE "$(printf '**%s** Every finding was verified whatever its severity — severity governs what happens afterwards, not whether the check happens.' "$counts")"
  fi

  if [[ -n "$commit" && "$commit" != "null" ]]; then
    printf 'Fixes pushed as `%s`.\n\n' "${commit:0:7}"
  else
    printf 'No code changed this pass.\n\n'
  fi

  # A degraded reply is recorded here rather than only in the run's own output,
  # because the pull request is what anyone reads afterwards and the run's output
  # is gone. Silence here is what made the same failure invisible on PR 5.
  local unthreaded
  unthreaded="$(jq -r '.unthreaded // 0' <<<"$marker")"
  if (( unthreaded == 1 )); then
    printf 'One reply could not be posted in the review thread it answers, so it is a top-level comment on this pull request naming its finding instead.\n\n'
  elif (( unthreaded > 1 )); then
    printf '%s replies could not be posted in the review threads they answer, so they are top-level comments on this pull request naming their findings instead.\n\n' "$unthreaded"
  fi

  printf '%s\n\n' "$summary"

  _resolutions_table "$resolutions" "$findings" "$(jq -r '.head_sha // ""' <<<"$marker")"

  if [[ -n "$deferred_lines" ]]; then
    printf '## Deferred work filed\n'
    printf '%s\n\n' "$deferred_lines"
    printf 'An unresolved thread on a merged pull request is visible in no GitHub view, which is why a deferred finding goes somewhere durable before its thread is resolved.\n\n'
  fi

  _run_details "$marker" resolve
}

# ---------------------------------------------------------------------------
# The drivers
# ---------------------------------------------------------------------------

# At the pass bound, finish the pass already under way, then stop.
#
# The bound stops another pass from *starting*; it must not strand one that has
# already begun. A cycle interrupted between the two legs of the last allowed
# pass would otherwise be unresumable: the restart reads the pass number, finds
# it at the bound and exits 0, leaving that pass's findings unresolved and its
# threads open — a halt that reads as a clean finish, which is the shape of
# failure this loop exists to avoid.
#
# Runs at most one leg. Nothing here starts a pass: the review leg is invoked
# only to resume an unfinished claim for the pass already at the bound.
#
# $1 the current review pass, $2 the bound, then the arguments for the legs.
_cycle_finish_at_bound() {
  local pass="$1" max="$2"; shift 2
  local marker state verdict actionable

  marker="$(state_marker_for "$CTX_MARKERS" "$pass" review)"
  state="$(jq -r '.state // ""' <<<"$marker")"
  if [[ "$state" != "complete" ]]; then
    ui_say "Pass $pass is unfinished, and max_passes_per_cycle ($max) starts no further pass. Resuming its review."
    # Whether the bound applies turns on which kind of unfinished this is, and
    # the marker's own state answers it.
    #
    # `started` is an open claim, and the review leg resumes an open claim at its
    # existing pass number rather than beginning another — `state_open_claim`
    # reads the same marker this did, so the two cannot disagree. The bound must
    # not be applied to that: a person may legitimately have started pass 4 under
    # a bound of 3, which is exactly what `crossrev review --pr N` typed by hand
    # does, and --continuation would refuse the resume, write a declined marker
    # over a pass that is mid-flight, and exit clean with the review unfinished.
    #
    # Anything else — a declined marker, or none — leaves the leg to compute a
    # pass number of its own, and the bound is the only thing stopping it
    # starting one. That case keeps the flag.
    if [[ "$state" == "started" ]]; then
      leg_review "$@" --no-tips || return 1
    else
      leg_review "$@" --continuation --no-tips || return 1
    fi
    ctx_load "$CTX_PR" "$CTX_REPO"
    pass="$(state_current_review_pass "$CTX_MARKERS")"
    marker="$(state_marker_for "$CTX_MARKERS" "$pass" review)"
    if [[ "$(jq -r '.state // ""' <<<"$marker")" != "complete" ]]; then
      ui_end "Stopped on pass $pass of $CTX_REPO#$CTX_PR — the review leg did not finish, so no resolve leg follows it."
      return 0
    fi
  fi

  verdict="$(jq -r '.verdict // "blocked"' <<<"$marker")"
  actionable="$(run_actionable "$(jq -c '.findings // []' <<<"$marker")")"
  if [[ "$verdict" == "blocked" ]]; then
    ui_end "Halted after pass $pass — the reviewer could not complete."
    return 0
  fi
  if [[ "$verdict" == "converged" ]] || (( actionable == 0 )); then
    # The review leg already wrote the label this message reports; the two
    # have to agree, so an empty pass while an escalation stands is not read
    # out as a convergence.
    if [[ "$verdict" != "converged" ]] && (( $(_markers_escalated "$CTX_MARKERS") > 0 )); then
      ui_end "Halted after pass $pass — the pass raised nothing new, and the escalated findings still need a human decision."
    else
      ui_end "Converged after pass $pass — nothing at or above min_fix_severity ($CTX_MIN_FIX_SEVERITY) remains."
    fi
    return 0
  fi
  if state_current_pass_complete "$CTX_MARKERS" "$pass" resolve; then
    ui_end "Reached max_passes_per_cycle ($max) on $CTX_REPO#$CTX_PR without starting another pass."
    return 0
  fi

  ui_say "Pass $pass is unfinished, and max_passes_per_cycle ($max) starts no further pass. Its resolve leg is still owed."
  leg_resolve "$@" --no-tips || return 1

  # The same outcomes the loop reports after a resolve leg, so the last line an
  # operator reads describes what actually happened rather than assuming the
  # bound was the reason it stopped.
  local rmarker rlabel
  ctx_load "$CTX_PR" "$CTX_REPO"
  rmarker="$(state_marker_for "$CTX_MARKERS" "$pass" resolve)"
  if [[ "$(jq -r '.blocked // false' <<<"$rmarker")" == "true" ]]; then
    ui_end "Halted after pass $pass — the resolver reported blocked."
    return 0
  fi
  if grep -qw "crossrev/stop" <<<"$CTX_LABELS"; then
    ui_end "Halted after pass $pass — a point needs a human decision, so crossrev/stop is applied."
    return 0
  fi
  # How the resolve pass ended outranks the bound, because the bound is a
  # statement about passes that will not start and this pass has a terminal
  # state of its own. Reported as the cap instead, a settle reads as a failure
  # to converge, and a deferral nobody filed or a fix that reached no commit
  # reads as a pass that finished cleanly.
  rlabel=""
  if [[ -n "$rmarker" && "$(jq -r '.state // ""' <<<"$rmarker")" == "complete" ]]; then
    rlabel="$(legs_resolve_pass_label "$rmarker" "$(_markers_escalated "$CTX_MARKERS")")"
  fi
  if [[ "$rlabel" == "converged" ]]; then
    ui_end "Converged after pass $pass — nothing at or above min_fix_severity ($CTX_MIN_FIX_SEVERITY) remains."
    return 0
  fi
  if [[ "$rlabel" == "halted" ]]; then
    ui_end "Halted after pass $pass — the resolve leg left something a person has to settle. \`crossrev status --pr $CTX_PR\` says what."
    return 0
  fi
  ui_end "Reached max_passes_per_cycle ($max) on $CTX_REPO#$CTX_PR — pass $pass is resolved, and no further pass starts."
  return 0
}

# local mode: one process calls the legs in sequence. Because state lives on
# the pull request rather than in process memory, this is a thin driver over the
# same legs the workflows invoke, and the two modes can be A/B tested on real
# pull requests without touching leg code.
cmd_cycle() {
  local pr="" repo="" trigger="human" no_tips=0
  local -a args=()
  while (( $# )); do
    case "$1" in
      --pr)      pr="${2:-}"; args+=(--pr "${2:-}"); shift 2 ;;
      --repo)    repo="${2:-}"; args+=(--repo "${2:-}"); shift 2 ;;
      --harness) args+=(--harness "${2:-}"); shift 2 ;;
      --trigger) trigger="${2:-}"; args+=(--trigger "${2:-}"); shift 2 ;;
      --no-tips) no_tips=1; shift ;;
      *) ui_die "unknown option for cycle: $1" \
           "Usage: crossrev cycle --pr <number> [--trigger human|automatic] [--no-tips]" ;;
    esac
  done
  [[ -n "$pr" ]] || ui_die "crossrev cycle needs a pull request number" "Usage: crossrev cycle --pr 42"
  # Checked here as well as in the legs, because ctx_load below reads it first
  # and anything it does not recognise it treats as human.
  case "$trigger" in
    human|automatic) ;;
    *) ui_die "unknown cycle trigger: $trigger" "Use --trigger human or --trigger automatic." ;;
  esac

  # The draft rule is applied once, here, rather than left to the first leg: a
  # cycle that declined mid-loop would report the reviewer as unable to finish.
  local load_rc
  ctx_load "$pr" "$repo" "$trigger" || {
    load_rc=$?
    (( load_rc == 2 )) && return 0
    return "$load_rc"
  }
  local max="$CTX_MAX_PASSES_PER_CYCLE" i pass marker rmarker rlabel verdict actionable
  ui_say "Cycling $CTX_REPO#$CTX_PR, up to $max passes. Ctrl-C is safe — each leg finishes the write in flight."

  pass="$(state_current_review_pass "$CTX_MARKERS")"
  if (( pass >= max )); then
    _cycle_finish_at_bound "$pass" "$max" "${args[@]}" || return 1
    return 0
  fi

  for (( i = 1; i <= max; i++ )); do
    if (( i > 1 )); then
      ctx_load "$pr" "$repo"
      pass="$(state_current_review_pass "$CTX_MARKERS")"
      if (( pass >= max )); then
        ui_end "Reached max_passes_per_cycle ($max) on $CTX_REPO#$CTX_PR without starting another pass."
        return 0
      fi
      leg_review "${args[@]}" --continuation --no-tips || return 1
    else
      leg_review "${args[@]}" --no-tips || return 1
    fi

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
      # Same agreement the review leg's label keeps: an empty pass while an
      # escalation stands is a halt, not a convergence.
      if [[ "$verdict" != "converged" ]] && (( $(_markers_escalated "$CTX_MARKERS") > 0 )); then
        ui_end "Halted after pass $pass — the pass raised nothing new, and the escalated findings still need a human decision."
      else
        ui_end "Converged after pass $pass — nothing at or above min_fix_severity ($CTX_MIN_FIX_SEVERITY) remains."
      fi
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
    if grep -qw "crossrev/stop" <<<"$CTX_LABELS"; then
      ui_end "Halted after pass $pass — a point needs a human decision, so crossrev/stop is applied."
      return 0
    fi
    # The label the resolve leg just applied, read here by the same function it
    # wrote it with, so the terminal and the pull request cannot disagree about
    # how the pass ended. Read once: two calls are two chances to answer
    # differently.
    rlabel=""
    if [[ -n "$rmarker" && "$(jq -r '.state // ""' <<<"$rmarker")" == "complete" ]]; then
      rlabel="$(legs_resolve_pass_label "$rmarker" "$(_markers_escalated "$CTX_MARKERS")")"
    fi
    # A pass that settled every finding without pushing is done: the next
    # review would decline the unchanged head, so the loop stops here rather
    # than spinning declines until the cap and reporting a convergence as a
    # failure to converge.
    if [[ "$rlabel" == "converged" ]]; then
      ui_end "Converged after pass $pass — nothing at or above min_fix_severity ($CTX_MIN_FIX_SEVERITY) remains."
      (( no_tips )) || run_upgrade_nudge
      return 0
    fi
    # And a halt ends the cycle too. Blocked and escalated are caught above,
    # each by the thing that records them; the halts left here — a deferral
    # nobody filed, a fix that reached no commit — apply no `crossrev/stop`,
    # because nobody pulled the brake. Without this the driver reads a pass the
    # resolve leg labelled halted, starts another one anyway, and re-drives the
    # resolver over work that is waiting on a person.
    if [[ "$rlabel" == "halted" ]]; then
      ui_end "Halted after pass $pass — the resolve leg left something a person has to settle. \`crossrev status --pr $CTX_PR\` says what."
      return 0
    fi
  done

  ui_end "Reached max_passes_per_cycle ($max) on $CTX_REPO#$CTX_PR without converging. Every finding and reply is on the pull request."
  (( no_tips )) || run_upgrade_nudge
  return 0
}

# Position, and what to do about it.
#
# Five sections, and the words in them are the words on the pull request. The
# header carries the state in one word — the only thing a reader needs when they
# are checking a dozen pull requests in a row — and that word is the loop label's
# own, so learning the labels on GitHub teaches the terminal and the other way
# round. There is one fewer place for the two to disagree because the header is
# read off the label rather than computed beside it.
#
# The state comes off the pull request, and only the liveness of an unfinished
# leg comes off the process — from the lock file locally and the Actions API in
# automated mode, never from the age. A leg the usage limit killed after forty
# seconds and a leg happily working for forty seconds leave identical state on
# the pull request, so an age-based "still running" would hand a dead loop a
# reassuring line; a pid that answers is a different kind of fact, and it is the
# only thing that puts "running now" on a row.
cmd_status() {
  local pr="" repo=""
  while (( $# )); do
    case "$1" in
      --pr)   pr="${2:-}"; shift 2 ;;
      --repo) repo="${2:-}"; shift 2 ;;
      # Accepted and ignored: the composite action forwards these to whichever
      # leg it was told to run, and status reads the pull request rather than
      # acting on it, so none of them has anything to change here.
      --harness) shift 2 ;;
      --trigger) shift 2 ;;
      --no-tips) shift ;;
      *) ui_die "unknown option for status: $1" "Usage: crossrev status --pr <number>" ;;
    esac
  done
  [[ -n "$pr" ]] || ui_die "crossrev status needs a pull request number" "Usage: crossrev status --pr 42"

  ctx_load "$pr" "$repo"

  local state kind pass max_pass
  state="$(_status_state)"
  case "$state" in
    converged)             kind="ok" ;;
    halted)                kind="warn" ;;
    stopped)               kind="bad" ;;
    *)                     kind="info" ;;
  esac
  local note=""
  grep -qw "crossrev/watchdog-retried" <<<"$CTX_LABELS" && note="(retried once)"
  ui_section_state "$CTX_REPO#$CTX_PR" "$state" "$kind" "$note"

  pass="$(state_current_review_pass "$CTX_MARKERS")"
  max_pass="$(state_max_pass "$CTX_MARKERS")"

  ui_gap
  ui_head "PULL REQUEST"
  ui_line "title      $CTX_TITLE"
  [[ -n "$CTX_URL" ]] && ui_line "url        $CTX_URL"
  ui_line "head       ${CTX_HEAD_SHA:0:7} on $CTX_HEAD_BRANCH, $CTX_CHANGED file(s)"
  ui_line "labels     ${CTX_LABELS:-none}"

  ui_gap
  ui_head "LOOP"
  ui_line "mode       $CTX_MODE, markers by $CTX_AUTHOR"
  if (( pass == 0 )); then
    ui_line "passes     none yet, up to $CTX_MAX_PASSES_PER_CYCLE"
  elif (( pass > CTX_MAX_PASSES_PER_CYCLE )); then
    ui_line "passes     $pass (past the cycle cap of $CTX_MAX_PASSES_PER_CYCLE)"
  else
    ui_line "passes     $pass of $CTX_MAX_PASSES_PER_CYCLE"
  fi
  ui_line "deferred   $CTX_BACKLOG${CTX_BACKLOG_LAYOUT:+ $CTX_BACKLOG_LAYOUT}${CTX_BACKLOG_PATH:+ $CTX_BACKLOG_PATH}"

  # Omitted entirely rather than printed with nothing under it. A heading with an
  # empty body reads as a bug, and the `passes` line above already says none yet.
  if (( max_pass > 0 )); then
    ui_gap
    ui_head "PASSES"
    local p
    for (( p = 1; p <= max_pass; p++ )); do
      _status_leg_row "$p" review  "$(printf '%-2s ' "$p")"
      _status_leg_row "$p" resolve "   "
    done
  fi

  ui_gap
  ui_head "NEXT"
  _status_next "$state" "$pass"
  ui_end "State is read from the pull request itself, so this is the same view a workflow gets."
}

# The header word, read off the loop labels rather than computed beside them.
#
# One to one with INIT_FIXED_LABELS, no exceptions, in the order a human would
# read them: a stop request outranks everything, then a halt, then convergence,
# then whichever leg is owed. Someone who learns the labels on GitHub already
# knows the terminal's words, and the header stops being computed independently
# of the label it duplicates.
_status_state() {
  if   grep -qw "crossrev/stop"                <<<"$CTX_LABELS"; then printf 'stopped'
  elif grep -qw "crossrev/halted"              <<<"$CTX_LABELS"; then printf 'halted'
  elif grep -qw "crossrev/converged"           <<<"$CTX_LABELS"; then printf 'converged'
  elif grep -qw "crossrev/awaiting-resolution" <<<"$CTX_LABELS"; then printf 'awaiting resolution'
  elif grep -qw "crossrev/awaiting-review"     <<<"$CTX_LABELS"; then printf 'awaiting review'
  else _status_state_from_markers
  fi
}

# No state label at all, which is not the same question as which one.
#
# Locally a label that will not apply is a warning rather than a fatal — one
# process drives both legs, so the chain does not depend on it — which means a
# repository that never ran `crossrev init` runs the loop perfectly well with no
# labels on it. Answering "awaiting review" there whenever a resolve leg is
# plainly owed would send the reader to the wrong command, so the markers answer
# instead. They say the same thing the labels would have; they are just the copy
# that is always written.
_status_state_from_markers() {
  local pass m_review m_resolve verdict
  pass="$(state_current_review_pass "$CTX_MARKERS")"
  (( pass > 0 )) || { printf 'awaiting review'; return 0; }

  # A pass a cap refused to start is a halt, and it is recorded one pass ahead of
  # the last one that ran.
  m_review="$(state_marker_for "$CTX_MARKERS" "$(( pass + 1 ))" review)"
  [[ -n "$m_review" && "$(jq -r '.state // ""' <<<"$m_review")" == "declined" ]] \
    && { printf 'halted'; return 0; }

  m_review="$(state_marker_for "$CTX_MARKERS" "$pass" review)"
  [[ "$(jq -r '.state // ""' <<<"$m_review")" == "complete" ]] \
    || { printf 'awaiting review'; return 0; }

  verdict="$(jq -r '.verdict // ""' <<<"$m_review")"
  case "$verdict" in
    converged) printf 'converged'; return 0 ;;
    blocked)   printf 'halted';    return 0 ;;
  esac

  m_resolve="$(state_marker_for "$CTX_MARKERS" "$pass" resolve)"
  if [[ "$(jq -r '.state // ""' <<<"$m_resolve")" != "complete" ]]; then
    # The marker copy of the label the review leg wrote (legs_pass_label), so
    # the two cannot disagree on a pull request with no labels to read: an
    # empty pass while an escalation stands is halted, not a resolve leg owed
    # — there is nothing for the leg to do.
    if (( $(run_actionable "$(jq -c '.findings // []' <<<"$m_review")") == 0 )) \
       && (( $(_markers_escalated "$CTX_MARKERS") > 0 )); then
      printf 'halted'
    else
      printf 'awaiting resolution'
    fi
  else
    # The marker copy of the label the resolve leg wrote
    # (legs_resolve_pass_label), read off the same marker by the same rule, so
    # the two cannot disagree on a pull request with no labels to read. A pass
    # that escalated is one of the halts inside it: reading only `blocked`
    # here would answer "awaiting review" on the path where the loop is
    # waiting on a person, and send the reader to start a pass that settles
    # nothing.
    case "$(legs_resolve_pass_label "$m_resolve" "$(_markers_escalated "$CTX_MARKERS")")" in
      halted)    printf 'halted' ;;
      converged) printf 'converged' ;;
      *)         printf 'awaiting review' ;;
    esac
  fi
}

# How many findings this resolve leg handed to a human. Empty marker counts as
# none, so callers can ask before they know one exists.
_status_escalated() {
  [[ -n "$1" ]] || { printf '0'; return 0; }
  jq '[(.resolutions // [])[] | select(.resolution == "escalated")] | length' <<<"$1"
}

# Escalated findings across every resolve marker on the pull request. Which
# pass handed one to a human stops mattering when a newer pass runs: the halt
# it caused is still standing, and only re-driving that pass — which rewrites
# its marker — or settling the thread by hand clears it.
_markers_escalated() {
  jq '[.[] | select(.leg == "resolve") | (.resolutions // [])[] | select(.resolution == "escalated")] | length' <<<"$1"
}

# One leg line: the glyph reflects the OUTCOME, not whether the leg ran.
#
# Green for a normal outcome, red for a bad one, a dim circle for a leg that
# never ran. Today's output prints a green tick for any leg that reached
# `complete` whatever its verdict, so a review that came back blocked looks
# identical to one that converged — green for "the reviewer gave up".
#
# The left column is always `review` or `resolve`, never a pseudo-leg standing in
# for a reason. The reason belongs in the description beside it, which is what
# lets a cap halt explain itself without inventing anything: caps are evaluated
# when a review leg decides whether the next pass may begin, so the refusal
# attaches to that review leg, which is literally what happened.
_status_leg_row() {
  local pass="$1" leg="$2" gutter="$3" m st label
  label="$(printf '%-9s' "$leg")"
  m="$(state_marker_for "$CTX_MARKERS" "$pass" "$leg")"

  if [[ -z "$m" ]]; then
    ui_row "$gutter" opt "$label$(_status_leg_absent "$pass" "$leg")"
    return 0
  fi

  st="$(jq -r '.state // ""' <<<"$m")"
  case "$st" in
    declined)
      ui_row "$gutter" no "$label""never started — $(jq -r '.reason // "a cap stopped it"' <<<"$m")" ;;
    complete)
      _status_leg_complete "$gutter" "$label" "$leg" "$m" ;;
    *)
      # An open claim is not an outcome, so the row says whether the process
      # behind it is still working — and says it only where the evidence is
      # nameable.
      #
      # This branch used to print "never finished" under a red cross whatever the
      # age, on the grounds that the marker posts before the harness is invoked
      # and survives the cleanup path, so a leg killed forty seconds in leaves
      # exactly what a leg working for forty seconds leaves. That is true of the
      # marker and false of the run: a local leg records its pid in the lock file
      # beside the git dir, an automated one carries its GITHUB_RUN_ID, and
      # `kill -0` and `gh run view` answer from those what the marker cannot. The
      # old line was making a liveness claim anyway — the negative one — and it
      # was wrong on every leg that was alive, which is the state a reader is
      # most likely to be looking at.
      #
      # Positive evidence outranks the age, in both directions. A leg the API
      # says is `completed` is abandoned two minutes in, without waiting an hour
      # for the window; a leg whose pid answers is running at ninety minutes,
      # however stale its claim. The window only decides the case where there is
      # no evidence either way, and there the row states the absence rather than
      # inventing a verdict for it.
      local age started stale
      age=$(( $(date +%s) - $(jq -r '.ts // 0' <<<"$m") ))
      started="started $(( age / 60 )) minute(s) ago"
      _status_liveness "$m"
      case "$STATUS_LIVENESS" in
        running)
          ui_row "$gutter" run "$label""running now — $started" ;;
        elsewhere)
          ui_row "$gutter" opt "$label$started on $STATUS_LIVENESS_DETAIL" ;;
        gone)
          ui_row "$gutter" no "$label$started, abandoned — $STATUS_LIVENESS_DETAIL" ;;
        *)
          # A stale claim carries its own reason, and both reasons already say
          # either how old it is or which revision it was made against, so the
          # age prefix would be printing the same fact twice.
          if stale="$(state_claim_is_stale "$m" "$CTX_HEAD_SHA")"; then
            ui_row "$gutter" no "$label""abandoned — $stale"
          else
            ui_row "$gutter" opt "$label$started, no result yet"
          fi ;;
      esac ;;
  esac
}

# Is the run behind an open claim still working?
#
# Sets STATUS_LIVENESS to one of `running`, `gone`, `elsewhere`, or empty for
# "no evidence either way", with STATUS_LIVENESS_DETAIL carrying the reason a
# `gone` is known or the host an `elsewhere` is on. Globals rather than stdout
# because the answer is memoised, and a `$( )` around it would run the memo in a
# subshell and throw the cached value away with it — which for an automated leg
# means a second API call per row.
#
# Empty is a real answer and the common one. Anything that cannot be shown is
# not claimed: an unreadable lock, a run in another checkout, a pull request
# being watched from a machine that never held the lock.
STATUS_LIVENESS=""
STATUS_LIVENESS_DETAIL=""
_STATUS_LIVENESS_FOR=""
_status_liveness() {
  local run_id
  run_id="$(jq -r '.run_id // ""' <<<"$1")"
  [[ "$run_id" != "$_STATUS_LIVENESS_FOR" ]] || return 0
  _STATUS_LIVENESS_FOR="$run_id"
  STATUS_LIVENESS=""; STATUS_LIVENESS_DETAIL=""
  [[ -n "$run_id" ]] || return 0
  if [[ "$run_id" == local-* ]]; then
    _status_liveness_local "${run_id#local-}"
  else
    _status_liveness_workflow "$run_id"
  fi
  return 0
}

# A local run, answered from the lock file the run took over the same pull
# request — `<pid> on <host> since <timestamp>`, written by run_lock_acquire.
#
# `kill -0` on the marker's pid alone would be cheaper and is what the lock
# check twenty lines up does, but it is only sound where the pid means what the
# marker meant by it. Pids are recycled and every machine has its own, so a bare
# probe from a second machine, or from the same one an hour later, can find a
# stranger's process and print "running now" over a leg that died — the exact
# reassurance the old rendering refused to give, arrived at by a different
# route. The lock is crossrev's own record that this pid, on this host, is
# running against this pull request, so requiring it to agree with the marker
# makes the answer as trustworthy as the collision check that writes it.
_status_liveness_local() {
  local pid="$1" gitdir lock holder lock_pid lock_host rest
  [[ "$pid" =~ ^[0-9]+$ ]] || return 0
  gitdir="$(git rev-parse --git-dir 2>/dev/null)" || return 0
  lock="$gitdir/crossrev/pr-$CTX_PR.lock"
  [[ -f "$lock" ]] || return 0
  holder="$(cat "$lock" 2>/dev/null)" || return 0
  lock_pid="${holder%% *}"
  [[ "$lock_pid" == "$pid" ]] || return 0
  rest="${holder#* on }"; lock_host="${rest%% since *}"

  # A checkout on a shared filesystem is the one case where the lock is readable
  # from a machine that cannot test the pid in it. Naming the host is the whole
  # of what is knowable, and it is enough to go and look.
  if [[ -n "$lock_host" && "$lock_host" != "$(hostname 2>/dev/null || printf 'local')" ]]; then
    STATUS_LIVENESS="elsewhere"; STATUS_LIVENESS_DETAIL="$lock_host"
    return 0
  fi
  if kill -0 "$pid" 2>/dev/null; then
    STATUS_LIVENESS="running"
  else
    STATUS_LIVENESS="gone"; STATUS_LIVENESS_DETAIL="the process that started it is gone"
  fi
}

# An automated run, answered by the Actions API from anywhere. `completed` is
# the useful half: the run is over and the marker never reached `complete`, so
# the leg died inside it however recently that was.
_status_liveness_workflow() {
  local st
  st="$(gh_workflow_run_status "$CTX_REPO" "$1")"
  case "$st" in
    queued|in_progress|requested|waiting|pending)
      STATUS_LIVENESS="running" ;;
    completed)
      STATUS_LIVENESS="gone"; STATUS_LIVENESS_DETAIL="the workflow run finished without it" ;;
  esac
}

# What a leg with no marker at all should say. "Has not run" reads as a step
# still outstanding, which is wrong for every case here but the first.
_status_leg_absent() {
  local pass="$1" leg="$2" m_review verdict
  [[ "$leg" == "resolve" ]] || { printf 'not run yet'; return 0; }

  grep -qw "crossrev/stop" <<<"$CTX_LABELS" && { printf 'not run — crossrev/stop is applied'; return 0; }

  # A pass a cap refused to start never reached the resolve leg and never will,
  # so "not run yet" would promise something that is not coming.
  m_review="$(state_marker_for "$CTX_MARKERS" "$pass" review)"
  [[ "$(jq -r '.state // ""' <<<"$m_review")" == "declined" ]] && { printf 'not run'; return 0; }
  [[ "$(jq -r '.state // ""' <<<"$m_review")" == "complete" ]] || { printf 'not run yet'; return 0; }

  # A converged review is the reason the loop stopped and a blocked one hands over
  # to a human, so in neither case was a resolve leg ever owed.
  verdict="$(jq -r '.verdict // ""' <<<"$m_review")"
  case "$verdict" in
    converged|blocked) printf 'not needed, the review %s' "$verdict" ;;
    *)                 printf 'not run yet' ;;
  esac
}

# A finished leg, described by what it found or did.
_status_leg_complete() {
  local gutter="$1" label="$2" leg="$3" m="$4" verdict blocked parts commit
  if [[ "$leg" == "review" ]]; then
    verdict="$(jq -r '.verdict // ""' <<<"$m")"
    if [[ "$verdict" == "blocked" ]]; then
      ui_row "$gutter" no "$label""blocked — $(jq -r '.blocked_reason // "the reviewer could not complete"' <<<"$m")"
      return 0
    fi
    parts="$(jq -r '
      (.findings // []) as $f
      | [ "high", "medium", "low" ]
      | map(. as $s | ($f | map(select(.severity == $s)) | length) as $n
            | select($n > 0) | "\($n) \($s)")
      | join(", ")' <<<"$m")"
    if [[ -z "$parts" ]]; then
      ui_row "$gutter" ok "$label""no findings — $verdict"
    else
      ui_row "$gutter" ok "$label$parts"
    fi
    return 0
  fi

  blocked="$(jq -r '.blocked // false' <<<"$m")"
  if [[ "$blocked" == "true" ]]; then
    ui_row "$gutter" no "$label""blocked — $(jq -r '.blocked_reason // "the resolve leg could not complete"' <<<"$m")"
    return 0
  fi
  parts="$(_resolution_counts "$(jq -c '.resolutions // []' <<<"$m")")"
  parts="${parts%.}"
  commit="$(jq -r '.commit_sha // ""' <<<"$m")"
  [[ -n "$commit" && "$commit" != "null" ]] && parts="$parts, pushed ${commit:0:7}"
  # An escalated resolution halted the loop for a human, so the row cannot carry
  # the tick a settled pass gets. The header above already says halted, and a
  # green leg line underneath it contradicts the section it sits in.
  if (( $(_status_escalated "$m") > 0 )); then
    ui_row "$gutter" no "$label$parts"
  else
    ui_row "$gutter" ok "$label$parts"
  fi
}

# NEXT always ends in something the reader can type: a command, or the condition
# that has to change first and the command that follows it. Never an empty
# section, never a bare dash, and never "nothing automatic" as the last word — a
# tool whose job is telling you what to do next should not end on a dead end.
_status_next() {
  local state="$1" pass="$2" m stale
  case "$state" in
    stopped)
      # The second command is the leg that was owed when the brake went on,
      # which is what "continue" means here. Read from the labels beside the
      # stop, or from the markers when none is there.
      local resume="crossrev review --pr $CTX_PR"
      if grep -qw "crossrev/awaiting-resolution" <<<"$CTX_LABELS" \
         || [[ "$(_status_state_from_markers)" == "awaiting resolution" ]]; then
        resume="crossrev resolve --pr $CTX_PR"
      fi
      # Same rule as the cap halt below: with no pass behind it there is nothing
      # to continue from, and "pass 0" names one that never existed.
      if (( pass > 0 )); then
        ui_line "someone applied crossrev/stop. To continue from pass $pass:"
      else
        ui_line "someone applied crossrev/stop. To start the loop:"
      fi
      ui_cmd  "gh pr edit $CTX_PR --remove-label crossrev/stop"
      ui_cmd  "$resume"
      return 0 ;;
    converged)
      # A revision pushed after the loop converged is unreviewed, and nothing
      # starts a pass for it by itself: the generated review workflow listens
      # for labels and comments rather than `synchronize`, and the converged
      # label stays where the loop left it, so no label change fires anything.
      # Reporting "nothing to run" over that head would be the one wrong answer
      # this section exists to avoid. The settle arm below compares the head for
      # the same reason.
      if (( pass > 0 )) && state_is_new_revision "$CTX_MARKERS" "$CTX_HEAD_SHA"; then
        ui_line "the loop converged on pass $pass, and the branch has moved since."
        ui_line "Nothing reviews a new revision on its own, so pass $(( pass + 1 )) is owed:"
        ui_cmd  "crossrev review --pr $CTX_PR"
        return 0
      fi
      # Deliberately the same vocabulary as the converged summary comment, so a
      # reader moving between the terminal and GitHub is not translating between
      # two descriptions of one state.
      ui_line "nothing to run — the loop converged on pass $pass: nothing at or"
      ui_line "above min_fix_severity ($CTX_MIN_FIX_SEVERITY) remains."
      return 0 ;;
    halted)
      _status_next_halted "$pass"
      return 0 ;;
    "awaiting resolution")
      ui_cmd "crossrev resolve --pr $CTX_PR"
      m="$(state_marker_for "$CTX_MARKERS" "$pass" resolve)"
      if [[ -n "$m" && "$(jq -r '.state // ""' <<<"$m")" == "started" ]]; then
        _status_liveness "$m"
        [[ "$STATUS_LIVENESS" == "running" ]] && _status_next_running "$pass"
      fi
      return 0 ;;
  esac

  # awaiting review, and why one is owed.
  #
  # A resolve pass that settled every finding without pushing is done: the
  # reviewer declines an unchanged head, so the review command below is one
  # that refuses. Passes labelled after the fact carry converged and never
  # reach here; this arm is the same decision (legs_resolve_pass_label) read
  # off the marker, for the pull requests whose awaiting-review label predates
  # it. A revision pushed after the settle is genuinely unreviewed, so the
  # head has to not have moved for the pass to be over.
  m="$(state_marker_for "$CTX_MARKERS" "$pass" resolve)"
  if (( pass > 0 )) && [[ -n "$m" && "$(jq -r '.state // ""' <<<"$m")" == "complete" ]] \
     && [[ "$(legs_resolve_pass_label "$m" "$(_markers_escalated "$CTX_MARKERS")")" == "converged" ]] \
     && ! state_is_new_revision "$CTX_MARKERS" "$CTX_HEAD_SHA"; then
    ui_line "nothing to run — pass $pass settled every finding without a code"
    ui_line "change, so a re-review would decline: there is nothing new to see."
    return 0
  fi

  # The cap comes before the plain invitation, because a pass at
  # max_passes_per_cycle is owed a review the loop will not start by itself. The
  # bound is a bound on the loop
  # continuing, not on a person: an automatic trigger and a cycle's own later
  # passes meet it, while `crossrev review --pr N` typed by hand runs one attended
  # pass regardless. So the state is described rather than the command withheld,
  # and the condition that changes the *automatic* behaviour goes above the
  # command — the shape the halted and stopped sections already use.
  m="$(state_marker_for "$CTX_MARKERS" "$pass" review)"
  if (( pass >= CTX_MAX_PASSES_PER_CYCLE )) \
     && [[ "$(jq -r '.state // ""' <<<"$m")" == "complete" ]] \
     && state_current_pass_complete "$CTX_MARKERS" "$pass" resolve; then
    ui_line "pass $pass reached max_passes_per_cycle ($CTX_MAX_PASSES_PER_CYCLE), so the loop will"
    ui_line "not start another pass on its own. Raise policy.max_passes_per_cycle in"
    ui_line ".github/crossrev.yml to let it continue by itself. Asking for one pass"
    ui_line "by hand runs it either way:"
    ui_cmd  "crossrev review --pr $CTX_PR"
    return 0
  fi

  ui_cmd "crossrev review --pr $CTX_PR"
  if [[ -n "$m" && "$(jq -r '.state // ""' <<<"$m")" == "started" ]]; then
    _status_liveness "$m"
    if [[ "$STATUS_LIVENESS" == "running" ]]; then
      _status_next_running "$pass"
    elif stale="$(state_claim_is_stale "$m" "$CTX_HEAD_SHA")"; then
      ui_line "The unfinished pass is stale — $stale — so a re-run abandons it"
      ui_line "and starts pass $pass again."
    else
      ui_line "The head has not moved, so a re-run resumes pass $pass and posts"
      ui_line "only what is missing."
    fi
  elif (( pass > 0 )) && state_is_new_revision "$CTX_MARKERS" "$CTX_HEAD_SHA"; then
    ui_line "Pass $pass is closed and the branch moved, so pass $(( pass + 1 )) reviews"
    ui_line "the new revision."
  fi
  grep -qw "crossrev/watchdog-retried" <<<"$CTX_LABELS" && {
    ui_line "The watchdog has already retried this leg once — a second failure"
    ui_line "halts the loop rather than retrying again."
  }
  return 0
}

# What NEXT says under a command whose leg is already running.
#
# The row above says the leg is alive, so a section that reads as an invitation
# to start it again would contradict the one thing this display exists to get
# right. The command keeps its place — it is what resumes the pass if that run
# dies, and NEXT always ends in something typable — but it stops being the thing
# to do now.
_status_next_running() {
  ui_line "Pass $1 is running now, so wait for it rather than starting a"
  ui_line "second run over the same pull request. The command above resumes"
  ui_line "the pass if that one dies."
}

# A halt has three shapes and they need different levers: a cap wants raising, a
# blocked leg wants the underlying decision made, and an escalated finding wants
# the disagreement settled. All three are read off the marker that recorded the
# halt rather than off the label, which says only that one happened.
_status_next_halted() {
  local pass="$1" m_review m_resolve escalated noun
  m_review="$(state_marker_for "$CTX_MARKERS" "$(( pass + 1 ))" review)"
  if [[ -n "$m_review" && "$(jq -r '.state // ""' <<<"$m_review")" == "declined" ]]; then
    ui_line "pass $(( pass + 1 )) never began — $(jq -r '.reason // "a cap stopped it"' <<<"$m_review")."
    if (( pass == 0 )); then
      # A cap that refuses the FIRST pass leaves nothing behind it. There is no
      # pass 0, and warning that its changes are unverified describes a review
      # that never ran over code nobody looked at.
      ui_line "No review has run on this pull request at all. Raise the cap in"
    else
      ui_line "So anything pass $pass changed is unverified. Raise the cap in"
    fi
    ui_line ".github/crossrev.yml, then:"
    ui_cmd  "crossrev review --pr $CTX_PR"
    return 0
  fi

  # Escalation is tested before blocked, and counted across every resolve
  # marker rather than only this pass's. A blocked pass is always a completed
  # pass, so a marker carrying both flags sent the reader to the one command
  # that declines; and a later pass that adds nothing leaves the halt standing
  # while the finding that caused it moves a pass back.
  #
  # An escalated finding is the one halt nobody can automate past: two agents
  # disagreed twice, or the point needs a judgement that is not theirs. The
  # thread is left open on purpose, so the lever is reading it, not re-running
  # the leg that already declined to decide.
  escalated="$(_markers_escalated "$CTX_MARKERS")"
  if (( escalated > 0 )); then
    noun="findings"; (( escalated == 1 )) && noun="finding"
    ui_line "$escalated $noun need a human decision. The resolve leg left the"
    ui_line "thread open and said why in it. Once you have settled it:"
    grep -qw "crossrev/stop" <<<"$CTX_LABELS" \
      && ui_cmd "gh pr edit $CTX_PR --remove-label crossrev/stop"
    ui_cmd  "crossrev review --pr $CTX_PR"
    return 0
  fi

  # Reachable because the resolve leg re-drives a blocked pass: what stopped it
  # was the environment rather than a disagreement, so once a human has fixed
  # that, running the leg again is exactly the remedy.
  m_resolve="$(state_marker_for "$CTX_MARKERS" "$pass" resolve)"
  # A deferral whose record never landed is not settled: the thread stayed
  # open on purpose, and the remedy is filing the work and driving the pass
  # again — which legs_resolve_redrivable admits for exactly this marker.
  if [[ -n "$m_resolve" && "$(jq -r '.state // ""' <<<"$m_resolve")" == "complete" ]] \
     && [[ "$(jq -r '.blocked // false' <<<"$m_resolve")" != "true" ]] \
     && [[ "$(jq '[(.resolutions // [])[] | select(.resolution == "deferred" and .crossrev_tracked == "")] | length' <<<"$m_resolve")" != "0" ]]; then
    ui_line "a deferred finding was never filed anywhere durable, so its thread"
    ui_line "stays open. Put the work somewhere tracked, then drive the pass again:"
    ui_cmd  "crossrev resolve --pr $CTX_PR"
    return 0
  fi

  # A fix the resolver claimed and never committed. The finding is real by the
  # resolver's own answer and the code is unchanged, so the thread stayed open
  # and the pass halted rather than converging over it. Reading the reply is
  # the first move; after that the leg is redrivable, as it is for the deferral
  # above.
  if [[ -n "$m_resolve" && "$(jq -r '.state // ""' <<<"$m_resolve")" == "complete" ]] \
     && [[ "$(jq -r '.blocked // false' <<<"$m_resolve")" != "true" ]] \
     && legs_resolve_unpushed_fix "$m_resolve"; then
    ui_line "the resolver claimed a fix and pushed no commit, so its thread stays"
    ui_line "open. Read the reply, then either make the change yourself or drive"
    ui_line "the pass again:"
    ui_cmd  "crossrev resolve --pr $CTX_PR"
    return 0
  fi

  # A pass whose resolutions were never recorded, written by a crossrev old
  # enough not to carry them. Nothing on it can be shown to have settled, so it
  # is not a convergence — and driving the pass again both answers the findings
  # and writes the record every reader here is missing.
  if [[ -n "$m_resolve" && "$(jq -r '.state // ""' <<<"$m_resolve")" == "complete" ]] \
     && [[ "$(jq -r '.blocked // false' <<<"$m_resolve")" != "true" ]] \
     && legs_resolve_unrecorded "$m_resolve"; then
    ui_line "the pass recorded no resolutions, so what it settled cannot be read"
    ui_line "back. Drive it again to answer the findings on the record:"
    ui_cmd  "crossrev resolve --pr $CTX_PR"
    return 0
  fi

  if [[ -n "$m_resolve" && "$(jq -r '.blocked // false' <<<"$m_resolve")" == "true" ]]; then
    ui_line "the resolve leg reported blocked and left its reasoning in the thread"
    ui_line "it belongs to. Once that is settled:"
    ui_cmd  "crossrev resolve --pr $CTX_PR"
    return 0
  fi

  m_review="$(state_marker_for "$CTX_MARKERS" "$pass" review)"
  if [[ -n "$m_review" && "$(jq -r '.verdict // ""' <<<"$m_review")" == "blocked" ]]; then
    ui_line "the review leg reported blocked, so what happens next is a human's"
    ui_line "call. Once you have looked:"
    ui_cmd  "crossrev review --pr $CTX_PR"
    return 0
  fi

  ui_line "the loop stopped short and needs a human. Remove crossrev/halted once"
  ui_line "you have looked, then:"
  ui_cmd  "crossrev review --pr $CTX_PR"
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
      # Accepted and ignored, as in status. The watchdog sweeps a repository
      # rather than acting on one pull request with one harness, and it only
      # ever runs on a schedule, so it is automatic by construction.
      --pr)      shift 2 ;;
      --harness) shift 2 ;;
      --trigger) shift 2 ;;
      --no-tips) shift ;;
      *) ui_die "unknown option for watchdog: $1" "Usage: crossrev watchdog [--repo owner/name] [--timeout <seconds>]" ;;
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
    --jq '[.[] | select([.labels[].name] | any(startswith("crossrev/awaiting-")))
           | {number, labels: [.labels[].name], head: .head.sha}]' 2>/dev/null)" || stuck="[]"

  ui_section "CrossRev watchdog on $repo"

  local n i pr labels head author markers marker age leg
  n="$(jq 'length' <<<"$stuck")"
  for (( i = 0; i < n; i++ )); do
    pr="$(jq -r ".[$i].number" <<<"$stuck")"
    labels="$(jq -r ".[$i].labels | join(\" \")" <<<"$stuck")"
    head="$(jq -r ".[$i].head" <<<"$stuck")"
    checked=$(( checked + 1 ))

    grep -qw "crossrev/stop" <<<"$labels" && continue

    if [[ "$labels" == *"crossrev/awaiting-resolution"* ]]; then leg="resolve"; else leg="review"; fi

    author="$(state_trusted_author automated)"
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

  if grep -qw "crossrev/watchdog-retried" <<<"$labels"; then
    state_label_remove "$pr" "$repo" "$label"
    gh_label_ensure "$repo" "crossrev/halted" "$(legs_label_colour crossrev/halted)" \
      "$(legs_label_description crossrev/halted)" >/dev/null
    state_label_add "$pr" "$repo" "crossrev/halted"
    gh_comment_create "$repo" "$pr" \
"**crossrev halted** — the $leg leg was already retried once and is still not finishing.

The last marker on this pull request records how far it got. Nothing here is a judgement about the code: the loop stopped, it did not converge.

To look yourself: \`crossrev status --pr $pr\`. To restart it, remove \`crossrev/halted\` and \`crossrev/watchdog-retried\`, then apply \`$label\`." >/dev/null
    ui_line "   halted — it had already been retried once"
    return 1
  fi

  # Re-applying a label GitHub already holds fires no event, so the retry has to
  # remove it first.
  gh_label_ensure "$repo" "crossrev/watchdog-retried" \
    "$(legs_label_colour crossrev/watchdog-retried)" \
    "$(legs_label_description crossrev/watchdog-retried)" >/dev/null
  state_label_add "$pr" "$repo" "crossrev/watchdog-retried"
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
# crossrev workflows. A repository with no CI at all gets nothing, because the
# suggestion would be noise. Rarely: only on a run that reached a terminal state,
# so at most once per pull request. Suppressible by flag and by config.
run_upgrade_nudge() {
  [[ "${CROSSREV_NO_TIPS:-0}" == "1" ]] && return 0
  [[ "$(jq -r '.enable_automation_hint' <<<"$CFG_MERGED")" == "false" ]] && return 0
  [[ -d .github/workflows ]] || return 0
  compgen -G ".github/workflows/crossrev-*.yml" >/dev/null 2>&1 && return 0

  cat <<'EOF'
  Tip: this repo already runs GitHub Actions. `crossrev init` would run this
  loop automatically on every pull request — review, fixes, re-review — and
  takes about a minute to set up. Silence this with `--no-tips`.

EOF
}
