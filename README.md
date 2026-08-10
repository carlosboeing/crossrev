# revloop

A cross-model PR review loop. One model reviews a pull request and leaves inline comments; a second verifies each point and either fixes it, skips it, defers it or pushes back, replies in-thread, resolves what it handled, and pushes. Then the first looks again. Two or three passes, then it stops.

It runs on the AI subscriptions you already have rather than per-token API keys. GitHub Actions triggers it where CI exists; a terminal command triggers it where CI doesn't — the same command in both cases.

> **Work in progress.** Every command is built and tested offline, and none has run against a real pull request yet. See [what works today](#what-works-today) for the honest current state.

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

**PATH is all the installer owns.** revloop reads the two skills straight out of `skills/` and reproduces them into each prompt, so the loop works with nothing installed. Install them anyway if you want to use them by hand, via the [`skills` CLI](https://github.com/obra/skills):

```bash
npx skills@latest add carlosboeing/claude-code-resources/tools/revloop \
  --skill pr-review --skill pr-address
```

Two details found by running it, both easy to get wrong:

- **Point it at `tools/revloop`, not the repo root.** From the root it finds the six standalone skills in `skills/` and does not walk into `tools/revloop/skills/`.
- **One name per `--skill` flag.** A comma-separated list matches nothing, and it reports "No matching skills found" rather than complaining about the syntax.

It also installs non-interactively when it detects an agent driving it, into the *current directory* by default — so run it from where you want the skills, not from a repo you were only reading.

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
| One of `claude`, `codex`, `agy` | Something has to do the reviewing |

`yq` is the one usually missing on macOS — `brew install yq`. It's preinstalled on both GitHub runner families.

## What works today

| Command | State |
|---|---|
| `revloop review --pr N` | Built. One review pass: inline comments, a summary, the pass marker |
| `revloop address --pr N` | Built. Verifies each finding, commits fixes, replies, resolves, files deferred work |
| `revloop run --pr N` | Built. The whole loop in one process, up to `max_passes` |
| `revloop status --pr N` | Built. Position *and* interruption, with the command that resumes it |
| `revloop init` | Built. Plan-then-confirm, `--dry-run`, `--yes`, `--upgrade` |
| `revloop watchdog` | Built. Finds stuck legs, retries once, then halts and says why |
| `revloop doctor` | Works |
| `revloop version` | Works |
| `revloop auth status` | Works |
| `revloop auth login` | Works — registers a GitHub App and installs it, end to end |
| `revloop auth install` | Works — installs an already-registered App |
| `revloop auth rotate` | Built. Guided, because GitHub has no API to generate an App key. It proves the new key works before replacing the old one |
| `revloop auth refresh` | Built. The refresher job's only command, and the only thing that writes a rotating harness credential |

**Not yet run against a real pull request.** Every one of those is exercised offline against a stubbed `gh` boundary — 330 assertions, no network, no model, no PR. That catches the deterministic half, which is the half that fails silently. It does not tell you whether the reviews are any good, and no repository has had the workflows installed yet.

## Subscriptions in CI

revloop runs on the subscriptions you already pay for rather than per-token API keys. Whether that works in CI is a property of the **runner**, because it comes down to whether a harness's credential can sit in a repository secret. These lifetimes were read off installed credentials, not documentation:

| Harness | Subscription credential | Lifetime | Survives an ephemeral runner |
|---|---|---|---|
| `claude` | `claude setup-token`, purpose-built | 1 year | Yes |
| `codex` | OAuth access token in `~/.codex/auth.json` | 10 days | Yes, with the refresher below |
| `agy` | OAuth access token in `~/.gemini/oauth_creds.json` | ~1 hour | No |
| `kimi` | OAuth access token | 15 minutes | No |

`revloop init` refuses a pairing its runner cannot serve, naming the lifetime and both fixes, rather than installing workflows that fail at the first API call. `runner: self-hosted` serves every pairing — the machine holds its own logins and refreshes them the ordinary way, so there are no secrets, no refresher and no rotation chain at all.

**Refresh tokens rotate: using one consumes it.** So the legs never refresh. On a hosted runner with Codex in the pairing, `init` generates `revloop-token-refresh.yml` — one scheduled job, on its own concurrency group, that is the only writer. Each leg restores a copy into a throwaway home and discards it, and a leg holding under an hour of token life stops rather than refreshing in flight.

That workflow is also the only place revloop needs `Secrets: write`, which is why it gets a second App (`--role refresher`) rather than widening the loop's. The refresher job never checks out the pull request branch, never runs a model and never reads a diff or a comment — there is nothing in it to inject into. `init` derives whether you need one from the pairing and never asks; most configurations, including the default, never see it.

## Local endpoints

`~/.config/revloop/config.yml` holds the endpoints that only exist on your machine — an Ollama box on your LAN, a router on localhost. There's a commented example at [`templates/operator-config.yml`](templates/operator-config.yml):

```bash
mkdir -p ~/.config/revloop
cp tools/revloop/templates/operator-config.yml ~/.config/revloop/config.yml
```

Endpoint definitions merge by name with the repository's, and this file wins. So a repo can declare a public endpoint while you point the same name at your own instance, with no change to the repo. Tokens stay out of both files: `token_env` names a variable, and its value comes from your shell locally or a repository secret in CI.

An endpoint a leg names but nothing defines is a hard failure. It never falls back to the vendor's own API — that would mean running Claude while the config says Ollama, which is the silent substitution the whole cross-model design exists to catch.

## Where the skill text comes from

The orchestrator reproduces `skills/pr-review/SKILL.md` and `skills/pr-address/SKILL.md` into each prompt rather than relying on the harness discovering them. That's a departure from the design's CI wiring, for two concrete reasons.

The quarantine moves `.claude/` and `.agents/` out of the checkout before any invocation, which is exactly where a workflow would have placed the skills. Re-planting into a quarantined tree and removing them again before the commit leaves a window where a crash commits revloop's own skills into someone's pull request. And reproducing the text makes the prompt byte-identical across harnesses, which is the property that lets pass 2 judge pass 1's findings.

The skills stay installable and usable by hand; nothing about them changes. The generated workflows just don't need to place them anywhere.

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
action.yml       composite action manifest, unusable until revloop is public
bin/revloop      entrypoint
lib/             sourced by bin/revloop
  ui.sh            output voice — six rules, enforced by the helpers' shapes
  preflight.sh     dependency checks that name the fix, not just the gap
  auth.sh          GitHub App registration via the manifest flow, per owner and role
  config.sh        two-layer config, endpoint and sink resolution
  credentials.sh   restoring a rotating subscription credential, read-only
  sandbox.sh       quarantining repository-provided harness configuration
  state.sh         labels, markers, trust, revision detection, finding ids
  legs.sh          termination, the push guard, the divergence guard
  github.sh        every GitHub read and write revloop makes
  validate.sh      structural jq checks on what a harness returned
  prompt.sh        what each leg is given
  run.sh           the two legs, the drivers, the watchdog
  init.sh          the plan-then-confirm upgrade to automated mode
  adapters/        claude.sh, codex.sh, agy.sh
schemas/         findings.schema.json, address.schema.json
skills/          pr-review/, pr-address/
templates/       workflows, starter config, example operator config
scripts/         lint.sh — syntax and shellcheck across everything
tests/           the stubbed-gh suite. `tests/run.sh` runs all of it
```

## Working on it

```bash
tools/revloop/tests/run.sh      # 248 offline assertions, no network, no model
tools/revloop/scripts/lint.sh   # syntax plus shellcheck -S warning
```

Both are offline and take seconds. The suite stubs `gh` and `claude` onto PATH and builds throwaway git repositories with real histories and real bare origins, so the assertions are about what revloop actually did rather than what it printed. `tests/stub/codex` is a deliberate tripwire: it exits loudly instead of running, because the no-config default names codex as reviewer and a fixture whose config failed to load would otherwise reach the real CLI and make a real billed call.
