# shellcheck shell=bash
# lib/state.sh — PR state: labels, markers, revision detection, finding ids.
#
# State lives on the pull request rather than in process memory, so it survives
# a crash, is readable by a human, and is identical in both modes.
#
# Every `gh` call in revloop goes through this file or legs.sh. The agent
# process never sees one.

REVLOOP_MARKER_PREFIX="<!-- revloop:"
REVLOOP_FINDING_PREFIX="<!-- revloop:f"

# ---------------------------------------------------------------------------
# Trust
# ---------------------------------------------------------------------------
#
# A marker is an HTML comment in a comment body, and anyone who can comment on
# the pull request can write one. Author identity is the only signal GitHub
# controls and nobody can forge — but WHICH author counts depends on the mode,
# and hard-coding the App here would break local mode outright: a local run
# would find no App-authored marker on any pass, report pass 1 forever,
# reconcile nothing, and never reach the cap.

state_trusted_author() {
  local mode="$1"
  case "$mode" in
    event-driven)
      # The App, and nothing else. A forged marker here makes an *agent* act:
      # push a commit, skip a finding, believe a leg finished.
      #
      # On a runner there is no ~/.config/revloop, so the slug comes from the
      # environment — actions/create-github-app-token emits it as `app-slug`, and
      # the generated workflows pass it through. The local metadata file is the
      # fallback for an event-driven run started from a machine.
      local slug="${REVLOOP_APP_SLUG:-}"
      [[ -n "$slug" ]] || slug="$(jq -r '.slug // empty' "$(_auth_meta "${REVLOOP_OWNER:-}")" 2>/dev/null)"
      [[ -n "$slug" ]] || ui_die \
        "cannot determine which App's markers to trust" \
        "Automated mode reads markers only from the App that writes them. In a workflow, set REVLOOP_APP_SLUG from the token step's app-slug output. Locally, run: revloop auth status"
      printf '%s[bot]' "$slug" ;;
    *)
      # The invoking user. No watchdog, no unattended push, no chaining — you
      # are the orchestrator, and a forged marker can only mislead you about
      # work you asked for.
      gh api user --jq .login 2>/dev/null || ui_die \
        "could not resolve your GitHub identity" "Run: gh auth login" ;;
  esac
}

# ---------------------------------------------------------------------------
# Markers
# ---------------------------------------------------------------------------

# Every comment on the PR authored by the trusted author, newest last.
#
# Filtered by piping into jq rather than with `gh --jq`, because that flag takes a
# bare expression and no `--arg`: passing one makes the expression literally
# "--arg", which fails. It only ever worked by falling through to a second call.
_state_comments() {
  local pr="$1" repo="$2" author="$3"
  gh api --paginate "repos/$repo/issues/$pr/comments" 2>/dev/null \
    | jq -c --arg a "$author" '.[] | select(.user.login == $a) | {id, body, created_at}' 2>/dev/null
}

# Pull the marker object out of a comment body, or nothing.
state_marker_of() {
  local body="$1"
  printf '%s' "$body" \
    | sed -n 's/.*<!-- revloop: \(.*\) -->.*/\1/p' \
    | tr -d '\n' | jq -c . 2>/dev/null
}

# Every trusted marker on the PR, as a JSON array in chronological order.
state_markers() {
  local pr="$1" repo="$2" author="$3"
  local out="[]" body m
  while IFS= read -r c; do
    [[ -n "$c" ]] || continue
    body="$(jq -r .body <<<"$c")"
    m="$(state_marker_of "$body")"
    [[ -n "$m" ]] || continue
    out="$(jq -c --argjson m "$m" --argjson id "$(jq .id <<<"$c")" \
      '. + [$m + {comment_id: $id}]' <<<"$out")"
  done < <(_state_comments "$pr" "$repo" "$author")
  printf '%s' "$out"
}

