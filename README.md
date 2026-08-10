# revloop

A cross-model PR review loop. One model reviews a pull request and leaves inline comments; a second verifies each point and either fixes it, skips it, defers it or pushes back, replies in-thread, resolves what it handled, and pushes. Then the first looks again. Two or three passes, then it stops.

It runs on the AI subscriptions you already have rather than per-token API keys. GitHub Actions triggers it where CI exists; a terminal command triggers it where CI doesn't — the same command in both cases.

> **Work in progress.** The design and plan are complete; the code is not. See [what works today](#what-works-today) for the honest current state.

## Two ways to use it

|  | **Local** | **Automated** |
|---|---|---|
| Invoked by | You, in a terminal | GitHub events |
| Setup | None beyond installing it | `revloop init` |
| Needs a GitHub App | No | Yes |
| Needs repository secrets | No | Yes |
| Typical command | `revloop review --pr 42` | Nothing. It runs |

**The local user never encounters the words "GitHub App".** Everything the App exists for — triggering the next workflow, proving a marker was written by a machine, minting scoped credentials — only matters once something runs unattended.

## Install

```bash
git clone git@github.com:carlosboeing/claude-code-resources.git
cd claude-code-resources
tools/revloop/install.sh
```

That puts `revloop` on your PATH by symlinking it into `~/.local/bin`, and checks what's installed. `--yes` runs it non-interactively; `--bin-dir` puts it somewhere else.

**PATH is all the installer owns.** The two skills are installed separately by the [`skills` CLI](https://github.com/obra/skills), which already does this better than a bash reimplementation would:

```bash
npx skills@latest add carlosboeing/claude-code-resources --skill pr-review,pr-address
```

Then check everything's in place:

```bash
revloop doctor
```

### What it needs

| Tool | Why |
|---|---|
| `git`, `gh` | Reading and writing the PR. `gh` must be authenticated |
| `jq` | The findings and address payloads are JSON |
| `yq` | Both config layers are YAML, and `jq` cannot read YAML |
| One of `claude`, `codex`, `kimi` | Something has to do the reviewing |

`yq` is the one usually missing on macOS — `brew install yq`. It's preinstalled on both GitHub runner families.

## What works today

| Command | State |
|---|---|
| `revloop doctor` | Works |
| `revloop version` | Works |
| `revloop auth status` | Works |
| `revloop auth login` | Works — registers a GitHub App and installs it, end to end |
| `revloop auth install` | Works — installs an already-registered App |
| `revloop auth rotate` | Not built. GitHub has no API to generate an App key; it's a web-UI action |
| `revloop review`, `address`, `run`, `status`, `init` | Not built |

Anything not built says so when you run it, rather than failing like a typo.

## Registering the App

Only needed for automated mode. One App per owner — not one globally, and not one per repository.

```bash
revloop auth login              # detects the owner from the repo you're in
revloop auth login --owner your-org
```

**Two approvals in a browser, nothing to copy back.** revloop builds a manifest prefilling the name, the three permissions and the webhook setting, opens your browser at the right registration page, catches GitHub's redirect on a local port, exchanges the code for an App ID and private key, then opens the install page with your account already selected and waits until the installation actually appears.

Nothing on either page is yours to get wrong. Creating the App by hand means a required homepage URL you don't need, a webhook that defaults to *on*, an install-scope choice that decides whether it can reach an org at all, and three permissions buried in a long list of three-state dropdowns.

If the local listener can't start — no `nc`, no free port — it falls back to asking you to paste the redirect URL. That path is the floor, not the plan.

Keys are stored per owner at `~/.config/revloop/apps/<owner>.pem`, mode 0600. `revloop auth status` confirms where each App is actually installed by signing a JWT and asking GitHub, rather than assuming the setup worked.

### Why those three permissions

Contents, Issues and Pull requests, all at write, and nothing else — no Secrets, no Administration, no Workflows.

`issues:write` looks surprising and isn't trimmable: **GitHub models pull request labels under the Issues API**, and the whole loop is label-driven. It also covers filing issues for deferred findings.

## Design

The full design and the implementation plan live in this repo's working memory:

- [Design](../../docs/2-design/2026-08-10-cross-model-pr-review-loop-design.md)
- [Plan](../../docs/3-plans/2026-08-10-cross-model-pr-review-loop-plan.md)
- [Extraction runbook](../../docs/guides/guide-extracting-revloop.md) — how this becomes its own repository, if it does

## Layout

The contents of this directory are laid out as the standalone repository it may later become, so extraction is `git subtree split -P tools/revloop` rather than a reorganisation.

```
bin/revloop      entrypoint
lib/             sourced by bin/revloop — ui, preflight, auth, config, state, legs, adapters
schemas/         findings.schema.json, address.schema.json
skills/          pr-review/, pr-address/
templates/       workflows and starter config, emitted by `revloop init`
tests/           stubbed-gh suite
```
