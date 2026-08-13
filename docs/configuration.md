# Configuring CrossRev

Two files, at two layers. Policy is repository-specific and belongs in the repository. Endpoint URLs are cross-project, and some of them — an Ollama box on your LAN, a router on localhost — are meaningless on a GitHub-hosted runner, so committing them would assert something false for half the places the config is read.

| File | Holds | Committed |
|---|---|---|
| `.github/crossrev.yml` | Repository policy: mode, runner, caps, the pairing, the backlog | Yes |
| `~/.config/crossrev/config.yml` | Endpoints that only exist on your machine | Never |

`.crossrev.yml` at the repository root works too, and is checked second. Both files are YAML and both carry `version: 1`. A `version` key present and not `1` is a refusal rather than a warning — the whole point of the key is that a future shape can be rejected by an old binary.

`crossrev config show` prints the merged result.

## The repository config

`crossrev init` writes this file, and it's yours to edit afterwards. Every field below has a default, so a repository can declare only what it wants to change.

**It is read from the base revision of every pull request, never from the branch under review.** Read from the head, a pull request could raise its own pass cap, repoint an endpoint at a server it controls and harvest every prompt, or ship an instruction file saying to return converged. So a pull request that legitimately changes review policy takes effect when it merges — the new policy is reviewed under the old one, which is the correct order.

### mode and runner

```yaml
version: 1
mode: automated             # automated | local — who may write trusted state
runner: github-hosted       # github-hosted | self-hosted
```

`mode` decides whose markers CrossRev trusts. In `automated` mode it reads markers from the GitHub App and nothing else, because a forged marker there makes an *agent* act — push a commit, skip a finding, believe a leg finished. In `local` mode the trusted author is you, the invoking user, and a forged marker can only mislead you about work you asked for.

`runner` is what `init` renders the workflows to run on. It also decides which pairings are servable: a self-hosted runner holds its own harness logins, so it serves every pairing with no secrets, no refresher and no rotation chain at all.

### policy

```yaml
policy:
  min_fix_severity: medium          # high | medium | low
  max_passes_per_cycle: 3
  max_files_changed_per_pr: 200
  max_prs_per_day: 25
```

| Field | Default | What it bounds |
|---|---|---|
| `min_fix_severity` | `medium` | The lowest severity the resolve leg may change code for |
| `max_passes_per_cycle` | `3` | Passes in one cycle |
| `max_files_changed_per_pr` | `200` | Pull request size CrossRev will review unattended |
| `max_prs_per_day` | `25` | Distinct pull requests reviewed across the repository in a rolling 24 hours |

**The last three are continuation bounds. They end automatic reviewing and never block a person** — a review a human asked for runs regardless. `min_fix_severity` is different in kind: it bounds what an agent may *change* rather than whether the loop continues, so it holds on attended and unattended runs alike.

A pull request consumes at most one daily unit however many passes it takes, because only the review marker participates in the count. The daily window rolls over 24 hours rather than resetting at midnight.

Two values are refused rather than accepted and misread:

- **`min_fix_severity` must be `high`, `medium` or `low`.** A typo ranks zero, zero meets nothing, so every finding would count as non-actionable, the pass would report converged, and the cycle would stop with a high-severity finding sitting on the pull request. A typo would look exactly like a clean review.
- **`max_passes_per_cycle` must be a whole number above zero.** Zero is already spoken for internally as "no pass bound applies to this invocation", which is what lets a person ask for one attended pass past the bound. To stop CrossRev reviewing a repository at all, remove its workflows rather than setting the bound to zero.

### The pairing

```yaml
reviewer:
  harness: claude
  model: claude-fable-5
  effort: medium

resolver:
  harness: claude
  model: claude-opus-5
  effort: high
```

`harness` is `claude`, `codex` or `agy`. `model` reaches the harness as given, so it must be **fully qualified** — `claude-fable-5`, never `fable-5`, which fails as an entitlement error rather than as a typo. `effort` is passed through verbatim. Either leg may name an `endpoint` instead of relying on the harness's own vendor.

With no config file anywhere the defaults are `codex` reviewing and `claude` resolving, in `local` mode, with no endpoints. Those defaults are deliberately not what `init` writes: a local user who has never heard of the CI pairing would otherwise be told to set an API key before their first review.

Cross-vendor is the strongest arrangement, because a bug one model family misses reviewing it misses resolving too. What works today:

| Pairing | Where | Cost |
|---|---|---|
| `codex` / `claude` | Hosted, cross-vendor | Codex's token rotates, so it adds a credential secret, a second App with `Secrets: write`, and a scheduled refresher |
| `claude` / `claude` | Hosted, one vendor two models | One secret, no refresher. Weaker, and doubles Claude usage |
| `agy` / `claude` | Self-hosted only | Antigravity's token lives about an hour |
| `kimi` / `claude` | Local only | Kimi is an endpoint on the Claude adapter, and its credential is a 15-minute OAuth token |

### backlog

```yaml
backlog:
  destination: github_issues      # github_issues | repository | none | auto
  github_issues:
    labels: [bug]
    tracking_label: crossrev-review
    create_missing_labels: true
    comment_on_existing_issue: false
  repository:
    layout: folder                # folder | file
    path: backlog/findings
```

