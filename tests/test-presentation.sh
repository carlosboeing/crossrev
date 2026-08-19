#!/usr/bin/env bash
#
# What crossrev shows a human: the emoji, the one native alert per summary
# comment, the run-details table, and the wording correction underneath all of
# it.
#
# The correction is checked through a real run rather than by grepping the
# source, and that is the point of putting it here. Three occurrences of "a
# different model" in this repository are correct — a test name, a code comment
# and a note in the config template — so a repository-wide grep would either
# fail on all three or be weakened until it caught nothing. Running both legs
# reaches every emitted site instead: the inline comment, the review summary,
# and both prompts, which reproduce the two skill files verbatim.

set -uo pipefail
# shellcheck source=harness.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/harness.sh"

ID_SEC="aaaa000000000001"
ID_PRE="bbbb000000000002"

# One finding of each shape the rendering has to handle: above the threshold,
# below it, and pre-existing. The pipe in the third title is deliberate — a
# title carrying a table delimiter must not break the table it lands in.
REVIEW_PAYLOAD='{"verdict":"issues-remain","blocked_reason":null,"prior":null,"findings":[
  {"path":"app.ts","line":2,"side":"RIGHT","severity":"high","category":"security","pre_existing":false,
   "title":"Token compared with == instead of a constant-time check",
   "why":"A timing side channel leaks the token","fix":"Use timingSafeEqual"},
  {"path":"app.ts","line":1,"side":"RIGHT","severity":"low","category":"maintainability","pre_existing":false,
   "title":"Third copy of the same date helper","why":"Drift between copies","fix":"Extract one"},
  {"path":"app.ts","line":1,"side":"RIGHT","severity":"medium","category":"correctness","pre_existing":true,
   "title":"Off-by-one in the drain loop | second clause",
   "why":"Drops the last item","fix":"Use <= in the bound"}
]}'

CONVERGED_PAYLOAD='{"verdict":"converged","blocked_reason":null,"prior":null,"findings":[]}'
BLOCKED_PAYLOAD='{"verdict":"blocked","blocked_reason":"the diff would not fetch","prior":null,"findings":[]}'

no_threads() {
  route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
}

# One review leg against a canned payload. $2 is an alternative repository
# config, for the one case that turns on what the config asked for. Leaves the
# call log and the prompt log in place for the caller to assert on.
run_review() {
  fixture_repo "${2:-$(fixture_default_config)}"; stub_reset
  routes_baseline "$(printf '[]' | payload)"
  route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
  no_threads
  CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$1" | payload)"; export CROSSREV_REVIEW_PAYLOAD
  "$CROSSREV" review --pr 42 >/dev/null 2>&1
}

# A completed review marker carrying the same three findings, so the resolve leg
# has something to resolution.
review_marker() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(( $(date +%s) - 192 ))" \
    --arg s "$ID_SEC" --arg p "$ID_PRE" '
    {v:1, leg:"review", pass:1, state:"complete", ts:$ts, done_ts:($ts + 192), run_id:"1",
     head_sha:$sha, harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     effort:"high", endpoint:null, tokens:41205, verdict:"issues-remain",
     findings:[
       {id:$s, path:"app.ts", line:2, side:"RIGHT", severity:"high", category:"security",
        pre_existing:false, title:"Token compared with == instead of a constant-time check",
        why:"w", fix:"f", anchor:"", thread_id:"T_SEC", resolution:null, tracked_as:null},
       {id:$p, path:"app.ts", line:1, side:"RIGHT", severity:"medium", category:"correctness",
        pre_existing:true, title:"Off-by-one in the drain loop | second clause",
        why:"w", fix:"f", anchor:"", thread_id:"T_PRE", resolution:null, tracked_as:null}]}'
}

