---
date: 2026-08-21
title: "Seed-and-self-refresh for reusable credentials"
type: adr
status: approved
scope: [credentials, ci, github-apps]
supersedes: 0004-subscription-credentials-in-ci.md
---

# 0015 — Seed-and-self-refresh for reusable credentials

Supersedes [0004](0004-subscription-credentials-in-ci.md).

## Context

[ADR 0004](0004-subscription-credentials-in-ci.md) decided that legs never refresh. Its reasoning rested on one sentence: *"refresh tokens rotate, and using one consumes it."* That sentence is true of some vendors and false of others, and the measurement behind it did not establish what it was taken to establish.

Two things were wrong with the record.

**The measurement was run against Kimi, not Codex.** Codex's rotation was never measured. The design that preceded 0004 says outright that it assumed rotation from OpenAI's guidance to store the refreshed file rather than the seed. No test against a Codex credential is recorded anywhere.

**A changed stored value does not prove the predecessor was revoked.** The Kimi test hashed a credential, forced a refresh, and hashed again. A different hash proves a replacement was issued. It says nothing about whether the original still works, and those are different properties.

Grok demonstrates the gap concretely. Presenting the same original refresh token twice returned HTTP 200 both times, with `expires_in` 21600 on each call, and each call handed back a *different* replacement. A replacement was issued and the original survived. A hash test would have called that rotation-with-consumption. It is not.

### The three archetypes

| | Refresh behaviour | Coordination needed |
|---|---|---|
| **A** | Static. Nothing to refresh. | None |
| **B** | Rotates, and use consumes the token. | Exactly one writer |
| **C** | Refreshes without invalidating the credential held. | None |

Archetype B needs a single writer because a second holder that refreshes invalidates the first. Archetypes A and C need no coordination at all.

### Membership, and how each was established

| Harness | Credential | Access-token lifetime | Archetype | How |
|---|---|---|---|---|
| Claude | `claude setup-token`, purpose-built | 1 year | A | Read off an installed credential |
| Codex | OAuth token in `~/.codex/auth.json` | 10 days | B | Measured, below |
| Antigravity | OAuth token in the platform credential store | about 1 hour | C | Original presented twice, accepted twice |
| Grok | OAuth token in `~/.grok/auth.json` | 6 hours | C | Original presented twice, accepted twice |
| Kimi | OAuth token | 15 minutes | unknown | Not measured |

Kimi's credential carries no issuer and no client id, so neither its token endpoint nor its client can be derived from it the way Grok's and Codex's can. It is `not_driven` and nothing depends on the answer.

### How Codex was measured

Not by a two-call test. The two-call test could not run, because the stored refresh token was already dead when it was attempted — call one failed, which proves nothing about reuse.

What killed it is the measurement. Two holders had the same Codex refresh token: a local `~/.codex/auth.json` last refreshed at `2026-08-17T03:30:45Z`, and a repository secret seeded from it. The refresher workflow ran against the secret's copy four times over the following two days, each time successfully. The local copy was then rejected with `Invalid refresh token`.

**A token whose use by one holder invalidates it for another holder is consumed on use.** That is archetype B, observed under production conditions rather than arranged.

It does not separate "consumed on first use" from "invalidated by a later refresh in the same family". The distinction does not matter: neither permits a static seeded copy, and that is the only question the archetype answers.

A related fact, measured at the same time: a fresh `codex login` does **not** invalidate a refresh token already issued to a different holder. The seeded secret refreshed cleanly afterwards. Re-seeding after a local re-login is therefore unnecessary.

## Decision

**Seed the harness's own credential store as a static secret, let it refresh in-process, and write nothing back.**

The rule covers archetype A, which has nothing to refresh, and **verified** archetype C, whose refresh does not invalidate what is held. It reaches Claude, Antigravity and Grok today. A harness joins only after its own two-call test, never by resemblance to one that passed.

It needs no per-vendor OAuth reimplementation, because the harness already knows how to talk to its own issuer. It grants no leg `Secrets: write`.

**Codex stays archetype B and keeps everything 0004 gave it**: the single-writer refresher on its own concurrency group, the second GitHub App, and `Secrets: write` scoped to that one injection-free job. Its measurement confirms rather than changes that machinery.

**Staging is a process-scoped overlay, not an exported home variable.** Antigravity offers no `CODEX_HOME` equivalent, so staging it through an environment variable would mean overriding `HOME` itself. Exporting `HOME` from the orchestrator would change every later child process, and `cred_discard` unsets rather than restores. The adapters already avoid the problem: each builds a `local -a run=(env -u GH_TOKEN …)` array and passes overrides on that one invocation.

## Options considered

**Keep "legs never refresh" and build an on-demand refresher for every rotating harness.** This is 0004's rule extended. It solves a problem that exists only for archetype B. Where C is verified there is nothing to serialise and nothing to write back, so the apparatus is machinery without a purpose — and it costs a second App and a `Secrets: write` grant per harness to get it.

**Reimplement each vendor's refresh inside CrossRev and write the result back.** Every vendor's endpoint, client id, body encoding and scope set differs. OpenAI's token endpoint is `/api/accounts/oauth/token`, not the `/oauth/token` the obvious guess produces. Each one is a thing to get wrong, to maintain, and to break silently when a vendor moves it. The harness already does this correctly for itself.

**Treat every harness as archetype B by default and coordinate a writer regardless.** Safe under any answer, and that is its appeal. It also permanently grants `Secrets: write` to a job for every harness, which is the permission this project has spent three ADRs keeping narrow. Paying that for Claude, which has nothing to refresh, is not caution but noise.

**Assume archetype C from a credential's shape.** Rejected outright, and it is the mistake this ADR corrects. Grok's shape is close to Codex's; their behaviour is opposite. One vendor's behaviour predicts nothing about another's.

## Consequences

- **No leg is ever granted `Secrets: write`.** Unchanged from 0004, and unchanged from [ADR 0001](0001-cross-model-review-loop.md)'s permission boundary. Seed-and-self-refresh removes a writer rather than adding one.

- **A silent vendor switch to rotation would be detected late, and badly.** Under this rule CrossRev never inspects an archetype-C credential, so it has no freshness assertion to fail. If a vendor starts rotating, the first leg succeeds and consumes the seed, and the second leg fails with a vendor authentication error surfaced as generic harness text. The operator sees a harness that stopped working, not a credential that was consumed. **The mitigation belongs in the adapter**: an authentication rejection must be classified distinctly from other harness failures and must name the harness. That is a requirement on the adapters, not an optional improvement.

- **An archetype is a measurement with a date, and it can go stale.** Each is recorded in `lib/harnesses.json` with a `provenance` field carrying `measured`, `inferred` or `vendor-documented`. `inferred` is a standing invitation to run the test, not a settled answer.

- **The record in 0004 stays as written, including the parts now known to be wrong.** Its Antigravity credential path is incorrect and its rotation claim over-generalises. An ADR records what was decided and why at the time; correcting it in place would erase the reasoning that produced this one. The wrong path is corrected in the documentation ([#77](https://github.com/carlosboeing/crossrev/issues/77)), not in 0004's body.

- **What 0004 got right is carried forward whole.** The refresher's safety comes from what it does not do: it never checks out the pull request branch, never runs a model, never reads a diff or a comment. `pull_request_target` is still never used. A self-hosted runner is still never pointed at a public repository. An organisation-level copy of a rotating credential is still a trap.
