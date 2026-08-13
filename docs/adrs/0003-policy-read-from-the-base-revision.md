---
date: 2026-08-10
title: "Repository policy is read from the base revision"
type: adr
status: approved
scope: [security, configuration]
---

# 0003 — Repository policy is read from the base revision

## Context

CrossRev reads instructions out of the repository it is reviewing: `.github/crossrev.yml`, and the `## Project Map` section of `AGENTS.md`, `CLAUDE.md` or `GEMINI.md` when resolving where deferred work goes.

Every one of those files lives in the repository under review, which means every one of them is a file a pull request can change. The job holds a GitHub App token and a model credential while reading that branch.

"The attacker" here includes a well-meaning collaborator who edits the wrong file.

## Decision

**Every policy input is read from the base revision of the pull request, with `git show <base-sha>:<path>`. The working copy is ignored entirely.**

The consequence stated plainly: a pull request that legitimately changes review policy takes effect **when it merges**, not while it is being reviewed.

**A backlog path is a write target, so it is bounded rather than trusted.** Even read from the base revision, a configured or discovered path is a string that ends in a file write. The orchestrator resolves it and asserts the result sits inside the checkout, so a `../` sequence or an absolute path fails loudly instead of landing somewhere surprising.

## Options considered

**Reading from the head, like every other tool that reads a config file.** Read from the head, a pull request could raise `max_passes_per_cycle`, repoint an endpoint at a server it controls and harvest every prompt, ship an instruction file saying to return converged, or repoint the backlog destination at somewhere it wants written to. Each of those is a one-line diff.

**Reading from the head but validating the diff for suspicious changes.** This turns a structural property into a heuristic, and the heuristic has to anticipate every field that could be abused. The base-revision read needs to anticipate nothing.

**Reading from the head only for fields judged harmless.** There is no stable line between harmless and not — a field's blast radius changes as the tool grows, and a two-tier read means every new config key needs that judgement made again, correctly, forever.

## Consequences

- **The correct order falls out of it:** the new policy is reviewed under the old one.
- **A repository adopting CrossRev sees nothing on the adopting pull request itself.** The config does not exist at that pull request's base. This surprises people once and is right anyway.
- **Config parsing has no fast path.** Every leg does a `git show` per policy file rather than reading the working tree, and `crossrev init` and `crossrev doctor` deliberately read the working tree instead, because there is no pull request in play for them.
- **The same rule extends to anything policy-shaped added later.** A future field is covered without being thought about, which is the whole value of making this structural.
- **Two config values are refused rather than accepted and misread**, for the same class of reason. An unrecognised `min_fix_severity` ranks zero and zero meets nothing, so a typo would count every finding as non-actionable, report converged, and stop the cycle with a high-severity finding sitting on the pull request — a typo that looks exactly like a clean review. And `max_passes_per_cycle: 0` collides with an internal sentinel meaning "no pass bound applies to this invocation", which two readers then interpret in opposite directions, the expensive one being an uncapped loop.