# Findings are named by the number the prompt gave them, in the order the review
# marker above lists them: 1 is ID_SEC, 2 is ID_PRE.
resolve_payload() {
  jq -cn '
    {blocked:false, blocked_reason:null,
     summary:"Replaced the comparison. The drain loop predates this branch.",
     resolutions:[
       {finding_number:1, resolution:"fixed", reply:"Replaced with a constant-time compare.",
        persist:null, duplicate_of:null},
       {finding_number:2, resolution:"skipped", reply:"Pre-existing, so it is reported not fixed.",
        persist:null, duplicate_of:null}]}'
}

# One resolve leg over that marker.
# $2 replaces the thread routing, for the one case that has no threads at all.
run_resolve() {
  fixture_repo; stub_reset
  routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
  route 'api --method POST repos/*/issues/42/comments*' '{"id":9002}'
  if [[ -n "${2:-}" ]]; then "$2"; else
    route '*reviewThreads*' "$(threads_response \
      "$(thread_node T_SEC app.ts 2 false "$ID_SEC")" \
      "$(thread_node T_PRE app.ts 1 false "$ID_PRE")")"
  fi
  route '*resolveReviewThread*' '{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}'
  route 'api --method POST repos/*/pulls/42/comments/*/replies*' '{"id":6001}'
  CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$1" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
  CROSSREV_RESOLVE_MODEL=resolver-model; export CROSSREV_RESOLVE_MODEL
  "$CROSSREV" resolve --pr 42 >/dev/null 2>&1
}

# ---------------------------------------------------------------------------
# The correction: nothing emitted claims a second MODEL
# ---------------------------------------------------------------------------
#
# `legs_assert_models_diverged` short-circuits when both legs were configured
# with the same model, so configuring them identically is permitted and the loop
# runs normally. Every emitted claim of a *different model* is therefore a claim
# crossrev cannot back. Agent-shaped wording is true in every configuration and
# still carries the independent-second-look property.

run_review "$REVIEW_PAYLOAD"
review_calls="$(calls)"
review_prompt="$(cat "$PROMPT_LOG")"
# Every run_* rebuilds the fixture, so FIX_HEAD has moved on by the time the
# assertions below run. Keep the sha that actually produced these calls.
review_head="$FIX_HEAD"

hasnt "the inline comment claims no second model"     "$review_calls" "second model"
hasnt "nor a different one"                           "$review_calls" "different model"
has   "it claims a second agent instead"              "$review_calls" "A second agent now verifies"
has   "and the summary hands over to one"             "$review_calls" "A second agent now verifies every finding below"
hasnt "the review prompt makes no model claim either" "$review_prompt" "two-model loop"
has   "the review skill says two-agent"               "$review_prompt" "two-agent loop"

run_resolve "$(resolve_payload)"
resolve_calls="$(calls)"
resolve_prompt="$(cat "$PROMPT_LOG")"

hasnt "the resolve prompt claims no different model"  "$resolve_prompt" "different model"
has   "it names the review leg as a separate agent"   "$resolve_prompt" "a separate agent"
has   "and the resolve skill agrees"                  "$resolve_prompt" "a second agent that reviewed it"

# ---------------------------------------------------------------------------
# Decision 1 — emoji for severity and category
# ---------------------------------------------------------------------------

# Severity dot, bracketed label, title, all on one line and promoted to a
# heading. It sits in a narrow diff column, which is why the category stays a
# word and only the severity carries a glyph.
has "the inline heading is a heading, so the finding has an edge against its own explanation" \
  "$review_calls" "#### 🔴 [High · Security] Token compared"
hasnt "and does not put the category emoji there too" "$review_calls" "🔴 🔒"
# Brackets rather than bold: with the emphasis off the label, they are the only
# thing delimiting it, so they carry weight instead of repeating it.
hasnt "the label is not bolded beside a plain title" "$review_calls" "**High · Security**"

# Provenance is not a fourth severity. It belongs where there is room to say what
# it means, which is the table's *what* cell and the note under the finding.
hasnt "the heading carries no provenance"             "$review_calls" "· Security · pre-existing"
has   "the table's what cell does, muted"             "$review_calls" "<sub>· pre-existing</sub>"

