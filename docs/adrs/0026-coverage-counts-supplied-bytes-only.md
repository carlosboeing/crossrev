---
date: 2026-09-29
title: "Coverage counts supplied bytes only"
type: adr
status: approved
scope: [review, ledger, prompt]
authors:
  - "Carlos Boeing"
  - "Muse Spark (Muse Code)"
related:
  - docs/adrs/0022-the-coverage-ledger-lives-in-git-refs.md
  - docs/adrs/0023-generated-files-are-recognised-without-configuration.md
  - docs/adrs/0025-review-input-is-hunks-that-split.md
---

# 0026 — Coverage counts supplied bytes only

## Context

The ledger recorded each covered file with a digest over the exact bytes
handed over — but a verdict named the whole file, and the evidence check
held cited spans inside the whole file's line count. A reviewer shown two
hunk neighbourhoods of a ten-line file could cite the eight unseen lines in
between and pass: the check could not tell examined code from omitted code.
Split files had the same hole per slice, with no record of which slices the
merged verdict rested on. Coverage that counts the file while the reviewer
saw its hunks overclaims on every file that did not arrive whole.

## Decision

### 1. The digest covers the rendered section bytes

The supplied digest covers the exact bytes the prompt showed: the
gutter-numbered hunk bytes for a shaped file, the body bytes for a legacy
whole-file render, the empty input for a header-only diff. A split file's
merged digest covers its rendered part diffs concatenated in part order.
The ledger records that digest beside the form it arrived in
(`full_text`, `hunks_context`, `diff_only`), the part count — one for a
whole file — and `truncated`, which stays false because an oversized file
splits rather than being cut.

### 2. The ledger records the supplied ranges on each side

Every covered record carries `ranges`: the base-side and head-side spans
shown, as `[start, end]` pairs numbered as the gutter shows them. A
whole-file hunk covers the full span on its content side; a
function-context hunk covers one span per change neighbourhood; a
header-only diff covers nothing on either side. A split file's merged
record carries the union of its parts' ranges.

### 3. Evidence must sit inside the supplied ranges

The evidence check holds a cited span inside the ranges on the side the
evidence revision names: a removed line cited at the base is accepted even
when the head side never showed it, and any span outside the supplied
ranges is refused even when it sits inside the whole file. A revision
naming neither side is checked against both, and CrossRev still records the
revision it reviewed when the verdict is accepted, so a wrong value is
corrected rather than refused. The refusal quotes the ranges that were
actually supplied, so the retry names what to fix.

### 4. A verdict means the supplied ranges were examined

A file verdict means the reviewer examined the supplied ranges on both
sides of the change, not merely the pathname. The prompt says exactly
that, and the skill says what it costs: lines outside the ranges are
unseen — never evidence, never a finding's anchor — and code the hunks did
not show that limits the review goes in `known_limits`. A context read
through the offered file-reading tool stays context: it never supplies
required work and never satisfies a verdict.

### 5. The engine moves to `hunk-v1` and the record schema to v3

Because verdict semantics changed, `core.FileEngineVersion` moves from
`file-v2` to `hunk-v1`. Stored generations under `file-v2` retire on the
next pass, so each open pull request is re-reviewed once. The record
schema moves to v3 beside it: the manifest gains the `reads` envelope,
reserved as null after the verification envelope, so the related-reads
work lands without a second bump. A v2 pair still decodes — no ranges, one
part, no reads — read well enough to retire under the hunk engine, never
well enough to reuse; anything older or newer refuses outright.

## Options considered

- **Digest the whole file beside the hunks.** Rejected: it records bytes
  nobody judged, and a later audit cannot tell which of them the verdict
  rested on.
- **One record per hunk.** Rejected: the required set, the verdict
  accounting and the convergence rule all run per file; hunk records would
  triple the ledger's moving parts for an audit granularity nothing reads.
- **Refuse v2 bytes instead of migrating them.** Rejected: every open pull
  request carrying a stored generation would fail its next pass closed
  instead of re-reviewing once. The migration reads only far enough to
  retire.

## Consequences

- A verdict that rests on unseen lines fails the pass with the supplied
  ranges quoted, instead of converging over code nobody read.
- Split files carry an honest merged record: the union of the slices'
  ranges with the slice count, digesting the rendered slices.
- The ledger's `reads` envelope stays null until the related-reads work
  populates it; writers emit nothing else, and readers refuse anything
  else.
