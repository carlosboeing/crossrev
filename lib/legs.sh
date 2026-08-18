# shellcheck shell=bash
# lib/legs.sh — termination, guards, and the shared shape of both legs.
#
# The decisions here are pure functions over state the orchestrator already
# holds, so they are testable without a network, a harness or a pull request.
# That matters: their failure mode is silence — a loop that stops one pass early
# looks exactly like a loop that converged.

# Should the loop continue? Prints "<decision> <reason>".
#
# Terminates on the first of: the reviewer returns converged; the pass count
# reaches max_passes_per_cycle; a human applies crossrev/stop; the resolver returns
# blocked; the daily run cap is exceeded; the PR is larger than the file cap.
#
# $1 verdict, $2 pass, $3 max_passes_per_cycle, $4 has_stop_label, $5 blocked,
# $6 other_prs_today, $7 max_prs_per_day, $8 files_changed, $9 max_files_changed_per_pr
legs_should_continue() {
  local verdict="$1" pass="$2" max_passes_per_cycle="$3" stop="$4" blocked="$5"
  local other_prs_today="$6" max_prs_per_day="$7" files="$8" max_files_changed_per_pr="$9"

  # A human's request outranks everything, including a healthy verdict.
  # Deliberately checked first: crossrev/stop is an instruction, not a state.
  [[ "$stop" == "true" ]] && { printf 'halt a human applied crossrev/stop'; return 1; }

  [[ "$blocked" == "true" ]] && { printf 'halt the resolver reported blocked'; return 1; }

  # Only findings at or above min_fix_severity keep the loop alive. Anything below it, and
  # anything pre-existing, is reported and cannot prevent convergence.
  [[ "$verdict" == "converged" ]] && { printf 'converged nothing at or above min_fix_severity remains'; return 1; }

  # At exactly the cap, stop. Pass 3 of max_passes_per_cycle 3 is the last pass, not the
  # one after which a fourth begins.
  if (( max_passes_per_cycle > 0 && pass >= max_passes_per_cycle )); then
    printf 'halt reached max_passes_per_cycle (%s)' "$max_passes_per_cycle"; return 1
  fi

  if (( max_prs_per_day > 0 && other_prs_today >= max_prs_per_day )); then
    printf 'halt reached max_prs_per_day (%s) — %s other pull requests were already reviewed in the last 24 hours' \
      "$max_prs_per_day" "$other_prs_today"; return 1
  fi

  if (( max_files_changed_per_pr > 0 && files > max_files_changed_per_pr )); then
    printf 'halt %s files changed, above max_files_changed_per_pr (%s)' "$files" "$max_files_changed_per_pr"; return 1
  fi

  printf 'continue issues remain and no cap is reached'
  return 0
}

# ---------------------------------------------------------------------------
# The fixing threshold
# ---------------------------------------------------------------------------
#
# `min_fix_severity` names the lowest severity the resolve leg may change code for while
# nobody is watching. Everything below it is still reported, still tabled, and
# still replied to — the threshold governs the commit, never the comment.

# An ordinal rank for a severity word. 0 for anything unrecognised, which cannot
# meet any real threshold.
legs_severity_rank() {
  case "$1" in
    high)   printf '3' ;;
    medium) printf '2' ;;
    low)    printf '1' ;;
    *)      printf '0' ;;
  esac
}

# May the resolve leg change code for this finding?
#
# $1 severity, $2 min_fix_severity, $3 pre_existing ("true" or anything else)
#
# Two rules, in order. A pre-existing defect is never fixed here whatever its
# severity — that is the one guardrail deliberately not configurable, because a
# pull request that also fixes old bugs is one nobody can review. Otherwise the
# severity has to reach the threshold.
#
# An unrecognised threshold fixes nothing rather than guessing. The two ways to
# be wrong are not symmetrical: refusing to fix leaves a finding reported and a
# human to read it, while guessing leaves an unattended commit nobody asked for.
legs_should_fix() {
  local severity="$1" min_fix_severity="$2" pre_existing="${3:-false}"
  [[ "$pre_existing" == "true" ]] && return 1
  local bar; bar="$(legs_severity_rank "$min_fix_severity")"
  (( bar == 0 )) && return 1
  (( $(legs_severity_rank "$severity") >= bar ))
}