# Serialise a marker for embedding in a comment body. Invisible in the UI, and
# it doubles as the audit trail.
state_marker_encode() { printf '\n\n%s %s -->' "$REVLOOP_MARKER_PREFIX" "$(jq -c . <<<"$1")"; }

# The per-write marker that makes recovery exact.
#
# A ledger written AFTER a successful post has a window in it: GitHub accepts
# the comment, the process dies, and the mapping from comment to finding is
# gone — recovery then cannot tell an already-posted finding from a missing one
# and posts it twice. Carrying the id in the body closes the window by
# construction, because the record and the thing it records are one HTTP call.
state_finding_marker() {
  local id="$1" pass="$2" leg="$3"
  printf '\n\n%s %s -->' "$REVLOOP_FINDING_PREFIX" \
    "$(jq -cn --arg i "$id" --argjson p "$pass" --arg l "$leg" '{id:$i,pass:$p,leg:$l}')"
}

# Which finding ids have already been written out, read back from the PR itself
# rather than from a ledger.
# $4 is the leg asking, and it is not optional. Every marker carries the leg
# that wrote it, because "already written out" means different things to the two
# legs: the review leg is asking which findings it already has inline comments
# for, the resolve leg which it has already replied to. Reading the ids without
# the leg conflates them — the review leg stamps a marker on every inline
# comment, so the resolve leg would find every id already present, skip every
# reply as a duplicate, and resolve the threads anyway. That leaves a
# collaborator with a resolved thread and no explanation.
state_posted_finding_ids() {
  local pr="$1" repo="$2" author="$3" leg="$4"
  {
    gh api --paginate "repos/$repo/pulls/$pr/comments" 2>/dev/null | jq -r --arg a "$author" '.[] | select(.user.login==$a) | .body'
    gh api --paginate "repos/$repo/issues/$pr/comments" 2>/dev/null | jq -r --arg a "$author" '.[] | select(.user.login==$a) | .body'
  } | sed -n 's/.*<!-- revloop:f \(.*\) -->.*/\1/p' \
    | jq -r --arg leg "$leg" 'select(.leg==$leg) | .id' 2>/dev/null | sort -u
}

# ---------------------------------------------------------------------------
# Finding identity
# ---------------------------------------------------------------------------
#
# Stable across passes so "already posted" is a set-membership test. Path and
# normalised title carry the identity; the anchor lets a finding still be
# matched after the line moves.
state_finding_id() {
  local path="$1" title="$2" anchor="${3:-}"
  local norm
  norm="$(printf '%s' "$title" | tr '[:upper:]' '[:lower:]' | tr -s '[:space:]' ' ' | sed 's/^ *//; s/ *$//')"
  printf '%s\n%s\n%s' "$path" "$norm" "$anchor" | shasum -a 256 | cut -c1-16
}

# Fingerprint of the commented line and its neighbours.
state_anchor() {
  local file="$1" line="$2"
  [[ -f "$file" ]] || { printf ''; return 0; }
  sed -n "$((line > 2 ? line - 2 : 1)),$((line + 2))p" "$file" 2>/dev/null \
    | tr -d '[:space:]' | shasum -a 256 | cut -c1-8
}

# ---------------------------------------------------------------------------
# Pass number and revision detection
# ---------------------------------------------------------------------------

# No trusted marker means pass 1, with trust resolved per mode. Pass numbering,
# revision detection and run counting all read the same rule, so there is one
# definition to get right rather than three.
state_pass() {
  local markers="$1"
  local n
  n="$(jq -r '[.[] | select(.leg == "review") | .pass] | max // 0' <<<"$markers")"
  printf '%s' "$(( n + 1 ))"
}

state_current_pass_complete() {
  local markers="$1" pass="$2" leg="$3"
  [[ "$(jq -r --argjson p "$pass" --arg l "$leg" \
      '[.[] | select(.pass == $p and .leg == $l and .state == "complete")] | length' <<<"$markers")" != "0" ]]
}

