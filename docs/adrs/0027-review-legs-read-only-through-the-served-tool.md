---
date: 2026-09-30
title: "Review legs read only through the served tool"
type: adr
status: approved
scope: [review, resolve]
authors:
  - "Carlos Boeing"
  - "Muse Spark (Muse Code)"
related:
  - docs/adrs/0001-cross-model-review-loop.md
  - docs/adrs/0003-policy-read-from-the-base-revision.md
---

# 0027 — Review legs read only through the served tool

## Context

A review leg used to read with whatever file tools its harness happened to
grant: Read and Grep on Claude Code, nothing at all on some pins, a shell a
flag forgot to deny on others. Three things followed that no prompt text
could fix. A reviewer could read files the prompt never supplied, so two
passes over the same diff could see different code. A reviewer could run a
command, and the loop had no record of it — the resolve leg already halts
on a command event, and the review leg had no equivalent. And a reviewer
with no read path at all answered `blocked` instead of reviewing, which is
how codex became unusable as a resolver: with its shell disabled it had
nothing to verify findings against.

The served read tool already exists for related-code reads. This decision
moves every review leg onto it, or onto nothing.

## Decision

1. **Served where a tool can be served, supplied everywhere else.** Codex
   and Claude Code review through CrossRev's read tool as their only read
   path, with their own file and command tools disabled where a flag
   exists. Grok, opencode and agy review supplied: the prompt carries
   everything and no read tool is granted. Grok keeps a tripwire over its
   tool record; opencode is denied through its isolation config; agy emits
   no record and doctor says so.

2. **A command on a review leg halts.** Any command event discards the call
   unpublished. The command reaches the run log only, redacted — never the
   pull request, never the terminal.

3. **A read path that is not serving degrades visibly or stops, by policy.**
   A failed self-test, a missing handshake, or refused calls are recorded
   with their reason in the pass comment, the ledger and the run log. The
   repository chooses degrade or halt in policy; the default is degrade.

4. **Codex resolves again.** The served tool serves resolve legs too, so
   the refusal that kept codex off the resolve leg is lifted.

## Consequences

Review legs answer from the same bytes the prompt supplied plus what the
served tool served, so a pass is reproducible from its own record: the
reads envelope on the marker and the generation, and reads.json beside the
generation. A harness whose served-or-tripwire command block is unverified
at its pin never reviews — moving a pin unverifies it until the flags are
verified again — and the installed CLI must be the exact verified version:
recorded compatibility history is not isolation evidence, and a failed
self-test that degrades reads to supplied keeps the version gate while the
supplied fallback keeps every served denial. Each model call is granted the
leg's remaining allowance against the pass caps, and served reads are
charged even when the call publishes nothing. The release proof re-proves
the event legs on the testbed once this ships, the way v0.8.0 did, because
this changes what CI legs run.