# The label a leg waits behind.
#
# Not `crossrev/awaiting-$leg`: the leg is named for the verb and the label for
# the noun, so `resolve` waits behind `crossrev/awaiting-resolution`. Derived in
# one place because the workflows key off these strings and a mismatch stalls the
# chain silently — the label sits on the pull request with nothing listening.
legs_awaiting_label() {
  case "$1" in
    review)  printf 'crossrev/awaiting-review' ;;
    resolve) printf 'crossrev/awaiting-resolution' ;;
    *)       printf 'crossrev/awaiting-%s' "$1" ;;
  esac
}

# A fix the resolver claimed and never committed.
#
# `gh_commit_and_push` produces no commit when the tree is unchanged, so a
# `fixed` resolution on a marker carrying no commit_sha is a promise about the
# code that the diff does not keep. The run already warns about it in the
# terminal, and every decision read off the marker has to agree: the finding
# was examined, it was not disputed, and nothing was written for it.
#
# A marker with no resolutions claims no fix either way; that shape is
# legs_resolve_unrecorded's, below.
#
# $1 the completed resolve marker.
legs_resolve_unpushed_fix() {
  local marker="$1"
  [[ -n "$(jq -r '.commit_sha // ""' <<<"$marker")" ]] && return 1
  [[ "$(jq '[(.resolutions // [])[] | select(.resolution == "fixed")] | length' <<<"$marker")" != "0" ]]
}

# A completed pass that recorded no resolutions and pushed no commit.
#
# The leg returns before it writes a resolve marker when its review pass raised
# nothing, so a marker that reached `complete` answered at least one finding.
# One that records none of those answers is a legacy marker, written before the
# leg carried them, and it says nothing about what happened to the findings.
# Silence is not a settle: reading it as one prints a green terminal over a pass
# nobody can check, and stops status offering a remedy for it. Driving the pass
# again is that remedy, and it writes the record the marker is missing.
#
# The commit is read for the same reason the unpushed-fix rule reads it. A
# legacy marker that pushed moved the head, so the reviewer has something new to
# see and awaiting-review is the honest answer there — the question this asks is
# only about a pass that ended with nothing recorded and nothing written.
#
# $1 the completed resolve marker.
legs_resolve_unrecorded() {
  local marker="$1"
  [[ -n "$(jq -r '.commit_sha // ""' <<<"$marker")" ]] && return 1
  [[ "$(jq '(.resolutions // []) | length' <<<"$marker")" == "0" ]]
}

# May a completed resolve pass be driven again?
#
# Escalating or reporting blocked completes a pass rather than abandoning it —
# the resolutions are on the pull request and the threads carry the reasoning —
# and completion is what the re-run guard reads, so without this a pass that
# ended either way could never run again once whatever stopped it was fixed.
# Both endings leave something undecided, so both admit a re-drive. So does a
# deferral whose record never landed, a fix that reached no commit, and a pass
# that recorded no resolutions at all — the last one because a marker that
# says nothing about its findings cannot say they are settled either. A pass
# that settled every finding stays refused: running it again would re-decide
# work that is done.
#
# $1 the completed resolve marker.
legs_resolve_redrivable() {
  local marker="$1"
  [[ "$(jq -r '.blocked // false' <<<"$marker")" == "true" ]] && return 0
  [[ "$(jq '[(.resolutions // [])[] | select(.resolution == "escalated")] | length' <<<"$marker")" != "0" ]] && return 0
  # A finding the resolver said it fixed and did not is undecided in the same
  # way: what stopped the write was the environment or the model, and once a
  # person has dealt with it, running the leg again is the remedy.
  legs_resolve_unpushed_fix "$marker" && return 0
  # A legacy marker recorded no answers at all, so nothing on it is settled
  # either. Re-running the leg over the same findings is what writes the record
  # a reader — and every decision above — needs.
  legs_resolve_unrecorded "$marker" && return 0
  # A deferral whose record never landed left its thread open on purpose, so
  # the pass is not settled either — once the filing is fixed, driving the
  # pass again is the remedy. `.crossrev_tracked == ""` matches only a marker
  # that records the field; a deferral without it is a legacy marker, or one
  # written with no backlog configured, and both read as settled.
  [[ "$(jq '[(.resolutions // [])[] | select(.resolution == "deferred" and .crossrev_tracked == "")] | length' <<<"$marker")" != "0" ]]
}

