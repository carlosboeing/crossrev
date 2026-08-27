---
date: 2026-08-27
title: "Go native parity contract and binary delivery"
type: adr
status: approved
scope: [architecture, go-migration, parity, release]
authors:
  - "Carlos Boeing"
  - "gemini-2.5-pro (agy)"
  - "GPT-5 (Codex)"
---

# 0018 — Go native parity contract and binary delivery

## Context

CrossRev originated as a set of Bash scripts driven through `bin/crossrev` with dependencies on `git`, `gh`, `jq`, `yq`, and `openssl`. While functional across local and CI environments, distributing a multi-script shell architecture requires maintaining script interpreters, external JSON/YAML processor dependencies, and platform-specific shell quirks across macOS and Linux runners.

To improve startup performance, eliminate internal reliance on tools such as `jq` and `yq`, provide single-binary distribution, and create a typed foundation for future capabilities, CrossRev is being ported to a compiled native binary.

Porting an active tool risks introducing subtle behavioral regressions. The port must establish verifiable equivalence against the existing implementation before any functional or Review Intelligence changes are introduced.

## Decision

**CrossRev is reimplemented in Go 1.27.0 at strict behavioral parity across eleven functional surfaces before any review semantics or Review Intelligence features change.**

The eleven surfaces forming the parity contract are:

1. **CLI** — commands, parse rule, flags, and exit codes.
2. **Environment** — all 42 supported environment variables.
3. **Paths and formats** — on-disk paths, file formats, and `owner/repo` to `owner-repo` slugging.
4. **Configuration** — three-layer precedence plus the refused severity, pass-count, git-hook, and log-retention/transcript values; configuration-version mismatch remains a separate hard refusal.
5. **Finding identity** — finding ID and anchor bytes.
6. **Markers** — prefixes, extraction, ordered encoding, fields, vocabulary migration, and `declined`.
7. **Loop policy** — pass numbering, resolution vocabulary, halt order, and review/resolve redrive predicates.
8. **Labels** — namespace, names, colours, and descriptions, including the separate watchdog bookkeeping label.
9. **Diff views** — numbering, snapping, C-style path decoding, and exclusion.
10. **Schemas and validation** — both JSON schemas and the shape-exit-1 versus semantic-exit-2 validator split.
11. **Prompt bytes** — byte-identical review and resolve prompts.

### Boundaries and external tools

- **`git`, `gh` and harness CLIs stay external subprocesses.** CrossRev invokes external `git` and `gh` executables rather than linking embedded libraries (such as `libgit2`). This preserves host git configuration, credential helpers, worktree semantics, and exact push URL validation rules. Harness CLIs continue to run in isolated subprocesses with GitHub credentials stripped from their environment ([ADR 0001](0001-cross-model-review-loop.md), [ADR 0005](0005-quarantine-repository-provided-harness-config.md)).
- **Review Intelligence is separated from the native port.** Parity is achieved against frozen oracle vectors (`tests/fixtures/parity/`) before higher-level Review Intelligence or heuristic changes are considered.

### Active divergences

Two active divergences from the Bash implementation are authorized:

1. **Path containment and symlink evaluation** — Bash resolves the configured path lexically, so a symlink through an existing ancestor can escape the checkout. Go resolves the deepest existing ancestor with `filepath.EvalSymlinks`, rejoins the nonexistent remainder lexically, and then compares containment against the resolved repository root.
2. **Honest daily count pagination** — Bash stops after ten pages of repository issue comments and reports that the distinct pull-request count may be rounded down. Go follows pagination until no next page remains and reports the exact distinct pull-request count.

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
- **Single-binary internal execution.** The Go implementation does not use `jq`, `yq`, or `openssl` internally. Parity keeps their existing `doctor` and preflight checks until a later behavior decision changes that observable surface.
- **Binary delivery uses the approved routes.** Immutable release assets support the composite action, bootstrap, and npm routes. Homebrew remains deferred until behavioral parity and the release pipeline are proven.
- **Parity remains verifiable offline.** Golden vectors in `tests/fixtures/parity/` and `tests/test-parity.sh` freeze and validate the Bash baseline. Later Go package tests and the black-box cutover gate compare the native implementation against those bytes.
