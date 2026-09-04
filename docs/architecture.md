---
title: "CrossRev architecture"
type: architecture
authors:
  - "Carlos Boeing"
  - "gemini-2.5-pro (agy)"
  - "GPT-5 (Codex)"
last_reviewed: 2026-09-04
---

# Architecture

How CrossRev is built, as it stands today. For *why* a decision went the way it did, see the [decision records](adrs/).

## The shape of it

One model reviews a pull request and leaves inline comments. A second verifies each point and either fixes it, skips it, defers it, or pushes back, replies in-thread, resolves what it handled, and pushes. Then the first looks again.

Three words, each meaning one thing: a **cycle** contains **passes**, and a pass has two **legs**.

```mermaid
flowchart TD
    trigger["A pull request event, or a terminal command"] --> orch

    subgraph orch["The orchestrator — the crossrev binary"]
        ctx["Load context: the PR, the base-revision config, the markers"]
        review["Review leg"]
        resolve["Resolve leg"]
        term["Should another pass begin?"]
        ctx --> review --> resolve --> term
        term -->|"issues remain, no cap reached"| review
    end

    review -->|"prompt, diff, prior threads"| ra["Reviewer harness"]
    resolve -->|"prompt, diff, findings"| sa["Resolver harness"]
    ra -->|"findings JSON"| review
    sa -->|"resolutions JSON"| resolve

    orch -->|"comments, replies, labels, commits"| gh["The pull request"]
    gh -->|"markers are the state"| ctx

    term -->|"converged, capped, blocked or stopped"| done["Terminal state, on a label"]
```