has "the findings table names all four columns"       "$review_calls" "| Severity | Category | Finding | Location |"
has "a high security finding carries both glyphs"     "$review_calls" "| 🔴&nbsp;High | 🔒&nbsp;Security |"
has "a low maintainability one carries its own"       "$review_calls" "| 🔵&nbsp;Low | 🧹&nbsp;Maintainability |"
has "and a medium correctness one"                    "$review_calls" "| 🟠&nbsp;Medium | 🐛&nbsp;Correctness |"

# A model-written title carrying a pipe splits the row into extra columns, and the
# surrounding rows still render — so the table is quietly wrong rather than
# obviously broken. Escaped at the cell, not hoped away in the prompt.
has "a pipe in a title is escaped rather than splitting the row" \
  "$review_calls" 'Off-by-one in the drain loop \| second clause'

# Severity and Category are the narrowest columns, so an ordinary space between
# glyph and word is a break opportunity the renderer takes — the emoji ends up
# orphaned on a line above its own label.
hasnt "no breakable space is left between a glyph and its word" \
  "$review_calls" "| 🔴 High |"

# The location has a column of its own, so it carries the full path rather than a
# basename with the path hidden in a hover title. Two files sharing a name are
# tellable apart without hovering, and the text stays readable where a link fails.
has "the location cell links the full path and line" \
  "$review_calls" '[`app.ts:2`](https://github.com/acme/widget/blob/'
has "and the link is pinned to the revision that was reviewed, not to the branch" \
  "$review_calls" "/blob/$review_head/app.ts#L2"
hasnt "the path is not demoted to a hover title"      "$review_calls" '#L2 "app.ts:2")'

# A path is a cell like any other, and the characters that break one are all
# legal in a path: `src/a|b (old)/app.ts` is a real path on any POSIX
# filesystem, and a finding's path is a model-written string nothing upstream
# holds to more than "not empty". The pipe splits the row, and in the
# destination beside it the space ends the link and the bracket closes it early
# — each of them quietly, with every row around it still rendering.
HOSTILE_PATH_PAYLOAD='{"verdict":"issues-remain","blocked_reason":null,"prior":null,"findings":[
  {"path":"src/a|b (old)/app.ts","line":2,"side":"RIGHT","severity":"high","category":"correctness",
   "pre_existing":false,"title":"Unchecked fetch response","why":"w","fix":"f"}
]}'
run_review "$HOSTILE_PATH_PAYLOAD"
path_calls="$(calls)"
path_head="$FIX_HEAD"

has "a pipe in a directory name is escaped rather than splitting the row" \
  "$path_calls" '[`src/a\|b (old)/app.ts:2`]'
# Escaped inside the code span, because GFM resolves `\|` before the span is
# parsed — backticks are no protection at all here.
hasnt "and no raw pipe is left in the cell"           "$path_calls" '`src/a|b'
has "the destination percent-encodes every one of the three" \
  "$path_calls" "/blob/$path_head/src/a%7Cb%20%28old%29/app.ts#L2"
hasnt "so none of them reaches the URL raw"           "$path_calls" "(old)/app.ts#L2"

# ---------------------------------------------------------------------------
# The reply lead belongs to the orchestrator, not to the model
# ---------------------------------------------------------------------------
#
# The resolve prompt hands the model "the conversation so far", which contains
# earlier replies opening with exactly these words — so the model reads the house
# style off the pull request, reproduces it, and crossrev prefixes its own on top.
# Seven replies on this repository's own PR 5 read "**Fixed.** **Fixed.** …".
#
# Checked through a real leg rather than against the helper alone, because the
# defect was never in the helper: it was in nothing owning the opening.
run_resolve "$(resolve_payload | jq -c '
  .resolutions[0].reply = "**Fixed.** Replaced with a constant-time compare."
  | .resolutions[1].reply = "Skipped. Pre-existing, so it is reported not fixed."')"
lead_calls="$(calls)"

hasnt "a lead the model wrote itself is not stacked on crossrev's" \
  "$lead_calls" "**Fixed.** **Fixed.**"
