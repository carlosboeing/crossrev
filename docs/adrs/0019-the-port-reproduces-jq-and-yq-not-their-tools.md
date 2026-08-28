---
date: 2026-08-28
title: "The port reproduces what jq and yq print, not the tools themselves"
type: adr
status: approved
scope: [architecture, go-migration, parity]
authors:
  - "Carlos Boeing"
  - "Claude Opus 5 (Claude Code)"
related:
  - docs/adrs/0018-go-native-parity-contract.md
---

# 0019 — The port reproduces what jq and yq print, not the tools themselves

## Context

The Bash implementation reads and writes JSON through `jq` and YAML through `yq`. Both parse and re-print, so both rewrite what they are handed. A marker goes onto a public pull-request comment through `jq -c .`; a configuration value reaches a refusal message through `jq -r`; a YAML literal reaches the merge through `yq -o=json`.

[ADR 0018](0018-go-native-parity-contract.md) makes byte parity the acceptance contract. That obliges the Go port to reproduce the rewriting, not merely the values. Porting the tools is not an option: the binary ships without `jq` and without `yq`.

Reproducing the rewriting exactly turned out to have a limit. Some of what these tools print is a property of how the tool was built, not of the format they implement. Encoding that into the frozen oracle would make a test suite depend on which build of `jq` a machine happens to carry.

## Decision

**The port reproduces what `jq` and `yq` print, up to the point where the answer stops being a property of the format and becomes a property of the build.** Past that point the port keeps the literal it was given, and the divergence is recorded rather than closed.

Three cases sit past that line.

### 1. `jq`'s number formatting depends on its decNumber build

Since 1.7, `jq` links decNumber and preserves the decimal literal it read. `1.50`, `-0.0` and a twenty-digit integer all survive unchanged. Only an exponent is rewritten, into the *to-scientific-string* form of the General Decimal Arithmetic specification: `1e2` becomes `1E+2`, and `1e-2` becomes `0.01`.

`jq` 1.6 did neither. It read every number into an IEEE double and printed that, so `1.50` came out `1.5`.

`jq` 1.7 and later can still be built that way, with `--disable-decnum`. No published binary is — not the project's own release, not Homebrew's, not Debian's — but the option exists.

**The configuration reader reproduces the decNumber form**, because the value reaches a refusal message and `crossrev config show`, and a reader comparing the two implementations would see the difference. **The marker writer does not**, because a marker's number never reaches a reader that parses it, and freezing `jq`'s exponent form would put one `jq` family into the oracle.

`decNumber` also clamps an exponent past roughly −1.1e9. The port does not. That is outside anything a configuration file or a marker carries.

### 2. `yq`'s merge-key precedence is behind a flag it warns will change

`yq` resolves a YAML merge key, and `--yaml-fix-merge-anchor-to-spec` decides how. The default today is `false`, and `yq` prints a warning on every merge-key file saying the default will flip.

Measured with the flag on, three things change: a key written beside the merge wins wherever it sits rather than only after it; a sequence of merge sources applies first-to-last rather than last-to-first; and a mapping written inline inside a merge sequence is applied rather than dropped.

**The port follows the current default**, which is what every installed `yq` does today. It resolves the merge key in all cases, which holds under either setting; only the precedence half would move.

### 3. Invalid UTF-8 is replaced the way `jq` replaces it, not the way Unicode recommends

`jq` replaces one malformed sequence with one replacement character. Go's `encoding/json` replaces each malformed byte. On a truncated three-byte sequence that is one replacement character against two.

The Unicode maximal-subpart recommendation is a third answer again, and `jq` does not follow it: `jq` consumes a trailing ASCII byte after a truncated four-byte lead where the recommendation keeps it.

**The marker writer reproduces `jq`'s rule**, derived by measuring `jq` rather than by reading the recommendation. Model output reaches a public comment through that path, so the bytes have to match.

## Consequences

- A parity vector may record a **mismatch**. Two in `marker_encode.json` do, and they fail if the port ever starts matching, so closing the gap later is a decision somebody makes rather than something that happens quietly.
- The frozen oracle records which `awk` and which `tr` produced it, and now needs the same for `jq` and `yq` if either of these answers is ever re-measured.
- A future `jq` or `yq` release can move an answer under the port. The cutover gates compare against the shell as it ships, so a moved answer shows up as a gate failure rather than as a silent divergence.
- Where the port and the tools differ, the difference is in this document. It is not in a code comment, because after the cutover the shell is gone and nobody opens the file that holds the comment.
