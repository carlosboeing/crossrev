---
date: 2026-08-13
title: "npm is a second install route for the local path only"
type: adr
status: approved
scope: [delivery, distribution, npm]
---

# 0011 — npm is a second install route for the local path only

## Context

Installing CrossRev today means cloning it. `bootstrap.sh` clones somewhere durable and hands off to `install.sh`, which symlinks `bin/crossrev` onto PATH. That is the whole mechanism, and [0009](0009-delivery-via-sha-pinned-composite-action.md) recorded why distribution stays clone-based at `v0`: `crossrev init` reads the delivery SHA with `git rev-parse HEAD` at its own root, so any install route without a `.git` directory leaves `init` unable to run.

Two things push against clone-only distribution.

**Trying the tool costs a clone.** Someone who wants to see what a review leg does has to pick a directory, run a bootstrap script piped from the internet, and accept a PATH change. `npx crossrev` is one line and leaves nothing behind.

**The name is unclaimed and unscoped names are first-come.** `crossrev` was free on the registry. Claiming it is cheap now and impossible later if somebody else takes it.

## Decision

**CrossRev is published to npm as `crossrev`, and the package serves the local path only.**

The package ships the five things the CLI reads at runtime and nothing else: `bin/`, `lib/`, `schemas/`, `skills/`, `templates/` and the `VERSION` file. A `files` allowlist in `package.json` names them explicitly. 34 files, about 142 kB.

**No build step, no dependencies, no scripts.** The manifest declares zero `dependencies` and zero `scripts`. `npm install` on this repository does nothing, which is what keeps the "no build step, no package manager, no lockfile" convention intact — the manifest is a distribution artifact, not a toolchain.

**`bin/crossrev` needs no wrapper.** It already walks its own symlink to find `ROOT`, because `install.sh` symlinks it onto PATH. npm's bin linking produces exactly that shape, so the resolver works unmodified. This was verified by installing a real tarball into an isolated prefix and running the CLI through the generated symlink.

**`crossrev init` does not work from an npm install, and this is stated rather than worked around.** The package has no `.git`, so `init` fails at the SHA pin with the error it already had. That is the correct behaviour: a workflow pinned to nothing is worse than a workflow that was never generated. Automated-mode setup needs a clone, and the docs say so at the point of install.

**The publish is authenticated by trusted publishing**, using OIDC from a GitHub Actions workflow on tag push. No npm token is stored in the repository or on a laptop.

**`os` is `["darwin", "linux"]`.** A bash tool depending on `git`, `gh`, `jq`, `yq` and `openssl` should fail at install time on Windows rather than install cleanly and break at first run. The field is per-version and costs nothing to lift later.

## Options considered

**Staying clone-only.** The status quo works and 0009 chose it deliberately. What changed is not the `init` constraint — that is intact — but the recognition that the local path and automated mode have different install needs. The local path never needed a `.git`; it was carried along by a decision made for `init`.

**A scoped `@carlosboeing/crossrev`.** Always available, immune to name races, and it costs the short `npx crossrev`. Rejected because the unscoped name was free, and a scope can still be added later for per-platform binary packages without giving up the flat name.

**Publishing a placeholder to reserve the name.** The registry discourages empty placeholder packages, and a stub that prints "go clone the repo" is worse than the working CLI it would stand in for. The package claims the name by being real.

**A Homebrew formula instead.** Not mutually exclusive and arguably a better fit for a bash tool, but it reaches fewer of the people who already run `npx` daily, and it does not claim a registry name that disappears if unclaimed.

**Shipping `docs/` in the tarball.** Rejected to keep the package to what the CLI actually reads. The README ships and links to the rest.

## Consequences

- **The npm package installs a tool whose automated half is unreachable.** `crossrev init` is the only affected command, and it fails loudly with a message naming the cause. Anyone setting up automated mode clones, exactly as before.
- **`package.json` duplicates the `VERSION` file**, which creates a way for `crossrev --version` to disagree with the published version. `scripts/lint.sh` now fails when the two drift, so the duplication cannot rot silently.
- **Two install routes now have to stay in step.** A release means a tag and a publish rather than just a tag. The publish workflow keys off the tag, so the extra step is CI's rather than a human's.
- **The published tarball is permanent.** npm's unpublish policy is conditional and cannot be undone, so what ships in a version ships forever. The `files` allowlist is the control, and the private workbench is structurally out of reach of it — the workbench is a separate nested clone that does not exist in a worktree, and it is not on the allowlist regardless.
- **Automated mode remains unproven**, unchanged by this. npm distributes the half that has run against real pull requests, which is the half worth handing to a stranger at `v0`.