has   "crossrev's own lead is what survives"           "$lead_calls" "**Fixed.** Replaced with a constant-time compare."
# The model's own word is dropped whether or not it was bolded, and whether or
# not it agreed with the resolution the orchestrator settled on.
hasnt "an unbolded lead is dropped too"               "$lead_calls" "**Skipped.** Skipped."
has   "and the resolution crossrev decided is the one shown" \
  "$lead_calls" "**Skipped.** Pre-existing, so it is reported not fixed."

# A reply that merely begins with the word is left alone. The trailing period is
# what separates a lead from a sentence, and without it "Fixed the comparison"
# would lose its first word.
run_resolve "$(resolve_payload | jq -c '
  .resolutions[0].reply = "Fixed the comparison so it is constant-time now."')"
has "a reply that opens with the word but not the lead keeps it" \
  "$(calls)" "**Fixed.** Fixed the comparison so it is constant-time now."

run_resolve "$(resolve_payload)"
resolve_calls="$(calls)"

has "the resolve table names all four columns" \
  "$resolve_calls" "| Severity | Finding | Location | Resolution |"
has "and names a finding by its title, with severity beside it" \
  "$resolve_calls" "| 🔴&nbsp;High | Token compared with == instead of a constant-time check"
# The id is a correlation key for crossrev's own state. It lives in a marker that
# renders invisible, so a reader who sees it cannot even search the page for it —
# printing it spends a column on a dead end.
hasnt "and never prints the finding id at the reader" \
  "$resolve_calls" "<sub>\`$ID_SEC\`</sub>"

# The reader's question at this table is what happened to the finding, and the
# answer is the conversation — so the location links the thread rather than the
# code. The files-tab anchor, not the conversation-tab one: `#discussion_r<id>`
# never scrolls on a first load, because the timeline renders after the browser
# has already applied the fragment.
has "the location links the review thread it was settled in" \
  "$resolve_calls" '[`app.ts:2`](https://github.com/acme/widget/pull/42/files#r5000)'
hasnt "not the conversation tab, which does not scroll on a first load" \
  "$resolve_calls" "#discussion_r"

# A finding GitHub refused to anchor has no thread to link. The location still
# has to be reachable, so it falls back to the code permalink rather than losing
# the link entirely.
run_resolve "$(resolve_payload)" no_threads
no_thread_calls="$(calls)"
has "a finding with no thread falls back to the code permalink" \
  "$no_thread_calls" '[`app.ts:2`](https://github.com/acme/widget/blob/'
# The positive above would pass on the review comment's own links alone, which
# this leg re-renders. The absence is what proves the fallback fired: a thread
# anchor can only come from _thread_url.
hasnt "and no thread anchor is invented for it" \
  "$no_thread_calls" "files#r"

# ---------------------------------------------------------------------------
# Decision 2 — exactly one native alert per summary comment
# ---------------------------------------------------------------------------
#
# One is the whole point. Stacking callouts turns the comment into wallpaper and
# destroys the thing that made the first one work, so the count is asserted
# rather than only the kind.

# "Exactly one" has to be counted against the comment a reader sees, not against
# the call log: the gh stub logs one line per line of its arguments, and a summary
# comment is edited more than once on its way to being finished, so the whole log
# holds several copies of the same body.
last_body() {
  awk -v pat="issues/comments/$1 -f body=" '
    index($0, pat)  { buf = substr($0, index($0, pat) + length(pat)); open = 1; next }
    open && /^(api|gh|repo|pr|secret|issue) / { last = buf; open = 0 }
    open            { buf = buf "\n" $0 }
    END             { if (open) last = buf; print last }' "$GH_LOG"
}
alerts_in() { grep -c '^> \[!' <<<"$1" || true; }

# Re-run the review so its log is the current one, then read the comment it left.
run_review "$REVIEW_PAYLOAD"
review_body="$(last_body 9001)"

