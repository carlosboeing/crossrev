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

# Create a file that is 0600 from birth. Callers that then `>"$f"` truncate in
# place, so the mode is never briefly the process umask (0644 under 022).
# Same convention as lib/auth.sh and lib/credentials.sh: umask rather than
# create-then-chmod, so nothing ever reads a file that was briefly wider.
log_create_private() {
  local f="$1"
  [[ -n "$f" ]] || return 0
  (umask 077; : >"$f") 2>/dev/null || true
}

# Create the run directory and sweep expired ones. Idempotent within a process:
# `crossrev cycle` runs both legs in one invocation, and the second leg's
# ctx_load must not restart the record the first began.
log_init() {
  local repo="$1" pr="$2"
  [[ -z "$CROSSREV_RUN_DIR" ]] || return 0
  CROSSREV_RUN_DIR="$(log_run_dir "$repo" "$pr")"
  (umask 077; mkdir -p "$CROSSREV_RUN_DIR") 2>/dev/null || { CROSSREV_RUN_DIR=""; return 0; }
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
  [[ -f "$CROSSREV_RUN_DIR/run.log" ]] || log_create_private "$CROSSREV_RUN_DIR/run.log"
  # Callers pass git tails and die reasons that already contain newlines.
  # Collapse them so the one-line-per-event invariant holds regardless.
  detail="${detail//$'\n'/ }"
  detail="${detail//$'\r'/ }"
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
  # Byte-wise: under a UTF-8 locale, BSD and GNU sed abort on the first byte
  # that is not valid UTF-8 (`illegal byte sequence`), which is exactly the
  # binary noise a failing harness dumps. LC_ALL=C keeps the ASCII token
  # patterns matching and lets the rest of the file through.
  LC_ALL=C sed -E \
    -e 's/(sk-ant-[A-Za-z0-9_-]{6})[A-Za-z0-9_-]+/\1…[redacted]/g' \
    -e 's/(github_pat_[A-Za-z0-9_]{6})[A-Za-z0-9_]+/\1…[redacted]/g' \
    -e 's/(gh[pousr]_[A-Za-z0-9]{6})[A-Za-z0-9]+/\1…[redacted]/g' \
    -e 's/(xai-[A-Za-z0-9_-]{6})[A-Za-z0-9_-]+/\1…[redacted]/g' \
    -e 's/(sk-[A-Za-z0-9_-]{6})[A-Za-z0-9_-]{12,}/\1…[redacted]/g'
}

# Redact a string rather than a file.
#
# The harness error message is extracted from the raw capture and then reaches a
# pull request comment, so it is filtered whether or not a transcript is kept.
# Filtering the file instead would mask the message only on runs that happen to
# have a run directory, and would rewrite the payload the adapter parses from
# that same file.
log_redact_str() {
  printf '%s' "$1" | log_redact
}

# The line appended to a published body the filter changed. A masked string on
# its own — `sk-ant-api03…[redacted]` inside a review comment — reads as a
# CrossRev defect to whoever finds it on the pull request, so the body says what
# happened to it.
LOG_REDACT_NOTICE='_CrossRev masked a string in this comment that matched a credential pattern._'

# Redact a body on its way to GitHub.
#
# The three routes out of a run are not the same one. log_redact_file covers the
# transcript kept on disk, log_redact_str covers the harness error message, and
# this covers the third: on a successful leg the findings text is parsed from the
# raw capture and published verbatim into inline review comments, replies, filed
# issues and the pass marker.
#
# The filter runs at the publish boundary rather than at the parse, so a leg's
# payload does not depend on whether a run directory exists — the fault the
# parse-then-filter order was introduced to remove.
#
# Idempotent. A masked string no longer matches any of the patterns, so a body
# that passes through twice is masked once and noted once — which is the case
# gh_review_comment_create creates every time GitHub refuses to anchor a line and
# it falls back through gh_comment_create.
#
# Fails closed. A filter that errors withholds the text rather than publishing
# it, because a body that could not be filtered is exactly the body that might
# carry the credential.
log_redact_publish() {
  local body="$1" out
  # `&& printf 'x'` twice over: it carries the pipeline's exit status out of the
  # command substitution, and the sentinel byte survives the substitution's
  # trailing-newline strip so a body keeps the newlines it was built with.
  if ! out="$(printf '%s' "$body" | log_redact && printf 'x')"; then
    log_event redact "publish filter failed; body withheld"
    printf 'CrossRev could not filter this text for credential shapes, so it withheld it rather than publishing it.'
    return 0
  fi
  out="${out%x}"
  # BSD and GNU sed disagree about a final line that carries no newline, so the
  # comparison ignores one trailing newline: the note must mean a mask happened,
  # not that a platform added a byte.
  [[ "${out%$'\n'}" == "${body%$'\n'}" ]] || out="$out

$LOG_REDACT_NOTICE"
  printf '%s' "$out"
}

# Redact a file in place. Through a temp file rather than sed -i, because BSD
# and GNU sed spell in-place editing differently and this runs on both.
#
# Fails closed: a filter error must not leave the original on disk. The
# unredacted copy is replaced with a notice, and the caller still sees
# return 0 — this file must not fail the EXIT trap or a harness-error path.
log_redact_file() {
  local f="$1" tmp
  [[ -f "$f" ]] || return 0
  tmp="$(mktemp)" || return 0
  if log_redact <"$f" >"$tmp"; then
    mv "$tmp" "$f"
  else
    rm -f "$tmp"
    log_create_private "$f"
    printf 'redaction failed; original discarded\n' >"$f"
    log_event redact "failed $f"
  fi
  return 0
}

# Age out run directories. Everything under runs/ answers to the one rule —
# run logs and transcripts alike — so nothing needs its own lifetime. The
# mtime of the directory moves with every file written into it, so a run in
# progress never reads as old.
log_sweep() {
  local days="${CROSSREV_LOG_RETENTION_DAYS:-14}"
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
  local attempt="$1" base
  [[ -n "$CROSSREV_RUN_DIR" && -n "$CROSSREV_TRANSCRIPT_LEG" ]] || return 1
  printf -v base '%s/%s.attempt-%s' "$CROSSREV_RUN_DIR" "$CROSSREV_TRANSCRIPT_LEG" "$attempt"
  # Pre-create so the adapters' `>"$out"` redirects inherit 0600. Codex also
  # writes a .payload; the empty file is harmless for the others and is
  # swept with the rest of the stem.
  log_create_private "$base.stdout"
  log_create_private "$base.stderr"
  log_create_private "$base.payload"
  printf '%s' "$base"
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
