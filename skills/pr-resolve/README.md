# pr-resolve

The resolve leg of [CrossRev](../../). Verifies each review finding against the codebase, fixes what's real, pushes back on what isn't, and returns resolutions and reply text as schema-constrained JSON.

Forked from Superpowers' `receiving-code-review`, which already encodes the required stance: verify before implementing, push back with technical reasoning when the reviewer lacks context, no performative agreement, reply in-thread rather than at top level.

## What the fork changes

| Change | Why |
|---|---|
| Five resolutions with explicit thread rules | The orchestrator needs a machine-readable decision per finding, not prose |
| Every resolution carries a reply | A skipped finding with no reply reads as an oversight, and the next pass raises it again |
| Policy governs what happens *after* verification, never whether it happens | `pre_existing` means "don't fix this here", not "don't look at it" — otherwise the backlog fills with one model's unchecked guesses |
| A hard rule against fixing `pre_existing` findings | That boolean exists to stop the diff growing without limit, and a helpful fix defeats it. The rule most likely to be broken by good intentions |
| Deferral intent — an issue title and body | An unresolved thread on a merged PR is visible in no GitHub view, so deferred work needs somewhere durable |
| Duplicate judgement over supplied candidates | Stops CrossRev filing a second issue for something you already filed by hand |
| Re-raised disputes escalate rather than re-argue | Two models disagreeing twice about the same line is a human's decision |
| The skill makes no GitHub call | It holds no credential. Every reply, resolution, label, commit, push and filing is the orchestrator acting on returned intent |

## Intent, not action

The sharpest difference from the skill it forks. `receiving-code-review` assumes the agent responds directly. Here the agent **decides and composes**; the orchestrator **acts**.

That's a security boundary rather than a division of labour. The process reading attacker-controlled text — a PR body, a diff, a review comment — is deliberately the one that cannot post as the App, push a commit, or read a secret. It costs some prompt-assembly code and a slightly larger schema, and in exchange an injection that reaches tool use still can't do anything on GitHub.

The one outward thing the skill does own is changing code in the working tree. The orchestrator commits and pushes it, and asserts the push target matches the PR head branch before doing so.

## The schema

[`schemas/resolve.schema.json`](../../schemas/resolve.schema.json). Same two harness-forced shapes as the findings schema, both found by running the harnesses rather than reading about them:

- **No `$schema` or `$id` key** — Claude Code's `--json-schema` rejects a schema naming the 2020-12 meta-schema and fails before the model is called.
- **Every property in `required`** — Codex enforces OpenAI strict mode. Optional fields are nullable rather than absent.

## Tiebreaks worth knowing

**Unsure whether an issue is a duplicate? Treat it as one.** A missed filing still has the PR thread behind it for the life of the PR; a duplicate is mess someone else cleans up. The asymmetry is one-sided, so the tiebreak is too.

**A closed candidate issue still counts.** Closing an issue is a decision, and re-filing something explicitly closed is the most irritating duplicate available.

## Install

```bash
npx skills@latest add carlosboeing/crossrev
```

Normally you don't invoke it yourself — `crossrev resolve --pr N` does.