is  "a review that found things carries exactly one alert" "$(alerts_in "$review_body")" "1"
has "and it is the red one"                           "$review_body" "> [!CAUTION]"
has "which says how many need resolving"              "$review_body" "**3 findings need resolving.**"
has "and what happens to them next"                   "$review_body" "at or above \`min_fix_severity\`"
# The verdict used to sit at the foot of the comment, under a table, which is the
# last place anyone looks for the answer to "what happens now".
hasnt "the verdict no longer trails the table as prose" "$review_body" "**Next:** a second agent"

run_review "$CONVERGED_PAYLOAD"
converged_body="$(last_body 9001)"
is  "a converged review carries exactly one alert"    "$(alerts_in "$converged_body")" "1"
has "and it is the green one"                         "$converged_body" "> [!TIP]"
has "which says the loop stopped on its own"          "$converged_body" "**Converged.**"
# The vocabulary has to match what `crossrev status` says about the same state.
has "in the same words the terminal uses"             "$converged_body" "Nothing at or above \`min_fix_severity\`"
hasnt "no caution is stacked underneath it"           "$converged_body" "> [!CAUTION]"

run_review "$BLOCKED_PAYLOAD"
blocked_body="$(last_body 9001)"
is  "a blocked review carries exactly one alert"      "$(alerts_in "$blocked_body")" "1"
has "and it is the amber one"                         "$blocked_body" "> [!WARNING]"
has "saying a human is needed"                        "$blocked_body" "a human is needed"
has "and that nothing here judges the code"           "$blocked_body" "Nothing in this comment is a judgement about the code"

run_resolve "$(resolve_payload)"
resolve_body="$(last_body 9002)"
is  "a normal resolve pass carries exactly one alert" "$(alerts_in "$resolve_body")" "1"
has "and it is the blue one"                          "$resolve_body" "> [!NOTE]"
has "carrying the resolution counts"                 "$resolve_body" "**1 fixed, 1 skipped.**"

# Blocked halts, and it needs the amber alert rather than the blue one — a reader
# has to be able to tell a pass that finished from one that stopped without
# working through the table.
run_resolve "$(resolve_payload | jq -c '.blocked = true | .blocked_reason = "one fix needs a schema migration nobody has approved."')"
blocked_resolve="$(last_body 9002)"
is  "a blocked resolve pass still carries exactly one alert" "$(alerts_in "$blocked_resolve")" "1"
has "and it is amber rather than the ordinary blue"   "$blocked_resolve" "> [!WARNING]"
has "naming what stopped"                             "$blocked_resolve" "schema migration nobody has approved"
is  "and the halt is stated once, not once in the alert and again below it" \
  "$(grep -c 'The loop halts here and needs a human' <<<"$blocked_resolve" || true)" "1"

# ---------------------------------------------------------------------------
# Decision 4 — the run-details table
# ---------------------------------------------------------------------------

rows_in() { grep -c '^| \(review\|resolve\) |' <<<"$1" || true; }

has "the review comment carries a run-details block" "$review_body" "**Run details**"
has "with the four columns"                          "$review_body" "| Leg | Agent | Duration | Tokens |"
is  "and exactly one row"                            "$(rows_in "$review_body")" "1"
has "for its own leg"                                "$review_body" "| review | \`claude\`"
# Harness, model and effort describe one thing — which agent ran — so they are
# one cell rather than three rows.
has "harness and model share the cell"               "$review_body" "\`claude\` · \`reviewer-model\`"
# Six figures of tokens is the one cell a reader would otherwise have to count.
has "the token count is grouped"                     "$review_body" "41,505"

# Unconditional. A pass that finds nothing still spends time and tokens, and that
# is exactly when you want to know — so the block appears on a converged comment
# with an empty findings list too.
has "a converged pass still reports what it cost"    "$converged_body" "**Run details**"
is  "with the same single row"                       "$(rows_in "$converged_body")" "1"
has "and a blocked one does as well"                 "$blocked_body" "**Run details**"

# Cost is deliberately absent rather than a blank column that reads as zero, and
# the sentence says what crossrev knows rather than what the run cost. A leg can
# be authenticated as a subscription, as a vendor API key, or as a named
# endpoint that charges per token, so a footnote claiming there is nothing to pay
# would be wrong on two of the three.
has "cost is named as absent rather than left blank" \
  "$review_body" "crossrev is given no billing figure by the harness"
