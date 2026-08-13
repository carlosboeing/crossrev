# CrossRev — project instructions for AI agents

Operator-facing brief for AI coding assistants working on this repo; `README.md` is visitor-facing. `AGENTS.md` symlinks to this file for assistants that expect that filename.

## What this project is

**CrossRev** — a cross-model pull request review loop. One model reviews a pull request and leaves inline comments; a second verifies each point and either fixes it, skips it, defers it or pushes back, replies in-thread, resolves what it handled, and pushes. Then the first looks again. It runs on the AI subscriptions you already have rather than per-token API keys, and the same command runs it from a terminal or from GitHub Actions.

Named 2026-08-13, renamed from the working title `revloop` ([ADR 0010](docs/adrs/0010-name-crossrev.md)). Key decisions live in `docs/adrs/0001`–`0010`.

**Pre-1.0, and honest about which half is proven.** Every command is covered by an offline suite and the local path has run against real pull requests. **Automated mode has never had its workflows installed in a repository.** That is what the `0.x` version records — do not describe automated mode as working.

## Project Map

- **Tracker**: GitHub Issues (this repo)
- **Board**: none
- **Roadmap**: `docs/ROADMAP.md`
- **Changelog**: `CHANGELOG.md` (repo root, not under `docs/`)
- **Architecture**: `docs/architecture.md` (current state); `README.md` `## Layout` is the short version
- **Working memory**: `.workbench/` — a **separate private repository**, nested here as an independent clone. See the gate below.
- **Other**:
  - No build step, no package manager, no lockfile. The checkout is the installation: `install.sh` symlinks `bin/crossrev` onto PATH and the tool reads its libraries, skills and templates from the checkout at runtime.
  - Dependencies are `git`, `gh`, `jq`, `yq`, `openssl`, plus `shellcheck` for the linter. Adding a language runtime needs an ADR first.
  - Delivery to consuming repositories is a composite action pinned by full 40-character SHA ([ADR 0009](docs/adrs/0009-delivery-via-sha-pinned-composite-action.md)). `crossrev init` generates the pinned form; the floating `@v0` exists only in the README's copy-paste example.
  - CI runs `scripts/lint.sh` and `tests/run.sh` on push and pull request. There is no release workflow — a release is a tag.

## The public/private gate

**This repository is public. `.workbench/` is a different, private repository.** Three layers keep them apart, and only the first needs no vigilance.

### 1. Structural

`.workbench/` is an independent clone nested at this root and named in `.gitignore`, so `git add -A` here can never sweep a workbench file into a public commit.

**Never cross-commit.** In this working tree, plain `git …` targets the public repo and `git -C .workbench …` targets the private one. There is no command that legitimately stages both.

### 2. The routing rule

| Goes to `.workbench/` (private) | Goes here (public) |
|---|---|
| Brainstorms, discovery, designs, phased plans, reviews, retros, scratch notes, spikes | Code, tests, templates, the skills |
| Cross-model review records and second opinions | Public user docs under `docs/` |
| Anything about brand, naming strategy, or commercial direction | ADRs, `docs/ROADMAP.md`, `CHANGELOG.md` |

The line is sharper than lifecycle-versus-product. **Anything about brand, company, naming strategy, or commercial direction — a hosted service, monetisation, pricing, the company-name question — is workbench-only even when it is a settled decision.** The public record covers the *what* of the product: features, bugs, user-relevant direction, including things that originate externally like a filed bug or a feature request. It never covers the *why* of the business.

The test that resolves most cases: *"a compiled binary is coming"* is a product statement and belongs here. *"so we can sell a hosted tier"* is a business statement and stays in the workbench.

The same rule governs anything distilled from a private artifact. An ADR records a product decision and its product reasoning; the brand and commercial analysis that sat beside it in the same source document does not travel with it.

### 3. The pre-commit privacy check

**Before committing anything here, ask the diff: does this contain anything workbench-shaped?** The scan list:

- any client or customer name
- `.workbench` paths, or references to workbench files
- internal cost or token figures
- commercial-strategy vocabulary — hosted service or hosted tier, monetisation (and monetization), pricing, per-seat or per-user figures, company naming

As a grep, which is worth running rather than eyeballing:

```bash
git diff --cached | grep -inE '\.workbench|hosted (service|tier)|monetiz|monetis|pricing|per (seat|month|user)|\$[0-9]'
```

Read the hits rather than counting them. This file is itself a hit, because stating a rule needs the words the rule forbids — which is exactly why the flip-time sweep excludes `CLAUDE.md`, `AGENTS.md` and `.gitignore` by name and then has a human read them.

Product-technical direction is fine. This check is about privacy the way the ship checklist is about tracking files: applied to every change, not only the ones that feel sensitive.

### Working Memory Location override

Any generic agent skill — `brainstorming`, `writing-plans`, or an equivalent — that instructs you to save specs, plans or brainstorms under `docs/` **must be overridden**. Read `.workbench/README.md` for its layout, and save them there. **Never write an SDLC brainstorm, spec or plan into this repo's `docs/`.**

