---
date: 2026-08-10
title: "Repository-provided harness configuration is quarantined before any invocation"
type: adr
status: approved
scope: [security, sandbox, adapters]
---

# 0005 — Repository-provided harness configuration is quarantined before any invocation

## Context

A pull request branch does not only contain content to review. It contains files that configure the thing reviewing it: project settings, instruction files, hooks, MCP server definitions, agent definitions. Claude Code and Codex both read those out of the working directory, and **a hook is arbitrary code execution before the model ever sees a token**.

The job reading that branch holds a GitHub App token and a model credential.

Two harness mechanisms for ignoring project configuration were available. Only one of them is usable, and finding that out took running both.

- **Claude Code's `--bare`** skips hooks, plugin sync, auto-memory and instruction-file auto-discovery, which is exactly right — and it also refuses subscription auth, stating that Anthropic auth is strictly an API key or an API-key helper and that OAuth and the keychain are never read. Verified by running it with no API key present, which fails with "Not logged in". So on Claude Code you can have project-config isolation or subscription billing, not both — and subscription billing is the headline.
- **Codex** requires persisted trust before running a hook, and exposes a flag to bypass that check. CrossRev never passes it.

## Decision

**Sanitising the checkout is the mechanism.** Before any model invocation, CrossRev moves every path a harness is known to load from a working directory out of the checkout, and restores them before anything is committed.

**Quarantined, not deleted.** The files move to a directory no harness auto-loads. A pull request that *adds* a hook is exactly the pull request a reviewer should be flagging, and the diff still carries the text.

**Harness flags are defence in depth where they are free**, not the mechanism. A flag that changes name in the next release fails open; a file that is not there cannot be read by anything. Codex runs with its user-config flag; Claude gets none, because `--bare` is the only isolation flag it has and it disables the billing model.

**The list is deliberately over-broad and deliberately not exhaustive.** It is a best-effort layer, not the thing standing between an injected hook and the App token. That is the credential separation in [0001](0001-cross-model-review-loop.md) and [0004](0004-subscription-credentials-in-ci.md): the agent process holds no GitHub credential at all, so an injection that reaches tool use still cannot post as the App, push a commit, or read a secret.

**A write to a quarantined path is warned about, never silent.** Anything sitting at a quarantined path when the checkout is restored was written blind — the real file was moved away before the harness started, so the agent never read it. Discarding that write is the correct outcome, since letting a pull request's own instructions survive the quarantine is precisely what the quarantine exists to stop. But a finding the resolver "fixed" by writing there is reported as fixed, lands in no commit, and the "reported fixes but changed no files" guard stays quiet because other files did change.

## Options considered

**Relying on the harness flags alone.** Ruled out by the `--bare` finding: the isolation and the billing model are mutually exclusive on the most important harness. Also ruled out on principle — a vendor flag is not a boundary you control.

**Relying on an interactive trust prompt.** Headless runs have none.

**Deleting the files instead of moving them.** Cheaper, and it destroys the evidence in exactly the case that matters most.

**Re-planting CrossRev's own skills into the quarantined tree for the harness to discover.** This is the natural CI wiring, and it is where the skills would go. It leaves a window in which a crash commits CrossRev's own skills into somebody's pull request. See [0007](0007-skill-text-reproduced-into-the-prompt.md).

## Consequences

- **Harness-agnostic by construction.** A file that is not there cannot be loaded by any harness, present or future.
- **The quarantine directory is removed when empty**, because an empty directory is itself a repository-provided path a harness might notice, and it is noise in `git status`.
- **The restore has to happen before the commit**, or the resolve leg commits the quarantine. That ordering is load-bearing.
- **Three layers, in descending order of how much they carry**: credential separation (structural, holds even when the others fail), the quarantine (best-effort), and an explicit notice in the prompt telling each leg that the pull request's text is data rather than instruction, and that text addressing the model is itself a finding.
- **The review leg modifies no files at all**, because it has no reason to.
