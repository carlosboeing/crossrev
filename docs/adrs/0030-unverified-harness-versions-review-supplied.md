---
date: 2026-11-15
title: "Unverified harness versions review supplied"
type: adr
status: approved
authors:
  - "Carlos Boeing"
  - "Muse (Muse)"
scope: [review]
related:
  - 0027-review-legs-read-only-through-the-served-tool.md
supersedes: 0027-review-legs-read-only-through-the-served-tool.md
---

# 0030 — Unverified harness versions review supplied

Supersedes [0027](0027-review-legs-read-only-through-the-served-tool.md) in one point: the exact-version gate. Every other decision in 0027 stands.

## Context

[ADR 0027](0027-review-legs-read-only-through-the-served-tool.md) verified the served command block on one exact CLI version per harness and refused any other installed version with `review_isolation_unverified`. The harness CLIs update constantly, so a routine vendor update (Codex 0.169.0 installed against a 0.159.2 pin, observed 2026-11-15) made every review refuse until a maintainer re-verified the flags and moved the pin. The safety claim behind the gate is sound — the block has been false on a vendor release before — but a hard refusal turns each vendor release into a CrossRev outage for every user who updates.

## Decision

1. **An unverified version reviews supplied, not refused.** When a served harness's pin carries no verified block, or the installed CLI is not the exact verified version (a different version, a probe that fails, or a banner naming no version), the review leg runs supplied: the prompt carries everything and no read tool is granted, as opencode and agy already review. No `--allow-unverified` flag is added: it would become the pasted answer to the first refusal, which is the same weakening with extra steps.

2. **The degradation is recorded, with its reason.** The leg records `unverified_version` in the pass comment, the marker's reads envelope and the run log, the way a failed served self-test already degrades. The repository's `.policy.on_reads_unavailable` choice governs it the same way: `degrade` (the default) runs supplied, `halt` stops the leg and publishes nothing.

3. **A verified version reviews served exactly as before.** The verified table, the leg-start self-test, the post-call handshake and refusal checks, and the review-leg tripwire are unchanged. A command event on a degraded leg still halts where the harness emits a record the tripwire reads. The resolve leg is unaffected either way, and supplied harnesses with no served block to verify (agy, opencode) are unchanged. Grok reviews stay refused with `review_isolation_unverified`: grok already reviews supplied, so there is no served path to degrade from, and its empty tools allowlist left the command tool callable on the pinned version.

## Consequences

A vendor release no longer halts reviews for users on the default policy; it narrows one pass to the supplied prompt until the pin is re-verified, and the pass record says so. Recorded compatibility history is still not isolation evidence: only the exact verified version reviews served, and an intermediate or prerelease install degrades rather than serving. This changes what CI legs run on an unverified install, so the release that ships it re-proves the event legs on the testbed the way v0.8.0 and v0.10.0 did.
