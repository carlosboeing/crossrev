---
date: 2026-08-11
title: "Subscription credentials in CI: lifetimes drive runner pairing"
type: adr
status: approved
scope: [credentials, ci, github-apps]
superseded_by: 0015-seed-and-self-refresh-for-reusable-credentials.md
---

# 0004 — Subscription credentials in CI: lifetimes drive runner pairing

Superseded by [0015](0015-seed-and-self-refresh-for-reusable-credentials.md). Its rotation rule generalised from a single measurement against Kimi, and its Antigravity credential path is wrong. The body below is left as written, because an ADR records what was decided at the time.

## Context

CrossRev's premise is that it runs on the AI subscriptions you already pay for rather than on per-token API keys. Whether that works in CI turns out to be a property of the **runner**, not of the harness, and it reduces to one question: can this vendor's subscription credential live in a repository secret?

Every lifetime below was read off an installed credential rather than out of documentation. Each one cost a wrong turn to establish, which is why they are recorded rather than left to be re-derived.

| Harness | Subscription credential | Access-token lifetime | Survives an ephemeral runner |
|---|---|---|---|
| Claude | `claude setup-token`, purpose-built | 1 year | Yes |
| Codex | OAuth access token in `~/.codex/auth.json` | 10 days | Yes, with a refresher |
| Antigravity | OAuth access token in `~/.gemini/oauth_creds.json` | about 1 hour | No |
| Kimi | OAuth access token | 15 minutes | No |

The governing fact, verified by hashing a stored credential, forcing a refresh, and hashing again: **refresh tokens rotate, and using one consumes it.** Both the access token and the refresh token change. One holder is fine. Several copies are not — the first to refresh invalidates every other copy, and a job holding a dead one that writes back overwrites the good one. One collision breaks the chain until somebody logs in by hand.

## Decision

**Legs never refresh.** A leg restores a credential into a throwaway home, reads it, and discards it. A leg holding under an hour of remaining token life **stops rather than refreshing in flight**, because an in-flight rotation invalidates the stored copy silently and the next scheduled refresh then fails with nothing to point at.

**Exactly one writer exists**, and it is a scheduled workflow on its own concurrency group whose only job is refreshing the stored credential.

**That writer gets its own GitHub App**, holding `Secrets: write` and nothing else. This refines "one App per owner" into **one App per owner per role**.

**`crossrev init` derives whether a refresher is needed and never asks.** The refresher exists for exactly one situation: a harness whose credential rotates, authenticating by subscription, on an ephemeral runner. Change any one of those three and it disappears. A pairing whose lifetime is too short to keep warm is **refused by name**, with the lifetime and both fixes, rather than installed and left to fail at the first API call.

**The rotating credential is a repository secret, and the refresher always writes at repository scope.** GitHub's concurrency groups are repository-scoped, so "exactly one writer" holds within a repository and nowhere else.

## Options considered

Three places to put the power to write a secret, and only one keeps setup automated.

**On the loop App.** Every review and resolve job authenticates with it, including the ones that check out a pull request branch and run a model over a diff. Granting it `Secrets: write` would put secret-rewriting one injection away from attacker-controlled text, and widen the blast radius of the private key.

**A fine-grained personal access token.** GitHub has no API to create one — token creation is web-UI only. That means opening a browser, choosing scopes by hand, copying a value and pasting it into a secret: exactly the manual setup this design set out to eliminate, reintroduced through a side door. PATs also expire, and they belong to a person rather than an organisation, so they die with that person's access.

**A second GitHub App through the same manifest flow.** `Secrets: write` is a repository permission for Apps, and registration is already automated. One more browser approval, no new mechanism.

Also considered and rejected: **every job refreshing for itself** (ruled out by rotation), and **persisting the refreshed credential to an Actions cache or an artifact** (neither is a secret store — cache entries are readable by other workflows in the repository, and artifacts are downloadable).

## Consequences

- **A correction worth recording, because the reasoning was wrong before the mechanism was.** An earlier version held that Codex was local-only because persisting a refreshed credential needs `Secrets: write`. That inverted cause and effect: the blocker was a token that cannot be a static secret, and the permission is a consequence of the fix. With a single-writer refresher, Codex is a genuine CI reviewer on a hosted runner, on a subscription, with no per-token key.
- **The refresher is safe because of what it does not do.** It never checks out the pull request branch, never runs a model, and never reads a diff or a comment. There is nothing in it to inject into, which is what makes `secrets: write` safe there and unsafe on a leg. It asks for GitHub's repository `secrets` permission and deliberately not `organization_secrets`, because the wider permission would enable exactly the configuration that breaks.
- **An organisation-level copy of a rotating credential is a trap, and `init` warns about it.** The repository secret takes precedence, so the repository you just set up works while every other one reading the organisation copy quietly breaks. A second repository needs its own login seed.
- **A refresher App is provisioned when needed, never defensively.** One created "just in case" is a private key with `Secrets: write` sitting unused, which is precisely the credential nobody remembers to rotate. Switch the reviewer to Codex later and `crossrev init --upgrade` adds it then.
- **The failure mode is loud.** If the refresher fails for a full token lifetime — an outage, a quota pause, a bad edit — the chain dies and needs a manual login. The next leg cannot authenticate and says so.
- **A self-hosted runner is the general answer.** Every harness refreshes its own credential on disk exactly as it does on a laptop: no secrets, no refresher, no rotation chain, no extra permission. It also needs no special hardware — an Actions runner is a persistent process. Durability cuts both ways, though, and that is the trade: on a hosted runner a compromised job evaporates with the container, while on a self-hosted one it persists on the machine holding every subscription credential you own. So **never point a self-hosted runner at a public repository** — absolute, not advisory. A fork pull request would execute arbitrary code as you, with your whole home directory.
- **`pull_request_target` is never used**, anywhere. It runs in the base repository's context *with* secrets against untrusted code, and it is the one configuration that turns a fork pull request into an exploit. Fork pull requests instead fail closed: GitHub withholds secrets from them, so CrossRev refuses rather than running unauthenticated.