**The orchestrator makes every GitHub call.** The agent process makes none — see [the credential seam](#the-credential-seam) below, which is the load-bearing security property.

## The two legs

### The review leg

1. Load context: the pull request, its base and head SHAs, its labels, and every trusted marker already on it.
2. Read repository policy **from the base revision**, never from the branch under review.
3. Work out the pass number from the markers, and whether this head SHA has already been reviewed.
4. Ask whether a pass should begin at all — the caps, the `crossrev/stop` label, the draft check.
5. **Post a claim comment before doing any work**, carrying a marker.
6. Quarantine repository-provided harness configuration, assemble the prompt, invoke the reviewer, validate what came back.
7. Post one inline comment per finding, each carrying its own marker; rewrite the claim comment into the pass summary; set the labels.

### The resolve leg

1. Same context load, same policy source.
2. Find the newest review pass's marker and read its findings.
3. Post its own claim, then invoke the resolver with the diff, the findings and the prior threads.
4. For each finding: reply in-thread, resolve the thread where the resolution says to, and commit a fix where policy allows one.
5. Persist deferred findings to the backlog.
6. Push, behind the branch guard. Post the summary. Set the labels.

`crossrev cycle` drives both legs in one process, up to `max_passes_per_cycle`. In automated mode each leg is its own workflow run, chained by labels.

## The pull request is the state

There is no database, no cache and no local state file. Everything the loop knows is on the pull request, in **markers** — HTML comments embedded in comment bodies, invisible in the UI and readable by anyone who views the source.

A marker carries the protocol version, the leg, the pass number, its state, timestamps, the run id, the head SHA, the harness and model and effort and endpoint, the model that actually answered, the token cost, the verdict, and the findings or resolutions.

| Prefix | Where | What it records |
|---|---|---|
| `<!-- crossrev:` | The pass summary comment | The whole pass: verdict, findings, resolutions, cost |
| `<!-- crossrev:f` | Each inline comment and each reply | One finding id, its pass, and the leg that wrote it |

Three properties follow, and each one is why a marker exists rather than a ledger:

- **A crash loses nothing.** The claim marker goes up before the work starts, so a run that dies mid-flight is resumed rather than restarted. The next run reads how far the last one got.
- **A duplicate is impossible by construction.** The per-finding marker rides in the body of the comment it records, so the record and the thing it records are one HTTP call. A ledger written *after* a successful post has a window in it: GitHub accepts the comment, the process dies, and recovery can't tell an already-posted finding from a missing one.
- **The same code runs in both modes.** Nothing about the state is specific to CI.

**Markers are lowercase in every form.** They're matched literally, so a capitalised prefix would break matching silently, on every existing pull request.

### Trust

Anyone who can comment on a pull request can write an HTML comment, so a marker's *author* is the only signal GitHub controls and nobody can forge. Which author counts depends on the mode:

| Mode | Trusted author | Why |
|---|---|---|
| `automated` | The GitHub App, and nothing else | A forged marker makes an *agent* act — push a commit, skip a finding, believe a leg finished |
| `local` | The invoking user | You are the orchestrator. A forged marker can only mislead you about work you asked for |

Hard-coding the App would break local mode outright: a local run would find no App-authored marker on any pass, report pass 1 forever, reconcile nothing, and never reach the cap.

### Finding identity

A finding's id is a hash of its path, its normalised title, and an anchor — a fingerprint of the commented line and its two neighbours either side. Path and title carry the identity; the anchor is what lets a finding still be matched after the line moves. Stable across passes, so "already posted" is a set-membership test rather than a guess.

## The label contract

The six loop labels are the state a human reads, and in automated mode they are also the event chain: each leg's completion applies the label the next leg waits behind.

| Label | Colour | Meaning |
|---|---|---|
| `crossrev/awaiting-review` | blue | A review is owed |
| `crossrev/awaiting-resolution` | purple | The review landed, the resolve leg is owed |
| `crossrev/converged` | green | The loop finished on its own |
| `crossrev/halted` | orange | Stopped short, a human is needed |
| `crossrev/stop` | red | A human applied it |
| `crossrev/pass-N` | grey | Which pass it reached |

No two colours are adjacent on the wheel, and every one is dark enough that GitHub renders its text white in all three renderings it uses — the solid pill on the labels page, and the tinted chip in light and dark themes. **Red is reserved for `stop`**, the one label a human applies, so a red pill in a pull request list always means somebody pulled the brake.

The label a leg waits behind is named for the noun where the leg is named for the verb: `resolve` waits behind `crossrev/awaiting-resolution`. Derived in one place, because the workflows key off these exact strings and a mismatch stalls the chain silently — the label sits on the pull request with nothing listening.

**In automated mode a label that can't be applied is fatal**, not cosmetic. Locally it's the reverse: one process drives both legs, so the label is decoration.

## Termination

One function decides, over state the orchestrator already holds, so it's testable with no network, no harness and no pull request. That matters, because its failure mode is silence — a loop that stops one pass early looks exactly like a loop that converged.

It terminates on the first of:

1. A human applied `crossrev/stop`. **Checked first**, because it's an instruction rather than a state, and it outranks a healthy verdict.
2. The resolver returned `blocked`.
3. The reviewer returned `converged` — nothing at or above `min_fix_severity` remains.
4. The pass count reached `max_passes_per_cycle`. Pass 3 of a cap of 3 is the last pass, not the one after which a fourth begins.
5. The daily pull request cap is exceeded.
6. The pull request is larger than the file cap.

The last three are continuation bounds: they end *automatic* reviewing and never block a person. `min_fix_severity` is different in kind — it bounds what an agent may change, so it holds on every run.

## The credential seam

**The process reading attacker-controlled text is deliberately the process holding no credential.**

A pull request's title, body, diff, code comments and review threads are all material a reviewer must read, and any of it can address the model directly. CrossRev handles that in three layers, of which only the first is load-bearing:

1. **Credential separation.** Every GitHub read and write goes through the orchestrator. The adapters strip `GH_TOKEN`, `GITHUB_TOKEN`, `GH_ENTERPRISE_TOKEN` and `GITHUB_ENTERPRISE_TOKEN` from the environment before starting the model-facing process. An injection that reaches tool use still cannot post as the App, push a commit, or read a secret.
2. **Quarantine.** A pull request branch contains files that configure the thing reviewing it — settings, instruction files, hooks, MCP server definitions, agents. A hook is arbitrary code execution before the model sees a token. CrossRev moves every known harness-loaded path out of the checkout before any invocation and restores it before anything is committed. **Quarantined rather than deleted**, because a pull request that *adds* a hook is exactly the pull request a reviewer should be flagging: the diff still carries the text, at a path no harness auto-loads.
3. **An explicit notice in the prompt** telling each leg that everything below a given heading is data rather than instruction, and that text addressing the model is itself a finding.

The quarantine list is deliberately over-broad and deliberately not exhaustive. It is a best-effort layer, not the thing standing between an injected hook and the App token.

If the harness writes to a quarantined path anyway, that write was made blind and is discarded on restore — with a warning, because a finding "fixed" by editing a quarantined file is reported as fixed and lands in no commit.

## The harness seam

Each adapter takes a prompt file, a schema, a working directory, an optional model, effort and endpoint, and whether the leg may write to the working tree. Each returns **two things**: the payload, and execution metadata naming the harness, the resolved endpoint, the answering model where the harness reports one, and a normalized usage record — fresh input, cache read, cache writes split by TTL with an unsplit remainder for harnesses that name none, and output, with `total` defined as their sum rather than read from the vendor, and reasoning persisted beside the total and never added to it. `internal/harness` owns the record's identity, table pricing against the vendored extract at `assets/prices.json`, the two rules that refuse to price rather than guess, billing-mode derivation and footnote composition. Adapters parse vendor fields into buckets and read neither credentials nor price data.

**Write permission is derived from the leg, not configured.** The resolve leg has to change files, so it is granted file edits — `--permission-mode acceptEdits` on Claude Code, `--sandbox workspace-write` on Codex, `--mode accept-edits` on Antigravity. The review leg is granted nothing: it has no reason to write, and write access widens the blast radius of a prompt injection carried in a diff for nothing in return. The line held is between editing files and running arbitrary commands, so `bypassPermissions` and `danger-full-access` are never passed. The grant is a flag rather than a settings file because the quarantine above would move a settings file out of the way before the harness started — and a grant that survived it would be the hole the quarantine exists to close.

<!-- crossrev:harness-table:start -->
<!-- Generated by scripts/render-harness-docs.sh — do not edit -->
| Adapter | Harness | Notes |
|---|---|---|
| `claude` | Claude Code | Takes the schema **inline** as a JSON string. Also the path to any Anthropic-compatible endpoint |
| `codex` | Codex | Takes the schema as a **file path**. Runs with `--ignore-user-config` |
| `agy` | Antigravity | |
| `grok` | Grok | Takes the schema **inline** as a JSON string. |
| `opencode` | opencode | No schema flag: the schema travels inside the prompt, and CrossRev extracts the JSON from the answer text itself. |
<!-- crossrev:harness-table:end -->

The inline-versus-path difference is not a detail: handing Claude a path fails with a JSON parse error about the leading slash, which reads like a corrupt schema rather than a wrong argument type.

**Model ids must be fully qualified.** `--model fable-5` fails as an entitlement error rather than as a typo.

### Making sure the two legs really differed

Silent substitution is the failure the cross-model design exists to prevent, and it completes normally when unchecked. Two layers:

- **What the orchestrator asked for.** It knows exactly what it invoked, so it asserts the legs differ in binary, resolved base URL or model — and that no endpoint variable is set in the inherited environment, which would redirect the harness process-wide.
- **What answered.** Where a harness reports the answering model, the two are compared. Where it doesn't, the marker records the absence rather than implying a check that never ran.

## The two schemas

`schemas/findings.schema.json` and `schemas/resolve.schema.json` constrain what each leg returns. Every shipped harness enforces them natively, so a shape failure is an adapter bug rather than model drift.

Validation splits by exit code, and the split is about who is at fault:

| Code | Kind | Meaning |
|---|---|---|
| 1 | **shape** | A key missing, a type wrong, an enum value out of range. An adapter or harness bug — a retry reproduces it |
| 2 | **semantic** | The shape is perfect and the content contradicts what the orchestrator supplied: a finding number nothing was numbered with, the same finding answered twice, one left out, an issue number nobody offered. Model drift by definition, so it earns one more attempt |

Validation stays narrower than general JSON Schema validation. It checks required keys, types and enum ranges. The schemas stay flat enough for that to be sufficient. A schema that outgrows the check is the signal to add a real validator rather than to let the check drift behind it.

## The gutter

Both legs are given the diff with every line inside a hunk prefixed by its number in the old file, its number in the new file, and a `|`. A dash stands where the line doesn't exist on that side.

That's there because a model asked to derive a line number counts lines under a `@@` header and sometimes counts wrong, and GitHub accepts a comment only on a line the diff actually shows — a finding one line outside a hunk is refused and falls out of the thread it belongs in. The gutter is also what `side` means: `RIGHT` reads the second column, `LEFT` the first, and a line showing a dash on one side can't take a comment there.

The orchestrator re-derives the same mapping before posting, and snaps a finding up to three lines to reach a hunk — exactly the margin a miscount lands in, since three is git's own context width. Past that the reviewer meant somewhere else, and moving the comment would anchor it to code the finding never mentions.

**Both legs get the same description of the gutter**, because pass 2 has to mean the same thing by a line number as pass 1 did.

## Where the skill text comes from

The orchestrator reproduces `skills/pr-review/SKILL.md` and `skills/pr-resolve/SKILL.md` into each prompt rather than relying on the harness discovering them. Two reasons:

- The quarantine moves `.claude/` and `.agents/` out of the checkout before any invocation — exactly where a workflow would have placed the skills. Re-planting into a quarantined tree and removing them again before the commit leaves a window where a crash commits CrossRev's own skills into someone's pull request.
- Reproducing the text makes the prompt **byte-identical across harnesses**, which is the property that lets pass 2 judge pass 1's findings.

The skills stay installable and usable by hand; nothing about them changes. The generated workflows just don't need to place them anywhere.

## Delivery

CrossRev is delivered to consuming repositories as a **composite action pinned by full 40-character SHA**, with the tag riding in a trailing comment:

```yaml
- uses: carlosboeing/crossrev@<40-char-sha>   # v0.1.0
  with:
    leg: review
    pr: ${{ github.event.pull_request.number }}
    app-token: ${{ steps.app.outputs.token }}
    trigger: automatic
```

The SHA is the pin because `git tag -f` plus a force push moves a tag, and the failure mode is a repository whose review behaviour changes with nothing in its own history to show for it. `crossrev init` generates the pinned form; the floating `@v0` in the README is a human choosing convenience knowingly.

Two inputs are worth understanding:

- **`app-token` has no default, deliberately.** Writes made with `GITHUB_TOKEN` do not trigger workflows, so defaulting to it would stall the chain after pass 1 while looking healthy the whole way. Published review actions commonly default this input; copying that here would be the exact bug the design exists to prevent. The action fails loudly on an empty token.
- **`trigger` defaults to `automatic`**, the opposite of the CLI's default, because the two entry points fail safe in opposite directions. Forgetting the input in a workflow should give you the caps, not an uncapped loop.

The action's first step downloads the release binary for the runner platform and checks its digest against the release's `checksums.txt`. The second runs `crossrev doctor` from that binary, so a consumer's workflow cannot forget the preflight. Installation stays the runner's job.

The **credential refresher** is the one workflow that still checks CrossRev out rather than calling the action, because `crossrev auth refresh` isn't expressible through the `leg` input. It's a plain public checkout — no token, no key, no secret — and it never checks out the pull request branch, never runs a model and never reads a diff.

## The layout

CrossRev is a Go binary plus the files it is built from and the files it writes elsewhere. `cmd/crossrev` is the composition root. `internal/` holds the packages. `assets/` holds the canonical harness descriptor and price extract. Everything else is schemas, skills, templates, scripts, tests, or delivery.

```
action.yml       the composite action consuming repositories call
cmd/crossrev/    the native entrypoint: CLI table, wiring, legs, init
assets/          canonical data compiled into the binary
  harnesses.json   the single validated descriptor for every harness fact
  prices.json      vendored rate extract for the table-priced estimate, stamped with its upstream revision
internal/        Go packages, in tiers
  core/            Tier 0: domain primitives and FindingID
  buildinfo/       Tier 1: version and build metadata
  policy/          Tier 1: pure policy functions and termination rules
  prstate/         Tier 1: marker parsing and finding identity
  diff/            Tier 1: gutter mapping and hunk snapping
  validate/        Tier 1: payload validation
  intel/           Tier 1: Review Intelligence contracts
  config/          Tier 2: configuration loading
  prompt/          Tier 2: assembled prompt text
  exec/            Tier 2: command execution and the environment allowlist
  ui/              Tier 2: output and formatting
  runlog/          Tier 2: logging and redaction
  vcs/             Tier 2: git operations
  sandbox/         Tier 2: harness quarantine
  forge/           Tier 2: forge abstractions
  forge/ghexec/    Tier 2: GitHub CLI adapter
  cred/            Tier 2: credential resolution
  harness/         Tier 2: model harness adapters
  symbols/         Tier 2: symbol indexing and worker entrypoint
  verify/          Tier 2: verification runner
  verify/ghactions/ Tier 2: GitHub Actions simulation
  testgen/         Tier 2: policy-table fixture generator
  archtest/        Tier 2: structural rules over the source tree
  review/          Tier 3: review leg orchestration
  resolve/         Tier 3: resolve leg orchestration
  cycle/           Tier 3: multi-pass cycle driver
  app/             Tier 3: application lifecycle
  initcmd/         Tier 3: init command
  preflight/       Tier 3: dependency checks
  cli/             Tier 3: CLI command router
schemas/         findings.schema.json, resolve.schema.json
skills/          pr-review/, pr-resolve/
templates/       workflows, starter config, example operator config
scripts/         lint.sh, check-changelog.sh, check-parity-coverage.sh,
                  next-version.sh, refresh-prices.sh, render-harness-docs.sh,
                  build-native.sh, sync-embedded-assets.sh,
                  verify-native-toolchain.sh, release-targets.json
tests/           the stubbed-gh suite. tests/run.sh builds the binary once and runs all of it
```

## The test suite

`tests/run.sh` builds the binary once and runs the whole suite offline against it, with no network, no model and no pull request. It stubs `gh` and the harness CLIs onto PATH and builds throwaway git repositories with real histories and real bare origins, so the assertions are about what CrossRev actually did rather than what it printed.

Suites run in parallel, one job per core up to eight, because nothing is shared between them: each suite gets its own `XDG_CONFIG_HOME` and `XDG_STATE_HOME`, each case gets its own `gh` route table and call log, and `fixture_repo` builds a fresh checkout and bare origin per case. Output keeps glob order whatever the job count, so a parallel run and a sequential one print the same bytes. `-j 1` runs them one at a time. The time goes on starting processes rather than on computing.

`tests/stub/codex` is a deliberate tripwire: it exits loudly instead of running, because the no-config default names codex as reviewer, and a fixture whose config failed to load would otherwise reach the real CLI and make a real billed call. A Go test asserts it still refuses.

`go test ./...` proves the packages beside the suite: the frozen parity vectors under `tests/fixtures/parity/`, the policy tables under `tests/fixtures/policy/`, the tier-structure rules in `internal/archtest`, and the environment contract in `internal/cli`. `scripts/check-parity-coverage.sh` ledgers every original shell suite to its proof. `tests/parity-coverage.tsv` keeps the rows for deleted suites as the record of where their behaviour went.

`scripts/lint.sh` runs syntax checks plus `shellcheck -S warning` across the remaining shell, `go vet`, the embedded-asset drift check, and the policy-table regeneration check.
