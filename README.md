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

One command, from nothing:

```bash
gh api repos/carlosboeing/claude-code-resources/contents/tools/revloop/bootstrap.sh \
  -H "Accept: application/vnd.github.raw" | bash
```

`gh api` rather than `curl` because the repository is private, and `curl` cannot reach the credentials `gh` and `git` already hold — it has to be handed a token, which then sits in your shell history and in the process table for every local process to read. `gh` needs no such handover, and revloop requires it anyway. **When the repository goes public this becomes a plain `curl` with no token and no `gh`**, and nothing inside the script changes.

That fetches [`bootstrap.sh`](bootstrap.sh), which clones the repository somewhere durable (`~/.local/share/revloop` by default), then hands to `install.sh`. Pass `--ref <tag>` to pin a known revision, `--dir` to clone elsewhere.

**Every step is optional and it is safe to re-run.** If you already have a checkout — you're inside one, you have `revloop` installed already, or one is at the destination — it uses that instead of cloning again.

Already have the repository? Skip the bootstrap entirely:

```bash
tools/revloop/install.sh
```

That puts `revloop` on your PATH by symlinking it into `~/.local/bin`, and checks what's installed. `--yes` runs it non-interactively; `--bin-dir` puts it somewhere else.

**The checkout is the installation.** `install.sh` symlinks rather than copies, and `revloop` reads its libraries, skills and templates from the checkout at runtime. So `git pull` updates the tool with no reinstall step — and moving or deleting the clone uninstalls it.

