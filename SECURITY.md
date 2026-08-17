# Security Policy

## Supported versions

CrossRev is pre-1.0. Only the latest released version receives security fixes.

| Version | Supported |
| ------- | --------- |
| 0.1.x   | Yes       |
| < 0.1   | No        |

## Reporting a vulnerability

Please do not report security vulnerabilities through public GitHub issues,
pull requests, or discussions.

Report privately through GitHub's private vulnerability reporting:

1. Go to the repository's **Security** tab.
2. Select **Report a vulnerability** under **Advisories**.
3. Provide a description, reproduction steps, affected version, and impact.

If you cannot use private reporting, email **carlosboeing@gmail.com** with the
same detail and `CrossRev security` in the subject.

You can expect an initial acknowledgement within 7 days. Once a report is
triaged, the fix and disclosure timeline will be coordinated with you before any
public advisory is published.

## Scope

CrossRev reads a pull request's title, body, diff and comment threads — all
attacker-controlled — and hands them to a model, while holding a GitHub App
token elsewhere in the same job. Findings of particular interest:

- **Any path that puts a GitHub credential into the agent process.** The
  adapters strip `GH_TOKEN`, `GITHUB_TOKEN` and `GH_ENTERPRISE_TOKEN` before
  starting the model-facing process, and every GitHub call is made by the
  orchestrator. That separation is the load-bearing control
  ([ADR 0001](docs/adrs/0001-cross-model-review-loop.md)).
- **A way for repository-provided harness configuration to survive the
  quarantine** — a hook, an instruction file, an MCP server definition, or a
  settings file that a pull request adds and a harness still loads
  ([ADR 0005](docs/adrs/0005-quarantine-repository-provided-harness-config.md)).
- **A way to get policy read from the pull request head rather than its base
  revision.** Reading policy from the head would let a pull request raise its own
  caps or repoint an endpoint at a server it controls
  ([ADR 0003](docs/adrs/0003-policy-read-from-the-base-revision.md)).
- **A push the branch guard should have refused** — a target other than the pull
  request's own head branch in the origin repository. Every URL `git push` would
  write to is resolved to an `owner/repo` slug. Each slug must match the pull
  request's head repository, checked before the resolver runs and again before
  the commit is pushed. A URL that resolves to no github.com slug is refused
  rather than checked against a different URL on the same remote.
- **A review leg that can write to the checkout.** The resolve leg's process is
  granted file edits in its workspace, because changing files is its job — never
  a blanket bypass, and never permission to run arbitrary commands outside it.
  The review leg is granted nothing, and the capability is derived from the leg
  rather than configured. A path that gives the review leg write access, or that
  widens either leg past editing files, is a finding. Neither process holds a
  GitHub credential either way.
- **A path where a backlog write escapes the checkout.**
- **Anything that widens what a GitHub App token can do**, or that reaches the
  refresher App's `Secrets: write` permission from a job that reads a pull
  request ([ADR 0006](docs/adrs/0006-three-app-permissions-and-nothing-else.md)).

A model that returns a bad finding, or that fails to spot a defect, is a quality
issue rather than a vulnerability — open an ordinary issue for that.

Findings in third-party dependencies should be reported to the upstream project;
report them here only when CrossRev's usage makes them exploitable in a way
upstream would not address.