# The loop-state label a finished resolve pass leaves behind.
#
# Same question legs_pass_label answers for the review leg, asked of the
# resolve leg's own record, and read off the marker rather than recomputed
# beside it so the label and the marker cannot disagree. A pass that pushed
# hands back to the reviewer, because the head moved and there is something
# new to see — and the push, not the resolution, is the signal: a deferral
# recorded into the repository backlog moves the head without fixing anything.
#
# A pass that settled every finding without pushing — each disputed, skipped,
# or deferred and tracked — is over. The reviewer declines an unchanged head,
# so awaiting-review would park the loop on a command that refuses, and
# converged is the honest label: nothing at or above the threshold remains.
# The findings were examined and found not to be defects, or their work lives
# in a tracked issue off this pull request.
#
# Five endings still halt, and each outranks a settle. Blocked and escalated
# are the existing ones — a dispute beside an escalation is a halt, the
# escalation wins, and an escalation left standing by an EARLIER pass halts a
# later settle just the same. The third is a deferral whose record never
# landed despite a backlog configured to land it in: its thread stays open on
# purpose, and a green label over an open thread is how work disappears, so a
# human has to put it somewhere durable first.
#
# The fourth is a fix the resolver claimed and never committed. Nothing was
# settled there and nothing was written: the finding is real by the resolver's
# own answer, the code is unchanged, and converged would tell a reader nothing
# at or above the threshold remains over exactly the defect the reviewer
# raised. It is not awaiting-review either, because the head never moved — so
# it halts, and the thread the leg leaves open is what a person reads.
#
# The fifth is a pass that recorded no resolutions and pushed nothing. Every
# settle above is read off the resolutions, so a marker that carries none
# cannot be shown to have settled anything: converging there would print the
# green terminal on the strength of a missing record. It halts instead, and
# legs_resolve_redrivable admits it, so the remedy is driving the pass again.
#
# $1 the completed resolve marker. $2 escalated findings standing in other
# passes' markers — this pass's own are read off the marker, and the caller
# may hold a newer record of this pass than the marker list does.
legs_resolve_pass_label() {
  local marker="$1" other_escalated="${2:-0}"
  if [[ "$(jq -r '.blocked // false' <<<"$marker")" == "true" ]] \
     || [[ "$(jq '[(.resolutions // [])[] | select(.resolution == "escalated")] | length' <<<"$marker")" != "0" ]] \
     || (( other_escalated > 0 )) \
     || legs_resolve_unpushed_fix "$marker" \
     || legs_resolve_unrecorded "$marker" \
     || [[ "$(jq '[(.resolutions // [])[] | select(.resolution == "deferred" and .crossrev_tracked == "")] | length' <<<"$marker")" != "0" ]]; then
    printf 'halted'
  elif [[ -z "$(jq -r '.commit_sha // ""' <<<"$marker")" ]]; then
    printf 'converged'
  else
    printf 'awaiting-review'
  fi
}

# The loop-state label a finished review pass leaves behind.
#
# An empty pass after an escalation is not a convergence. The reviewer
# correctly declines to re-raise a settled finding, so nothing actionable
# means nothing NEW — while the escalated thread still waits on a human, and
# halted is the honest label beside a marker that reads issues-remain. The
# converged verdict arm is exempt: a reviewer that says converged after the
# human settled the thread is the settlement being verified, which is the one
# way out of the halt that does not need a person again.
#
# $1 verdict, $2 actionable finding count, $3 open escalations count.
legs_pass_label() {
  local verdict="$1" actionable="$2" escalated="$3"
  case "$verdict" in
    converged) printf 'converged' ;;
    blocked)   printf 'halted' ;;
    *) if (( actionable > 0 )); then printf 'awaiting-resolution'
       elif (( escalated > 0 )); then printf 'halted'
       else printf 'converged'; fi ;;
  esac
}

