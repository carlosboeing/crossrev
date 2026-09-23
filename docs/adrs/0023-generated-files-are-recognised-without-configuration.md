---
date: 2026-09-23
title: "Generated files are recognised without configuration"
type: adr
status: approved
scope: [review, intel, gitattributes, resolve, generated]
authors:
  - "Carlos Boeing"
  - "Kimi (Kimi Code)"
  - "Gemini 3.8 Flash (Antigravity)"
related:
  - docs/adrs/0003-policy-read-from-the-base-revision.md
  - docs/adrs/0022-the-coverage-ledger-lives-in-git-refs.md
---

# 0023 — Generated files are recognised without configuration

## Context

CrossRev measures each review prompt against a 180 KiB (184,320 bytes) budget. A file that fits into no batch stays outstanding with `input_exceeds_budget`. This stops the pass with `crossrev/halted`, leaving files after it unreviewed.

Large generated files cause this failure:

1. **Web assets and vendor bundles.** Single-file bundles (such as `webAssets.ts` or minified scripts) often exceed 350 KiB.
2. **Lockfiles.** Large package lockfiles frequently exceed 180 KiB.
3. **No automatic recognition.** CrossRev previously lacked built-in knowledge of generated files or `.gitattributes` annotations. Repositories had to either avoid committing generated files or manually alter their diffs.

Reviewing an oversized generated file is rarely useful: human reviewers check the source that generates the file, not the bundle. Halting the entire review cycle because of one generated bundle leaves the rest of the pull request unreviewed.

## Decision

### 1. Built-in detectors only convert a halt into a skip

CrossRev uses pure built-in detectors in fixed precedence:
1. `lockfile`: Exact match on known lockfile basenames.
2. `bundle-name`: Suffixes `.min.js`, `.min.css`, `.js.map`, and `.css.map`.
3. `header`: Header window scan (first 10 lines or 1,024 bytes) for Go code generation comments, `@generated`, or `generated` with `do not edit` or `do not modify`.
4. `minified`: Average line length strictly greater than 110 bytes across non-binary files.

A built-in match only acts when a file cannot fit the prompt budget alone. Fitting files continue to be packed and reviewed. Oversized generated files are skipped rather than halting the pass.

### 2. Lockfiles that fit are reviewed

Lockfiles decide installed dependencies and integrity hashes. They can change independently of manifests. Small lockfiles that fit within prompt budgets are reviewed. Only lockfiles exceeding the prompt budget are skipped.

### 3. Upstream rules are vendored and inspected without model classification

Rules are vendored as data in `internal/intel/generated.go`, seeded from Linguist's `generated.rb` (MIT). A maintainer script, `scripts/refresh-generated-rules.sh`, compares the committed tables against upstream Linguist and reports additions. It does not run in production paths.

Model-based classification is rejected:
- It produces nondeterministic results that break coverage reproducibility.
- It introduces latency (70–500 ms per file) across 400-file sets.
- It introduces prompt injection risks from author-controlled file contents.

### 4. `.gitattributes` at the base is repository policy

CrossRev queries `.gitattributes` at the base revision with `git check-attr --source=<base-sha> -z --stdin linguist-generated`:
- `set` or `true`: Excluded from review before packing.
- `unset` or `false` (`-linguist-generated`): Overrides built-in detectors. The file must be reviewed; if oversized, it halts the pass.
- Unspecified: Falls through to built-in detectors.

All `crossrev-*` attributes are reserved and ignored.

Git versions below 2.40 (which lack `--source` support) emit a single warning and fall through to built-in rules.

### 5. Minified detection applies to all extensions

The 110-byte average line length threshold applies to all non-binary file extensions. A file with NUL bytes is treated as binary and does not match the minified rule.

### 6. Empty required sets halt without invoking a model

If all changed files in a pull request are excluded by policy or skipped by the batch planner, the review leg settles with verdict `blocked` and applies label `crossrev/halted`. It writes an explanatory summary without calling a model harness.

### 7. The resolve leg filters generated context

The resolve leg diff excludes generated and excluded paths to keep the prompt focused. Any path named in an open finding's `path` field remains in the diff, ensuring the resolver sees files it must modify.

### 8. Every skip is visible in pull request comments and terminal output

Skipped files produce:
- A prominent warning block at the top of the pull request summary comment, above verdict alerts.
- A terminal warning (`ui.Warn`) during execution.
- Footnote counts reflecting all changed files (`Reviewed N of M changed files`).

Guidance in the warning tells operators how to mark or unmark files using `.gitattributes`.

### 9. Coverage engine advances to `file-v2`

Because enumeration semantics changed, `core.FileEngineVersion` moves from `file-v1` to `file-v2`. Stored generations under `file-v1` retire on the next pass, triggering a clean re-review.

## Options considered

- **Model classifier.** Rejected for latency, cost, and prompt injection vulnerabilities.
- **Link `go-enry`.** Rejected because it added ~10 MB of compiled binary size for language data CrossRev does not need, without returning rule names.
- **New configuration key (`review.exclude`).** Rejected in favour of standard `.gitattributes`, which GitHub and other tools already support.
- **Custom attribute (`crossrev-generated`).** Rejected to prevent proprietary configuration drift. `crossrev-*` attributes remain reserved.

## Consequences

- Pull requests with large generated bundles or lockfiles now complete and can converge.
- Oversized plain files without a generated marker continue to halt with `input_exceeds_budget`.
- Operators control review behavior directly through standard `.gitattributes` lines committed to base branches.