hasnt "and the absence is not explained by a claim about how the leg was paid for" \
  "$review_body" "subscription"

# One row per leg, and only its own. The two comments sit adjacent on the pull
# request, so nothing is lost by not duplicating — and duplicated rows can
# disagree after a retry, with no rule saying which wins.
is  "the resolve comment carries one row"            "$(rows_in "$resolve_body")" "1"
has "and it is the resolve leg's"                    "$resolve_body" "| resolve | \`claude\`"
hasnt "not the review leg's as well"                 "$resolve_body" "| review |"

# A genuine mismatch — one model requested, a different one answering — is not a
# footnote. It may mean the cross-model property broke, and it has to be
# impossible to skim past, so it goes inline and in bold.
CROSSREV_REVIEW_MODEL=some-other-model; export CROSSREV_REVIEW_MODEL
run_review "$REVIEW_PAYLOAD"
mismatch_body="$(last_body 9001)"
unset CROSSREV_REVIEW_MODEL

has "a model the config did not ask for is called out inline" \
  "$mismatch_body" "**requested \`reviewer-model\`, a different model answered**"
hasnt "and not tucked into the footnote under the table" \
  "$mismatch_body" "does not report which model answered"

# An alias is not a substitution. A harness resolves `opus` to the canonical id
# it landed on and reports that, so a raw string comparison renders ordinary
# configuration as the warning above — and a warning that fires on a healthy run
# is one nobody reads on the run where it means something.
alias_config() { fixture_default_config | sed 's/model: reviewer-model/model: opus/'; }

CROSSREV_REVIEW_MODEL=claude-opus-4-5-20251101; export CROSSREV_REVIEW_MODEL
run_review "$REVIEW_PAYLOAD" "$(alias_config)"
alias_body="$(last_body 9001)"
unset CROSSREV_REVIEW_MODEL

hasnt "an alias resolved to its canonical id is not called a substitution" \
  "$alias_body" "a different model answered"
has "and the cell names the model that actually answered" \
  "$alias_body" "\`claude\` · \`claude-opus-4-5-20251101\`"
# The canonical id is the more precise answer to the same question, so the alias
# the config happened to type does not need repeating beside it.
hasnt "nor does it echo the alias back alongside the id it resolved to" \
  "$alias_body" "requested \`opus\`"

# --- a finding that missed the diff by a line ------------------------------
#
# GitHub takes a comment only on a line the diff actually shows, and the
# baseline fixture shows two of them: `app.ts` line 1 as context and line 2 as
# the addition. A reviewer that counts lines under a `@@` header instead of
# reading the gutter lands just outside that, which is what happened on pull
# request 14 — a finding on `CHANGELOG.md:14` when the sentence it faulted was
# on line 13, the last line of the hunk.
#
# The cost of not repairing it is two comments in the wrong place, not one. With
# no inline comment there is no review thread, so the resolve leg's reply falls
# back to the top of the pull request as well.
OUTSIDE_PAYLOAD='{"verdict":"issues-remain","blocked_reason":null,"prior":null,"findings":[
  {"path":"app.ts","line":4,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,
   "title":"Refresh swallows the fetch rejection",
   "why":"A failed refresh is indistinguishable from a successful one","fix":"Return the promise"}
]}'

fixture_repo "$(fixture_default_config)"; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
no_threads
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$OUTSIDE_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
outside_out="$("$CROSSREV" review --pr 42 2>&1)"

is  "a line two past the end of the hunk is posted on the last line in it" \
  "$(count '-F line=2')" "1"
is  "and the line the reviewer named is not the one posted" \
  "$(count '-F line=4')" "0"
has "the run says the line moved, and where to" \
  "$outside_out" "app.ts:4 (RIGHT) is not a line the diff shows; anchoring the finding to line 2 instead"
