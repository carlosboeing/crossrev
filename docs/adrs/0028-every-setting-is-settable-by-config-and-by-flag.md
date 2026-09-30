---
date: 2026-09-30
title: "Every setting is settable by config and by flag"
type: adr
status: draft
authors:
  - "Carlos Boeing"
  - "GPT-6 (Codex)"
scope: [configuration, cli, security]
related:
  - docs/adrs/0003-policy-read-from-the-base-revision.md
---

# 0028 — Every setting is settable by config and by flag

## Context

CrossRev exposes some settings through flags and others only through configuration. A one-run change to a config-only setting requires a persistent edit, and an operator cannot discover that distinction from a consistent rule. This ADR is draft, awaiting the maintainer's approval. It records the rule and audits the gaps; it implements no flags or runtime changes.

The [Command Line Interface Guidelines](https://clig.dev/#configuration) put flags ahead of configuration files in precedence and recommend flags for values that vary per invocation. CrossRev adopts that precedence and extends parity to every user-facing setting, including settings usually stable within a repository. The guide does not supply the base-revision security constraint required by [ADR 0003](0003-policy-read-from-the-base-revision.md).

## Decision

Every user-facing setting is settable in the config file and by a command-line flag on each command it affects. A flag overrides the config for that run only; it never rewrites the config. The precedence for permitted overrides is flag, environment, config, default. Only documented, supported environment mappings participate. For example, `CROSSREV_NO_TIPS=1` overrides `enable_automation_hint: true`; an explicit flag value takes precedence over that environment override. Config and flag values receive the same validation.

The run log records every effective setting's value and its source: `flag`, `environment`, `config` or `default`. An environment source also names the variable. The record describes what the command used, including each leg's settings in a cycle, so an operator need not reconstruct precedence from the invocation and files. Existing supported environment overrides remain compatible. The environment mapping and exact log format belong to implementation work.

In local mode, a flag may set any valid value. In automated mode, a flag may not override any policy-governed setting ADR 0003 reads from the base revision. Such a flag is refused with a message naming ADR 0003, even if its value equals the base value. This includes every `policy.*` limit, including `min_fix_severity`, `max_passes_per_cycle`, `max_files_changed_per_pr` and `max_prs_per_day`; endpoint selection (`reviewer.endpoint`, `reviewers[].endpoint`, `resolver.endpoint`) and endpoint definitions (`endpoints.<name>.base_url`, `endpoints.<name>.token_env`); backlog destination, layout, path and issue settings; `coverage.*`; `git.hooks`; and mode, trusted-state identity and any other policy-governed key. Defaults for absent policy keys remain authoritative. Automated convergence and posting use the base-revision `min_fix_severity`, as do resolver edits.

Flags outside that policy-governed set remain allowed in automated mode: harness, model, effort, `review.input_policy` and output toggles such as transcript retention and tips. An allowed override must not indirectly replace a protected value, for example by changing the harness and clearing a configured endpoint. Mode is determined from the base policy before checking overrides; a flag cannot select local mode to evade the automated restriction. Environment precedence does not authorise a new policy override. Credentials remain subject to the existing endpoint restrictions.

The generated `pull_request` workflows are not a trusted invocation. GitHub runs them at the merge ref, `refs/pull/N/merge`, so a same-repository pull request can edit their action inputs, which become flags. The review workflow's `issue_comment` path instead runs from the default branch. See [GitHub's event reference](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request), `templates/crossrev-review.yml`, `templates/crossrev-resolve.yml` and `action.yml`. Refusing automated policy flags preserves ADR 0003's configuration boundary even when those flags originate in a pull request: endpoint and backlog redirection, changed convergence thresholds and changed limits must wait until the policy change merges. This rule does not make a pull-request-controlled workflow itself trusted.

`--trigger human` is an existing exception to automatic admission, not a config-value override. Today it bypasses the automatic review admission checks for `max_passes_per_cycle`, `max_files_changed_per_pr` and `max_prs_per_day`, including with `mode: automated`; a cycle still obeys its own pass bound, and review continuations still enforce the pass cap. Human-triggered review and resolve also skip the automatic fork and draft admission checks. It does not bypass `min_fix_severity`. This ADR does not change that behavior or authenticate a human trigger. A same-repository writer can therefore still change a workflow's trigger input to claim an attended run. The proposed refusal protects policy values from flag overrides, but does not close that existing admission bypass. Changing trigger authorization requires a separate decision.

Parity does not change base-revision reads, trusted marker authors, endpoint credential restrictions or backlog path containment.

Settings that are not user-facing are out of scope. Credentials stay in the environment and secrets, never in flags where process listings expose their values, and never in the effective-setting log. An endpoint's `token_env` is the name of a variable, not the credential it contains; that name remains a user-facing setting.

## Audit of the current gaps

This audit compares every documented config field in [configuration.md](../configuration.md) with the binary's printed usage on 2026-09-30 at revision `6bb38a90326a563a78c55e16708c7efd0e847160`. The following commands exit with an unknown-option error before running a leg:

