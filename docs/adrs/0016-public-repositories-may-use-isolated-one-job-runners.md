---
date: 2026-08-22
title: "Public repositories may use isolated one-job self-hosted runners"
type: adr
status: approved
authors:
  - Carlos Boeing
  - "GPT-5 (Codex)"
scope: [security, ci, runners]
related:
  - 0004-subscription-credentials-in-ci.md
  - 0015-seed-and-self-refresh-for-reusable-credentials.md
---

# 0016 — Public repositories may use isolated one-job self-hosted runners

Supersedes only the blanket public-repository prohibition in [0004](0004-subscription-credentials-in-ci.md) and [0015](0015-seed-and-self-refresh-for-reusable-credentials.md). Their credential decisions remain in force.

## Context

ADRs 0004 and 0015 prohibited every self-hosted runner from public repositories. The rule protected persistent machines holding subscription credentials and state across jobs.

CrossRev already supports `runner: self-hosted`. `crossrev init` targets `[self-hosted, crossrev]` and expects each harness login to exist before the job starts.

The old rule joined two different claims. CrossRev can run on self-hosted infrastructure, but a reusable runner creates an unsafe boundary for attacker-controlled pull request text.

GitHub warns that self-hosted runners can be persistently compromised and should generally not serve public repositories. GitHub recommends [ephemeral one-job runners](https://docs.github.com/en/actions/reference/runners/self-hosted-runners#ephemeral-runners-for-autoscaling) for self-hosted autoscaling.

Ephemeral registration alone is insufficient. A job container can still reach host credentials, shared volumes, runner control sockets, network peers, or state used by later jobs.

## Decision

**A public repository may use self-hosted infrastructure only when every job receives a fresh, isolated runner that is destroyed afterward.**

The runner must process one job. The runner must not expose host credentials, shared writable volumes, control sockets such as Docker's, or reusable state.

The runner must limit outbound traffic to services required by CrossRev and the configured harnesses.

The operator must provision harness credentials for that job and discard them with the runner. CrossRev does not create or verify this boundary.

A container qualifies only when its host integration satisfies these rules. Placing a job inside an ordinary container does not satisfy them by itself.

Persistent self-hosted runners remain prohibited for public repositories. GitHub-hosted runners remain the default recommendation. Private repositories may continue using persistent self-hosted runners.

These controls limit persistence and cross-job exposure. They do not prevent credential theft during the current job, so sandboxing and egress controls still matter.

## Options considered

**Keep the blanket prohibition.** This matches GitHub's strongest recommendation and is easy to explain. The blanket rule also rejects isolated one-job infrastructure without evaluating its boundary.

**Allow any container described as sandboxed.** Rejected because a container can retain host access, mounted credentials, shared state, or control over sibling workloads.

**Allow fresh one-job runners with explicit isolation requirements.** This option names the security properties rather than treating runner ownership as the boundary.

## Consequences

- Public documentation distinguishes functional support from the recommended deployment.
- Operators must provision and destroy self-hosted runners for public repositories outside CrossRev.
- `crossrev init` selects the self-hosted runner label but does not provision credentials or isolation.
- `crossrev doctor` cannot certify the host boundary, credential lifetime, or cleanup process.
- The credential archetypes and refresher design in ADR 0015 remain unchanged.
