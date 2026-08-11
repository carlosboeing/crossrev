#!/usr/bin/env bash
#
# What revloop shows a human: the emoji, the one native alert per summary
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
  REVLOOP_REVIEW_PAYLOAD="$(printf '%s' "$1" | payload)"; export REVLOOP_REVIEW_PAYLOAD
  "$REVLOOP" review --pr 42 >/dev/null 2>&1
}

# A completed review marker carrying the same three findings, so the resolve leg
# has something to disposition.
review_marker() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(( $(date +%s) - 192 ))" \
    --arg s "$ID_SEC" --arg p "$ID_PRE" '
    {v:1, leg:"review", pass:1, state:"complete", ts:$ts, done_ts:($ts + 192), run_id:"1",
     head_sha:$sha, harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     effort:"high", endpoint:null, tokens:41205, verdict:"issues-remain",
     findings:[
       {id:$s, path:"app.ts", line:2, side:"RIGHT", severity:"high", category:"security",
        pre_existing:false, title:"Token compared with == instead of a constant-time check",
        why:"w", fix:"f", anchor:"", thread_id:"T_SEC", disposition:null, tracked_as:null},
       {id:$p, path:"app.ts", line:1, side:"RIGHT", severity:"medium", category:"correctness",
        pre_existing:true, title:"Off-by-one in the drain loop | second clause",
        why:"w", fix:"f", anchor:"", thread_id:"T_PRE", disposition:null, tracked_as:null}]}'
}

resolve_payload() {
  jq -cn --arg s "$ID_SEC" --arg p "$ID_PRE" '
    {blocked:false, blocked_reason:null,
     summary:"Replaced the comparison. The drain loop predates this branch.",
     dispositions:[
       {finding_id:$s, disposition:"fixed", reply:"Replaced with a constant-time compare.",
        persist:null, duplicate_of:null},
       {finding_id:$p, disposition:"skipped", reply:"Pre-existing, so it is reported not fixed.",
        persist:null, duplicate_of:null}]}'
}

# One resolve leg over that marker.
run_resolve() {
  fixture_repo; stub_reset
  routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
  route 'api --method POST repos/*/issues/42/comments*' '{"id":9002}'
  route '*reviewThreads*' "$(threads_response \
    "$(thread_node T_SEC app.ts 2 false "$ID_SEC")" \
    "$(thread_node T_PRE app.ts 1 false "$ID_PRE")")"
  route '*resolveReviewThread*' '{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}'
  route 'api --method POST repos/*/pulls/42/comments/*/replies*' '{"id":6001}'
  REVLOOP_RESOLVE_PAYLOAD="$(printf '%s' "$1" | payload)"; export REVLOOP_RESOLVE_PAYLOAD
  REVLOOP_RESOLVE_MODEL=resolver-model; export REVLOOP_RESOLVE_MODEL
  "$REVLOOP" resolve --pr 42 >/dev/null 2>&1
}

# ---------------------------------------------------------------------------
# The correction: nothing emitted claims a second MODEL
# ---------------------------------------------------------------------------
#
# `legs_assert_models_diverged` short-circuits when both legs were configured
# with the same model, so configuring them identically is permitted and the loop
# runs normally. Every emitted claim of a *different model* is therefore a claim
# revloop cannot back. Agent-shaped wording is true in every configuration and
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

has "the summary table header names both columns"     "$review_calls" "| Severity | Category | Where | What |"
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

# The cell shows the basename and links to the line, so the columns that carry
# meaning get the width. The full path survives as the link title, which is what
# a hover shows, so two files sharing a name are still tellable apart.
has "the where cell links to the line rather than printing the path" \
  "$review_calls" '[`app.ts:2`](https://github.com/acme/widget/blob/'
has "and the link is pinned to the revision that was reviewed, not to the branch" \
  "$review_calls" "/blob/$review_head/app.ts#L2"
has "with the full path kept as the link title"       "$review_calls" '#L2 "app.ts:2")'

# ---------------------------------------------------------------------------
# The reply lead belongs to the orchestrator, not to the model
# ---------------------------------------------------------------------------
#
# The resolve prompt hands the model "the conversation so far", which contains
# earlier replies opening with exactly these words — so the model reads the house
# style off the pull request, reproduces it, and revloop prefixes its own on top.
# Seven replies on this repository's own PR 5 read "**Fixed.** **Fixed.** …".
#
# Checked through a real leg rather than against the helper alone, because the
# defect was never in the helper: it was in nothing owning the opening.
run_resolve "$(resolve_payload | jq -c '
  .dispositions[0].reply = "**Fixed.** Replaced with a constant-time compare."
  | .dispositions[1].reply = "Skipped. Pre-existing, so it is reported not fixed."')"
