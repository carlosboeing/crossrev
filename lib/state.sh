# shellcheck shell=bash
# lib/state.sh — PR state: labels, markers, revision detection, finding ids.
#
# State lives on the pull request rather than in process memory, so it survives
# a crash, is readable by a human, and is identical in both modes.
#
# Every `gh` call in crossrev goes through this file or legs.sh. The agent
# process never sees one.

CROSSREV_MARKER_PREFIX="<!-- crossrev:"
CROSSREV_FINDING_PREFIX="<!-- crossrev:f"

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
    automated)
      # The App, and nothing else. A forged marker here makes an *agent* act:
      # push a commit, skip a finding, believe a leg finished.
      #
      # On a runner there is no ~/.config/crossrev, so the slug comes from the
      # environment — actions/create-github-app-token emits it as `app-slug`, and
      # the generated workflows pass it through. The local metadata file is the
      # fallback for an automated run started from a machine.
      local slug="${CROSSREV_APP_SLUG:-}"
      [[ -n "$slug" ]] || slug="$(jq -r '.slug // empty' "$(_auth_meta "${CROSSREV_OWNER:-}")" 2>/dev/null)"
      [[ -n "$slug" ]] || ui_die \
        "cannot determine which App's markers to trust" \
        "Automated mode reads markers only from the App that writes them. In a workflow, set CROSSREV_APP_SLUG from the token step's app-slug output. Locally, run: crossrev auth status"
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

# The whole of marker decoding, as one jq program both readers share.
#
# It is held in a variable rather than written out twice because the two callers
# below must not be able to drift apart. `state_marker_of` decodes one body and
# `state_markers` decodes a whole comment stream in a single process; a marker
# that went through one path and not the other would read as a pass that settled
# nothing, and the loop would drive a finished pull request again.
#
# `_marker_of` applies the same rule per line the previous `sed` did — the last
# opening delimiter, and the last closing one after it. `<!-- crossrev:f` is not
# matched, because the state prefix ends in a space the finding one lacks.
#
# A body carrying two markers concatenates to two JSON values, and `fromjson`
# rejects that, so the comment is skipped. The previous path did not skip it: it
# fed the concatenation to jq's own parser, which reads it as a stream and
# printed both. The caller then rejected the pair and returned nothing at all,
# losing every marker on the pull request rather than that one comment.
#
# `_migrate` is the vocabulary migration, at the single point every marker is
# decoded through rather than at each of the dozen places one is read. A read
# that forgot the fallback would not fail loudly, which is why it lives here.
# `dispositions` became `resolutions` and `rebutted` became `disputed`, because
# both were borrowed vocabulary — one from records management, one from
# argumentation — for a field bug trackers have called a resolution for decades.
# Reading the old keys is one jq clause; the new key is the only one written.
#
# `_decode` is the pair applied to one comment object, yielding one migrated
# marker or nothing. An array, a string or a number payload makes `_migrate`
# throw on `has`, and the `?` swallows it. A `null` payload needs the trailing
# `select` instead, because `null | has(...)` answers `false` rather than
# throwing, so the migration returns it untouched. Both end as nothing, which is
# what a payload that is not one marker object should decode to.
_CROSSREV_MARKER_JQ='
  def _rename($from; $to):
    if has($from) and (has($to) | not)
    then .[$to] = .[$from] | del(.[$from]) else . end;
  def _migrate:
    _rename("dispositions"; "resolutions")
    | if (.resolutions | type) == "array" then
        .resolutions = [ .resolutions[]
          | _rename("disposition"; "resolution")
          | if .resolution == "rebutted" then .resolution = "disputed" else . end ]
      else . end
    | if (.findings | type) == "array" then
        .findings = [ .findings[]
          | _rename("disposition"; "resolution")
          | if .resolution == "rebutted" then .resolution = "disputed" else . end ]
      else . end;
  def _marker_of:
    [ (.body // "") | split("\n")[]
      | split("<!-- crossrev: ") | select(length > 1) | last
      | split(" -->") | select(length > 1) | .[:-1] | join(" -->") ]
    | join("") | fromjson? ;
  def _decode:
    [ _marker_of | _migrate ]? | .[0] | select(. != null);
'

# Pull the marker object out of a comment body, or nothing.
state_marker_of() {
  jq -cn --arg body "$1" "$_CROSSREV_MARKER_JQ"'
    {body: $body} | _decode
  ' 2>/dev/null
}

# Every trusted marker on the PR, as a JSON array in chronological order.
#
# One jq for the whole stream, not six processes per comment. The per-comment
# form read the body, extracted the marker, read the id and appended to the
# accumulating array in separate invocations, so a pull request carrying forty
# CrossRev comments spent about nine tenths of a second in process startup —
# on every leg, because all four of them read pass numbering through here.
#
# Read as raw text and parsed line by line rather than slurped as JSON, so one
# unparseable line is skipped the way the per-comment loop skipped it instead of
# failing the whole read. `_state_comments` emits one compact object per line and
# cannot produce a raw newline inside one, so a line is a comment.
state_markers() {
  local pr="$1" repo="$2" author="$3" out
  out="$(_state_comments "$pr" "$repo" "$author" \
    | jq -Rsc "$_CROSSREV_MARKER_JQ"'
        [ split("\n")[]
          | select(length > 0)
          | fromjson?
          | select(type == "object")
          | . as $c
          | ($c | _decode)
          | . + {comment_id: $c.id} ]
      ')"
  [[ -n "$out" ]] || out="[]"
  printf '%s' "$out"
}

# Serialise a marker for embedding in a comment body. Invisible in the UI, and
# it doubles as the audit trail.
state_marker_encode() { printf '\n\n%s %s -->' "$CROSSREV_MARKER_PREFIX" "$(jq -c . <<<"$1")"; }

# The per-write marker that makes recovery exact.
#
# A ledger written AFTER a successful post has a window in it: GitHub accepts
# the comment, the process dies, and the mapping from comment to finding is
# gone — recovery then cannot tell an already-posted finding from a missing one
# and posts it twice. Carrying the id in the body closes the window by
# construction, because the record and the thing it records are one HTTP call.
state_finding_marker() {
  local id="$1" pass="$2" leg="$3"
  printf '\n\n%s %s -->' "$CROSSREV_FINDING_PREFIX" \
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
  } | _state_finding_ids "$leg"
}

