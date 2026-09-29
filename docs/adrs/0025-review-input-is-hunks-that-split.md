---
date: 2026-09-29
title: "Review input is hunks that split and never halt"
type: adr
status: approved
scope: [review, intel, prompt]
authors:
  - "Carlos Boeing"
  - "Muse Spark (Muse Code)"
related:
  - docs/adrs/0022-the-coverage-ledger-lives-in-git-refs.md
  - docs/adrs/0023-generated-files-are-recognised-without-configuration.md
---

# 0025 — Review input is hunks that split and never halt

## Context

One prompt cannot hold a large pull request, and a model asked to derive a
line number by counting under a `@@` header sometimes counts wrong — while
GitHub accepts a comment only on a line the diff actually shows. CrossRev
used to hand the reviewer a whole-file body beside a whole-diff slice, and a
file that fit into no batch stayed outstanding with `input_exceeds_budget`,
halting the pass with `crossrev/halted` and leaving the files after it
unreviewed. A halt over one oversized file cost the review of every other
file, and the reviewer judged bytes the ledger recorded only as a whole-file
digest it could not audit.

## Decision

### 1. Every required file arrives as its own gutter-numbered diff

Each required file reaches the reviewer as its own diff in one of three
forms: the whole file as one hunk, the enclosing function of each change
clipped to context near a changed line, or the header lines alone with the
access reason. Added files, deleted files and small edited files read in
full; larger edited files read as function-context hunks; binary,
unreadable, pure-rename and mode-only changes read header-only. The shared
whole-diff slice no longer renders on a batched prompt: the file's own
hunks are the supplied content. Findings still anchor against the full
base-to-head diff, and a finding on a context line lands as a file-level
comment.

### 2. A file that fits no call alone splits across calls

A handwritten file that fits no call alone splits into parts at hunk
boundaries — one oversized hunk into line chunks, each with its header and
gutter numbers — and the part verdicts merge back into one when every part
lands in the same pass. Part verdicts and findings stay in memory and never
reach a publication, so an interrupted pass restarts the file from part 1
with nothing partial recorded. A recognised generated file whose rendering
would need splitting is skipped and recorded as an exclusion instead: its
content is machine-shaped, and a split review of it costs calls without
judging intent.

### 3. Oversized input never halts the pass

The `input_exceeds_budget` halt is gone. Only shared context alone past the
hard limit still halts, with `shared_context_exceeds_window`; between 0.75
of the packing limit and the hard limit the pass runs over budget and
records `over_budget`. Calls measure the whole rendered prompt — headers,
shared context and file content together — against the calling harness's
packing limit, and the repair delta stays required input of the first call
or calls only.

### 4. This amends ADR 0023 §1

ADR 0023 §1 gave the built-in detectors one job: converting an oversized
generated file's halt into a skip. There is no halt left to convert for
oversized input — a plain oversized file splits, a generated one that would
need splitting skips — so the detectors now decide skip versus split rather
than skip versus halt. Fitting generated files are still reviewed, and the
base-revision `.gitattributes` authority in 0023 §4 is unchanged.

### 5. The hunk, defined

A hunk is the gutter-numbered diff the prompt shows for one file: every
line inside a hunk prefixed by its number in the old file, its number in
the new file, and a `|`, with a dash standing where the line does not exist
on that side. `full_text` shows the whole file as one hunk,
`hunks_context` shows each change with its enclosing function clipped to
100 lines of surrounding context, and `diff_only` shows the diff header
with the access reason and no hunks. The spans each side shows are the
supplied ranges, numbered as the gutter shows them; [ADR 0026](0026-coverage-counts-supplied-bytes-only.md)
records what coverage counts over them.

## Options considered

- **Keep the whole-file body beside the diff slice.** Rejected: the reviewer
  judged bytes twice under two numberings, and the ledger could not say
  which bytes a verdict rested on.
- **Halt on oversized input with a larger budget.** Rejected: any fixed
  budget still halts on the file past it, and the halt still costs the rest
  of the pull request.
- **Split generated files too.** Rejected: machine-shaped content reviewed
  in slices costs model calls without judging intent; the skip with its
  visible warning is the honest record.

## Consequences

- Pull requests with oversized handwritten files complete instead of
  halting, and each open pull request is re-reviewed once under the
  `hunk-v1` engine.
- A split file is announced inside the model prompt ("Shown as slice N of
  M"); naming split-reviewed files in the pass summary stays open work.
- The prompt wording keys verdicts to the supplied ranges: a file verdict
  means the ranges on both sides were examined, not merely the pathname.
