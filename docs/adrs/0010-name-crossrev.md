---
date: 2026-08-13
title: "Name: CrossRev, renamed from revloop"
type: adr
status: approved
scope: [naming, writing-style]
---

# 0010 — Name: CrossRev, renamed from revloop

## Context

The tool was built under the working title **revloop**, chosen before it was real and never checked against anything. Extraction into its own public repository was the last moment renaming was free: once `v0.1.0` ships, the name is in bootstrap URLs, CI configs, generated workflow filenames, a label namespace, and every link anyone has shared.

`revloop` turned out to be unusable in public for two independent reasons.

**A search problem.** Revloop is a commercial usage-billing SaaS product that has been shipping since 2022; `revloop.io` is a RevOps consultancy selling software services; a third company sells an AI workflow builder under a longer variant. Three companies rank ahead of you for your own name, indefinitely.

**A semantic problem.** In B2B software, "rev" means revenue. The intended reading — review — is the third one people reach, after revenue and revision.

Survivable for a tool nobody publishes. Not survivable for one that goes public.

## Decision

**The tool ships publicly as CrossRev.** The rename landed as the new repository's first commits rather than in the repository it was extracted from, so that repository kept a coherent final state and this one's first act was becoming CrossRev.

**Cross-model review.** The name states the differentiator — running two models against each other rather than one model twice — and "cross" describes the *mechanism* rather than a count, so it survives a roadmap that includes multiple reviewers with different lenses. `rev` is also a genuine git primitive (`rev-parse`, `rev-list`), so for the audience that matters it reads as revision.

### How it is written: `CrossRev` by default, `crossrev` only where a machine reads it

An allowlist, not a split down the middle. **`CrossRev` is the default for every human-readable reference**, and lowercase `crossrev` is the exception, limited to a closed list of places where the string is typed, resolved, or executed.

**Lowercase `crossrev` — the complete list:**

- the CLI command as invoked: `crossrev review --pr 42`
- the skill slash command
- package names
- the GitHub repository name
- code: identifiers, variables, function names, file and config paths

**`CrossRev` — everything else a person reads**, including the places easiest to forget: printed CLI output (status lines, progress, errors, banners), the prose inside help text, anything a harness skill emits, documentation, and the human-visible text of pull request comments.

The boundary that catches people: in help text, `crossrev review --pr 42` stays lowercase and the sentence above it does not.

**The markers stay lowercase in every form**, and that is not a style choice. `<!-- crossrev:` and `<!-- crossrev:f` are matched literally by `sed` and by `jq scan` ([0002](0002-the-pull-request-is-the-state.md)). A case change inside a parsed string is the kind of thing that works until it doesn't — and here it would break silently, on every existing pull request. Same for the `crossrev/*` label namespace ([0008](0008-six-labels-are-the-contract.md)).

## Options considered

Names were screened in batches and eliminated against criteria that emerged through the screening rather than up front. Three of those criteria were load-bearing and are worth keeping, because they will apply again to anything named later:

1. **It has to decode.** A reader should be able to take the name apart and see why it is named that. Opaque coined names cleared every check and were rejected on sight. **Legibility beats availability.**
2. **No count in the name.** Multiple reviewers with different lenses and models are on the roadmap, so `dualrev` would have been obsolete at reviewer three.
3. **Forge-neutral.** GitLab has merge requests and Gerrit has changes, so anything containing "PR" — `prloop`, and everything like it — locks the name to GitHub against a roadmap that names other forges.

Two other patterns from the screening, recorded because they cost real time:

- **Charm and availability are inversely correlated.** Three of the strongest candidates were already taken by people with the *same idea* within the preceding two months — an autonomous diff-review bug hunter with the same premise, a published placeholder using the same metaphor, and an active open-source project in the same category with the same name. A lovely metaphor means other people had it last quarter. The names that survive adversarial checking are the less charming, more functional ones, which is most of why `crossrev` was still standing.
- **Web search alone is insufficient for a name check.** One direct competitor was missed on the first pass because the search used the unhyphenated form, while the project lives under a hyphenated name on a Pages site with no repository description and no stars — invisible to both web search and repository search. Any future name check must also cover hyphenated variants, `*.github.io/<name>` sites, description-less repositories, and every package registry directly.

The casing convention follows CodeRabbit's. It costs nothing and it moves the name a useful distance away from reading like an internal service.

## Consequences

- **The rename touched roughly 1,340 occurrences across 55 files, plus six filenames**, and landed as a single ordered case-sensitive sweep with the offline suite as the safety net — 835 assertions, unchanged before and after. The ordering mattered: the prefixed environment-variable form had to resolve before the bare uppercase one, or the test harness's own binary variable survived the sweep.
- **No historical name survives in a shipped changelog**, because nothing had ever shipped under the old name. Everything ships for the first time as `0.1.0` under CrossRev. The provenance lives in this ADR and in the rename commit.
- **No compatibility shim exists**, and none is planned. Nothing was ever published under the old name, so there is nothing to be compatible with.
- **Two weaknesses were accepted knowingly**, rather than overlooked. The name has the cadence of an internal service — a prefix bolted to a truncation — which the camel-case convention softens without solving. And in developer vocabulary `cross-` most often primes cross-platform or cross-origin, so the cross-*model* reading is not the first one a developer reaches.
- **The casing rule is stated in the repository's own agent brief**, so future sessions apply it to new prose, CLI output and comment text rather than rediscovering it.
