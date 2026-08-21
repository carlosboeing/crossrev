# CrossRev

[![npm](https://img.shields.io/npm/v/crossrev-ai.svg)](https://www.npmjs.com/package/crossrev-ai)
[![CI](https://github.com/carlosboeing/crossrev/actions/workflows/ci.yml/badge.svg)](https://github.com/carlosboeing/crossrev/actions/workflows/ci.yml)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![platform: macOS | Linux](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-blue.svg)](docs/installation.md)
[![status: pre-1.0](https://img.shields.io/badge/status-pre--1.0-orange.svg)](docs/ROADMAP.md)

**Two AI models review your pull request, and the second is not asked to trust the first.** One reads the diff and posts findings. A different model checks each finding against the code, fixes what holds up, argues back on what does not, replies in every thread, and pushes. Then the first looks again.

CrossRev runs on the AI subscriptions you already pay for rather than per-token API keys. One command runs it from your terminal. The same command runs it from GitHub Actions.

## How the loop works

<!-- The edge declaration order below controls the layout. Declaring RES --> Q3 before
     RES -- "finding deferred" --> BACKLOG is what keeps the arrows from crossing. -->

```mermaid
%%{init: {'flowchart': {'wrappingWidth': 300, 'rankSpacing': 28, 'nodeSpacing': 40}}}%%
flowchart TD
    PR(["Pull request opened or updated"]) --> Q1

    Q1{"Label crossrev/stop<br/>applied?"}
    Q1 -- "yes" --> STOPPED
    Q1 -- "no" --> REV("<b>Reviewer Agent</b><br/><i>(codex by default)</i><br/>&bull; reads the diff<br/>&bull; posts findings<br/>&bull; never commits")

    REV --> Q2{"Findings at or above<br/>min_fix_severity?"}
    Q2 -- "no" --> OK
    Q2 -- "yes" --> RES

    RES("<b>Resolver Agent</b><br/><i>(claude by default)</i><br/>&bull; verifies findings<br/>&bull; fixes or disputes<br/>&bull; replies and pushes")
    RES --> Q3
    RES -- "finding deferred" --> BACKLOG

    Q3{"Commit pushed?"}
    Q3 -- "yes" --> Q4
    Q3 -- "no" --> Q5

    Q5{"Finding escalated?"}
    Q5 -- "yes" --> HALT
    Q5 -- "no" --> OK

    Q4{"Review limit<br/>reached?"}
    Q4 -- "no" --> Q1
    Q4 -- "yes" --> HALT

    BACKLOG(["<b>Backlog</b><br/>a GitHub issue, or a file in the repo"])
    OK(["<b>Converged</b><br/>no findings above the threshold"])
    HALT(["<b>Halted</b><br/>a person is needed"])
    STOPPED(["<b>Stopped</b><br/>a person ended the run"])

    classDef reviewer fill:#1f6feb,stroke:#0969da,stroke-width:2px,color:#fff
    classDef resolver fill:#8250df,stroke:#6639ba,stroke-width:2px,color:#fff
    classDef ask fill:#bf8700,stroke:#9a6700,stroke-width:2px,color:#fff
    classDef good fill:#1a7f37,stroke:#116329,stroke-width:2px,color:#fff
    classDef warn fill:#bc4c00,stroke:#953800,stroke-width:2px,color:#fff
    classDef brake fill:#cf222e,stroke:#a40e26,stroke-width:2px,color:#fff
    classDef start fill:#57606a,stroke:#424a53,stroke-width:2px,color:#fff
    classDef sink fill:#6e7781,stroke:#57606a,stroke-width:2px,color:#fff
    class REV reviewer
    class RES resolver
    class Q1,Q2,Q3,Q4,Q5 ask
    class OK good
    class HALT warn
    class STOPPED brake
    class PR start
    class BACKLOG sink
```

The review limit is `policy.max_passes_per_cycle`, 3 by default. The diagram shows the common path. Six conditions can end a cycle, and [usage.md](docs/usage.md) lists all of them with their precedence.

### The Reviewer Agent

Reads, and never touches the branch. It sees the diff and every CrossRev thread already on the pull request. It returns findings as JSON validated against [`schemas/findings.schema.json`](schemas/findings.schema.json).

- Anchors each finding to a file, a line, and a side of the diff.
- Rates it `high`, `medium` or `low`.
- Categorises it as correctness, security, performance, maintainability, testing or docs.
- Re-judges the previous pass as `addressed`, `credibly-disputed`, `still-open` or `regressed`.
- Posts one inline comment per finding, plus a summary comment carrying a hidden marker.

### The Resolver Agent

A different model, and it verifies each finding against the code before acting. Agreement is not assumed, and that disagreement is the reason for running two models. Every finding gets one of five dispositions:

| Disposition | What it means |
|---|---|
| `fixed` | The code changed |
| `disputed` | Technically wrong for this codebase, with the reason in the thread |
| `deferred` | Real and worth doing, but not here — written to the backlog |
| `skipped` | Below the pass's `min_fix_severity`, so no code is changed |
| `escalated` | Needs a person, so it applies `crossrev/stop` and leaves the thread open |

It then replies in every thread, resolves the ones it settled, commits its fixes and pushes.

## Try it

A local run needs no setup. No GitHub App, no secrets, no workflows — CrossRev uses the `gh` authentication you already have, so its comments appear as **you**.

```bash
npx crossrev-ai --pr 42        # nothing installed, nothing left behind
```

To keep it, install from the bootstrap script:

```bash
curl -fsSL https://raw.githubusercontent.com/carlosboeing/crossrev/main/bootstrap.sh | bash
crossrev doctor                # checks every dependency and names the fix for each gap
crossrev review --pr 42        # one review pass: comments only, nothing is pushed
```

No token and no credential of any kind. The repository is public, so raw.githubusercontent serves the script anonymously. [installation.md](docs/installation.md) covers the other routes, including installing from a checkout you already have.

**Start with `review` on its own.** It only writes comments, so it is the cheapest way to find out whether the findings are any good. That is the question that decides whether the rest is worth it.

```bash
crossrev         --pr 42    # a cycle: both agents, alternating, up to the review limit
crossrev cycle   --pr 42    # the same thing, spelled out
crossrev review  --pr 42    # one review pass: inline comments plus a summary
crossrev resolve --pr 42    # verify each finding, fix, reply, resolve, push
crossrev status  --pr 42    # where the loop is, and how to resume it
```

### What it needs

| Tool | Why |
|---|---|
| `git`, `gh` | Reading and writing the pull request. `gh` must be authenticated |
| `jq` | The findings and resolve payloads are JSON |
| `yq` | Both config layers are YAML, and `jq` cannot read YAML |
| One of `claude`, `codex`, `agy`, `grok` | Something has to do the reviewing |

`yq` is the one usually missing on macOS — `brew install yq`. It is preinstalled on both GitHub runner families.

## Which models it drives

Four harnesses, each driven through its own adapter. With no config file anywhere, `codex` reviews and `claude` resolves. Override per run without touching the repository:

```bash
crossrev review --pr 42 --harness claude
```

Whether a harness works in CI is a property of the **runner**. It comes down to whether that harness's credential can sit in a repository secret. These lifetimes were read off installed credentials, not documentation:

<!-- crossrev:harness-table:start -->
<!-- Generated by scripts/render-harness-docs.sh — do not edit -->
| Harness | Subscription credential | Lifetime | Survives an ephemeral runner |
|---|---|---|---|
| `claude` | `claude setup-token`, purpose-built | 1 year | Yes |
| `codex` | OAuth access token in `~/.codex/auth.json` | 10 days | Yes, with the refresher below |
| `agy` | the OS keyring on macOS (`Antigravity Safe Storage`); `~/.gemini/antigravity-cli/antigravity-oauth-token` on a host with no D-Bus session bus | 56 minutes | No, CrossRev cannot seed into a hosted runner yet |
| `grok` | `~/.grok/auth.json` | 6 hours | No, CrossRev cannot seed into a hosted runner yet |
| `kimi` | OAuth access token | 15 minutes | No |
<!-- crossrev:harness-table:end -->

`crossrev init` refuses a pairing its runner cannot serve. It names the lifetime and both fixes, rather than installing workflows that fail at the first API call. `runner: self-hosted` serves every pairing, because the machine holds its own logins and refreshes them the ordinary way. [credentials.md](docs/credentials.md) covers what each secret holds and why Codex needs a second App.

## Two ways to run it

|  | **Local** | **Automated** |
|---|---|---|
| Invoked by | You, in a terminal | GitHub events |
| Setup | None beyond installing it | `crossrev init` |
| Needs a GitHub App | No | Yes |
| Needs repository secrets | No | Yes |
| Typical command | `crossrev review --pr 42` | Nothing. It runs |

**The local user never encounters the words "GitHub App".** Everything the App exists for — triggering the next workflow, proving a marker was written by a machine, minting scoped credentials — only matters once something runs unattended.

Automated mode is two commands, and the second prints an itemised plan before it changes anything:

```bash
crossrev auth login          # register and install the GitHub App, two browser approvals
crossrev init                # prints every path, secret and label it would touch, then asks once
```

Repository policy lives in `.github/crossrev.yml`, and **it is read from the base revision, never the branch under review**. A config committed on the pull request branch has no effect until it merges, so a pull request cannot rewrite the loop that reviews it. Every field is documented in [configuration.md](docs/configuration.md).

## Before you point it at something you care about

**`resolve` and `cycle` commit and push to the pull request's branch.** That is the point of the tool, and it is the thing to know first. Three rails constrain it:

- **The branch guard** refuses to push unless the checkout is on the pull request's own head branch, that branch is not the repository default, and the head repository matches the origin.
- **`policy.max_passes_per_cycle`** caps the loop at 3 by default.
- **The `crossrev/stop` label** halts it and outranks a healthy verdict. It is checked first, every pass.

To watch with no risk of a push at all, run `review` only.

**Every pass is reconstructable from the pull request alone.** The markers are the state, so there is nothing to clean up locally and nothing to lose if a run dies mid-flight.

**On a public repository, automated mode reviews pull requests from branches in the repository, including Dependabot's, and not contributions from forks.** GitHub withholds secrets from fork workflows, so CrossRev refuses them in automated mode rather than running unauthenticated. Local runs from a terminal can review fork pull requests directly, and resolve them when maintainer edits are allowed.

## What works today

| Command | State |
|---|---|
| `crossrev review --pr N` | One review pass: inline comments, a summary, the pass marker |
| `crossrev resolve --pr N` | Verifies each finding, commits fixes, replies, resolves, files deferred work |
| `crossrev cycle --pr N` | The whole loop in one process. Also what a bare `crossrev --pr N` runs |
| `crossrev status --pr N` | The state in one word, every pass with both agents, and the command that resumes it |
| `crossrev init` | Plan-then-confirm, `--dry-run`, `--yes`, `--upgrade` |
| `crossrev watchdog` | Finds stuck passes, retries once, then halts and says why |
| `crossrev doctor`, `crossrev version` | Dependency and version checks |
| `crossrev auth login`, `install`, `status`, `rotate`, `refresh` | GitHub App registration, installation, verification and key rotation |

**Exercised offline, and run against real pull requests locally.** Every command above is asserted against a stubbed `gh` boundary — no network, no model, no pull request — which catches the deterministic half, the half that fails silently. Live local runs cover the other half. The loop has reviewed real pull requests, converged on its own, found real defects in the branch under review, and pushed back on findings that did not hold up.

**No repository has had the workflows installed yet, so automated mode is unproven end to end.** That is what the `0.x` version records.

## Documentation

| Page | What's in it |
|---|---|
| [installation.md](docs/installation.md) | Getting CrossRev onto your machine, updating it, removing it |
| [usage.md](docs/usage.md) | Running the loop, what a pass writes, when it stops, where deferred work goes |
| [configuration.md](docs/configuration.md) | `.github/crossrev.yml` field by field, machine-local endpoints, environment variables |
| [credentials.md](docs/credentials.md) | Which secrets automated mode needs, and why Codex needs a second App |
| [troubleshooting.md](docs/troubleshooting.md) | The failure modes, each under the name it reports itself with |
| [architecture.md](docs/architecture.md) | The two legs, the orchestrator, the adapters, the marker and label contract, the layout |
| [adrs/](docs/adrs/) | Decision records — what was decided, what was considered, what it costs |
| [ROADMAP.md](docs/ROADMAP.md) | What's next, and what's deliberately deferred |

## Working on it

```bash
tests/run.sh      # the offline suite: no network, no model
scripts/lint.sh   # syntax plus shellcheck -S warning
```

Both are offline and take seconds. The suite stubs `gh` and `claude` onto PATH, then builds throwaway git repositories with real histories and real bare origins. So each assertion covers what CrossRev did, rather than what it printed.

`tests/stub/codex` is a deliberate tripwire. It exits loudly instead of running, because the no-config default names codex as reviewer. A fixture whose config failed to load would otherwise reach the real CLI and make a real billed call.

The repository root is the tool. `bin/crossrev` reads its libraries, skills and templates from alongside itself, so a checkout is a working installation. [architecture.md](docs/architecture.md) has the file-by-file layout.
