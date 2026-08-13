---
date: 2026-08-10
title: "The loop App holds Contents, Issues and Pull requests at write, and nothing else"
type: adr
status: approved
scope: [security, github-apps, permissions]
---

# 0006 — The loop App holds Contents, Issues and Pull requests at write, and nothing else

## Context

Automated mode needs a GitHub identity that is not a person. The obvious candidate is the `GITHUB_TOKEN` every workflow already gets, and it does not work for this: **GitHub deliberately does not trigger another workflow from writes made with it.** The loop chains one leg to the next by applying a label, so with the default token the chain stops after one leg while looking healthy the whole way.

So the loop needs a GitHub App. The question is what it may do.

## Decision

**Three repository permissions, all at write, and nothing else** — no Secrets, no Administration, no Workflows.

| Permission | Purpose |
|---|---|
| `contents: write` | Push fixes |
| `pull_requests: write` | Comment, reply, resolve threads |
| `issues: write` | Apply pull request labels, and file issues for deferred findings |

**`issues: write` is not trimmable.** GitHub models pull request labels under the Issues API, and the whole loop is label-driven. It also covers filing issues for deferred findings.

**One App per owner** — not one globally, and not one per repository. Per owner and role, once [0004](0004-subscription-credentials-in-ci.md) adds the refresher.

**The private key is the crown jewel, not the runtime token.** The token minted in a workflow is already least-privilege: scoped to the current repository, expiring after an hour, and revoked in a post step at job end. The private key belongs to the App, so whoever holds it can mint a token for any installation of that App.

**The App's private key is never an organisation secret with `--visibility all`.** That would hand it to every workflow in the organisation, including the review job that checks out a pull request branch and runs a model over a diff.

**Registration is automated, and the `app-token` action input has no default.** CrossRev builds a manifest prefilling the name, the three permissions and the webhook setting, opens the browser at the right page, catches the redirect on a local port, exchanges the code for an ID and key, then opens the install page and waits until the installation appears. And the composite action's token input deliberately has no default: published review actions commonly default theirs to `GITHUB_TOKEN`, and copying that here would reintroduce the exact bug this ADR opens with.

## Options considered

**The default `GITHUB_TOKEN`.** Ruled out by the no-chaining rule above. Read-only workflow steps may still use it.

**A personal access token.** It belongs to a person, so it dies with their access; it expires; and there is no API to create one, so setup becomes a manual browser exercise.

**Adding `Secrets: write` to this App** so the credential refresher could share it. Rejected in [0004](0004-subscription-credentials-in-ci.md): it would put secret-rewriting one injection away from attacker-controlled text.

**Adding `Workflows: write`** so `crossrev init` could commit its own workflow files. Rejected — `init` runs on the operator's machine with the operator's own `gh` credentials, so it does not need the App for that, and the permission would let a compromised leg rewrite the workflows that constrain it.

**Creating the App by hand.** What that costs, and the reason `auth login` exists: a required homepage URL nobody needs, a webhook that defaults to *on*, an install-scope choice that decides whether the App can reach an organisation at all, and three permissions buried in a long list of three-state dropdowns. Nothing on either automated page is yours to get wrong.

## Consequences

- **The trust boundary is everyone with write access to any repository in an installation**, since a workflow they can commit could exfiltrate the key. One App per owner keeps that boundary where GitHub already draws it, and an organisation secret keeps one copy of the key per organisation rather than one per repository.
- **`crossrev auth rotate` exists because keys do not rotate themselves.** A key that has sat in several organisations' secrets for a year is exactly the thing nobody gets around to replacing by hand. It is guided rather than automatic, because GitHub has no API to generate an App private key, and it proves the new key works before replacing the old one.
- **`crossrev auth status` verifies rather than assumes.** It signs a JWT and asks GitHub where each App is actually installed, instead of trusting that the setup worked.
- **Keys are stored per owner and role**, mode 0600, under the user's config directory. Apps registered before roles existed stay readable at the older filename.
- **The push guard is a separate control, not a permission.** The App is granted nothing that would let it bypass branch protection, and the orchestrator additionally asserts the push target before anything leaves the machine — branch protection fires only after a bad push is attempted and says nothing about which branch was targeted.
- **The local user never encounters the words "GitHub App".** Everything the App exists for — triggering the next workflow, proving a marker was written by a machine, minting scoped credentials — only matters once something runs unattended.