# The colour a loop label is minted with.
#
# Six hues, no two adjacent on the wheel, so the label row on a pull request
# carries state at a glance rather than a row of identical purple pills. Red is
# reserved for `crossrev/stop`, the one label a human applies — a red pill in a
# pull request list always means somebody pulled the brake, never that the loop
# had trouble.
#
# Dark enough that GitHub renders the label text white. It picks by brightness
# and cannot be told otherwise, so the background is the only lever — and the
# lighter first palette left `crossrev/pass-N` at 4.42:1 on a pull request chip,
# under the 4.5:1 the accessibility standard asks for. Every colour here clears
# that in all three renderings GitHub uses: the solid pill on the labels page,
# and the tinted chip in light and dark themes.
#
# One Primer step darker again reads better on the labels page and fails in dark
# mode, where GitHub derives the chip's text from this same hex. That is the wall
# the design hit when it rejected near-black for `stop`, one step further along.
#
# One map rather than a constant per call site. The watchdog mints
# `crossrev/halted` itself when it gives up, and a second hex there is how the
# same label ends up two colours depending on which code path created it.
legs_label_colour() {
  case "$1" in
    crossrev/awaiting-review)     printf '0969da' ;;   # blue, in progress
    crossrev/awaiting-resolution) printf '8250df' ;;   # purple, a leg owes an answer
    crossrev/converged)           printf '1a7f37' ;;   # green, finished on its own
    crossrev/halted)              printf 'bc4c00' ;;   # orange, a human is needed
    crossrev/stop)                printf 'cf222e' ;;   # red, a human applied it
    crossrev/pass-*)              printf '57606a' ;;   # grey, informational
    # Not a loop state and not one of the six: the watchdog's own bookkeeping,
    # which reads as a qualifier on whatever state it sits beside.
    crossrev/watchdog-retried)    printf 'fbca04' ;;
    *)                           printf 'ededed' ;;
  esac
}

# What the label means, in the words the design uses for it.
#
# Per label rather than one string for all of them, because a label description
# is the only place GitHub shows a reader what a label means without them going
# looking — it is the hover text on the pill and the second column on the labels
# page. "crossrev loop state" on all six answered nothing, and the colours are
# the only other signal a reader has.
#
# One map rather than a constant per call site, for the same reason the colours
# are one map: the watchdog mints `crossrev/halted` itself when it gives up, and a
# second string there is how the same label ends up described two ways depending
# on which code path created it.
legs_label_description() {
  case "$1" in
    crossrev/awaiting-review)     printf 'crossrev: a review is owed on this pull request' ;;
    crossrev/awaiting-resolution) printf 'crossrev: the review landed, the resolve leg is owed' ;;
    crossrev/converged)           printf 'crossrev: the loop finished on its own' ;;
    crossrev/halted)              printf 'crossrev: stopped short, a human is needed' ;;
    crossrev/stop)                printf 'crossrev: apply this to stop the loop' ;;
    crossrev/pass-*)              printf 'crossrev: reached pass %s' "${1##*-}" ;;
    crossrev/watchdog-retried)    printf 'crossrev: the watchdog retried this leg once' ;;
    *)                           printf 'crossrev loop state' ;;
  esac
}

