# shellcheck shell=bash
# lib/log.sh — the per-run record on disk.
#
# Every run leaves a directory beside the worktrees CrossRev already keeps:
#
#   ${XDG_STATE_HOME:-$HOME/.local/state}/crossrev/runs/<repo-slug>/pr-<n>/<run_id>/
#
# holding a run log and, while a failure is being looked at, the harness
# transcripts. The run id is the same one the pull request marker carries
# (${GITHUB_RUN_ID:-local-$$}), so the file a marker names is the file on disk.
#
# Two rules run through everything written here:
#
#   1. No credential reaches either file. The harness process holds no GitHub
#      token (ADR 0001), and anything captured from it passes log_redact before
#      it lands. The redaction is pattern-based, the same shape lib/init.sh
#      uses when it tees a credential capture.
#   2. The directory cannot grow without bound. log_init sweeps run directories
#      older than logs.retention_days, so retention is one rule covering
#      everything written here rather than a policy per file.
#
# Nothing in this file may call ui_die or fail the caller: it is invoked from
# the EXIT trap and from functions whose own failure paths are the thing being
# recorded. Every helper degrades to a no-op instead.

# Empty until log_init runs, and every function below no-ops while it is. That
# is what lets a library consumer (a test sourcing run.sh directly, `crossrev
# config show`) share the code without scattering directories it never asked
# for.
CROSSREV_RUN_DIR=""
# Set by the --keep-transcripts flag or logs.keep_transcripts. Empty and 0 both
# mean the default: transcripts survive a failed leg only.
CROSSREV_KEEP_TRANSCRIPTS=""
# The leg the current invocation belongs to, set by the legs ahead of
# run_invoke so the transcript files name review or resolve rather than the
# harness that happened to serve them.
CROSSREV_TRANSCRIPT_LEG=""

# The same id the pull request markers carry, so `crossrev status` naming a run
# and the directory on disk agree.
log_run_id() { printf '%s' "${GITHUB_RUN_ID:-local-$$}"; }

# The per-run directory for a repo and pull request. Printed, not created.
log_run_dir() {
  local repo="$1" pr="$2" slug
  slug="${repo//\//-}"
  printf '%s/crossrev/runs/%s/pr-%s/%s' \
    "${XDG_STATE_HOME:-$HOME/.local/state}" "$slug" "$pr" "$(log_run_id)"
}

# Create the run directory and sweep expired ones. Idempotent within a process:
# `crossrev cycle` runs both legs in one invocation, and the second leg's
# ctx_load must not restart the record the first began.
log_init() {
  local repo="$1" pr="$2"
  [[ -z "$CROSSREV_RUN_DIR" ]] || return 0
  CROSSREV_RUN_DIR="$(log_run_dir "$repo" "$pr")"
  mkdir -p "$CROSSREV_RUN_DIR" 2>/dev/null || { CROSSREV_RUN_DIR=""; return 0; }
  log_sweep
  log_event run "start repo=$repo pr=$pr"
  return 0
}

# One line per event: timestamp, phase, detail. Appended through the redaction
# filter even though the callers build these strings from names and exit codes
# — the rule "nothing reaches the file unfiltered" has no exceptions to
# remember.
log_event() {
  local phase="$1" detail="${2:-}"
  [[ -n "$CROSSREV_RUN_DIR" ]] || return 0
  printf '%s %s %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$phase" "$detail" \
    | log_redact >>"$CROSSREV_RUN_DIR/run.log" 2>/dev/null || true
  return 0
}

# The credential shapes CrossRev handles, masked wherever they appear. The
# prefix survives so a redacted line still names the kind of token it held;
# the body does not. Kept deliberately broader than the tokens a run is
# expected to hold: a harness echoing its environment on a failure path is the
# case this exists for.
log_redact() {
  sed -E \
    -e 's/(sk-ant-[A-Za-z0-9_-]{6})[A-Za-z0-9_-]+/\1…[redacted]/g' \
    -e 's/(github_pat_[A-Za-z0-9_]{6})[A-Za-z0-9_]+/\1…[redacted]/g' \
    -e 's/(gh[pousr]_[A-Za-z0-9]{6})[A-Za-z0-9]+/\1…[redacted]/g' \
    -e 's/(xai-[A-Za-z0-9_-]{6})[A-Za-z0-9_-]+/\1…[redacted]/g' \
    -e 's/(sk-[A-Za-z0-9_-]{6})[A-Za-z0-9_-]{12,}/\1…[redacted]/g'
}

# Redact a file in place. Through a temp file rather than sed -i, because BSD
# and GNU sed spell in-place editing differently and this runs on both.
log_redact_file() {
  local f="$1" tmp
  [[ -f "$f" ]] || return 0
  tmp="$(mktemp)" || return 0
  log_redact <"$f" >"$tmp" && mv "$tmp" "$f" || rm -f "$tmp"
  return 0
}

# Age out run directories. Everything under runs/ answers to the one rule —
# run logs and transcripts alike — so nothing needs its own lifetime. The
# mtime of the directory moves with every file written into it, so a run in
# progress never reads as old.
log_sweep() {
  local days="${1:-${CROSSREV_LOG_RETENTION_DAYS:-14}}"
  local base="${XDG_STATE_HOME:-$HOME/.local/state}/crossrev/runs"
  [[ -d "$base" ]] || return 0
  [[ "$days" =~ ^[0-9]+$ ]] || days=14
  # Exactly depth 3 (runs/<slug>/pr-<n>/<run_id>), so the slug and pr levels are
  # never matched for deletion here; emptied parents go below. maxdepth keeps
  # find out of a directory rm is already removing.
  find "$base" -mindepth 3 -maxdepth 3 -type d -mtime "+$days" -exec rm -rf {} + 2>/dev/null || true
  # Directories left empty by the sweep, aged past the same window so a run
  # another process has just made and not yet written into survives.
  find "$base" -mindepth 1 -maxdepth 2 -type d -empty -mtime "+$days" -delete 2>/dev/null || true
  return 0
}

# Should the transcripts of a successful invocation survive? The flag and the
# config key both land in CROSSREV_KEEP_TRANSCRIPTS before this is asked.
log_transcripts_kept() { [[ "$CROSSREV_KEEP_TRANSCRIPTS" == "1" ]]; }

# The stem an attempt's transcripts are written under: <leg>.attempt-<n> inside
# the run directory. Printed, or nothing when no run directory exists — the
# adapters fall back to anonymous temp files then, which is the behaviour every
# caller of an adapter outside a run already has.
log_transcript_base() {
  local attempt="$1"
  [[ -n "$CROSSREV_RUN_DIR" && -n "$CROSSREV_TRANSCRIPT_LEG" ]] || return 1
  printf '%s/%s.attempt-%s' "$CROSSREV_RUN_DIR" "$CROSSREV_TRANSCRIPT_LEG" "$attempt"
}

# Delete one attempt's transcripts, or every attempt's for the leg with no $1.
# Success-path hygiene only: the failure path is the reason the files exist,
# and nothing on it calls this.
log_transcripts_clear() {
  local base="${1:-}"
  [[ -n "$CROSSREV_RUN_DIR" ]] || return 0
  log_transcripts_kept && return 0
  if [[ -n "$base" ]]; then
    rm -f "$base".* 2>/dev/null || true
  else
    rm -f "$CROSSREV_RUN_DIR/$CROSSREV_TRANSCRIPT_LEG".attempt-*.* 2>/dev/null || true
  fi
  return 0
}
