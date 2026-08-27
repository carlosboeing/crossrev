---
date: 2026-08-27
title: "Go native parity contract and binary delivery"
type: adr
status: approved
scope: [architecture, go-migration, parity, release]
authors:
  - "Carlos Boeing"
  - "gemini-2.5-pro (agy)"
---

# 0018 — Go native parity contract and binary delivery

## Context

CrossRev originated as a set of Bash scripts driven through `bin/crossrev` with dependencies on `git`, `gh`, `jq`, `yq`, and `openssl`. While functional across local and CI environments, distributing a multi-script shell architecture requires maintaining script interpreters, external JSON/YAML processor dependencies, and platform-specific shell quirks across macOS and Linux runners.

To improve startup performance, eliminate runtime dependencies like `jq` and `yq`, provide single-binary distribution, and create a typed foundation for future capabilities, CrossRev is being ported to a compiled native binary.

Porting an active tool risks introducing subtle behavioral regressions. The port must establish verifiable equivalence against the existing implementation before any functional or review intelligence changes are introduced.

## Decision

**CrossRev is reimplemented in Go 1.27.0 at strict behavioral parity across eleven functional surfaces before any review semantics or Review Intelligence features change.**

The eleven surfaces forming the parity contract are:

1. **CLI surface and entrypoint** — flag parsing, subcommands (`review`, `resolve`, `cycle`, `status`, `init`, `auth`, `doctor`), environment variables, and exit codes matching `bin/crossrev`.
2. **Configuration system** — `.github/crossrev.yml` schema validation, default fallbacks, and multi-layer precedence merging matching `lib/config.sh`.
3. **Diff generation and views** — three-dot merge-base diff computation, per-file diff segmentation, and stat generation matching `lib/diff.sh`.
4. **Prompt assembly** — review and resolve prompt template formatting with exact rubric reproduction matching `lib/prompt.sh`.
5. **Marker codec** — HTML comment marker encoding, decoding, validation, and schema version migrations matching `lib/state.sh`.
6. **State and identity hashing** — deterministic finding ID generation (`state_finding_id`) and location anchor hashing (`state_anchor`) with fixed byte-level normalization matching `lib/state.sh`.
7. **Label lifecycle** — the six-label state machine contract (`crossrev/*`), precedence, and transition rules matching [ADR 0008](0008-six-labels-are-the-contract.md) and `lib/legs.sh`.
8. **Harness adaptation and isolation** — model CLI invocation, permission flags, sandbox directory quarantine, and credential staging matching `lib/adapters/` and `lib/sandbox.sh`.
9. **GitHub integration** — pull request metadata fetching, review comment posting, inline thread replies, reactions, and issue creation matching `lib/github.sh`.
10. **Worktree and git operations** — isolated detached HEAD worktree management, commit generation without host git hooks, and validated push operations matching [ADR 0017](0017-the-resolver-commits-without-host-git-hooks.md) and `lib/run.sh`.
11. **Run logging and redaction** — structured run event logging, token redaction filters, and retention sweep matching `lib/log.sh`.

### Boundaries and external tools

- **`git`, `gh` and harness CLIs stay external subprocesses.** CrossRev invokes external `git` and `gh` executables rather than linking embedded libraries (such as `libgit2`). This preserves host git configuration, credential helpers, worktree semantics, and exact push URL validation rules. Harness CLIs continue to run in isolated subprocesses with GitHub credentials stripped from their environment ([ADR 0001](0001-cross-model-review-loop.md), [ADR 0005](0005-quarantine-repository-provided-harness-config.md)).
- **Review Intelligence is separated from the native port.** Parity is achieved against frozen oracle vectors (`tests/fixtures/parity/`) before higher-level review intelligence or heuristic changes are considered.

### Active divergences

Two active divergences from the Bash implementation are authorized:

1. **Path containment and symlink evaluation** — In `cfg_assert_path_inside_repo`, the Go implementation evaluates symlinks using physical path resolution (`filepath.EvalSymlinks`) alongside lexical containment checks, preventing symlink traversal out of the repository checkout even when complex directory structures exist.
2. **Honest daily count pagination** — When querying GitHub comment and event counts for rate tracking, the Go implementation traverses paginated API responses across all pages rather than stopping at the first API page.

### Toolchain, signing, and reproducibility

Go 1.27.0 is the required toolchain. Darwin builds and code signing reproducibility have been measured and verified:

- **Darwin arm64**: CGO builds (`CGO_ENABLED=1`, `GOOS=darwin`, `GOARCH=arm64`, `-trimpath`) produce Mach-O thin binaries automatically signed with linker ad-hoc signatures (`Signature=adhoc`, `flags=...adhoc`).
- **Darwin amd64**: CGO cross-compilation (`CC="clang -arch x86_64"`, `GOARCH=amd64`, `-trimpath`) produces unsigned binaries that require explicit ad-hoc signing via `codesign -s - -i crossrev -f <bin>` (`Signature=adhoc`, `flags=0x2(adhoc)`).
- **Reproducibility**: Repeated builds of both architectures produce byte-identical post-sign SHA-256 digests.
- **Verification**: `scripts/verify-native-toolchain.sh` enforces the exact `go1.27.0` pin, rejecting beta or release candidate toolchains, and verifies Darwin arm64 and amd64 compilation, architecture headers via `lipo`, ad-hoc signatures, and digest reproducibility.

### Versioning and release gate

The native rewrite foundation ships under the `0.x` version series. Settling binary distribution, the packaging matrix, and behavioral parity does not advance the version to `1.0.0`; `v1.0.0` remains an explicit future decision dependent on full end-to-end automated mode verification ([ADR 0012](0012-versions-are-cut-deliberately.md)).

## Options considered

- **Rewrite directly with new review semantics.** Rejected. Blending language migration with semantic alterations prevents isolating regressions. Achieving behavioral parity across eleven surfaces first provides an exact baseline.
- **Embed `libgit2` via CGO.** Rejected. Using native `libgit2` bindings adds CGO build complexity, diverges from host `git` configuration (such as `pushInsteadOf` rewrites and custom credential helpers), and prevents sharing exact worktree behavior with host git. External `git` invocation remains predictable and robust.
- **Support floating Go toolchains.** Rejected. Pinned Go 1.27.0 ensures reproducible builds, consistent Darwin ad-hoc code signing semantics, and deterministic binary outputs across development machines and CI runners.

## Consequences

- **Skill text is embedded into the binary (`go:embed`).** In accordance with [ADR 0007](0007-skill-text-reproduced-into-the-prompt.md), skill text (`skills/pr-review/SKILL.md` and `skills/pr-resolve/SKILL.md`) is reproduced byte-identically into prompts. In the native binary, this text is embedded at compile time using `go:embed`. Consequently, editing `SKILL.md` during development requires recompiling the binary (`go build`) for changes to take effect in the executable. Byte identity across harnesses and shipping rubric and engine at one revision are preserved.
- **Single-binary execution.** Eliminates runtime dependencies on `jq`, `yq`, and `openssl` for end users.
- **Binary distribution replaces source checkout dependency.** Pre-compiled native binaries can be delivered via GitHub Releases, homebrew, or direct download while maintaining the existing `bin/crossrev` wrapper script compatibility.
- **Parity is verifiable offline.** Golden vectors in `tests/fixtures/parity/` and tests in `tests/test-parity.sh` validate that the native Go implementation produces outputs identical to the Bash baseline.
