---
date: 2026-08-10
title: "The pull request is the state"
type: adr
status: approved
scope: [architecture, state, idempotency]
---

# 0002 — The pull request is the state

## Context

The loop runs across passes and, in automated mode, across separate workflow runs on separate ephemeral machines. Something has to remember which pass it is on, what the last review found, which findings already have comments, and how far a run that died got.

Anywhere that state could live outside the pull request is a place it can disagree with the pull request.

## Decision

**State lives on the pull request, in labels and in markers.** There is no database, no cache and no local state file. Markers are HTML comments embedded in comment bodies — invisible in the GitHub UI, readable by anyone who views the source.

A marker carries the protocol version, the leg, the pass number, its own state, timestamps, the run id, the head SHA, the harness and model and effort and endpoint, the model that actually answered, the token cost, the verdict, and the findings or resolutions.

Three mechanisms make a leg survive being run twice:

1. **Claim before work.** A leg's first write is a marker comment in state `started`. A second invocation that finds an unfinished claim for the same pull request, pass and leg enters recovery rather than starting fresh.
2. **The ledger is the write itself.** Every outward write carries its finding id in its own body, as a hidden marker on the comment or reply being posted. Recovery lists the pull request's comments from the trusted author, reads the ids out of the bodies, and posts only the difference.
3. **Complete last.** The claim marker is edited to `complete` only after every write has landed, so a marker without completion means the leg died mid-flight — which is exactly what the watchdog looks for.

Write ordering is part of the decision: inline comments first, then the summary comment, then completion. Losing the last write costs a retry; losing the first would cost a duplicate.

**Trust is resolved by mode.** Anyone who can comment on a pull request can write an HTML comment, so a marker's author is the only signal GitHub controls and nobody can forge. In `automated` mode the trusted author is the GitHub App and nothing else, because a forged marker there makes an agent act. In `local` mode it is the invoking user, where a forged marker can only mislead you about work you asked for.

## Options considered

**A ledger file written after each successful post.** This was the first design and it has a window in it: GitHub accepts the comment, the process dies, and the mapping from that comment to its finding is gone. Recovery then cannot tell an already-posted finding from a missing one, and posts it twice. Carrying the id in the body closes the window *by construction*, because the record and the thing it records are one HTTP call.

**Hard-coding the App as the trusted author.** It would break local mode outright: a local run would find no App-authored marker on any pass, report pass 1 forever, reconcile nothing, and never reach the cap.

**Relying on GitHub concurrency groups for idempotency.** They cancel older runs; they do not make external writes atomic. A crash between posting inline comments and writing the marker would still duplicate threads on retry.

## Consequences

- **A crash loses nothing.** The next run reads how far the last one got and posts the difference.
- **A human can read the state.** The markers are in the comment source, and `crossrev status` is a rendering of them rather than a separate account of events.
- **The same code runs in both modes.** Nothing about the state is specific to CI, so the two modes are thin drivers over identical legs — and you can compare them on real pull requests without touching leg code.
- **Comment idempotency is exact; commit idempotency is best-effort, and saying so is the honest position.** A finding id makes "already posted" a set-membership test. Pushed code cannot work that way. The resolve leg records the commit SHA it produced, so recovery that finds one skips to reconciling comments. If it crashed *before* recording, recovery re-runs the agent against a working tree where the fix is already applied, and an agent that re-reads the code sees the fix present. That is a behavioural guarantee from the skill, not a structural one from the orchestrator.
- **Markers are matched literally, so their prefix is lowercase everywhere.** A capitalised prefix would break matching silently, on every existing pull request. This is why the casing rule in [0010](0010-name-crossrev.md) treats markers as code rather than prose.
- **A finding's id is a hash of its path, its normalised title and an anchor** — a fingerprint of the commented line and its two neighbours either side. Path and title carry the identity; the anchor lets a finding still be matched after the line moves. One consequence for the rendering: decorate the heading, never the `title` field, or every finding re-posts as new on the next pass.
- **A refused pass gets a marker too**, flagged `declined`, so `status` can render the refusal rather than inferring it from a label plus the prose of a comment. It is excluded from pass numbering, revision detection and the daily count, because it records a pass that did not happen.
