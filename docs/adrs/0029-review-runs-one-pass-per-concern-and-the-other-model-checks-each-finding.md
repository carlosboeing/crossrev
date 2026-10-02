---
date: 2026-10-02
title: "Review runs one pass per concern, and the other model checks each finding"
type: adr
status: approved
scope: [review]
authors:
  - "Carlos Boeing"
  - "Muse Spark (Muse Code)"
related:
  - docs/adrs/0001-cross-model-review-loop.md
  - docs/adrs/0003-policy-read-from-the-base-revision.md
  - docs/adrs/0025-review-input-is-hunks-that-split.md
  - docs/adrs/0026-coverage-counts-supplied-bytes-only.md
---

# 0029 — Review runs one pass per concern, and the other model checks each finding

## Context

A review pass made one model call per input and asked it to look for
everything at once: bugs in the logic and breaks against the code around
it. The two lenses compete for the same attention, and a finding went
from one model's answer straight onto the pull request — the loop's
second model never saw it until the resolve leg, where disputing it
costs a full pass. [ADR 0001](0001-cross-model-review-loop.md) pairs
two models so that what one misses the other catches; the review leg
itself used only one of them.

## Decision

1. **One pass per concern.** Every planned input — a whole file or a
   split part — runs once per configured concern, in correctness then
   consistency order. The answers merge before coverage publishes:
   identical candidates collapse into one claim carrying both
   concerns, and verdicts merge with `could_not_review` winning over
   any other verdict while naming no findings, so a unit one concern
   could not judge stays unjudged rather than inheriting the other's
   finding links. An
   interrupted input restarts from its first concern, so no partial
   answer enters a generation and each call is charged once.

2. **The other model checks each finding before it posts.** With the
   check on, the review leg numbers its merged findings as candidates
   and has the resolver's harness and model judge each one —
   confirmed, rejected, or duplicate of another candidate — under the
   review leg's own isolation. Only confirmed candidates post and
   count as actionable. Rejected and duplicate candidates stay on the
   pass marker with the checker's reason, and severity and
   `pre_existing` corrections apply before posting with the reviewer's
   values kept beside them.

3. **A check that cannot judge degrades visibly; a check that must not
   run fails the pass.** Harness, quota, transient and schema
   failures degrade to posting everything unchecked, with
   `check: degraded` and the reason in the summary and the marker; a
   checker that cannot run at all — unverified isolation included —
   records `check: unavailable` the same way —
   the reviewer's findings are still the review. The command tripwire,
   a restore failure, a credential, endpoint or hardening refusal, a
   reads halt and cancellation propagate as the review leg's own
   failures do, because none of them is evidence about the findings.
   Decisions persist on the claim, so a resume reuses a matching
   record without a model call.

4. **Both mechanisms are settings, not constants.**
   `review.concerns` narrows the lenses and `review.check` turns the
   check off, per repository or per run. The concerns, check mode,
   input policy and read mode the pass actually ran with fingerprint
   the coverage engine identity — a local flag override moves it —
   so a generation judged under other settings retires instead of
   being trusted by a later pass. The pass records the identity on
   its marker; resolve and status judge by the recorded identity,
   falling back to the base-policy identity for markers written
   before it.

## Consequences

A review pass makes `k × c + m` model calls for `k` inputs, `c`
concerns and `m` check calls — one input under the defaults costs
three calls where it cost one. Each call keeps its own attempts: one
semantic and one transient retry, plus one shape retry where the
harness constrains nothing. An operator who wants the old cost back
sets `review.concerns: [correctness]` and `review.check: off` and is
back to one call. Whether the defaults earn their calls is a
measurement, not an assertion: the benchmark the roadmap carries runs
the frozen corpus with all mechanisms on and with each one reduced
before any default changes.
