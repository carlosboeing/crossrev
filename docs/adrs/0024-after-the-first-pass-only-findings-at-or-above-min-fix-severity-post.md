---
date: 2026-09-27
title: "After the first pass, only findings at or above min_fix_severity post"
type: adr
status: approved
scope: [review, resolve, prompt]
authors:
  - "Carlos Boeing"
  - "Muse Spark (Muse Code)"
related:
  - docs/adrs/0002-the-pull-request-is-the-state.md
  - docs/adrs/0003-policy-read-from-the-base-revision.md
---

# 0024 — After the first pass, only findings at or above min_fix_severity post

## Context

Every review pass posts every finding as a comment on the pull request. The first pass should keep doing that: it is the one full picture of what the reviewer saw, and anything it withholds is invisible work.

Later passes are different. Findings below `min_fix_severity` cannot keep the loop alive — convergence already ignores them, and the resolve leg verifies them without changing code (`skipped`, typically). Re-posting each one as a fresh inline comment on every pass buries the findings that matter under restated nits, and each restated comment invites a reply the resolver must then write into a thread about work nobody will do.

## Decision

### 1. Pass 1 posts everything; later passes hold below-threshold findings

A finding whose severity ranks below `min_fix_severity` on a pass after the first is recorded on the pass marker with `posted: false`, skipped by the posting loop, and counted in the summary comment: "N findings below \<severity\> recorded and not posted". The summary's findings table still lists every finding, held or posted, so the count is auditable.

The hold is a severity comparison, not `ShouldFix`. A pre-existing finding never fixes, but one at or above the threshold still posts; only severity below the threshold is held. A pass-1 marker carries no `posted` key on posted findings, so nothing already written changes shape.

### 2. `posted` defaults to posted

`Finding` gains `posted`, and absent reads as posted. Markers written before this field existed keep their meaning with no migration: only an explicit `false` holds a finding back.

### 3. Held findings never reach the resolver

The resolve leg drops `posted: false` findings where it enriches its input, before numbering. With no comment on the pull request there is no thread to reply into and no top-level comment to name, so no reply or resolution can reference one. Findings without the key still arrive. The full record stays on the session for the marker and summary rewrite, so a mixed pass keeps its held findings with their `not_posted` priors.

### 4. Held findings return as `not_posted` priors

A held finding still reaches the next review's prior table, with resolution `not_posted`, so the reviewer can tell a held finding from a settled one. A finding raised again at a higher severity ranks at or above the bar and posts then, under its stable id — even when an earlier pass posted it at the lower severity, since that comment records the lower severity and must not suppress the upgrade. Findings never held back stay duplicate-suppressed, and two findings under one id in a single pass post once.

### 5. Convergence is untouched

Held findings are a subset of the findings convergence already ignores. Actionable counts, verdicts and labels read the same record as before.

## Options considered

- **Hold on pass 1 too.** Rejected: the first review is the baseline the whole loop reasons about, and a finding nobody ever saw is indistinguishable from one never raised.
- **Hold whatever `ShouldFix` refuses, pre-existing included.** Rejected: that would swallow a pre-existing defect at high severity, which the loop reports precisely because a human should see it.
- **Drop held findings instead of recording them.** Rejected: the next pass needs them as priors to avoid re-deriving — and re-posting — the same judgement, and the operator needs the summary count to know the record is complete rather than filtered.

## Consequences

- Pull requests with noisy low-severity findings get one comment per finding on pass 1, then only the findings that matter on later passes.
- The resolve leg's input shrinks to what was actually posted; a pass whose every finding was held leaves the resolver nothing to verify.
- The `not_posted` resolution appears only in review prior tables, never as a resolve-leg output: the five resolutions are unchanged.