hasnt "and nothing fell back to the top of the pull request" \
  "$outside_out" "could not be anchored"

# The gutter is the other half: the reviewer should not have had to count in the
# first place. Both legs read the same rendering, so a line number means the
# same thing to the one that finds a defect and the one that verifies it.
has "the review prompt carries the diff with its line numbers" \
  "$(cat "$PROMPT_LOG")" "   1    1 | export const ok = 1"
has "and marks the side an added line cannot be commented on" \
  "$(cat "$PROMPT_LOG")" "   -    2 |+export function refresh()"

# The lines checked above and the commit the comment is posted against have to
# describe one revision. `repos/{repo}/pulls/{n}` returns whatever the diff is
# at the moment of the call, so asking for it by number is a second read of a
# moving target: a push landing between that read and the `gh pr view` the leg
# already did would validate lines from one revision and post them against
# another. Both revisions now come from the one call that read them together.
has "the diff is fetched pinned to the revisions the leg loaded" \
  "$(calls)" "compare/$FIX_BASE...$FIX_HEAD"
hasnt "and not by pull request number, which is whatever it is at the time" \
  "$(calls)" "vnd.github.diff repos/$FIX_REPO/pulls/42"

# --- a finding that could not be anchored at all ---------------------------
#
# The snap is bounded, and GitHub can refuse a line for reasons the diff does
# not predict, so the top-level fallback stays. What is not acceptable is the
# run reporting a clean inline post over a degraded one: `gh_review_comment_create`
# said which of the two happened and the caller sent that answer to /dev/null.
FAR_PAYLOAD='{"verdict":"issues-remain","blocked_reason":null,"prior":null,"findings":[
  {"path":"app.ts","line":40,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,
   "title":"Refresh swallows the fetch rejection",
   "why":"A failed refresh is indistinguishable from a successful one","fix":"Return the promise"}
]}'

fixture_repo "$(fixture_default_config)"; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
no_threads
route_first 'api --method POST repos/*/pulls/42/comments *' '!fail'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$FAR_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
far_out="$("$CROSSREV" review --pr 42 2>&1)"; far_rc=$?
far_calls="$(calls)"

is  "the leg still finishes, because the finding is not lost" "$far_rc" "0"
has "a line too far from any hunk is left where the reviewer put it" \
  "$far_out" "GitHub would not anchor a comment to app.ts:40"
has "the run says how many findings missed their line" \
  "$far_out" "1 finding could not be anchored to a line"
has "the fallback comment names the location it could not reach" \
  "$(calls)" "**app.ts:40** (RIGHT)"
has "and the summary comment records it, not just the terminal" \
  "$(last_body 9001)" "One finding could not be anchored to a line of the diff"

# The counter above the warning counts findings written out, not findings written
# out inline, and calling it the second thing put a clean line directly above the
# warning contradicting it. The subtraction is not the fix: `unanchored` is seeded
# from the pass's own markers before the loop runs, so a run resumed after an
# interruption starts non-zero over findings it never posts, and `posted` minus
# `unanchored` can reach zero on a pass where inline comments genuinely landed.
hasnt "a fallback is not counted as an inline comment" \
  "$far_out" "posted 1 inline comment"
has "the count says what it counts — findings written out, however each landed" \
  "$far_out" "posted 1 finding comment(s)"
has "and says the reply will land there too, so the second orphan is expected" \
  "$(last_body 9001)" "no review thread to put one in"

# It has to survive a recovery, which is where a count held only in a local goes
# missing. A run that stops between the fallback comment and the summary comes
# back with that comment already posted, so the loop skips it as a duplicate and
# never counts it. Where the marker landed is the record: an anchored finding's
# marker is on a review comment, a fallback's is on an issue comment.
# Taken from the marker the run above actually wrote, not recomputed here. The
# id hashes a window of lines around the anchor, so a copy of that arithmetic in
# the test would be a second implementation to keep in step — and it would agree
# with the first right up until the thing it is meant to catch.
ID_FAR="$(sed -n 's/.*<!-- crossrev:f \(.*\) -->.*/\1/p' <<<"$far_calls" | jq -r .id | head -1)"
is "the fallback comment carries the finding's own marker" "${#ID_FAR}" "16"