lead_calls="$(calls)"

hasnt "a lead the model wrote itself is not stacked on revloop's" \
  "$lead_calls" "**Fixed.** **Fixed.**"
has   "revloop's own lead is what survives"           "$lead_calls" "**Fixed.** Replaced with a constant-time compare."
# The model's own word is dropped whether or not it was bolded, and whether or
# not it agreed with the disposition the orchestrator settled on.
hasnt "an unbolded lead is dropped too"               "$lead_calls" "**Skipped.** Skipped."
has   "and the disposition revloop decided is the one shown" \
  "$lead_calls" "**Skipped.** Pre-existing, so it is reported not fixed."

# A reply that merely begins with the word is left alone. The trailing period is
# what separates a lead from a sentence, and without it "Fixed the comparison"
# would lose its first word.
run_resolve "$(resolve_payload | jq -c '
  .dispositions[0].reply = "Fixed the comparison so it is constant-time now."')"
has "a reply that opens with the word but not the lead keeps it" \
  "$(calls)" "**Fixed.** Fixed the comparison so it is constant-time now."

run_resolve "$(resolve_payload)"
resolve_calls="$(calls)"

has "the resolve table names findings, not ids alone" \
  "$resolve_calls" "| 🔴 Token compared with == instead of a constant-time check"
has "and keeps the id small for matching a row to a thread" \
  "$resolve_calls" "<sub>\`$ID_SEC\`</sub> | fixed |"

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
has "and what happens to them next"                   "$review_body" "at or above \`fix_at\`"
# The verdict used to sit at the foot of the comment, under a table, which is the
# last place anyone looks for the answer to "what happens now".
hasnt "the verdict no longer trails the table as prose" "$review_body" "**Next:** a second agent"

run_review "$CONVERGED_PAYLOAD"
converged_body="$(last_body 9001)"
is  "a converged review carries exactly one alert"    "$(alerts_in "$converged_body")" "1"
has "and it is the green one"                         "$converged_body" "> [!TIP]"
has "which says the loop stopped on its own"          "$converged_body" "**Converged.**"
# The vocabulary has to match what `revloop status` says about the same state.
has "in the same words the terminal uses"             "$converged_body" "Nothing at or above \`fix_at\`"
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
has "carrying the disposition counts"                 "$resolve_body" "**1 fixed, 1 skipped.**"

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
# the sentence says what revloop knows rather than what the run cost. A leg can
# be authenticated as a subscription, as a vendor API key, or as a named
# endpoint that charges per token, so a footnote claiming there is nothing to pay
# would be wrong on two of the three.
has "cost is named as absent rather than left blank" \
  "$review_body" "revloop is given no billing figure by the harness"
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
REVLOOP_REVIEW_MODEL=some-other-model; export REVLOOP_REVIEW_MODEL
run_review "$REVIEW_PAYLOAD"
mismatch_body="$(last_body 9001)"
unset REVLOOP_REVIEW_MODEL

has "a model the config did not ask for is called out inline" \
  "$mismatch_body" "**requested \`reviewer-model\`, a different model answered**"
hasnt "and not tucked into the footnote under the table" \
  "$mismatch_body" "does not report which model answered"

# An alias is not a substitution. A harness resolves `opus` to the canonical id
# it landed on and reports that, so a raw string comparison renders ordinary
# configuration as the warning above — and a warning that fires on a healthy run
# is one nobody reads on the run where it means something.
alias_config() { fixture_default_config | sed 's/model: reviewer-model/model: opus/'; }

REVLOOP_REVIEW_MODEL=claude-opus-4-5-20251101; export REVLOOP_REVIEW_MODEL
run_review "$REVIEW_PAYLOAD" "$(alias_config)"
alias_body="$(last_body 9001)"
unset REVLOOP_REVIEW_MODEL

hasnt "an alias resolved to its canonical id is not called a substitution" \
  "$alias_body" "a different model answered"
has "and the cell names the model that actually answered" \
  "$alias_body" "\`claude\` · \`claude-opus-4-5-20251101\`"
# The canonical id is the more precise answer to the same question, so the alias
# the config happened to type does not need repeating beside it.
hasnt "nor does it echo the alias back alongside the id it resolved to" \
  "$alias_body" "requested \`opus\`"

finish
