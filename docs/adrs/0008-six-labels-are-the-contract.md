---
date: 2026-08-11
title: "Six labels are the loop's contract, and crossrev/stop outranks a healthy verdict"
type: adr
status: approved
scope: [labels, presentation, termination]
---

# 0008 — Six labels are the loop's contract, and `crossrev/stop` outranks a healthy verdict

## Context

Labels do two jobs at once. In automated mode they are the event chain: each leg's completion applies the label the next leg waits behind, and a GitHub event on that label starts the next run. For a human they are the state of the loop, visible in a pull request list without opening anything.

Both jobs are served by the same six strings, which means the strings are an interface and not decoration.

## Decision

**Six labels, and they are the contract.**

| Label | Colour | Meaning |
|---|---|---|
| `crossrev/awaiting-review` | blue | A review is owed |
| `crossrev/awaiting-resolution` | purple | The review landed, the resolve leg is owed |
| `crossrev/converged` | green | The loop finished on its own |
| `crossrev/halted` | orange | Stopped short, a human is needed |
| `crossrev/stop` | red | Stop the loop |
| `crossrev/pass-N` | grey | Which pass it reached |

**`crossrev/stop` is checked first, every pass, and it outranks a healthy verdict.** It is an instruction rather than a state. It is deliberately not spelled `halted` — a request and a terminal state one letter apart is a footgun.

**Red is reserved for `stop`**, the one label a human applies, so a red pill in a pull request list always means somebody pulled the brake rather than that the loop had trouble.

**The label a leg waits behind is named for the noun where the leg is named for the verb**, so `resolve` waits behind `crossrev/awaiting-resolution`. Derived in one place, because the workflows key off these exact strings and a mismatch stalls the chain silently — the label sits on the pull request with nothing listening.

**Colours and descriptions live in one map each**, not a constant per call site. The watchdog mints `crossrev/halted` itself when it gives up, and a second hex or a second description there is how one label ends up looking two ways depending on which code path created it.

**Every colour clears 4.5:1 contrast in all three renderings GitHub uses** — the solid pill on the labels page, and the tinted chip in light and dark themes. GitHub picks the label's text colour by brightness and cannot be told otherwise, so the background is the only lever. A lighter first palette left the pass label at 4.42:1 on a pull request chip; one Primer step darker again reads better on the labels page and fails in dark mode, where GitHub derives the chip's text from the same hex.

**Each label carries its own description.** A label description is the only place GitHub shows a reader what a label means without them going looking — it is the hover text on the pill and the second column on the labels page. One shared string answered nothing.

## Options considered

**A single `crossrev` label plus state in the marker.** The marker is the state ([0002](0002-the-pull-request-is-the-state.md)), so this is tempting. It fails the other job: no event to chain on, and nothing legible in a pull request list.

**Six shades of one hue.** Rejected on the reading test — a row of near-identical pills carries no state at a glance, which is the whole reason the labels are visible.

**A `critical` severity rung that halts and escalates.** Considered and rejected in favour of an action: the "stop, get a human" case is the `escalated` resolution, which applies `crossrev/stop` and leaves the thread open. An action beats a label, and a severity word with no distinct consequence teaches readers that the scale is decorative.

**Spelling the human's brake `crossrev/halted` and letting it double as the terminal state.** Rejected: the two mean opposite things about who acts next, and the difference has to survive being read quickly.

## Consequences

- **The one case CrossRev applies `crossrev/stop` itself is an escalated finding**, which is the same semantics rather than an exception: control is being handed to a human, deliberately, and the label is how that is said. Everything else the tool applies is a state.
- **In automated mode a label that cannot be applied is fatal, not cosmetic**, because the next workflow then has no event to hear. Locally it is the reverse — one process drives both legs, so the label is decoration.
- **Absence is not the failure mode.** GitHub's add-labels endpoint creates a missing label with default metadata, which is why `crossrev init` mints the six deliberately with their colours and descriptions rather than letting them appear grey and undocumented.
- **`crossrev init --upgrade` recolours labels minted before this palette**, so there is no migration to run.
- **A seventh label exists and is not part of the contract.** `crossrev/watchdog-retried` is yellow and is the watchdog's own bookkeeping, reading as a qualifier on whatever state it sits beside.
- **Termination is one function over state the orchestrator already holds**, so it is testable with no network, no harness and no pull request. That matters because its failure mode is silence: a loop that stops one pass early looks exactly like a loop that converged. It terminates on the first of `stop`, a blocked resolver, a converged reviewer, the pass cap, the daily pull request cap, or the file cap — in that order, with `stop` first.
