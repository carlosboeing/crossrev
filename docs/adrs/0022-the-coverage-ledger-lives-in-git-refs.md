---
date: 2026-09-21
title: "The coverage ledger lives in git refs"
type: adr
status: approved
scope: [coverage, storage, git-refs, doctor]
authors:
  - "Carlos Boeing"
  - "muse-spark (muse)"
related:
  - docs/adrs/0002-the-pull-request-is-the-state.md
  - docs/adrs/0006-three-app-permissions-and-nothing-else.md
---

# 0022 — The coverage ledger lives in git refs

## Context

Each review pass records one coverage generation: a verdict for every required file. Until now those generations lived in hidden pull request comments — small shards first, then one manifest naming them, with a halt past 32 shards.

Comments were the wrong shelf for three reasons, each observed rather than argued:

1. **They are visible clutter.** Every generation added a row of identical blank boxes to the pull request — GitHub's "No description provided." v0.7.2 gave each comment an identifying line, which treated the symptom: the ledger is machine state, and machine state should not render beside human conversation at all.
2. **They halt large pull requests.** Past 32 shards the pass keeps the last full generation and stops. The bound is an artifact of the storage, not of the review: nothing about the 33rd shard is different from the 32nd except where it would have to be put.
3. **They cap what a generation can say.** A comment-sized generation cannot carry per-record evidence and survive, so anything the record schema gains in future is paid for out of comment bytes.

## Decision

### 1. Generations publish to git refs

One ref per pull request per reviewer slot, under a dedicated namespace:

```
refs/crossrev/pr/42/reviewer1/coverage
```

Each generation is a commit carrying `manifest.json` and `records.json`, parented on the previous generation, with a commit message stating the generation, the revision pair and the slot in plain text. The default namespace is deliberately neither `refs/heads/` nor `refs/tags/`, so branch lists and tag lists stay exactly as the team left them.

### 2. The marker stays the authority; the ref is storage

What a reader trusts is the handle the pass marker records — the generation number, the commit SHA, and where it lives — and it resolves the commit, not the ref. This extends [ADR 0002](0002-the-pull-request-is-the-state.md) rather than overturning it: the authority still sits on the pull request, in the marker; only the bulk moved off it, into the repository's object store.

The split is what makes every failure mode fail safe. A ref that went missing while its commit survives is re-created on read. Objects that are gone are a lost ledger, and the next pass re-reviews — losing the ledger costs repeated work, never a wrong answer. Objects that do not verify are corruption, and the pass fails closed rather than trusting them.

### 3. The store is selected, not assumed

`coverage.store` takes `auto`, `refs` or `marker`:

| Store | Meaning |
|---|---|
| `auto` | Tries refs first, falls back to the marker comment when a ref write is refused. The default. |
| `refs` | Requires refs. Fails loudly rather than degrading silently, for a team that wants the guarantee. |
| `marker` | Never touches refs at all, whatever the token permits. For an organisation that does not want CrossRev writing to its object store. |

Availability is determined by attempting the write, not by inferring from configuration: a token's stated permissions and its actual ones diverge often enough that guessing is worse than asking. An organisation ruleset restricting ref creation is discovered only by the refused write, at which point `auto` falls back and records why. `coverage.ref_namespace` relocates the namespace for teams whose rules permit only known prefixes.

### 4. The marker fallback sheds in a fixed order

A marker-carried generation rides inside the pass summary comment, bounded at 64 KiB. When it does not fit, the retention ladder runs: shed the predecessor generation first, because nothing consults it; then, under `on_overflow: degrade`, compact the current generation to counts; then halt with `ledger_exhausted`. Under `on_overflow: halt` the ladder skips compaction and halts.

Compaction keeps the envelope — slot, producer, revision — and retires the per-record detail, so invalidation keeps working when the detail is gone. The predecessor is a convenience the ref store gets free from its parent chain, which is why it goes first and the current generation costs a re-review.

### 5. No new permission

The ref store writes through the GitHub API with the loop App's installation token, and it needs `contents: write` — the same permission the resolve leg already holds to push fixes. [ADR 0006](0006-three-app-permissions-and-nothing-else.md)'s three permissions stand unchanged: what `contents: write` now also covers is ledger refs under the configured namespace, and [the blast-radius contract](../what-crossrev-writes.md) bounds that write the way the push guard bounds the fix push.

### 6. Doctor reports the store; the skill carries the rule

`crossrev doctor` reports which store is in force and why, the namespace it would write, the overflow behaviour, what the token can do, and the resolved reviewer — so an operator who expected refs and got the fallback learns it there rather than from a halt three weeks later. Its permission probe states its limit plainly: it proves the permission, not the namespace.

Evidence notes carry locations and reasoning, never source text. Coverage refs sit outside normal history, so a quoted line nobody expected to persist would survive a force-push intended to remove it. The review skill states the rule, and the shape validator refuses a note carrying a fenced block, so the rule is enforced rather than requested.

## Options considered

- **Keep comments.** Rejected. It keeps all three defects in Context: the clutter, the shard halt, and the size cap on the record schema.
- **One ledger branch per pull request.** Rejected. A branch appears in branch lists, which the contract promises to leave untouched, and branch creation is exactly the write organisation rulesets most often restrict.
- **Workflow artifacts.** Rejected. Artifacts expire on a retention clock and are addressed per run rather than per pull request; the ledger must outlive both.
- **A database beside the repository.** Rejected. [ADR 0002](0002-the-pull-request-is-the-state.md) keeps all state where the pull request is, so the same code runs locally and in CI with nothing to provision. Refs keep that property; a database surrenders it.

## Consequences

- CrossRev writes git refs to repositories it reviews, bounded by the contract in [What CrossRev writes](../what-crossrev-writes.md) and enforced by `prstate.ValidNamespace`, which runs both at config load and where the ref name is built.
- `git push --mirror` deletes the ledger's refs. That is not an immediate loss — the reader resolves the commit and re-creates the ref — but the objects become collectable, so a mirror push followed by collection loses the ledger and the next pass re-reviews. The docs state this plainly; CrossRev cannot prevent someone else's mirror push.
- The ref path is unproven on GitHub Enterprise Server: API-compatible in principle, version floor unestablished. Doctor detects GHES and names it as unproven, with `store: marker` the safe setting until someone proves otherwise.
- Comment-era generations are never read again. A marker carrying only the legacy manifest id is treated as a lost ledger: the next pass re-reviews rather than trusting bytes from a retired store.
- Slot ids are explicit and stable, never derived from list position: an operator reordering their config must not orphan every stored ref. One reviewer runs in this release; the slot is where the second will land.