# The pass the resolve leg belongs to: the newest review pass, not the next one.
state_current_review_pass() {
  jq -r '[.[] | select(.leg == "review") | .pass] | max // 0' <<<"$1"
}

# The newest marker for a (pass, leg), whatever its state. The resolve leg needs
# the review leg's to read the finding list; recovery needs its own to reuse the
# comment id rather than posting a second claim.
state_marker_for() {
  local markers="$1" pass="$2" leg="$3"
  jq -c --argjson p "$pass" --arg l "$leg" \
    'map(select(.pass == $p and .leg == $l)) | last // empty' <<<"$markers"
}

# An unfinished claim for the same (pr, pass, leg) means recovery, not a fresh
# start.
state_open_claim() {
  local markers="$1" pass="$2" leg="$3"
  jq -c --argjson p "$pass" --arg l "$leg" \
    'map(select(.pass == $p and .leg == $l)) | last // empty
     | select(.state == "started")' <<<"$markers"
}

# Is a claim too old, or against a revision that has since moved on?
#
# Resuming either one is worse than abandoning it: coming back a week later would
# resume into a pull request that has changed underneath, and the work would be
# reconciled against findings that no longer describe the code. Prints the reason
# it is stale, or nothing.
state_claim_is_stale() {
  local claim="$1" head_sha="$2" window="${3:-3600}"
  local ts claim_sha now
  now="$(date +%s)"
  ts="$(jq -r '.ts // 0' <<<"$claim")"
  claim_sha="$(jq -r '.head_sha // empty' <<<"$claim")"

  if [[ -n "$claim_sha" && "$claim_sha" != "$head_sha" ]]; then
    printf 'it claimed %s and the pull request is now at %s' "${claim_sha:0:7}" "${head_sha:0:7}"
    return 0
  fi
  if (( ts > 0 && now - ts > window )); then
    printf 'it was made %s minutes ago, past the %s-minute window' \
      "$(( (now - ts) / 60 ))" "$(( window / 60 ))"
    return 0
  fi
  return 1
}

# GitHub has no "new revision" event and `synchronize` fires per push, so rather
# than guessing, compare the PR head against the last review marker's head_sha.
# Same SHA, nothing new. Different SHA, a revision landed.
state_is_new_revision() {
  local markers="$1" head_sha="$2" last
  last="$(jq -r '[.[] | select(.leg == "review") | .head_sha] | last // empty' <<<"$markers")"
  [[ -z "$last" || "$last" != "$head_sha" ]]
}

# The daily cap needs no extra state: count trusted markers in the last 24
# hours, which the orchestrator is already reading. Counting *trusted* rather
# than App-authored is what makes the cap mean anything locally, and a local
# loop burning quota is the case it was written for.
state_runs_today() {
  local markers="$1" cutoff
  cutoff="$(( $(date +%s) - 86400 ))"
  jq -r --argjson c "$cutoff" \
    '[.[] | select((.ts // 0) > $c)] | length' <<<"$markers"
}

# ---------------------------------------------------------------------------
# Labels
# ---------------------------------------------------------------------------
#
# The chain is label-driven and `gh` refuses to apply a label that does not
# exist, so a repository without them posts its first review and then dies
# applying revloop/awaiting-resolution, looking healthy the whole time.

state_label_add() {
  local pr="$1" repo="$2" label="$3"
  gh api --method POST "repos/$repo/issues/$pr/labels" -f "labels[]=$label" >/dev/null 2>&1 || ui_die \
    "could not apply the label '$label' to $repo#$pr" \
    "The loop is label-driven, so this is fatal rather than cosmetic. If the label does not exist, create it with: revloop init"
}

state_label_remove() {
  local pr="$1" repo="$2" label="$3"
  gh api --method DELETE "repos/$repo/issues/$pr/labels/$label" >/dev/null 2>&1 || true
}

state_labels() {
  gh api "repos/$2/issues/$1/labels" --jq '.[].name' 2>/dev/null
}

state_has_label() { state_labels "$1" "$2" | grep -qx "$3"; }