# owner/repo for a github.com URL, or non-zero for anything else.
#
# Substring matching is not enough here, because the strings this rejects are
# the ones that matter: `https://github.com.example.net/a/b` and a local path
# holding `github.com/a/b` both contain the host and are not it. So the host is
# isolated and compared whole, in every shape git accepts a remote in — https,
# ssh://, git://, and the scp-style `git@github.com:owner/repo.git`.
legs_github_slug() {
  local url="$1" rest host path
  if [[ "$url" == *"://"* ]]; then
    rest="${url#*://}"
  elif [[ "$url" == *:* && "${url%%:*}" != */* ]]; then
    # scp-style. The first colon separates host from path; a colon after a slash
    # is part of a local path, which is why the guard above tests for one.
    rest="${url%%:*}/${url#*:}"
  else
    return 1
  fi
  # Drop userinfo only when the `@` is in the authority, never when it is a
  # character in the path.
  if [[ "${rest%%/*}" == *@* ]]; then rest="${rest#*@}"; fi
  host="${rest%%/*}"
  host="${host%%:*}"
  path="${rest#*/}"
  [[ "$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]')" == "github.com" ]] || return 1
  path="${path%/}"
  path="${path%.git}"
  [[ "$path" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]] || return 1
  printf '%s' "$path"
}

# The repository `git push <remote>` would write to, into LEGS_PUSH_REPO.
#
# `git remote get-url --push` cannot answer this. A remote may carry several
# `remote.<name>.pushurl` entries and git pushes to every one of them, but that
# command returns only the first — so a second entry pointing somewhere else is
# invisible to anything that reads it. When no pushurl is set at all, git pushes
# to the `remote.<name>.url` entries instead, so those are the fallback.
#
# A URL that does not resolve to a github.com slug is refused rather than
# swapped for one that does. Checking the fetch URL because the push URL is
# unreadable validates an address the commit will never reach, which is exactly
# the configuration the guard exists to catch.
#
# What this does not cover, stated rather than implied: `git config` returns the
# configured URL, and git resolves `url.<base>.pushInsteadOf` after reading it.
# A rewrite rule can still redirect a URL this approved. Reading the effective
# URLs instead would close that and break every offline test that pushes, since
# a fixture's effective push URL is a local path and no local path resolves to a
# github.com slug. Closing it needs a decision about the suite, not a one-liner.
#
# Sets LEGS_PUSH_REPO empty when the remote has no URL at all; the caller owns
# that message, because what to say about it depends on how the remote was
# resolved. Deliberately not printed from a command substitution: it dies on a
# bad URL, and a subshell would swallow the exit.
LEGS_PUSH_REPO=""
legs_resolve_push_repo() {
  local remote="$1" urls url slug repo=""
  urls="$(git config --get-all "remote.${remote}.pushurl" 2>/dev/null || true)"
  [[ -n "$urls" ]] || urls="$(git config --get-all "remote.${remote}.url" 2>/dev/null || true)"

  while IFS= read -r url; do
    [[ -n "$url" ]] || continue
    slug="$(legs_github_slug "$url")" || ui_die \
      "remote '$remote' pushes to '$url', which is not a github.com repository URL" \
      "CrossRev checks where a fix will land before it leaves the machine, and it can only do that for a github.com URL. Check \`git config --get-all remote.$remote.pushurl\`."
    if [[ -z "$repo" ]]; then
      repo="$slug"
    elif [[ "$repo" != "$slug" ]]; then
      ui_die \
        "remote '$remote' pushes to two different repositories, '$repo' and '$slug'" \
        "git pushes to every \`remote.$remote.pushurl\` entry, and CrossRev pushes only to the head repository of the pull request under review. Remove the entry that does not belong."
    fi
  done <<EOF
$urls
EOF

  # shellcheck disable=SC2034  # read by run.sh and github.sh
  LEGS_PUSH_REPO="$repo"
}

# Refuse to push anywhere except the PR's own head branch.
#
# Branch protection is a backstop, not a control: it fires after a bad push is
# attempted and says nothing about which branch was targeted. This asserts the
# target before anything leaves the machine.
legs_assert_push_target() {
  # `${n-…}` rather than `${n:-…}`: the defaults are for a caller that passes
  # five arguments, not for one that passes an empty value it could not read. An
  # empty cross-repository flag has to stay empty, because "false" here means
  # "this repository's own branch" and would skip the maintainer-edit check.
  local current_branch="$1" head_branch="$2" default_branch="$3" head_repo="$4" origin_repo="$5" maintainer_can_modify="${6-false}" is_cross_repo="${7-false}"

  [[ "$current_branch" == "$head_branch" ]] || ui_die \
    "the checkout is on '$current_branch' but the pull request's head branch is '$head_branch'" \
    "crossrev pushes only to the branch under review. Check out $head_branch first."

  [[ "$head_branch" != "$default_branch" ]] || ui_die \
    "the pull request's head branch is the repository default branch ('$default_branch')" \
    "crossrev refuses to push to a default branch. Re-open the pull request from a feature branch."

  [[ -n "$head_repo" ]] || ui_die \
    "could not determine the head repository for this pull request" \
    "The pull request metadata is missing head repository information."

  [[ "$head_repo" == "$origin_repo" ]] || ui_die \
    "the pull request's head is in '$head_repo' but this checkout pushes to '$origin_repo'" \
    "crossrev pushes only to the head repository of the pull request under review."

  # Anything other than an explicit "false" — a fork, or provenance that could
  # not be read — needs the contributor's permission before a push.
  if [[ "$is_cross_repo" != "false" ]]; then
    [[ "$maintainer_can_modify" == "true" ]] || ui_die \
      "the contributor has not allowed maintainer edits on $head_repo" \
      "The contributor has not allowed maintainer edits on this pull request, so the fix cannot be pushed."
  fi

  return 0
}

# Assert the two legs will actually run differently.
#
# Layer one, always available: the orchestrator knows exactly what it invoked,
# so it asserts the legs differ in binary, resolved base URL or model — and that
# the endpoint variables are unset in the inherited environment, which is the
# specific failure this exists for. Leakage from one leg into the next is then
# caught exactly, with no cooperation needed from any vendor.
legs_assert_env_clean() {
  local leaked=()
  [[ -n "${ANTHROPIC_BASE_URL:-}" ]] && leaked+=("ANTHROPIC_BASE_URL")
  [[ -n "${ANTHROPIC_AUTH_TOKEN:-}" ]] && leaked+=("ANTHROPIC_AUTH_TOKEN")
  (( ${#leaked[@]} == 0 )) && return 0
  ui_die "these endpoint variables are set in the environment CrossRev inherited: ${leaked[*]}" \
    "They redirect the harness process-wide, so a leg would silently run on the wrong model and the loop would complete normally with no error anywhere. Unset them; crossrev sets them per invocation."
}

# The part of a harness's stderr worth showing, capped at $2 bytes.
#
# `head -c 400` reads the wrong end of the stream. Every harness opens with a
# banner — version, workdir, model, sandbox, session id — and codex echoes the
# prompt after it, so on a real leg the first 400 bytes are banner and diff and
# the error is nowhere in them. Measured on a captured unauthenticated run: the
# first "401" sat at byte 402, two bytes past the window, with a two-word prompt.
# A review prompt pushes it thousands of bytes out.
#
# So: search for a line that looks like a diagnosis, and prefer the last one.
# Harnesses retry, and the final message is the one that stuck — codex reports
# the same 401 nine times and only the last carries the reason phrase. Falling
# back to the tail rather than the head keeps that property when no keyword
# matches, because the banner is always at the top and never worth the budget.
legs_harness_error() {
  local err="$1" cap="${2:-400}" picked trimmed
  [[ -s "$err" ]] || return 1
  picked="$(grep -iE 'error|fatal|denied|unauthor|forbidden|invalid|expired|timed out|refused|not found' \
    "$err" 2>/dev/null | tail -2)"
  [[ -n "$picked" ]] || picked="$(tail -3 "$err")"

  # Trimming to the cap takes the end, for the same reason the search does. A cut
  # that lands mid-line then drops that partial line, so the output never opens
  # halfway through a sentence — unless dropping it would leave nothing, which is
  # one line longer than the whole budget and better shown cut than not at all.
  if (( ${#picked} > cap )); then
    picked="${picked: -cap}"
    trimmed="${picked#*$'\n'}"
    [[ "$picked" == *$'\n'* && -n "$trimmed" ]] && picked="$trimmed"
  fi
  printf '%s' "$picked"
}

# Layer one's other half: did the two legs differ in anything the orchestrator
# controls? Prints "same" or "different".
legs_configured_difference() {
  local r_harness="$1" r_endpoint="$2" r_model="$3"
  local s_harness="$4" s_endpoint="$5" s_model="$6"
  if [[ "$r_harness" != "$s_harness" || "$r_endpoint" != "$s_endpoint" || "$r_model" != "$s_model" ]]; then
    printf 'different'
  else
    printf 'same'
  fi
}

# Layer two, where the harness reports it: compare answering models.
#
# Do not halt merely because a harness reports none — that would disqualify the
# codex adapter for a field Codex does not emit. What this adds is detection of
# *server-side* substitution; where it is unavailable the marker records its
# absence rather than implying a check that never ran.
legs_assert_models_diverged() {
  local configured="$1" reviewer_model="$2" resolver_model="$3"
  [[ "$configured" == "different" ]] || return 0          # one model was asked for
  [[ -n "$reviewer_model" && "$reviewer_model" != "null" ]] || return 0
  [[ -n "$resolver_model" && "$resolver_model" != "null" ]] || return 0
  [[ "$reviewer_model" != "$resolver_model" ]] || ui_die \
    "both legs were configured to differ but the same model answered each: $reviewer_model" \
    "This is the failure the cross-model design exists to prevent, and it completes normally when unchecked. Check the endpoint block and that no endpoint variable is exported."
}
