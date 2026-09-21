# What CrossRev writes to a repository

A team evaluating CrossRev should be able to answer "what will this write to my repository" without reading the source. This page is that answer, as a contract rather than a reassurance: each promise names the code that keeps it.

## The inventory

CrossRev writes four things, and nothing else:

| What | Where | When |
|---|---|---|
| Review comments | Inline on the pull request, plus one summary comment per leg | Every pass |
| The coverage ledger | One git ref per pull request per reviewer, under a dedicated namespace | Every review pass, unless the fallback below is in force |
| Pass markers | Hidden HTML comments inside the summary comments | Every pass |
| Fix commits | The pull request's own branch, pushed | A resolve pass that fixed something |

Comments, markers and commits are covered in [Using CrossRev](usage.md). The rest of this page is about the ledger ref, because it is the write a team has not seen before.

## The ledger ref

Each review pass records one **generation**: a verdict for every **required file** — a changed file the review must account for — where a file with a recorded verdict is **covered** and a file still waiting for one is **outstanding**. The generation is stored as a commit carrying two files, `manifest.json` and `records.json`, addressed by a ref shaped like this:

```
refs/crossrev/pr/42/reviewer1/coverage
```

One ref per pull request per reviewer slot. The default namespace is `refs/crossrev`, deliberately neither `refs/heads/` nor `refs/tags/`, so branch lists and tag lists stay exactly as the team left them. The commit message states the generation, the revision pair and the slot in plain text, so `git log refs/crossrev/pr/42/reviewer1/coverage` is a readable audit trail.

The ref is storage, not authority. What a reader trusts is the handle the pass marker records — the generation number, the commit SHA, and where it lives — and it resolves the commit, not the ref. A ref that went missing while its commit survives is re-created on read, not mourned.

## The blast-radius contract

- **Never writes outside its configured namespace.** Every ledger ref starts with `coverage.ref_namespace`, default `refs/crossrev`.
- **Never creates refs under `refs/heads/` or `refs/tags/`.** Branch lists and tag lists stay exactly as the team left them, which is why the default namespace is deliberately neither — **and `prstate.ValidNamespace` is what enforces it, not this sentence.** It refuses anything not under `refs/`, anything under `refs/heads`, `refs/tags`, `refs/pull` or `refs/remotes`, and any component that could escape or confuse. It runs both when the configuration loads and where the ref name is built.
- **Never deletes a ref it did not create, and never deletes one at all in this design.** There is no delete path: refs are created once and moved forward along their own parent chain.
- **Never force-pushes a branch and never rewrites history.** The only ref CrossRev moves is its own ledger ref. Fix commits go onto the pull request's branch through the push guard, which refuses anything that is not the pull request's head.
- **Never touches another slot's refs, another tool's refs, or anything it does not own.** A slot id must be one safe path component, and each slot writes only its own ref.

## The marker fallback

`coverage.store` decides where generations go: `auto` (the default) tries refs first and falls back to the marker comment when a ref write is refused; `refs` requires refs and fails loudly rather than degrading silently; `marker` never touches refs at all, whatever the token permits.

The fallback exists because "it works" is not the same as "it is welcome". An organisation may decline a novel ref namespace, or an organisation ruleset may restrict which refs can be created — a refusal only an attempted ref write discovers, which is why availability is determined by attempting the write rather than by inferring from configuration. The answer to either is one config key rather than an argument.

A marker-carried generation rides inside the pass summary comment, which is bounded: the whole comment must fit in 64 KiB. When it does not fit, the retention ladder runs — shed the predecessor generation first, because nothing consults it; then, under `on_overflow: degrade` (the default), compact the current generation to counts; then halt with `ledger_exhausted`. Under `on_overflow: halt` the ladder skips compaction and halts. Compaction keeps the envelope — slot, producer, revision — and retires the per-record detail, so invalidation keeps working when the detail is gone. `crossrev doctor` reports which store is in force and why.

## The permission question

The ref store writes through the GitHub API with the loop App's installation token, and it needs `contents: write` to do it — the same permission the resolve leg already holds to push fixes ([Credentials](credentials.md)). No new permission, no new secret, no new installation step: the permission was already paid for by the resolve leg. A security review asking what that permission now also covers has its answer in the contract above.

`crossrev doctor` probes the permission and states its limit plainly: the probe proves the permission, not the namespace. A ruleset restricting ref creation is invisible to any probe and is discovered only by the attempted write, at which point `auto` falls back and records why.

## The cost, measured

GitHub already advertises roughly one ref per pull request: 26 of the 30 refs `git ls-remote` reports for [`carlosboeing/crossrev-testbed`](https://github.com/carlosboeing/crossrev-testbed) are `refs/pull/*` (measured 2026-09-21; 24 distinct pull requests). CrossRev adds one ref per pull request per reviewer slot, so it roughly doubles a cost every repository already carries rather than introducing a new category of one. A default clone fetches none of them — the default fetch refspec covers branches and tags only.

## Keeping the ledger out of clones and mirrors

Most setups need nothing: a default clone, and the checkout step of a CI job, fetch branches and tags, never the ledger namespace.

A pipeline that fetches every ref — a mirror, or a job with an explicit all-refs refspec — picks the ledger up too. Exclude it with a negative refspec (git 2.29 and later):

```bash
# A mirror that should not carry the ledger:
git config --add remote.origin.fetch '^refs/crossrev/*'
```

```yaml
# A CI job that fetches everything except the ledger:
- run: |
    git config --add remote.origin.fetch '^refs/crossrev/*'
    git fetch origin
```

**The mirror-push caveat, plainly.** `git push --mirror` deletes remote refs that do not exist locally, including the ledger's. That is not an immediate loss: the reader resolves the commit rather than the ref and re-creates the ref on read. But it makes the objects collectable, so a mirror push followed by garbage collection loses the ledger for real — and the next pass re-reviews from zero rather than failing. CrossRev cannot prevent someone else's mirror push and does not pretend to.

## The decision of record

[ADR 0022](adrs/0022-the-coverage-ledger-lives-in-git-refs.md) records why the ledger lives in git refs: refs over comments, the marker as authority, the fallback, the retention, and the `contents: write` question.
