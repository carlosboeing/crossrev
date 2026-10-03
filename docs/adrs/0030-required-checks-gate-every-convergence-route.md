---
date: 2026-10-02
title: "Required checks gate every convergence route"
type: adr
status: approved
scope: [review, resolve]
authors:
  - "Carlos Boeing"
  - "Muse Spark (Muse Code)"
  - "GPT-6 (Codex)"
related:
  - docs/adrs/0001-cross-model-review-loop.md
  - docs/adrs/0003-policy-read-from-the-base-revision.md
---

# 0030 — Required checks gate every convergence route

## Context

`crossrev/converged` meant the review work was complete: no open
fixable finding, every required file covered, the scope reported. It
said nothing about the repository's own CI, which the loop never read.
A green label on a pull request whose build was red stated something
false — the same false green an unexamined file used to give, through
a different door. The generation's verification envelope stays
reserved; the gate evidence lives on the pass marker instead, where
every route already reads.

## Decision

1. **The repository names the checks that must pass.**
   `verification.required_checks` lists check names with their Apps,
   and `verification.wait_minutes` bounds how long either leg waits for
   them. Both are policy, read from the base revision like every
   other key, with local flags for one run that automated mode
   refuses.

2. **The gate judges the runs GitHub reports for the head commit.**
   Name and App match together, the run with the greatest id wins so
   a rerun supersedes, and only check runs count — a commit-status
   context never satisfies an entry. A failure outranks a wait, a
   wait outranks an absence, and a refused or truncated enumeration
   is unreadable and fails closed. Both legs re-read every 30
   seconds up to the wait, stopping early on `crossrev/stop`, a head
   change or cancellation; then a pending, missing, failed or
   unreadable gate halts with a named word rather than judging
   mid-run.

3. **Every route that can converge judges the gate.** The review publish, the resolve no-commit settle and the resolve empty-findings route all read the same evidence, and only `passed` and `none_required` converge. Resolve loads the verification settings and validates local overrides before the empty-findings route. When coverage would converge, both resolve routes wait for pending or missing checks through the same bounded loop as review. A gate still blocked afterwards applies `crossrev/halted`, with its halt word, blocking checks and restart command in the summary and output. The complete resolve marker records the evidence under `verification`. Restart reads that evidence and selects resolve. An unchanged pass that settled every finding without a commit re-judges only the gate, without invoking the model or repeating replies. Empty findings and older review markers without a coverage claim follow the same rules. A failed check never becomes a code finding. It is a fact for its own logs, not an attribution the loop makes.

4. **Reading runs needs a permission the App now asks for.**
   `checks: read` joins the installation, and `crossrev doctor`
   reports whether the installation holds it whenever checks are
   required, so an installation approved before the permission
   existed fails loudly instead of converging blind.

## Consequences

`crossrev/converged` now means the review work is complete and the
required checks passed — or none were required, in which case every
route behaves as before and nothing is read. The marker records the
evidence each pass judged, the summary names each check with its run
URL, and each halt names its word with `crossrev restart --pr N` as
the next step. Without configured checks the gate costs no call and
changes no byte.
