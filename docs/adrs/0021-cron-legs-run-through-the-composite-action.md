---
date: 2026-09-16
title: "Cron legs run through the composite action"
type: adr
status: approved
scope: [action, preflight, auth-refresh, templates, proof-standard]
authors:
  - "Carlos Boeing"
  - "Gemini (Antigravity)"
related:
  - docs/adrs/0009-delivery-via-sha-pinned-composite-action.md
  - docs/adrs/0018-go-native-parity-contract.md
  - docs/adrs/0020-the-first-native-release-ships-a-reduced-scope.md
---

# 0021 — Cron legs run through the composite action

## Context

The v0.6.0 cutover deleted `bin/` and `lib/` and routed every execution path through the compiled Go binary. Two cron workflows broke in consuming repositories the moment they repinned to v0.6.x, and neither failure was caught before shipping:

1. **The watchdog leg failed preflight.** The watchdog installs no harness CLI and runs no model. The composite action unconditionally executed `crossrev doctor`, which required a model harness CLI. On hosted runners without one installed, the watchdog failed with `no harness CLI found` before doing any work.
2. **The token-refresh workflow failed to execute.** The template checked out CrossRev source and added `$GITHUB_WORKSPACE/.crossrev-src/bin` to PATH. Because `bin/` was deleted in v0.6.0, the job failed on every scheduled tick with `command not found`.

Both regressions shipped green under the offline suite. The token-refresh golden test byte-locked the stale `.crossrev-src/bin` line, confirming byte fidelity against an obsolete expectation. The action tests asserted nothing about which preflight level a leg requires. CHANGELOG 0.6.0's claim that "Nothing the tool does changes" was false when written.

## Decision

### 1. Per-leg preflight levels

`crossrev doctor` accepts `--level core|harness`. Unset continues to default to `harness`, preserving interactive and local behavior.

The composite action (`action.yml`) maps each leg to the preflight level it needs:

| Leg | Level | Rationale |
|---|---|---|
| `review`, `resolve`, `cycle` | `harness` | Invokes a model CLI; must verify harness availability before starting work |
| `status`, `watchdog`, `auth-refresh` | `core` | Only reads or writes the forge; requires `git`, `gh`, `jq`, `yq` and `openssl`, but no model CLI |
| *(unrecognised)* | `harness` | Fails closed: an omitted leg mapping requires more rather than skipping checks |

### 2. `auth-refresh` joins the action's leg vocabulary

This **amends ADR 0009's "one workflow keeps a checkout" clause**. ADR 0009 kept a plain public checkout for token refresh because `auth refresh` was not expressible through the action's `leg` input and widening an unexercised interface was considered worse than a checkout. The native cutover removed the shell script the checkout ran, overturning that trade.

Binary acquisition and verification against `checksums.txt` now have exactly one home: the composite action. The token-refresh template calls the composite action with `leg: auth-refresh`, passing `app-token`, `harness`, and the refresh secret as step `env`. It performs no source checkout at all.

### 3. The proof standard

A release whose diff touches `action.yml`, `templates/`, or the binary's CI entry paths is not proven until every workflow `crossrev init` generates has run green on `carlosboeing/crossrev-testbed`:

- The event-driven legs (`review`, `resolve`) must run on a real pull request.
- Both cron workflows (`watchdog`, `token-refresh`) must be verified by manual `workflow_dispatch` before the release tag is cut.

The offline suite remains mandatory but cannot substitute for live execution on GitHub's hosted runner environment.

## Options considered

- **Retain the checkout and compile the binary in the refresh job.** Rejected. It adds a Go toolchain dependency, lengthens run duration, and duplicates the release asset verification logic implemented in `action.yml`.
- **Lower `crossrev doctor` to core across all legs.** Rejected. A model-running leg must fail fast during preflight when a harness CLI is missing, rather than failing mid-leg after consuming forge API quota.
- **Add a separate binary download step to the refresh template.** Rejected. Splitting binary acquisition across two workflow files re-creates the divergence that caused this defect.

## Consequences

- Binary acquisition, runner platform detection, and checksum verification live in exactly one file (`action.yml`).
- The token-refresh workflow operates with no source tree present, strengthening its isolation while running with `secrets: write`.
- The watchdog runs unattended on hosted runners without requiring dummy or unneeded harness installations.
- Release proof requires active verification of all four generated workflow shapes.
