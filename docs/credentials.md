# Which credentials do I need?

Local CrossRev uses logins you already have. Automated CrossRev uses GitHub Actions secrets. Start with the table, then read the map below for what each value holds and where it lives.

## Start with how you run it

The credential model depends on where the `crossrev` command runs:

- **Local CLI, or a harness skill you invoke by hand** — no Actions secrets at all. CrossRev uses your `gh` login and the selected harness's own login.
- **GitHub-hosted Actions runner** — two secrets are always required, plus a credential for each subscription harness in the pairing.
- **Self-hosted Actions runner** — the same two GitHub secrets, and no harness credentials: the machine is already logged in.

A self-hosted runner is still automated mode. It removes copied harness logins, not the GitHub credentials the workflows need.

## Find your exact secret set

Each row is a complete set. Secrets within a row are not alternatives.

| Configuration | Required Actions secrets |
|---|---|
| Local CLI or skill-invoked run | None |
| GitHub-hosted, Claude on both legs | `APP_ID`, `APP_PRIVATE_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` |
| GitHub-hosted, one Claude leg and one Codex leg | `APP_ID`, `APP_PRIVATE_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `CROSSREV_CODEX_AUTH`, `CROSSREV_REFRESH_APP_ID`, `CROSSREV_REFRESH_APP_PRIVATE_KEY` |
| GitHub-hosted, Codex on both legs | `APP_ID`, `APP_PRIVATE_KEY`, `CROSSREV_CODEX_AUTH`, `CROSSREV_REFRESH_APP_ID`, `CROSSREV_REFRESH_APP_PRIVATE_KEY` |
| Self-hosted, both legs using logins already on the runner | `APP_ID`, `APP_PRIVATE_KEY` |

Two legs on the same harness share one harness credential. Any GitHub-hosted Codex leg adds the Codex secret and both refresher App secrets. A leg pointed at a named endpoint adds whatever variable that endpoint's `token_env` names, on any runner.

**There is no source-checkout secret.** CrossRev is delivered as a public composite action pinned by SHA, so the workflows reach it with no credential — earlier versions needed a read-only deploy key because GitHub shares private actions only within one account. If you are looking for `CROSSREV_SOURCE_KEY` or a deploy key on a Deploy keys page, neither exists any more.

Run `crossrev init --dry-run` for the exact list derived from your current configuration. It prints the plan and changes nothing.

## What each secret holds

| Secret | Value | Stored where | Needed when |
|---|---|---|---|
| `APP_ID` | Numeric ID of the loop App | Organisation secret when available, otherwise a repository secret | Every automated setup |
| `APP_PRIVATE_KEY` | Loop App RSA private key, PEM format | Same Actions scope as `APP_ID` | Every automated setup |
| `CLAUDE_CODE_OAUTH_TOKEN` | Claude subscription token from `claude setup-token` | Organisation or repository secret | A hosted leg uses Claude by subscription |
| `CROSSREV_CODEX_AUTH` | The complete Codex `auth.json`, including access and refresh tokens | Repository secret only | A hosted leg uses Codex by subscription |
| `CROSSREV_REFRESH_APP_ID` | Numeric ID of the refresher App | Repository secret only | A hosted leg uses Codex by subscription |
| `CROSSREV_REFRESH_APP_PRIVATE_KEY` | Refresher App RSA private key, PEM format | Repository secret only | A hosted leg uses Codex by subscription |
| Whatever an endpoint's `token_env` names | A static token that endpoint accepts | Your shell locally, an Actions secret in CI | A leg names that endpoint |

`crossrev auth login` also stores each App's PEM and metadata under `~/.config/crossrev/apps/`, at mode 0600, keyed by owner **and role** — `<owner>.loop.pem` and `<owner>.refresher.pem`. CrossRev uses those local files for setup, status checks and key rotation. CI receives copies through the Actions secrets above.

## The loop App

`APP_ID` and `APP_PRIVATE_KEY` identify the loop App. Each job exchanges them for a one-hour installation token, uses it as `GH_TOKEN`, and revokes it at the end.

The App holds three repository permissions, at write, and nothing else — no Secrets, no Administration, no Workflows:

| Permission | Purpose |
|---|---|
| `contents: write` | Push fixes |
| `pull_requests: write` | Comment, reply and resolve threads |
| `issues: write` | Apply pull request labels, and file issues for deferred findings |

`issues: write` looks surprising and isn't trimmable: **GitHub models pull request labels under the Issues API**, and the whole loop is label-driven.

**CrossRev does not use the default `GITHUB_TOKEN` for the writes that advance the loop.** GitHub deliberately does not trigger another workflow from those writes, so the loop would stop after one leg. Read-only workflow steps may still use the default token.

One App per owner — not one globally, and not one per repository:

```bash
crossrev auth login                  # detects the owner from the repository you're in
crossrev auth login --owner your-org
```

Two approvals in a browser, nothing to copy back. CrossRev builds a manifest prefilling the name, the three permissions and the webhook setting, opens your browser at the right page, catches GitHub's redirect on a local port, exchanges the code for an App ID and private key, then opens the install page with your account already selected and waits until the installation appears. If the local listener can't start, it falls back to asking you to paste the redirect URL — that path is the floor, not the plan.

`crossrev auth status` confirms where each App is actually installed by signing a JWT and asking GitHub, rather than assuming the setup worked.

## Harness credentials on hosted runners

GitHub-hosted runners are disposable, so each run restores the harness credential from a secret. Whether a harness can work that way is a property of its credential's lifetime, and these were read off installed credentials rather than documentation:

| Harness | Subscription credential | Access-token lifetime | On a hosted runner |
|---|---|---|---|
| `claude` | `claude setup-token`, purpose-built | 1 year | Works directly |
| `codex` | OAuth access token in `~/.codex/auth.json` | 10 days | Works, with the refresher below |
| `agy` | OAuth access token in `~/.gemini/oauth_creds.json` | about 1 hour | Use a self-hosted runner |
| `kimi` | OAuth access token | 15 minutes | Use a self-hosted runner, or a static endpoint token |

CrossRev has adapters for Claude, Codex and Antigravity. Kimi is reached through the Claude adapter as a named endpoint.

**`crossrev init` refuses a pairing its runner cannot serve**, naming the lifetime and both fixes, rather than installing workflows that fail at the first API call. `crossrev doctor` reports the same thing before you get that far.

Local and self-hosted runs use each harness's normal login, so Codex reads `~/.codex/auth.json` on those machines and nothing needs copying anywhere.

## Why Codex needs a second App

**Refresh tokens rotate: using one consumes it.** The vendor hands back a replacement and invalidates what you presented. One holder is fine; several copies are not, because the first to refresh kills every other copy, and a job holding a dead one that writes back overwrites the good one.

So the rule: **a leg restores, reads and discards.** It never refreshes and it never writes back. A leg holding under an hour of remaining token life stops rather than refreshing in flight.

Exactly one job writes the stored credential — the refresher workflow, on its own concurrency group. It exchanges the refresher App's ID and key for a short-lived token with `secrets: write` on repository secrets only. `organization_secrets` is a separate GitHub permission and is deliberately not requested.

That workflow is the one place CrossRev needs `Secrets: write`, which is why it gets its own App (`crossrev auth login --role refresher`) rather than widening the loop App's permissions. **The refresher never checks out the pull request branch, never runs a model and never reads a diff or a comment** — there is nothing in it to inject into, which is what makes `secrets: write` safe there and unsafe on a leg.

`init` derives whether you need one from the pairing and never asks. Most configurations, including the default, never see it.

Keep both Codex-specific credentials at repository scope:

- `CROSSREV_CODEX_AUTH` can't be shared across repositories, because GitHub concurrency groups are repository-scoped.
- `CROSSREV_REFRESH_APP_PRIVATE_KEY` must not be exposed to every workflow through an organisation secret.

Each repository therefore needs its own `codex login` seed.

## What the skills can access

Calling CrossRev through a harness skill doesn't change the credential model. The orchestrator owns every GitHub call.

The `pr-review` and `pr-resolve` skills receive the diff and the rest of their context from the orchestrator. **They receive no GitHub token and make no GitHub call.** The adapters also remove `GH_TOKEN`, `GITHUB_TOKEN` and `GH_ENTERPRISE_TOKEN` from the environment before starting the model-facing process.

That separation is the load-bearing one. The process reading attacker-controlled text — a pull request's title, body, diff and comments — is deliberately the process holding no credential, so an injection that reaches tool use still cannot post as the App, push a commit, or read a secret.

## Setting it up

In this order:

1. `crossrev auth login` — create and install the loop App.
2. `crossrev init --dry-run` — see the exact files, labels and secrets the current pairing needs.
3. `crossrev init` — write the workflows and set the values CrossRev already holds.
4. Supply anything still reported as missing.

What's left depends on the pairing:

- **Claude** — interactive `crossrev init` can run `claude setup-token` and store the result without printing it.
- **Codex** — run `codex login`, then seed `CROSSREV_CODEX_AUTH` from `~/.codex/auth.json`.
- **An endpoint** — set the variable its `token_env` names.

Re-run `crossrev init --dry-run` after changing a runner, a harness or an endpoint. The required set changes with the pairing.

## Rotating a key

```bash
crossrev auth rotate
```

Guided rather than automatic, because GitHub has no API for generating an App private key. It proves the new key works before replacing the old one.
