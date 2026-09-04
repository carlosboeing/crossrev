---
date: 2026-09-04
title: "The first native release ships a reduced scope"
type: adr
status: approved
scope: [release, packaging, go-migration, distribution]
authors:
  - "Carlos Boeing"
  - "Claude Opus 5 (Claude Code)"
related:
  - docs/adrs/0018-go-native-parity-contract.md
  - docs/adrs/0009-delivery-via-sha-pinned-composite-action.md
  - docs/adrs/0011-npm-as-a-second-install-route.md
  - docs/adrs/0012-versions-are-cut-deliberately.md
---

# 0020 — The first native release ships a reduced scope

## Context

[ADR 0018](0018-go-native-parity-contract.md) fixes the parity contract: eleven surfaces, Go 1.27.0, two authorised divergences. It says nothing about what the first native release contains. That was decided separately, and the decision assumed a continuity that does not exist.

The remaining work is not porting. Every parity package is implemented and merged. What remains is distribution, supply-chain proof and cutover ceremony, priced at 49 engineer-days.

**That work protects installed users, and CrossRev has none.** The Bash implementation is published and parked. Nobody depends on its release architecture, its checksum signatures, its npm platform packages or its attested action pin. A property nobody relies on today can be added the first time somebody does.

Three specific costs do not earn their place in a `0.x` release with no installed base:

- **Ten linked tree-sitter grammars.** No parity code imports `internal/symbols`. The grammars exist for affected-code discovery, which is Review Intelligence work. A fallback for it already exists and needs no parser: git plus exact search, which enumerates changed files, finds adjacent tests by filename convention, and locates references by identifier. Units found that way are file-granular and marked uncertain rather than reported as clean. Each grammar passes five release gates, and the binary grows from about 2.1 MiB to about 9 MiB.
- **Supply-chain verification.** Reproducible double-builds, ECDSA-signed checksums and release-attestation verification each defend against an attacker substituting a published artifact. There is no artifact anyone fetches.
- **Four build targets and four npm platform packages.** Two targets cover every machine the project runs on today.

The counter-argument is recorded rather than dismissed. Changing release architecture between two consecutive releases would normally invalidate the proof gathered for the first. It does not here, because nobody installed the first.

## Decision

**The first native release ships two targets, plain checksums and no linked grammars. Everything else moves to a later release, decided when a user exists.**

### What the first native release contains

| Item | First native release |
|---|---|
| Build targets | `darwin/arm64` and `linux/amd64` |
| Grammars linked | None. `crossrev doctor` reports an empty grammar set |
| Artifact verification | `checksums.txt`, unsigned |
| Composite action | Downloads the asset for the runner platform, checks its digest |
| `crossrev init` pin | Unchanged. Read from `runtime/debug.ReadBuildInfo`, refused when absent or modified |
| Install routes | `bootstrap.sh`, `install.sh` and the composite action |
| npm | Paused. No new version published until platform packages exist |
| Cutover proof | The complete suite against the Go binary, and one automated-mode run on the testbed |

### What moves to a later release

Reproducible double-builds with digest comparison. ECDSA P-256 checksum signing with the key embedded in `bootstrap.sh`. Release-attestation verification in the composite action. The `linux/arm64` and `darwin/amd64` targets. The four npm platform packages. The ten grammars.

The measurements behind the deferred work stay valid and are not repeated. macOS ad-hoc signing behaviour, `gh release verify-asset` mechanics and the seven negative attestation cases were all measured on 2026-08-25.

### What does not move

Every security and boundary property in [ADR 0018](0018-go-native-parity-contract.md) holds unchanged. The model-facing process receives no GitHub credential. `os.Environ` appears in one file. Policy is read from the base revision. `git`, `gh` and the harness CLIs stay external.

Darwin ad-hoc signing stays, because `darwin/arm64` signs during linking and costs nothing.

### npm is paused, not dropped

`crossrev-ai` is published and its versions are permanent. A native binary cannot ship through the current single-package layout, so publishing stops at the last Bash version until the platform packages exist. The release workflow skips npm rather than publishing a package that cannot run.

### The version stays in `0.x`

Consistent with [ADR 0012](0012-versions-are-cut-deliberately.md). A reduced first native release is not a reason to advance the version, and `v1.0.0` remains a separate explicit decision.

## Options considered

- **Ship the full designed release.** Rejected. It costs about 49 engineer-days to defend an installed base of zero, and it delays Review Intelligence, which is the reason the port exists.
- **Ship parity in Bash and port later.** Rejected. Parity is already implemented and merged in Go. Reverting spends the work rather than the ceremony.
- **Keep the grammars and cut only supply-chain work.** Rejected. The grammars are the largest single item and the least reachable. Nothing outside Review Intelligence calls them.
- **Ship supply-chain verification and cut the grammars.** Rejected for now on the same measure. Signing and attestation both need a fetched artifact to protect, and every current fetch is the maintainer's own.

## Consequences

- The first native release is smaller in bytes and in scope than the design specified. `crossrev doctor` reports no grammars, which is the honest answer rather than a silent absence.
- Adding a deferred item later is additive rather than a rewrite. A signed checksum, an extra target and an attestation check each land beside what ships now.
- Somebody installing from `npm` stays on the last Bash version until the platform packages exist. This is stated in the release notes rather than left to be discovered.
- The five cutover gates reduce to two: the complete suite against the Go binary, and one automated-mode run on `carlosboeing/crossrev-testbed`. Mixed-version continuation is covered by package tests rather than by its own gate, because no deployed pull request holds Bash-written state.
- Review Intelligence becomes the next stage of work rather than the stage after distribution hardening. The grammars land there, or are cut from its first slice, when that stage is scoped.
