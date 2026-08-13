---
date: 2026-08-13
title: "Delivery is a composite action pinned by full-length SHA"
type: adr
status: approved
scope: [delivery, github-actions, supply-chain]
---

# 0009 — Delivery is a composite action pinned by full-length SHA

## Context

A consuming repository's workflows have to reach CrossRev's code somehow.

While CrossRev lived inside a private repository, `uses:` was not available: GitHub shares a private action only within the account that owns it. So each generated workflow did a **second checkout** of the source repository into a subdirectory of the runner's workspace, at a pinned SHA, authenticated by a read-only SSH deploy key held in a repository secret. That mode worked and cost a secret, a Deploy keys page entry, an origin-URL parse to discover the source repository, and three separate workspace exclusions whose only job was hiding the extra checkout from `git add`, from a temp-index capture, and from a changed-paths walk.

Going public removes the reason all of that existed.

## Decision

**CrossRev is delivered as a composite action**, and consuming workflows reach it with `uses:` and no credential at all.

**The pin is a full 40-character SHA. The tag rides in a trailing comment.**

```yaml
- uses: carlosboeing/crossrev@<40-char-sha>   # v0.1.0
  with:
    leg: review
    pr: ${{ github.event.pull_request.number }}
    app-token: ${{ steps.app.outputs.token }}
    trigger: automatic
```

**The source-checkout mode is retired**, along with the deploy key, its secret, the origin sniff, and the three workspace exclusions. A composite action runs from the action's own path, outside the consumer's workspace, so all three exclusions were dead the moment delivery changed.

**A floating `@v0` is reserved for human copy-paste**, and appears only in the README's example. Generated workflows never produce it.

**One workflow keeps a checkout, and it is not an exception to the pin.** `crossrev auth refresh` is not expressible through the action's `leg` input, whose values are the loop's own commands. Rather than widen an interface that has never been exercised in CI, the credential refresher does a plain public checkout at the same pinned SHA — no token, no key, no secret. Its threat posture is unchanged: it never checks out the pull request branch, never runs a model, and never reads a diff or a comment.

**The action takes responsibility for dependency preflight** as its first step. Nothing in any template ever installed `jq`, `yq`, `gh` or `openssl` — it was implicitly "the runner happens to have them", never proven. The action is the one place a consumer's workflow cannot forget the check.

## The overturn, recorded explicitly

The code that generates the pin used to carry this objection:

> A tag only looks immutable: `git tag -f` plus a force push moves it, and the failure mode is a repository whose review behaviour changes with nothing in its own history to show for it.

**That objection survives intact for generated workflows**, which is why they still emit a SHA. What is overturned is only its application to a human typing `@v0` into a workflow by hand, and the reasons are narrow and stated so they can be re-examined when they stop holding:

- One owner controls both the action and its consumers at `v0`, so a moved tag has one blast radius and that owner can see it.
- The convenience is real and the alternative is copying a 40-character string.
- GitHub's own supply-chain guidance pins SHAs, and the generated form — the one that lands in repositories without anyone thinking about it — does exactly that.

The property the objection protects is kept where it matters: **anything CrossRev writes is pinned; only a human choosing convenience knowingly is not.**

## Options considered

**Keeping the source checkout even though it is no longer necessary.** It works, and it costs a secret, a deploy key, and three workarounds that exist only to hide it. Every one of those is a thing a new adopter has to set up correctly and a thing that can go wrong.

**A published Marketplace action with a JavaScript wrapper.** A composite action runs the existing bash directly, so a wrapper would add a build step and a Node dependency to a tool that otherwise needs git, bash and coreutils.

**Tag-only pinning in generated workflows.** Ruled out by the objection above, which was written before the delivery change and is unaffected by it.

**Having the action derive `trigger` from `github.event_name` instead of taking it as an input.** The automatic/human distinction controls the policy caps and the draft skip, so it could not simply be dropped when the leg's `run:` step collapsed into `uses:`. Deriving it inside the action hardcodes a policy mapping consumers cannot override, and it fails *unsafe* on the first event nobody anticipated — a scheduled event would map to "human" and run uncapped, which is precisely the runaway the caps exist to stop. So `trigger` is a sixth input, defaulting to `automatic`: the two entry points fail safe in opposite directions, and forgetting the input in a workflow should give you the caps rather than an uncapped loop.

**Keeping the leg as a `run:` step that calls the action's binary.** Not viable at all. The action's own path resolves only inside the action, so a `run:` step in the consumer's workflow has nothing to call without a checkout — and removing that checkout is the point.

## Consequences

- **A consuming repository needs two secrets to start**, not three. See [0004](0004-subscription-credentials-in-ci.md) for the rest of the set.
- **`crossrev init` still requires a git checkout**, because the SHA pin is read from `git rev-parse HEAD`. Any install route producing no `.git` — a tarball, a package, a formula — leaves `init` unable to run, which is why distribution stays clone-based at `v0`. The error was reworded rather than removed.
- **The `app-token` input has no default, deliberately.** Writes made with `GITHUB_TOKEN` do not trigger workflows, so defaulting to it would stall the chain after pass 1 while looking healthy the whole way. Published review actions commonly default this input; the action fails loudly on an empty token instead. See [0006](0006-three-app-permissions-and-nothing-else.md).
- **`openssl` joined the preflight's core dependency list**, which had never named it despite the credential path decoding a restored token with it.
- **Live `uses:` execution is still unproven.** It is untestable until a consuming repository runs a real loop, and that — along with verifying `yq`'s presence on hosted runner images — is the automated-mode proof rather than a launch gate. It is why the tag is `v0`.