fixture_repo "$(fixture_default_config)"; stub_reset
routes_baseline "$(POSTED_LEG=review posted_comments "$ID_FAR" | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
no_threads
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$FAR_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
resumed_out="$("$CROSSREV" review --pr 42 2>&1)"; resumed_rc=$?

is  "the resumed leg exits clean"                     "$resumed_rc" "0"
is  "the comment already on the pull request is not posted twice" \
  "$(count 'pulls/42/comments -f body')" "0"
has "the resumed run still says a finding missed its line" \
  "$resumed_out" "1 finding could not be anchored to a line"
has "and the summary comment still records it" \
  "$(last_body 9001)" "One finding could not be anchored to a line of the diff"

# --- pass formatting beyond the cycle cap -----------------------------------
#
# An operator running a pass by hand beyond max_passes_per_cycle is driving, so
# the cap no longer bounds the run. The terminal line and the pull request
# comments must say which pass this is without an impossible denominator.
pass4_review_marker() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(( $(date +%s) - 100 ))" '
    {v:1, leg:"review", pass:4, state:"complete", ts:$ts, done_ts:($ts + 100), run_id:"1",
     head_sha:$sha, harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     effort:"high", endpoint:null, tokens:1000, verdict:"issues-remain",
     findings:[
       {id:"a1", path:"app.ts", line:2, side:"RIGHT", severity:"high", category:"correctness",
        pre_existing:false, title:"t", why:"w", fix:"f", anchor:"", thread_id:"T_A",
        resolution:null, tracked_as:null}]}'
}

three_done_marker() {
  jq -cn --argjson ts "$(date +%s)" '
    {v:1, leg:"review", pass:3, state:"complete", ts:$ts, run_id:"x",
     head_sha:"0000000000000000000000000000000000000000",
     harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     verdict:"issues-remain", findings:[]}'
}

fixture_repo; stub_reset
routes_baseline "$(marker_comment 9001 "$(three_done_marker)" | jq -cs . | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9004}'
no_threads
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$CONVERGED_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
p4_out="$("$CROSSREV" review --pr 42 2>&1)"

has "a manual review pass beyond the cycle cap formats the header without a false denominator" \
  "$p4_out" "Reviewing acme/widget#42 — pass 4 (past the cycle cap of 3)"
has "and the review summary comment formats the header the same way" \
  "$(last_body 9004)" "## crossrev review — pass 4 (past the cycle cap of 3)"

fixture_repo; stub_reset
routes_baseline "$(marker_comment 9004 "$(pass4_review_marker)" | jq -cs . | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9005}'
route '*reviewThreads*' "$(threads_response "$(thread_node T_A app.ts 2 false "a1")")"
route '*resolveReviewThread*' '{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}'
route 'api --method POST repos/*/pulls/42/comments/*/replies*' '{"id":6001}'
CROSSREV_RESOLVE_PAYLOAD="$(jq -cn '{blocked:false, blocked_reason:null, summary:"All settled.", resolutions:[{finding_number:1, resolution:"fixed", reply:"Fixed.", persist:null, duplicate_of:null}]}' | payload)"
export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(mktemp)"; printf 'printf "export const ok = 2\\n" >app.ts\n' >"$CROSSREV_RESOLVE_EDIT"; export CROSSREV_RESOLVE_EDIT
CROSSREV_RESOLVE_MODEL=resolver-model; export CROSSREV_RESOLVE_MODEL
p4_res_out="$("$CROSSREV" resolve --pr 42 2>&1)"

has "a manual resolve pass beyond the cycle cap formats the header without a false denominator" \
  "$p4_res_out" "Resolving acme/widget#42 — pass 4 (past the cycle cap of 3)"
has "and the resolve summary comment formats the header the same way" \
  "$(last_body 9005)" "## crossrev resolved pass 4 (past the cycle cap of 3)"

finish
