---
date: 2026-11-15
title: "User config overrides shipped defaults, project config overrides user"
type: adr
status: approved
authors:
  - "Carlos Boeing"
  - "Muse (Muse)"
scope: [configuration]
related:
  - 0003-policy-read-from-the-base-revision.md
  - 0028-every-setting-is-settable-by-config-and-by-flag.md
---

# 0029 — User config overrides shipped defaults, project config overrides user

## Context

Configuration merged in two layers over the shipped defaults: the repository file, and the operator file's endpoints by name. Everything else in `~/.config/crossrev/config.yml` was read for its version and ignored.

That left a personal default nowhere to live. A user who resolves with one model across every project — the ordinary case for the person paying for the subscription — had to repeat the pairing in each repository, and `crossrev config show` in any repository printed the shipped resolver no matter what the operator file said. The file a user is told is theirs read as a place settings go to be silently dropped.

## Decision

The layer order is shipped defaults, the operator file over them, and the repository file over those. The operator file is the user's own defaults across projects; a repository narrows them wherever it declares a value of its own.

Endpoints keep their existing rule on top of that order: definitions merge by name and the operator file wins, so a repository can declare a public endpoint while a user points the same name at a local instance.

## Consequences

- A setting a repository does not declare now comes from the operator file before the shipped default, for every key, not endpoints alone. The pairing, models and effort a person runs everywhere are set once, in one file that is never committed.
- [ADR 0003](0003-policy-read-from-the-base-revision.md) is unchanged in the direction that matters. For a leg, repository policy still comes from the pull request's base revision, and it still wins over the operator file wherever it declares a value — so an operator default can never widen what a repository's own policy narrowed, and a pull request still cannot reach either layer that governs it.
- On a hosted runner the operator file is absent, because nobody's home directory is staged there. Automated runs therefore still read shipped defaults narrowed by repository policy alone, exactly as before.
- An operator value a repository overrides is not validated in effect: validation runs on the merged result, as it always has. An invalid operator value that no repository overrides now refuses at load where before it was ignored, which is the correct direction for a file that has become load-bearing.
