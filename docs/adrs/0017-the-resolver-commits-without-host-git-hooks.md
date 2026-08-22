---
date: 2026-08-22
title: "The resolver commits without the host repository's git hooks"
type: adr
status: approved
scope: [resolve, git, configuration]
---

# 0017 — The resolver commits without the host repository's git hooks

## Context

The resolve leg edits files and commits them. It does that inside a worktree CrossRev creates in the repository under review, with `git worktree add --detach` (`lib/run.sh`). A worktree shares the repository's `.git/hooks/`, so every hook a developer installed there fires on CrossRev's automated commit.

**That only happens locally.** In automated mode a GitHub-hosted runner clones fresh, and a fresh clone's `.git/hooks/` holds fourteen `.sample` files and nothing else. Git hooks are not repository content: they are never committed and never cloned, because cloning a repository must not hand its author code execution on the machine that cloned it. Teams share hooks through a per-clone install step instead — `core.hooksPath` at a committed directory, Husky, the `pre-commit` framework, Lefthook — and every one of those needs a command run in each working copy.

So the same pull request commits in GitHub Actions and can fail on a laptop, and the laptop is the side nobody has tested against real hooks.

It failed on one. A resolve pass on `carlosboeing/copydesk#21` hit both installed hooks. The `pre-commit` hook ran that repository's whole test suite inside the leg — 489 tests, 46 seconds, its output interleaved with CrossRev's own progress line and attributed to nothing. The `commit-msg` hook then refused the message, `git commit` exited non-zero, and the leg died before replying to a single review thread.

The divergence is the part that matters. The path that runs hooks is the local one, which has no continuous integration behind it. The path that skips them is the automated one, which does.

## Decision

**The resolver's commit and push skip the host repository's git hooks. `.github/crossrev.yml` gains a `git.hooks` key, with the values `skip` and `run`, defaulting to `skip`.**

Three edges, stated rather than left to be discovered:

**It covers the push as well as the commit.** `git push --no-verify` is a separate flag from `git commit --no-verify`, and a `pre-push` hook is a host hook firing on an automated action for the same reason a `pre-commit` hook is.

**The review leg is unaffected**, because it commits nothing.

**`git.hooks: run` restores every hook, not some of them.** A repository whose hook enforces something it cares about sets that key, and then owns the consequence that a refused commit stops the pass.

## Options considered

**Run the hooks by default and report the failure well.** This keeps the divergence and inverts it: the checked path skips hooks, the unchecked path runs them. Reporting the failure well is worth doing regardless, and it is a separate change.

**Run hooks in local mode only, and document it.** The same behaviour with a name on it. Documenting a divergence does not remove it, and the operator still gets a different result from the runner for the same pull request.

**Make the resolver's commit message satisfy common linters.** Unbounded: every repository has different rules and the model cannot see them. It also aims at the wrong half. The model writes only `commit_subject`, and the text that was refused was the body, which `_commit_body` composes in `lib/run.sh` from the findings that were fixed.

**Detect the installed hooks and skip selectively.** There is no stable line between a hook that must run and one that must not, and the judgement would have to be made again, correctly, for every hook anyone writes.

## Consequences

- **A local run and an automated run now commit the same way.** That is the whole decision; everything below follows from it.
- **A hook that guards something real is bypassed unless the repository opts in.** This costs less than it reads. Client-side hooks have never been enforcement — `git commit --no-verify` bypasses them by git's own documented contract, and the hook frameworks say so themselves. What holds on a CrossRev push is branch protection and required status checks, and neither changes here.
- **A repository whose `pre-commit` hook runs a test suite stops paying for it once per resolver pass.** Continuous integration runs those tests on the push in any case, so the leg was buying a second copy of an answer it was about to get.
- **`git.hooks: run` cannot be used to gain execution**, because a pull request cannot install a hook: hooks are not repository content. The key is read from the base revision regardless, per [ADR 0003](0003-policy-read-from-the-base-revision.md).
- **Nothing about the labels changes.** Under `git.hooks: run`, a refused commit still reports through the halted path and still applies `crossrev/halted`, so [ADR 0008](0008-six-labels-are-the-contract.md) holds unchanged.
- **The reason a commit was refused has to be reported**, or this decision hides a class of failure instead of removing it. That is why the same change captures git's own output into the error rather than discarding it.
