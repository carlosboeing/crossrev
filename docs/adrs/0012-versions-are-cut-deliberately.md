---
date: 2026-08-14
title: "Versions are cut deliberately, and the commit type decides the bump"
type: adr
status: approved
scope: [release, versioning, ci]
---

# 0012 — Versions are cut deliberately, and the commit type decides the bump

## Context

Until `crossrev-ai` was published, a version was a tag and nothing else consumed it. Publishing changed that in two ways worth stating plainly.

**A published version is permanent.** npm's unpublish policy is conditional and cannot be undone, and a `name@version` pair can never be reused even after a successful unpublish. Every release is a decision that cannot be walked back.

**A version now means something to a stranger.** It is what `npm install` resolves and what `crossrev --version` reports, so the two have to agree or the tool is lying about itself.

That raised two questions that had never needed answering: how often a version gets cut, and who decides whether a change is a patch, a minor or a major.

## Decision

**Changes accumulate under `## [Unreleased]`. A version is cut deliberately, not per merge.**

**The commit type decides the bump, and it is not decided twice.** The repository already writes Conventional Commits, so the decision is made once — when someone chooses `fix:` or `feat:` — and read back at release time rather than reasoned about again from a diff. `scripts/next-version.sh` does the reading: it counts the types since the last version tag and reports what the next version should be.

**Major is never chosen automatically.** While the version is `0.x`, semver already permits a breaking change inside a minor bump, and `v1.0.0` is gated on proving automated mode end to end ([ROADMAP](../ROADMAP.md)). That is a conversation, not a commit type. The script reports a breaking change and declines to act on it.

**CI enforces the thing that actually gets forgotten.** `scripts/check-changelog.sh` fails a pull request that changes what ships without adding anything under `## [Unreleased]`. It derives the shipped set from `npm pack` rather than restating it, so it cannot drift from the `files` allowlist. Changes to docs, tests or CI configuration are exempt automatically, because they do not ship.

**The changelog stays hand-written.**

## Options considered

**Release on every merge.** Coherent, and the only model where the version on `main` always equals a published artifact. Rejected for now because a rewrite to a compiled binary is planned, and releasing per merge through it would leave a permanent tail of published tarballs representing an implementation being abandoned. Worth revisiting at `v1.0.0`, when releases are meaningful and the artifact is stable.

**Bump the version on every change, but publish occasionally.** Rejected because it breaks the invariant it appears to strengthen. `crossrev --version` would report a version nobody can install, and the `[Unreleased]` section would have nothing left to describe.

**release-please or semantic-release.** Both do all of this from the commit types with no bespoke code, and both generate the changelog from commit subjects. This repository's entries are multi-sentence explanations of why a change was made and what failure it prevents; a generated line reproduces the subject and loses the rest. The usual argument for generated changelogs is that nobody will write them by hand, and that does not hold here. Rejected on quality, not on capability.

**Requiring a `VERSION` bump per pull request.** This is the natural gate for release-per-merge and the wrong one here — under deliberate cutting, a feature branch should not touch `VERSION`, so the check would fail every pull request it was meant to protect.

## Consequences

- **npm lags `main` between releases.** Someone installing from the registry gets something older than the repository's documentation describes. This is the real cost of the decision and is worth a note wherever installation is documented.
- **A release is now three steps rather than one:** run `scripts/next-version.sh`, bump `VERSION` and `package.json` together, move the `[Unreleased]` entries under the new heading. The tag then triggers the publish, which re-checks the tag against the manifest.
- **`scripts/check-changelog.sh` needs `npm`**, which is a new dependency for a check in a repository that otherwise runs on git, bash and coreutils. It is CI-only and npm is already required to publish, so this does not widen what the tool itself depends on.
- **The gate cannot judge whether an entry is any good.** It proves something was written, not that it explains anything. That remains a review question.
- **`v0` is excluded from version-tag matching.** It is the floating tag reserved for the README's copy-paste example ([0009](0009-delivery-via-sha-pinned-composite-action.md)) and it moves, so describing against it would silently measure from the wrong place.
