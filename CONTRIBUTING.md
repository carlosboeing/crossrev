# Contributing to CrossRev

CrossRev is a cross-model pull request review loop: one model reviews, another
resolves. Contributions are welcome. This guide covers setup, the constraints
that keep the tool portable, and the pull-request flow.

For project context and decisions, see [`README.md`](README.md),
[`docs/ROADMAP.md`](docs/ROADMAP.md), [`docs/architecture.md`](docs/architecture.md),
and the decision records in [`docs/adrs/`](docs/adrs/). `CLAUDE.md` is the
operator-facing brief for AI agent sessions.

## Dev setup

There is no build step and no package manager. The checkout is the installation.

```bash
git clone https://github.com/carlosboeing/crossrev.git
cd crossrev
./install.sh          # symlinks bin/crossrev onto your PATH
crossrev doctor       # names anything missing, and the fix
```

`install.sh` symlinks rather than copies, so editing the checkout takes effect
immediately.

Dependencies: `git`, `gh` (authenticated), `jq`, `yq`, `openssl`, plus
`shellcheck` for the linter. On macOS, `yq` and `shellcheck` are the two usually
missing — `brew install yq shellcheck`.

## Tests

```bash
bash tests/run.sh      # the offline suite: no network, no model, no PR
bash tests/run.sh -j 1 # the same suites, one at a time
bash scripts/lint.sh   # bash -n syntax plus shellcheck -S warning
```

Suites run in parallel, one job per core up to eight. Nothing is shared between
them, so the order means nothing and the output stays in glob order whatever the
job count. `-j 1` runs them one at a time and streams as it goes, which is easier
to read when you are watching one suite. `CROSSREV_TEST_JOBS` sets the default.

Both are offline and take seconds. **Run both before opening a pull request**;
CI runs the same two commands.

The suite stubs `gh` and `claude` onto PATH and builds throwaway git
repositories with real histories and real bare origins, so the assertions are
about what CrossRev actually did rather than what it printed. Test files are
auto-discovered — `tests/test-*.sh` is the whole registration mechanism.

`tests/stub/codex` is a deliberate tripwire: it exits loudly instead of running,
because the no-config default names codex as reviewer and a fixture whose config
failed to load would otherwise reach the real CLI and make a real billed call.
Do not "fix" it.

## Constraints that matter

- **bash, `gh`, `jq`, `yq`, `openssl`, and nothing else.** No runtime, no
  lockfile, no dependency tree. That is what makes the tool installable with a
  clone and runnable on a runner with no setup step. A change that adds a
  language runtime needs an ADR first.
- **Every GitHub call goes through the orchestrator.** The agent process holds no
  GitHub credential, ever. This is the security boundary, not a convention —
  see [ADR 0001](docs/adrs/0001-cross-model-review-loop.md) and
  [`SECURITY.md`](SECURITY.md).
- **Policy is read from the pull request's base revision, never its head.**
  [ADR 0003](docs/adrs/0003-policy-read-from-the-base-revision.md).
- **Markers and the label namespace stay lowercase.** They are matched literally
  by `sed` and `jq scan`, so a case change breaks matching silently on every
  existing pull request.
- **The writing rule:** `CrossRev` in anything a person reads — prose, printed
  CLI output, help text, comment text. Lowercase `crossrev` only for the command
  as typed, the slash command, package names, the repository name, and code.
  [ADR 0010](docs/adrs/0010-name-crossrev.md) has the complete list.
- **Decisions of record live in [`docs/adrs/`](docs/adrs/)**; don't re-litigate
  them in a pull request. Propose a new ADR if you want to change one.

## Pull-request flow

1. Branch from `main`.
2. Make a focused change; keep unrelated refactors out.
3. Use [Conventional Commits](https://www.conventionalcommits.org/):
   `type(scope): description`, imperative mood, subject <= 72 chars; the body
   explains *why*. Types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`,
   `chore`, `perf`, `ci`.
4. If the change affects user-facing behaviour, update `CHANGELOG.md`,
   `docs/ROADMAP.md`, and any relevant doc or ADR in the **same commit set**.
   Touching `README.md` needs a `CHANGELOG.md` entry too, even when no
   behaviour changed: npm packs it into the published tarball, so
   `scripts/check-changelog.sh` fails the pull request without one.
5. Open a pull request using the template. CI must be green before merge.

## Filing an issue

Use the templates in [`.github/ISSUE_TEMPLATE/`](.github/ISSUE_TEMPLATE/) — one
for a bug, one for an enhancement. `gh issue create` does not apply them, so
paste the sections in yourself when you file from the command line.

The title is one clause, under 60 characters, naming the change or the symptom:
imperative for an enhancement, the wrong behaviour for a bug. The rest of the
rule is in [`CLAUDE.md`](CLAUDE.md#issues).

## Reporting a security issue

Not through a pull request or a public issue. See [`SECURITY.md`](SECURITY.md).