| Field | What it does |
|---|---|
| `github_issues.labels` | Added to every issue CrossRev opens. Your taxonomy wins |
| `github_issues.tracking_label` | How CrossRev recognises its own earlier issues, to avoid duplicates |
| `github_issues.create_missing_labels` | `false` uses only labels that exist, and stops rather than inventing one |
| `github_issues.comment_on_existing_issue` | `true` comments on a matching issue instead of only linking to it from the pull request |
| `repository.layout` | `folder` writes one file per finding and survives concurrent merges; `file` appends to one list |
| `repository.path` | Repository-relative. An absolute path, or one that resolves outside the checkout, is refused |

**`auto` resolves in three tiers, first hit wins.** Inventing a folder in someone else's repository is the wrong default, so CrossRev does not.

1. **The repository's own declaration.** A `## Project Map` section in `AGENTS.md`, `CLAUDE.md` or `GEMINI.md` naming a `Tracker` — read from the base revision like every other policy. `none` means none; anything naming GitHub Issues resolves there; a path or a `.md` file resolves to a repository backlog; a URL or a hosted tracker name like Linear resolves to nothing writable.
2. **A convention already in use.** A backlog config file, or `BACKLOG.md`, or `TODO.md`.
3. **A default location**, but only when `repository` was explicitly asked for. Under `auto` this tier does not fire — it falls to `none` rather than creating a location nobody asked for.

`crossrev config backlog` prints where it landed.

### enable_automation_hint

```yaml
enable_automation_hint: true
```

Set to `false` to permanently silence the suggestion that a repository already running Actions could run this loop automatically. The hint appears only where it applies — a repository with `.github/workflows` but no CrossRev workflows — and only on a run that reached a terminal state. `--no-tips` does it per run.

## Endpoints and the operator file

`~/.config/crossrev/config.yml` never leaves your machine and is never committed. It holds what's true *here* and false everywhere else.

```yaml
version: 1

endpoints:
  kimi:
    base_url: https://api.kimi.com/coding/
    token_env: KIMI_API_KEY
  ollama:
    base_url: http://tower.local:11434
    token_env: OLLAMA_TOKEN
```

An endpoint block is a `base_url` and a `token_env`. **Tokens are never in either file**: `token_env` names an environment variable, and its value comes from your shell locally or from a repository secret in CI. The variable name is named rather than assumed because it genuinely differs by service — Ollama's documentation uses `ANTHROPIC_AUTH_TOKEN` where Kimi's uses `ANTHROPIC_API_KEY`.

**Endpoint definitions merge by name, and the operator file wins.** So a repository can declare a public endpoint while you point the same name at your own instance, with no change to the repository.

Claude Code talks to anything exposing an Anthropic-compatible `/v1/messages` endpoint, which is what makes the harness and the model separate choices.

**An endpoint a leg names but nothing defines is a hard failure, never a fallback.** Falling back to the vendor's own API would mean running Claude while the config says Ollama — the same silent substitution the whole cross-model design exists to catch, arriving through a different door.

`crossrev init` detects endpoint secret names, but the generated review and resolve workflows do not yet map arbitrary endpoint secrets into the leg environment. Automated endpoint use needs a manual workflow `env` mapping. Local endpoint use only needs the variable in your shell.

## Environment variables

**This is the complete list of what a person or a workflow sets. Anything else beginning with `CROSSREV_` is internal** — the code uses that prefix for its own state and for arguments it passes to child processes, and setting one of those from outside is unsupported rather than merely undocumented.

| Variable | Set by | What it does |
|---|---|---|
| `CROSSREV_CODEX_AUTH` | the workflow, from a secret | Codex credentials, restored read-only into a throwaway home for the leg |
| `CROSSREV_APP_SLUG` | you, or `crossrev auth login` | Which GitHub App the loop authenticates as |
| `CROSSREV_OWNER` | you | The owner whose App key and role files to read, when it cannot be inferred |
| `CROSSREV_REFRESH_APP_ID`, `CROSSREV_REFRESH_APP_PRIVATE_KEY` | the token-refresh workflow | The second App, which holds `Secrets: write` and nothing else |
| `CROSSREV_HARNESS_INSTALL` | the workflow | Whether the runner installs the harness per run |
| `CROSSREV_ASSUME_YES` | you | Answers the install and upgrade prompts, same as `--yes` |
| `CROSSREV_NO_TIPS` | you | Drops the closing suggestion from a command's output |
| `CROSSREV_GIT_NAME`, `CROSSREV_GIT_EMAIL` | you | Overrides the identity the resolve leg commits under |
| `CROSSREV_BIN_DIR` | you | Where `install.sh` puts the PATH symlink |
| `CROSSREV_REPO`, `CROSSREV_REF` | you | What `bootstrap.sh` clones, and at which revision |

`XDG_CONFIG_HOME` and `XDG_DATA_HOME` are respected for the config directory and the default clone location.

**Two endpoint variables must not be exported.** `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN` redirect the harness process-wide, so a leg would silently run on the wrong model and the loop would complete normally with no error anywhere. CrossRev refuses to run when either is set in the environment it inherited, and sets them itself per invocation.
