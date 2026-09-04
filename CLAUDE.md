# CrossRev — project instructions for AI agents

Operator-facing brief for AI coding assistants working on this repo; `README.md` is visitor-facing. `AGENTS.md` symlinks to this file for assistants that expect that filename.

## What this project is

**CrossRev** — a cross-model pull request review loop. One model reviews a pull request and leaves inline comments; a second verifies each point and either fixes it, skips it, defers it or pushes back, replies in-thread, resolves what it handled, and pushes. Then the first looks again. It runs on the AI subscriptions you already have rather than per-token API keys, and the same command runs it from a terminal or from GitHub Actions.

Named 2026-08-13, renamed from the working title `revloop` ([ADR 0010](docs/adrs/0010-name-crossrev.md)). Key decisions live in `docs/adrs/0001`–`0010`.

**Pre-1.0, and honest about how far the proof reaches.** Every command is covered by an offline suite and the local path has run against real pull requests. **Automated mode's workflows are installed in one repository, [`carlosboeing/crossrev-testbed`](https://github.com/carlosboeing/crossrev-testbed), and the loop has chained leg to leg there on GitHub's runners** — at v0.2.0 on 2026-08-17 and again at v0.5.0 on 2026-08-24.

That is a named set of runs on one repository with one pairing, not a general guarantee. Nothing has run on a self-hosted runner, under any pairing other than codex reviewing and claude resolving, or at any volume. The draft defect ([#122](https://github.com/carlosboeing/crossrev/issues/122)) is fixed and covered offline, and has not been watched run on a draft in CI. `0.x` records that gap — describe the runs that happened, not automated mode as working.

## Project Map

- **Tracker**: GitHub Issues (this repo)
- **Board**: none
- **Roadmap**: `docs/ROADMAP.md`
- **Changelog**: `CHANGELOG.md` (repo root, not under `docs/`)
- **Architecture**: `docs/architecture.md` (current state, including the file-by-file layout under `## The layout`)
- **Working memory**: `.workbench/` — a **separate private repository**, nested here as an independent clone. See the gate below.
- **Other**:
  - No package manager, no lockfile. The binary is the installation: `install.sh` builds it from the checkout with `scripts/build-native.sh` and copies it onto PATH, and `bootstrap.sh` downloads a release asset the same way. The skills, templates and schemas are embedded at build time.
  - Dependencies are `git`, `gh`, `jq`, `yq`, `openssl`, plus `shellcheck` and Go 1.21 or newer for the linter. `go.mod` pins the exact `go1.27.0` toolchain, which any Go from 1.21 downloads and switches to on first use, so the installed version does not have to match. Go arrived with the native parity port and is authorised by [ADR 0018](docs/adrs/0018-go-native-parity-contract.md). Adding any other language runtime needs an ADR first.
  - Delivery to consuming repositories is a composite action pinned by full 40-character SHA ([ADR 0009](docs/adrs/0009-delivery-via-sha-pinned-composite-action.md)). `crossrev init` generates the pinned form; the floating `@v0` exists only in the README's copy-paste example.
  - CI runs `scripts/lint.sh`, `go test ./...`, `scripts/test-native.sh` and `tests/run.sh` on push and pull request, plus `scripts/check-changelog.sh` on pull requests only. A release is a tag, and the tag triggers `.github/workflows/release.yml`, which verifies the version, publishes to npm and creates the GitHub Release.

## The public/private gate

**This repository is public. `.workbench/` is a different, private repository.** Three layers keep them apart, and only the first needs no vigilance.

### 1. Structural

`.workbench/` is an independent clone nested at this root and named in `.gitignore`, so `git add -A` here can never sweep a workbench file into a public commit.

**Never cross-commit.** In this working tree, plain `git …` targets **whichever repository the shell is currently inside** — the public one at the root, the private one from anywhere under `.workbench/`. From the root, `git -C .workbench …` names the private one explicitly. Nothing in git's output says which repo it resolved, so when you are not certain where the shell is, name the target with `-C` rather than assuming. There is no command that legitimately stages both.

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

**`scripts/githooks/pre-commit` now runs this automatically** and refuses the commit, so it is enforced rather than remembered. Enable it once per clone — git will not do it for you — with `git config core.hooksPath scripts/githooks`. Its money pattern is tighter than the grep above: a bare `\$[0-9]` also matches every shell positional parameter, which blocked 16 of 40 real commits when measured. And it exempts one sense of `pricing` rather than denying less of it — CrossRev prices nothing, it reads a vendored table of vendor token rates to tell an operator what a leg cost them, and that wording is load-bearing in `lib/usage.sh`, `scripts/refresh-prices.sh`, `docs/architecture.md` and their native port. The exempt phrases are removed from a line before it is scanned, so a sentence carrying one beside a real leak still trips on the leak. `tests/test-githooks.sh` holds both sets and is the only thing that proves either. The override for a commit that is genuinely fine is `git commit --no-verify`.

Product-technical direction is fine. This check is about privacy the way the ship checklist is about tracking files: applied to every change, not only the ones that feel sensitive.

### 4. What a public artifact may say

The three layers above guard *files*. They do not guard text *about* files. Commit messages, pull request and issue titles and bodies, review comments and release notes are public and permanent, and none of them passes through the staged diff the hook reads. `gh pr create --body` reaches the GitHub API without touching git at all.

**Never name a private or local source in one.** Not `.workbench`, not `crossrev-workbench`, not the bare phrase "the workbench", not a path to a document held there, not `/Users/...`, not a client name or an internal cost figure.

The damage is a citation the reader cannot follow. "See the plan in the workbench" says something exists and withholds it, which is worse than saying nothing. **Restate the fact instead:** put the reasoning in the body in its own words, or in an ADR under `docs/adrs/` that the artifact then links.

### Working Memory Location override

Any generic agent skill — `brainstorming`, `writing-plans`, or an equivalent — that instructs you to save specs, plans or brainstorms under `docs/` **must be overridden**. Read `.workbench/README.md` for its layout, and save them there. **Never write an SDLC brainstorm, spec or plan into this repo's `docs/`.**

## `docs/` — public documentation

```
docs/
├── README.md               — docs index
├── installation.md         — bootstrap, install.sh, doctor, the skills offer
├── usage.md                — the loop, what it writes, the labels, the resolutions
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

Conventional Commits: `<type>(<scope>): <description>`, imperative, subject ≤72 chars, body explains *why*. Types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`, `perf`, `ci`. Reference an ADR in the body when relevant. Never reference a private workbench path in any public artifact — commit message, pull request, issue, review comment or release note.

**The type you choose is the version decision** ([ADR 0012](docs/adrs/0012-versions-are-cut-deliberately.md)). `fix` is a patch, `feat` is a minor, `!` or a `BREAKING CHANGE:` footer is a breaking change. Nothing re-derives this later from a diff, so a `feat` written as a `chore` disappears from the next version silently.

## Pull requests

**The title is one clause under 72 characters in Conventional Commit form.** The body follows `.github/PULL_REQUEST_TEMPLATE.md`, which `gh pr create --body` bypasses, so filing from the command line means applying the template by hand.

A body reading "implements the design" has said nothing. Say what the design was, cite files in this repository by path, and give verification its output rather than asserting it ran.

## Issues

Most issues here are filed with `gh issue create --title ... --body ...`, which bypasses `.github/ISSUE_TEMPLATE/` entirely. The templates guide the body and say nothing about the title, so both halves are written out here.

**A title is one clause, under 60 characters, naming the change or the symptom.**

- **Enhancement titles are imperative.** "Extend doctor to check the repository setup", not "Improve doctor".
- **Bug titles state the wrong behaviour.** "Push-target validation does not see a pushInsteadOf rewrite".
- **No internal vocabulary.** "The legs" means nothing to a reader outside this codebase.
- **A title needing "and" is probably two issues.**

**A body follows `.github/ISSUE_TEMPLATE/`**, including when you file with `gh issue create`, which does not apply the templates for you.

- **Cite `file:line` for anything already in the code.**
- **Say what was measured, not what was assumed.** A rate needs its sample.
- **Link the ADRs and issues it touches.**

## Releases

Versions are **cut deliberately, not per merge**. Changes accumulate under `## [Unreleased]`; a release turns that heading into a version.

- **Every branch that changes what ships must add an entry under `## [Unreleased]`.** CI enforces it on pull requests via `scripts/check-changelog.sh`, which reads the shipped set from `npm pack` rather than a hardcoded list. Tests and CI changes are exempt because they do not ship. **Documentation is not exempt as a category**: `README.md` ships, and so does every Markdown file under `skills/`. npm packs `README`, `LICENSE` and `package.json` whatever the `files` allowlist in `package.json` says, so a branch touching `README.md` needs an entry even though nothing executable changed. Run `npm pack --dry-run --json | jq -r '.[0].files[].path'` rather than guessing which side of the line a file falls on.
- **To cut a release:** run `scripts/next-version.sh` for the bump the commit types imply, set `VERSION` and `package.json` to it together — `lint.sh` fails if they disagree — move the `[Unreleased]` entries under the new heading, commit, then tag `vX.Y.Z` and push the tag.
- **Never choose major on your own.** `v1.0.0` is gated on proving automated mode end to end. Raise it rather than deciding it.
- **A published version is permanent.** npm's unpublish is conditional and cannot be undone, and a `name@version` pair is never reusable. Treat a tag push as irreversible, because it is.
- **Every tag gets a GitHub Release.** The tag publishes to npm; the Release is where a person finds out what changed. A tag alone renders nothing, and nobody can subscribe to one — GitHub's watch-for-releases needs a Release object.

### Release notes

Written by hand on top, generated underneath. The top half is for somebody deciding whether to upgrade; the bottom half is for somebody who already has.

- **Title: `CrossRev vX.Y.Z`.** Product name capitalised per the writing rule, `v` kept so the title matches the tag, the compare link and the release page's own label. Never a marketing phrase.
- **The generated list goes in unedited**, at the bottom: `gh api repos/<owner>/<repo>/releases/generate-notes -f tag_name=vX.Y.Z -f previous_tag_name=vW.X.Y --jq .body`. It carries `## What's Changed`, one line per pull request with its author, and the `Full Changelog` compare link. It is provenance, so do not curate it.
- **The hand-written half is terse and technical.** The audience is software engineers, so schema keys, environment variables and flag names stay as they are. What does not belong: paragraphs of narration, the same point made twice, and a casual register that matches nothing else the project prints.
- **Sections, in order:** two sentences on what the release is; the standing note on automated mode, which says which runs prove it and which gaps remain rather than that it is unproven; `## Breaking` if there is any; `## Highlights`; `## Also fixed`; `## Install`.
- **`## Breaking` is a table of old to new**, then one line naming who has to act, then one line on what happens to work already in flight. A rename nobody has to act on still gets a row.
- **`## Highlights` uses `Was:` and `Now:`**, one line each under a bold claim. Six at most. A before-and-after states the change without narrating it, which is the whole reason the format is worth keeping.
- **`## Also fixed` is one line per fix, no elaboration.** Anything needing more than a line is a highlight or belongs only in `CHANGELOG.md`.
- **`CHANGELOG.md` stays as it is.** It is the technical record, linked once from the notes and never summarised into them.

## Working principles for agent sessions

- **The Bash implementation is frozen, and the Go port is where a defect gets fixed.** Nobody runs the shipped tool. `main` no longer takes shell fixes, so a defect found in `lib/*.sh` is fixed once, in the Go port, or left alone where the port never reproduced it. The earlier rule sent every finding to the shell on `main` first and then matched it in Go. That was right while the parity oracle was still being captured, because a vector freezes the shell's behaviour and a stale vector freezes behaviour that no longer ships. The vectors are frozen now. Applying the rule past that point pays twice for a change nobody will run.
- **Judge a finding before you file it.** A code defect in the port gets fixed in the branch that found it. A wrong comment, a stale line citation, a capitalisation slip or a doc typo gets fixed on the spot when that takes minutes, and dropped when it does not. Neither becomes a GitHub issue while the port is in flight. An issue is for work somebody has to schedule, and the backlog is not a place to record that a sentence read badly.
- **Branch and workspace isolation.** Verify the active branch and workspace state at the start of a session. Brainstorm, design and plan work happens in the main checkout — no branch, no worktree. At implementation, branch off `origin/main` and ask whether to use a worktree before the first branch command. More than one entry in `git worktree list` means another session is live, so a worktree is required rather than offered. Worktrees go at `.worktrees/<harness>/<branch>`, branch slashes preserved. `.workbench/` is a separate repository with its own worktrees; check it with `git -C .workbench worktree list`.
- **Run both checks before claiming done.** `bash tests/run.sh` must report `all suites passed` and `bash scripts/lint.sh` must report `lint clean`. The suite is offline. `lint.sh` is offline once the pinned `go1.27.0` toolchain has been fetched, and fetches it on first use when the installed Go is a different version. Neither is an excuse for asserting instead of verifying. `lint.sh` is quick. The suite runs its 30 files in parallel, one job per core up to eight, which measured about 90 seconds against about 380 seconds one at a time. `-j 1` restores the single-file order when you want to watch one suite.
  - **The suite prints a per-file count, not a total.** `tail` shows the last file's assertions, not the run's. To quote a total, sum it: `bash tests/run.sh | awk '/passed, [0-9]+ failed/ {p+=$1; f+=$3} END {print p, f}'`. **No baseline count is recorded anywhere, and that is deliberate.** It changes with every branch that adds a test, and it drifted three different ways across four files before being removed. The pass signal is `all suites passed`, not a number.
  - **`lint.sh` skips shellcheck with a note if it is not installed, and still prints `lint clean`.** A green lint on a machine without shellcheck has linted nothing. Check that it ran.
  - **An interactive run is not the check CI performs.** Commands that ask a question die without a controlling terminal, by design — `_ui_input_source` in `lib/ui.sh` tries `/dev/tty`, falls back to `[[ -t 0 ]]`, then calls `_ui_no_input`. A session at a terminal never meets that path and GitHub's runners always do. `tests/run.sh` now closes stdin for every suite it starts, in both modes, so the `[[ -t 0 ]]` arm no longer separates the two. `/dev/tty` still does, and it is tried first, so a session at a terminal can still answer a prompt that a runner cannot. So verify the way the runner does, with output redirected and stdin closed: `bash tests/run.sh > /tmp/suite.log 2>&1 < /dev/null; echo "exit=$?"`. Four honest reports on one branch said the suite passed while CI stayed red, because none of them measured this.
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
