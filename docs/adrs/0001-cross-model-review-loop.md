---
date: 2026-08-10
title: "Cross-model review loop with schema-constrained legs"
type: adr
status: approved
scope: [architecture, models, protocol]
---

# 0001 — Cross-model review loop with schema-constrained legs

## Context

The review-and-fix ping-pong on a pull request is repetitive work with a specific quality property: a second reader with different priors catches things the first missed. Doing it by hand means driving both halves yourself. Doing it with one model means the model that wrote a finding is the model that judges whether the finding was real — which is not a review, it is agreement with itself.

Two things had to be decided together: whether the two halves run on different models, and what crosses the boundary between the model and the code driving it.

## Decision

**The reviewer and the resolver are different model families**, and CrossRev asserts that they actually differed rather than assuming it.

**Each leg returns schema-constrained JSON, and the orchestrator acts on it.** The model returns findings or dispositions; every GitHub call — inline comments, threaded replies, thread resolution, labels, commits, the push — is made by the orchestrator. The model makes none.

**One protocol, many models.** The rubric, the severity vocabulary and the output schema are identical whichever harness runs them. Only the model varies. That is precisely what lets pass 2 judge pass 1's findings: if the two legs read a different brief, a disagreement between them says nothing.

Two layers check that the legs diverged, because silent substitution completes normally when unchecked:

1. **What was asked for.** The orchestrator knows what it invoked, so it asserts the legs differ in binary, resolved base URL or model — and refuses to run when endpoint variables are already set in the inherited environment, since those redirect the harness process-wide.
2. **What answered.** Where a harness reports the answering model, the two are compared. Where it reports none, the marker records the absence rather than implying a check that never ran.

## Options considered

**One model doing both halves.** Cheaper and simpler, and it forfeits the entire premise. A model asked to verify its own finding agrees with it.

**A model that also holds the GitHub credential and posts its own comments.** An earlier draft had the skills fetch threads and resolve them with `gh` themselves. Moving those calls into the orchestrator costs some prompt-assembly code and a slightly larger resolve schema, and it removes the credential from the one process that reads attacker-controlled text. It also makes the push guard enforceable rather than advisory, since only the orchestrator can push at all. See [0005](0005-quarantine-repository-provided-harness-config.md).

**Free-text output parsed by the orchestrator.** Every shipped harness can constrain output to a schema natively, so taking prose and parsing it would discard a guarantee that is already available. A fenced-JSON fallback exists for a harness that cannot, and it is the only path where the orchestrator's own structural check is the sole check.

**A managed review service.** Viable products exist. Ruled out on plan tier, billing model, and protocol inconsistency across models — the last of which is the property this design is built on.

## Consequences

- **Two subscriptions, or two models within one.** Cross-vendor is strongest, because a bug one model family misses reviewing it misses resolving too. One vendor with two models is weaker and still useful; it doubles usage against a single quota.
- **Validation splits by fault.** A shape failure — a missing key, a wrong type, an out-of-range enum — is an adapter or harness bug and a retry reproduces it. A semantic failure, where the shape is perfect and the content contradicts what the orchestrator supplied, is model drift and earns one more attempt. The exit code says which.
- **The orchestrator is bash.** It is string handling and API calls, so it needs no runtime, no lockfile and no dependency tree. Its tools are `gh`, `jq` and `yq`. What bash cannot do is stated rather than discovered: `jq` reads JSON not YAML, and neither tool validates a JSON Schema, so the orchestrator's own check is structural and the schemas stay flat enough for that to be sufficient. A schema that outgrows the check is the signal to add a real validator, not to let the check drift behind it.
- **The skills live outside the CLI.** Review quality improves by editing prose. Baking the prompts into the tool would make every rubric tweak a code change.
- **CrossRev never gates a merge.** It annotates, reviews, fixes and informs. Whether a pull request can merge is branch protection and required checks — the organisation's setup and the organisation's call. A team that wants a hard gate wires `crossrev/converged` to a required check themselves: CrossRev supplies the fact, they supply the policy.