```sh
go run ./cmd/crossrev review --bogus
go run ./cmd/crossrev resolve --bogus
go run ./cmd/crossrev cycle --bogus
```

Their usage lines print:

```text
Usage: crossrev review --pr <number> [--harness <one of: claude|codex|agy|grok|opencode>] [--model <id>] [--effort <level>] [--no-tips] [--keep-transcripts]
Usage: crossrev resolve --pr <number> [--harness <one of: claude|codex|agy|grok|opencode>] [--model <id>] [--effort <level>] [--trigger human|automatic] [--keep-transcripts]
Usage: crossrev cycle --pr <number> [--trigger human|automatic] [--model <id>] [--effort <level>] [--no-tips] [--keep-transcripts]
```

`Present` means a corresponding flag appears in that command's usage. `Missing` means no corresponding flag is printed for a setting that affects that command. `N/A` means not applicable to that command or excluded from this rule. Parent mappings group the leaf fields below; `reviewers[].*` names fields in the reviewer slot list, and `endpoints.<name>.*` covers every endpoint definition.

| Config field | `review` | `resolve` | `cycle` | Qualification |
|---|---|---|---|---|
| `version` | N/A | N/A | N/A | File schema discriminator, not a run setting; applies to both config files. |
| `mode` | Missing | Missing | Missing | Determines trusted state authors; an override cannot bypass automated safeguards. |
| `runner` | N/A | N/A | N/A | Workflow rendering and pairing availability for setup, not a leg's runner selection. Parity also applies to affected setup commands. |
| `policy.min_fix_severity` | Missing | Missing | Missing | Review posting and convergence, resolver fix threshold. |
| `policy.max_passes_per_cycle` | Missing | Missing | Missing | Pass accounting and automatic continuation. |
| `policy.max_files_changed_per_pr` | Missing | N/A | Missing | Automatic review admission. |
| `policy.max_prs_per_day` | Missing | N/A | Missing | Automatic review admission. |
| `git.hooks` | N/A | Missing | Missing | Resolve commit and push hooks. |
| `logs.retention_days` | Missing | Missing | Missing | Run directory retention. |
| `logs.keep_transcripts` | Present: `--keep-transcripts` | Present: `--keep-transcripts` | Present: `--keep-transcripts` | Can force true only; cannot override configured true to false. |
| `reviewer.harness` | Present: `--harness` | Missing | Missing | Resolve also reads reviewer identity for coverage verification; its `--harness` selects the resolver. |
| `reviewer.model` | Present: `--model` | Missing | Present: `--model` | Cycle applies one value to both legs, without independent slot overrides. |
| `reviewer.effort` | Present: `--effort` | Missing | Present: `--effort` | Same shared cycle override. |
| `reviewer.endpoint` | Missing | Missing | Missing | Reviewer selection and coverage producer identity. |
| `resolver.harness` | N/A | Present: `--harness` | Missing | Cycle has no advertised `--harness`. |
| `resolver.model` | N/A | Present: `--model` | Present: `--model` | Same shared cycle override. |
| `resolver.effort` | N/A | Present: `--effort` | Present: `--effort` | Same shared cycle override. |
| `resolver.endpoint` | N/A | Missing | Missing | Resolver endpoint selection. |
| `coverage.store` | Missing | N/A | Missing | Review publishes generations; resolve reads the store named by the review handle. |
| `coverage.ref_namespace` | Missing | Missing | Missing | Ledger writes and reads. |
| `coverage.on_overflow` | Missing | Missing | Missing | Review publication and construction of marker stores for reads. |
| `reviewers` | Missing | Missing | Missing | Slot list overrides the singular shorthand; only one slot is currently supported. |
| `reviewers[].id` | Missing | Missing | Missing | Stable ledger slot identity. |
| `reviewers[].harness` | Present: `--harness` | Missing | Missing | Review flag selects the active reviewer; resolve uses the slot for coverage verification. |
| `reviewers[].model` | Present: `--model` | Missing | Present: `--model` | Cycle cannot independently address a slot and the resolver. |
| `reviewers[].effort` | Present: `--effort` | Missing | Present: `--effort` | Same shared cycle override. |
| `reviewers[].endpoint` | Missing | Missing | Missing | Slot endpoint and coverage producer identity. |
| `review.input_policy` | Missing | N/A | Missing | Review input shaping. |
| `backlog.destination` | Missing | Missing | Missing | Review context and resolve deferred-work destination. |
| `backlog.github_issues.labels` | N/A | Missing | Missing | Labels on filed issues. |
| `backlog.github_issues.tracking_label` | N/A | Missing | Missing | Matching earlier issues. |
| `backlog.github_issues.create_missing_labels` | N/A | Missing | Missing | Issue label creation. |
| `backlog.github_issues.comment_on_existing_issue` | N/A | Missing | Missing | Comments on matching issues. |
| `backlog.repository.layout` | Missing | Missing | Missing | Resolved backlog context and repository writes. |
| `backlog.repository.path` | Missing | Missing | Missing | Resolved backlog context and contained write target. |
| `enable_automation_hint` | Present: `--no-tips` | Missing | Present: `--no-tips` | Can force false only; cannot override configured false to true. |
| `endpoints.<name>.base_url` | Missing | Missing | Missing | Definitions merge by name; the operator file wins. |
| `endpoints.<name>.token_env` | Missing | Missing | Missing | Variable name only; its secret value is excluded. |

