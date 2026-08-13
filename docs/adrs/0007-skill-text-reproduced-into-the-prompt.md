---
date: 2026-08-11
title: "Skill text is reproduced into each prompt rather than discovered"
type: adr
status: approved
scope: [protocol, prompts, sandbox]
---

# 0007 — Skill text is reproduced into each prompt rather than discovered

## Context

The two legs' rubrics live as harness skills: `skills/pr-review/SKILL.md` and `skills/pr-resolve/SKILL.md`. Keeping them as prose files outside the CLI is deliberate — review quality improves by editing prose, and baking prompts into the tool would make every rubric tweak a code change.

Every harness has a skill mechanism that discovers files at known paths, and the natural CI wiring is to place the two skills where the harness will find them. That is what an earlier draft of the design said to do.

Two things get in the way.

## Decision

**The orchestrator reads both skill files out of its own checkout and reproduces their text into each prompt.** No harness skill discovery is involved in a CrossRev run.

Two reasons, both concrete:

1. **The quarantine has already moved the discovery paths.** [0005](0005-quarantine-repository-provided-harness-config.md) moves `.claude/`, `.agents/` and the rest out of the checkout before any invocation — which is exactly where a workflow would have placed the skills. Re-planting into a quarantined tree and removing them again before the commit leaves a window in which a crash commits CrossRev's own skills into somebody's pull request.
2. **Reproduction makes the prompt byte-identical across harnesses**, and that is the property [0001](0001-cross-model-review-loop.md) depends on. If pass 2 read a different brief from pass 1, a disagreement between them would say nothing about the code.

**The skills stay installable and usable by hand, and nothing about them changes.** `install.sh` offers to install them, and installing them is for invoking them yourself in an ordinary session. The generated workflows simply do not need to place them anywhere.

## Options considered

**Placing the skills at each harness's discovery path in the workflow.** The original design. Ruled out by the quarantine interaction above, and by the byte-identity requirement — every harness's discovery mechanism wraps or truncates differently, so "the same skill file" does not guarantee the same prompt.

**Using a harness flag that points at a skills directory.** One harness has a repeatable directory flag that would point straight at the CI checkout, and it is genuinely cleaner than placing files at discovery paths. Rejected for the same byte-identity reason: a per-harness mechanism means a per-harness prompt, and only one of the harnesses has the flag.

**Publishing the skills to a registry and having each harness fetch them.** Adds a network dependency and a version skew between the orchestrator and the rubric it is supposed to be running. The checkout already holds both at one revision.

## Consequences

- **The prompt is large, and that is the cost.** Both skill texts are reproduced on every invocation rather than referenced.
- **The rubric and the orchestrator cannot drift apart.** They ship in one checkout at one revision, so the version of the skill that ran is the version that was installed.
- **Editing the rubric still needs no code change.** Editing `SKILL.md` in the checkout changes the next run's prompt, because the checkout is the installation.
- **The skills are an optional install, not a dependency.** They are also the only step of `install.sh` that wants Node, and making the whole install depend on `npx` for an optional extra would be a poor trade. With no `npx`, or no terminal to ask at, the offer is skipped and the loop is unaffected.
- **Anything the prompt needs, the orchestrator supplies.** The agent fetches nothing — not the diff, not the prior threads, not the pull request body. That follows from the credential separation in [0001](0001-cross-model-review-loop.md), and reproducing the skill text is the same principle applied to the rubric.