## `docs/` — public documentation

```
docs/
├── README.md               — docs index
├── installation.md         — bootstrap, install.sh, doctor, the skills offer
├── usage.md                — the loop, what it writes, the labels, the dispositions
├── configuration.md        — .github/crossrev.yml, endpoints, environment variables
├── credentials.md          — which secrets automated mode needs, and why
├── troubleshooting.md      — the failure modes, by the name each reports itself under
├── architecture.md         — current-state architecture
├── ROADMAP.md              — what's in flight / next / shipped
└── adrs/                   — single-decision records (NNNN-title.md)
```

Everything else — brainstorms, discovery, phased plans, cross-model reviews, spikes — lives in the private workbench under the numbered-lifecycle convention.

## The writing rule: `CrossRev` by default, `crossrev` only where a machine reads it

An allowlist, not a split down the middle. Apply it to new prose, new printed output and new comment text rather than rediscovering it. Full reasoning in [ADR 0010](docs/adrs/0010-name-crossrev.md).

**Lowercase `crossrev` — the complete list:**

- the CLI command as invoked: `crossrev review --pr 42`
- the skill slash command
- package names
- the GitHub repository name
- code: identifiers, variables, function names, file and config paths

**`CrossRev` — everything else a person reads**, including the easy-to-forget ones: printed CLI output (status lines, progress, errors, banners), the prose inside help text, anything a harness skill emits, documentation, and the human-visible text of pull request comments.

The boundary that catches people: in help text, `crossrev review --pr 42` stays lowercase and the sentence above it does not. The practical test: **inside backticks it is code and stays lowercase; bare in a sentence it is the product name.**

**Two things stay lowercase because they are matched literally, and a case change breaks them silently on every existing pull request:** the `<!-- crossrev:` and `<!-- crossrev:f` marker prefixes, and the `crossrev/*` label namespace.

## Conventions

- **Single source of truth.** ROADMAP is "what's next"; ADRs are decisions; `docs/architecture.md` is current state.
- **Self-describing filenames.** Lifecycle artifacts in the workbench: `YYYY-MM-DD-<topic>-<suffix>.md`. ADRs: `NNNN-<short-title>.md`.
- **Always-current versus frozen-in-time.** Lifecycle docs freeze with `status:`; evergreen docs are updated in place.
- **Status flow:** `draft` → `approved` → `shipped` → optionally `superseded`.
- **Change discipline.** A shipping commit updates `CHANGELOG.md`, `docs/ROADMAP.md`, the affected docs and any relevant ADR together.

## Commits

Conventional Commits: `<type>(<scope>): <description>`, imperative, subject ≤72 chars, body explains *why*. Types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`, `perf`, `ci`. Reference an ADR in the body when relevant. Never reference a private workbench path in a public commit message.

## Working principles for agent sessions

- **Branch and workspace isolation.** Verify the active branch and workspace state at the start of a session. New feature work goes on a clean branch off `origin/main` — a worktree, or a manual checkout. If your runtime has a `using-git-worktrees` skill, invoke it.
- **Run both checks before claiming done.** `bash tests/run.sh` must report `all suites passed` and `bash scripts/lint.sh` must report `lint clean`. Both are offline and take seconds, so there is no excuse for asserting instead of verifying.
  - **The suite prints a per-file count, not a total.** `tail` shows the last file's assertions. To quote a total, sum it: `bash tests/run.sh | awk '/passed, [0-9]+ failed/ {p+=$1; f+=$3} END {print p, f}'`. The current baseline is **842 passed, 0 failed**.
  - **`lint.sh` skips shellcheck with a note if it is not installed, and still prints `lint clean`.** A green lint on a machine without shellcheck has linted nothing. Check that it ran.
- **Never let a GitHub credential reach the agent process.** Every GitHub read and write goes through the orchestrator. The adapters strip `GH_TOKEN`, `GITHUB_TOKEN` and `GH_ENTERPRISE_TOKEN` before starting the model-facing process. This is the security boundary rather than a convention ([ADR 0001](docs/adrs/0001-cross-model-review-loop.md), `SECURITY.md`).
- **Policy is read from the pull request's base revision, never its head** ([ADR 0003](docs/adrs/0003-policy-read-from-the-base-revision.md)). This covers any new config key without being thought about, which is the point of it being structural.
- **`tests/stub/codex` is a deliberate tripwire.** It exits loudly instead of running, because the no-config default names codex as reviewer and a fixture whose config failed to load would otherwise reach the real CLI and make a real billed call. Do not make it pass.
- **Decisions of record are in `docs/adrs/`.** Don't re-litigate them; propose a new ADR to change one.
- Verify before answering; no speculative features; don't suppress errors; no emojis in files.

## Where to look first

- Visitor-facing: [`README.md`](README.md)
- User docs: [`docs/`](docs/)
- What's next: [`docs/ROADMAP.md`](docs/ROADMAP.md)
- What shipped: [`CHANGELOG.md`](CHANGELOG.md)
- Decisions: [`docs/adrs/`](docs/adrs/)
- Private working memory: `.workbench/README.md`