Cycle has no `--harness` in its usage today. The parser does accept it, just as resolve accepts an unadvertised `--no-tips`; these are discoverability gaps rather than absent parser support. The audit deliberately uses printed usage as its contract. Parser behavior is in `internal/cli/parse.go`; the usage strings are in `internal/cli/flags.go`. Resolve's reviewer dependencies are in `internal/resolve/convergence.go`. Partial boolean overrides and shared cycle values do not yet satisfy full parity.

Path attributes in `.gitattributes` are a separate base-revision policy format under [ADR 0023](0023-generated-files-are-recognised-without-configuration.md), not config-file keys in this audit. Internal `CROSSREV_*` state is excluded.

### Documented environment overrides

The environment table in configuration.md also exposes user choices without equivalent config keys. Non-secret user-facing choices need config and flag parity on their affected commands, even when none of the three audited commands applies.

| Variable | Existing override | Flag requirement under this proposal |
|---|---|---|
| `CROSSREV_CODEX_AUTH` | Restored Codex credentials. | None; credential. |
| `CROSSREV_APP_SLUG` | Selects the GitHub App. | Needed for commands that select trusted App state; missing from the audited usage. |
| `CROSSREV_OWNER` | Selects the owner for App files. | Needed where owner selection applies; setup/auth already expose `--owner`. |
| `CROSSREV_REFRESH_APP_ID` | Refresh App identity. | None; part of the credential setup, outside run settings. |
| `CROSSREV_REFRESH_APP_PRIVATE_KEY` | Refresh App key. | None; credential. |
| `CROSSREV_HARNESS_INSTALL` | Selects per-run harness installation. | Needed where installation is controlled; no corresponding flag in the audited usage. |
| `CROSSREV_ASSUME_YES` | Answers installation and upgrade prompts. | Existing `--yes` on affected setup commands; N/A to these legs. |
| `CROSSREV_NO_TIPS` | Suppresses closing suggestions. | Existing advertised `--no-tips` on review/cycle; resolve's usage omits it. Needs a way to set either value. |
| `CROSSREV_GIT_NAME` | Resolve commit author name. | Needed on resolve/cycle; missing. |
| `CROSSREV_GIT_EMAIL` | Resolve commit author email. | Needed on resolve/cycle; missing. |
| `CROSSREV_BIN_DIR` | Binary install location. | Needed for the installers it affects; N/A to the three commands. |
| `CROSSREV_REPO` | Download source repository. | Needed for the download installer; N/A to the three commands. |
| `CROSSREV_REF` | Download revision. | Needed for the download installer; N/A to the three commands. |

The documented `XDG_CONFIG_HOME` and `XDG_DATA_HOME` are standard directory conventions, not CrossRev settings requiring duplicate flags. Inherited `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN` are refused, not supported overrides. Endpoint selection needs flags for the non-secret config fields above; endpoint tokens remain environment/secret inputs.

## Options considered

**Keep flags only for common overrides.** The strongest counter-argument is flag sprawl and a second place to look for a setting's value. Full parity expands help text, validation and compatibility obligations, and the config alone no longer describes a particular run. CrossRev still chooses parity because an operator should be able to change one run without changing repository policy or a machine's persistent config. Effective-value source records make the run auditable, and flags belong only on commands the setting affects.

**Allow unrestricted flag precedence in automated mode.** Rejected because an invocation can bypass a safely read policy by changing a limit, an endpoint or a backlog target. Automated policy flags must be refused; allowed non-policy flags retain one-run precedence.

**Allow automated guard overrides that only tighten limits.** Rejected because pull-request workflows can supply the flags, and tightening has no consistent meaning across settings. Raising `min_fix_severity` permits fewer resolver edits but also makes convergence and posting ignore findings the base policy considers actionable. Endpoint selection, `base_url`, backlog targets, coverage storage and git hooks have no numeric ordering. Refusal keeps those decisions at the base revision without inventing an ordering for each new key.

## Consequences

- New user-facing settings must supply both config and flag forms on affected commands, with effective-value source records.
- Existing settings have the gaps recorded above; approval of this ADR does not implement or claim to close them.
- Automated policy overrides are refused with an ADR 0003 message; non-policy overrides remain allowed. The existing human-trigger admission bypass is unchanged.
- Credentials and internal state acquire no flags under this rule.