That also **offers** to install `pr-review` and `pr-resolve` for your harnesses, and hands over to the [`skills` CLI](https://github.com/obra/skills) if you say yes. That CLI runs its own flow — it detects which harnesses you have, and asks about project versus global scope and whether to symlink. Those are its questions, deliberately: suppressing them would mean making three choices on your behalf and calling it convenience. `--skills` and `--no-skills` decide the offer up front for scripted installs, and only the scripted path passes flags that answer them.

**It stays an offer rather than part of the install**, for two reasons. The loop does not need them: revloop reads both skills out of the checkout and reproduces their text into each prompt, so installing them is for invoking them by hand in an ordinary session. And it is the only step that wants Node — everything else runs on git, bash and coreutils, so a hard dependency on `npx` for an optional extra would be a poor trade. With no `npx`, or no terminal to ask at, it skips and prints the command.

By hand, it is:

```bash
npx skills@latest add ./tools/revloop --skill pr-review --skill pr-resolve
```

Three details found by running it, each of which fails by reporting nothing rather than erroring:

- **Point it at `tools/revloop`, not the repo root.** From the root it finds the six standalone skills in `skills/` and does not walk into `tools/revloop/skills/`.
- **One name per `--skill` flag.** A comma-separated list matches nothing, and it reports "No matching skills found" rather than complaining about the syntax.
- **It goes non-interactive when an agent is driving it**, printing "Agent detected". In that mode nobody is asked anything and the scope defaults to project — which, run from inside the clone, means into the clone: present in the repository you were only installing from, absent everywhere you work. Pass `--global` explicitly whenever it is not a human answering.

Then check everything's in place:

```bash
revloop doctor
```

### What it needs

| Tool | Why |
|---|---|
| `git`, `gh` | Reading and writing the PR. `gh` must be authenticated |
| `jq` | The findings and resolve payloads are JSON |
| `yq` | Both config layers are YAML, and `jq` cannot read YAML |
| One of `claude`, `codex`, `agy` | Something has to do the reviewing |

`yq` is the one usually missing on macOS — `brew install yq`. It's preinstalled on both GitHub runner families.

## What works today

| Command | State |
|---|---|
| `revloop review --pr N` | Built. One review pass: inline comments, a summary, the pass marker |
| `revloop resolve --pr N` | Built. Verifies each finding, commits fixes, replies, resolves, files deferred work |
| `revloop cycle --pr N` | Built. The whole loop in one process, up to `max_passes`. Also what a bare `revloop --pr N` runs |
| `revloop status --pr N` | Built. The state in one word, every pass with both legs, and the command that resumes it |
| `revloop init` | Built. Plan-then-confirm, `--dry-run`, `--yes`, `--upgrade` |
| `revloop watchdog` | Built. Finds stuck legs, retries once, then halts and says why |
| `revloop doctor` | Works |
| `revloop version` | Works |
| `revloop auth status` | Works |
| `revloop auth login` | Works — registers a GitHub App and installs it, end to end |
| `revloop auth install` | Works — installs an already-registered App |
| `revloop auth rotate` | Built. Guided, because GitHub has no API to generate an App key. It proves the new key works before replacing the old one |
| `revloop auth refresh` | Built. The refresher job's only command, and the only thing that writes a rotating harness credential |

**Exercised offline, and run against real pull requests locally.** Every command above is asserted against a stubbed `gh` boundary — 587 assertions, no network, no model, no PR — which catches the deterministic half, the half that fails silently. Three live local runs cover the other half. PR 3 converged on pass 3 against three planted defects. PR 4 converged on pass 2 while reviewing revloop's own rename, where the reviewer found two real defects in the change under review and pushed back on a third. PR 5 ran all three passes over revloop's own presentation change and found ten findings, nine of them real defects in the branch under review — including one in the token accounting the same branch had just added. **No repository has had the workflows installed yet**, so automated mode is still unproven end to end.

## Using it

### Locally, against a real pull request

Nothing to set up. No App, no secrets, no workflows — it uses the `gh` authentication you already have, so its comments appear as **you**.

```bash
revloop        --pr 42      # a cycle: both legs, alternating, up to max_passes
revloop cycle   --pr 42     # the same thing, spelled out
revloop review  --pr 42     # one review leg: inline comments plus a summary
revloop resolve --pr 42     # verify each finding, fix, reply, resolve, push
revloop status  --pr 42     # where the loop is, and how to resume it
```

**Start with `review` on its own.** It only writes comments, so it is the cheapest way to find out whether the findings are any good — which is the question that decides whether the rest is worth it.

**`resolve` and `cycle` commit and push to the pull request's branch.** That is the point of the tool and it is the thing to know before pointing it at something you care about. Three rails constrain it:

- **The branch guard** refuses to push unless the checkout is on the pull request's own head branch, that branch is not the repository default, and the head repository matches the origin. Asserted before anything leaves the machine.
- **`max_passes`** caps the loop at 3 by default.
- **The `revloop/stop` label** halts it, and outranks a healthy verdict. It is checked first, every pass.

To watch without any risk of a push, run `review` only.

### What it writes

One inline comment per finding on the line it affects, one summary comment carrying a hidden marker, and the `revloop/*` labels. The resolve leg adds threaded replies, resolves the threads it settled, commits any fixes, and files deferred defects to whichever sink the config resolves to.

Each finding's heading carries a coloured circle for severity and its category as a word — `🔴 **High · Security** — <title>` — and the summary table adds a pictogram per category. Each summary comment opens with exactly one native GitHub alert carrying the verdict, and ends with a run-details table naming the agent that ran, how long it took and what it cost in tokens. One row, for that comment's own leg.

The six loop labels carry six colours, so the label row on a pull request reads at a glance: blue for a review owed, purple for a resolution owed, green for converged, orange for halted, red for `revloop/stop`, grey for the pass number. Red is reserved for `stop` — the one label a human applies — so a red pill in a list always means somebody pulled the brake. `revloop init --upgrade` recolours labels minted before this, so there is no migration.

**Every pass is reconstructable from the pull request alone** — the markers are the state, so there is nothing to clean up locally and nothing to lose if a run dies mid-flight.

### Which models run

With no config file anywhere, the defaults are `codex` reviewing and `claude` resolving, in `single-run` mode, with nothing persisted. Override per run without touching the repository:

```bash
revloop review --pr 42 --harness claude
```

Repository policy lives in `.github/revloop.yml`, and **it is read from the base revision, never the branch under review** — so a config committed on the pull request branch has no effect until it merges. That is deliberate: a pull request cannot rewrite the loop that reviews it.

### Automated mode

```bash
revloop auth login          # register and install the GitHub App, two browser approvals
revloop init                # prints an itemised plan, asks once, then sets it up
```

`init` is the most consequential command here — it registers a GitHub identity, writes secrets and adds workflow files. It prints every path, secret and label it would touch, flags anything it would overwrite, and stops there under `--dry-run`.

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

The orchestrator reproduces `skills/pr-review/SKILL.md` and `skills/pr-resolve/SKILL.md` into each prompt rather than relying on the harness discovering them. That's a departure from the design's CI wiring, for two concrete reasons.

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

Keys are stored per owner **and role** at `~/.config/revloop/apps/<owner>.<role>.pem`, mode 0600 — `loop` for the review and resolve jobs, `refresher` for the credential refresher when a pairing needs one. Apps registered before roles existed stay readable at `<owner>.pem`. `revloop auth status` confirms where each App is actually installed by signing a JWT and asking GitHub, rather than assuming the setup worked.

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
schemas/         findings.schema.json, resolve.schema.json
skills/          pr-review/, pr-resolve/
templates/       workflows, starter config, example operator config
scripts/         lint.sh — syntax and shellcheck across everything
tests/           the stubbed-gh suite. `tests/run.sh` runs all of it
```

## Working on it

```bash
tools/revloop/tests/run.sh      # 587 offline assertions, no network, no model
tools/revloop/scripts/lint.sh   # syntax plus shellcheck -S warning
```

Both are offline and take seconds. The suite stubs `gh` and `claude` onto PATH and builds throwaway git repositories with real histories and real bare origins, so the assertions are about what revloop actually did rather than what it printed. `tests/stub/codex` is a deliberate tripwire: it exits loudly instead of running, because the no-config default names codex as reviewer and a fixture whose config failed to load would otherwise reach the real CLI and make a real billed call.