# Which findings got a reply that landed as a top-level comment rather than in
# the thread it answers, for one pass.
#
# The same markers state_posted_finding_ids reads, narrowed to the issue-comment
# endpoint — and that narrowing is the whole trick. A threaded reply is a review
# comment and a fallback reply is an issue comment, so WHERE the marker was
# recorded already is the record of which of the two happened, written by the
# same HTTP call that posted the reply.
#
# Read back rather than counted in a local, for the reason every other fact
# about a pass is: a run that stops between a fallback reply and the summary
# comment resumes with that reply already posted and skipped as a duplicate, so
# a counter starting at zero on the way back in reports a clean pass over a
# degraded one — which is exactly the silence the count was added to remove.
state_unthreaded_finding_ids() {
  local pr="$1" repo="$2" author="$3" leg="$4" pass="$5"
  gh api --paginate "repos/$repo/issues/$pr/comments" 2>/dev/null \
    | jq -r --arg a "$author" '.[] | select(.user.login==$a) | .body' \
    | _state_finding_ids "$leg" "$pass"
}

# Finding ids out of a stream of comment bodies, optionally for one pass only.
# One reader for the marker format, so a change to it cannot update one call
# site and quietly miss the other.
_state_finding_ids() {
  local leg="$1" pass="${2:-}"
  sed -n 's/.*<!-- crossrev:f \(.*\) -->.*/\1/p' \
    | jq -r --arg leg "$leg" --arg pass "$pass" \
        'select(.leg == $leg) | select($pass == "" or .pass == ($pass | tonumber)) | .id' 2>/dev/null \
    | sort -u
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

# A pass that a cap refused to start writes a marker so `status` has something to
# render the refusal from — otherwise the state is a comment body plus a label,
# and neither is machine-readable. But that marker records a pass that did not
# happen, so it must not count as one.
#
# Three places care, and all three would be wrong without this. Pass numbering
# would report the refused pass as the current one, so raising the cap and
# re-running would answer "already reviewed". Revision detection would take the
# refused marker's head_sha as a revision that had been reviewed. And the daily
# cap would count a run that never ran, against the cap that stopped it.
_state_real() { jq -c '[.[] | select((.state // "") != "declined")]' <<<"$1"; }

# No trusted marker means pass 1, with trust resolved per mode. Pass numbering,
# revision detection and run counting all read the same rule, so there is one
# definition to get right rather than three.
state_pass() {
  local markers="$1"
  local n
  n="$(jq -r '[.[] | select(.leg == "review") | .pass] | max // 0' <<<"$(_state_real "$markers")")"
  printf '%s' "$(( n + 1 ))"
}

# The highest pass number any marker mentions, refused passes included. `status`
# renders every pass, and a pass that was refused is one of the things it has to
# show.
state_max_pass() {
  jq -r '[.[] | .pass // 0] | max // 0' <<<"$1"
}

state_current_pass_complete() {
  local markers="$1" pass="$2" leg="$3"
  [[ "$(jq -r --argjson p "$pass" --arg l "$leg" \
      '[.[] | select(.pass == $p and .leg == $l and .state == "complete")] | length' <<<"$markers")" != "0" ]]
}

# The pass the resolve leg belongs to: the newest review pass, not the next one.
state_current_review_pass() {
  jq -r '[.[] | select(.leg == "review") | .pass] | max // 0' <<<"$(_state_real "$1")"
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
    printf 'it started against %s and the pull request is now at %s' "${claim_sha:0:7}" "${head_sha:0:7}"
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
  last="$(jq -r '[.[] | select(.leg == "review") | .head_sha] | last // empty' \
    <<<"$(_state_real "$markers")")"
  [[ -z "$last" || "$last" != "$head_sha" ]]
}

# Count distinct pull requests other than the current one that carry a trusted
# review marker inside the rolling window. A review-and-resolve cycle counts
# once because only the review marker participates. If the current pull request
# is already in the window, reviewing it again consumes no new unit and returns
# zero without reading the repository-wide list.
state_prs_reviewed_today() {
  local repo="$1" author="$2" cutoff="$3" cap="$4" current_pr="$5" current_markers="$6"
  if [[ "$(jq -r --argjson c "$cutoff" \
      '[.[] | select(.leg == "review" and (.state // "") != "declined" and (.ts // 0) > $c)] | length' \
      <<<"$current_markers")" != "0" ]]; then
    printf '0'
    return 0
  fi

  local seen="[]" page comments count=0 n batch
  for page in 1 2 3 4 5 6 7 8 9 10; do
    if ! comments="$(gh_repo_issue_comments_page "$repo" "$cutoff" "$page")"; then
      ui_warn "could not read repository comments while checking max_prs_per_day" \
        "The backstop rounds down to zero rather than stopping a healthy automatic review early. Check GitHub availability and the token's issues read permission."
      printf '0'
      return 0
    fi
    n="$(jq 'length' <<<"$comments" 2>/dev/null || printf '0')"
    # One jq for the whole page, not one per comment. The per-comment form forked
    # nine times for each of a hundred comments, so a full ten-page read spent
    # about twenty-five seconds in process startup and nothing else.
    #
    # The marker is pulled out here rather than through state_marker_of because
    # this read never touches a migrated key: it looks at leg, state and ts only.
    # The extraction below is the same rule that function's sed applies — per
    # line, the last opening delimiter and the last closing one after it — so a
    # body carrying two markers still concatenates to invalid JSON and is still
    # skipped.
    batch="$(jq -c --arg a "$author" --argjson cutoff "$cutoff" \
      --arg suffix "/$current_pr" --argjson seen "$seen" '
        def marker_of:
          [ (.body // "") | split("\n")[]
            | split("<!-- crossrev: ") | select(length > 1) | last
            | split(" -->") | select(length > 1) | .[:-1] | join(" -->") ]
          | join("") | fromjson? ;
        reduce (.[] | select((.user.login // "") == $a)) as $c ($seen;
          . as $acc
          | ([$c | marker_of] | .[0]) as $m
          | ($c.issue_url // "") as $u
          | if ($m | type) == "object"
               and $m.leg == "review"
               and ($m.state // "") != "declined"
               and ($m.ts // 0) > $cutoff
               and ($u | length) > 0
               and (($u | endswith($suffix)) | not)
               and (($acc | index($u)) == null)
            then $acc + [$u] else $acc end)
      ' <<<"$comments" 2>/dev/null)" || batch=""
    [[ -n "$batch" ]] && seen="$batch"
    count="$(jq 'length' <<<"$seen")"
    # The per-comment loop returned the instant the count reached the cap, so it
    # could never report more than the cap itself. A whole page is folded in at
    # once now, so the cap is what gets printed rather than the overshoot.
    if (( cap > 0 && count >= cap )); then
      printf '%s' "$cap"
      return 0
    fi
    (( n < 100 )) && { printf '%s' "$count"; return 0; }
  done

  ui_warn "the daily review count stopped after the first 10 pages of repository comments" \
    "The bound intentionally rounds down rather than stopping healthy automatic reviews early. The count below includes only the comments crossrev inspected."
  printf '%s' "$count"
}

# ---------------------------------------------------------------------------
# Labels
# ---------------------------------------------------------------------------
#
# The chain is label-driven, so any API failure applying a label leaves the next
# workflow with no event to hear. Absence itself is not the failure: GitHub's
# add-labels endpoint creates a missing label with default metadata.

state_label_add() {
  local pr="$1" repo="$2" label="$3"
  gh api --method POST "repos/$repo/issues/$pr/labels" -f "labels[]=$label" >/dev/null 2>&1 || ui_die \
    "could not apply the label '$label' to $repo#$pr" \
    "The loop is label-driven, so this is fatal rather than cosmetic. Check the token's issues permission and GitHub's availability, then retry."
}

state_label_remove() {
  local pr="$1" repo="$2" label="$3"
  gh api --method DELETE "repos/$repo/issues/$pr/labels/$label" >/dev/null 2>&1 || true
}

state_labels() {
  gh api "repos/$2/issues/$1/labels" --jq '.[].name' 2>/dev/null
}

state_has_label() { state_labels "$1" "$2" | grep -qx "$3"; }
