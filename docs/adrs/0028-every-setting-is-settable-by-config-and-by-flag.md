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

In local mode, a flag may set any valid value. Automated setting overrides are default-deny. The exhaustive allowlist is `--harness`, `--model`, `--effort`, `--input-policy`, `--keep-transcripts` and `--no-tips`, on commands each affects. Every other setting override flag is refused with a message naming ADR 0003, even if its value equals the base value. New settings are refused unless a later decision explicitly extends this allowlist. Parity supplies a flag for local use; it does not grant permission to override the base value in automated mode.

The refusal includes every `policy.*` limit, endpoint selection and definitions including `base_url` and `token_env`, backlog settings, `coverage.*`, `git.hooks`, `logs.retention_days`, the `reviewers` list and `reviewers[].id`, mode and trusted-state identity. Automated convergence, posting and resolver edits use the base-revision `min_fix_severity`. Defaults for absent keys remain authoritative. The allowlist changes selected effective values after the base-revision read; it creates no exception to where configuration is read.

An allowed override must not indirectly replace a refused value, for example by changing the harness and clearing a configured endpoint or replacing the reviewer slot id. `--keep-transcripts` permits transcript retention for the run, not a change to `logs.retention_days`. Mode is determined from the base policy before checking overrides; a flag cannot select local mode to evade the automated restriction. Environment precedence does not authorise a new policy override. Existing supported environment inputs remain compatible, including resolve commit identity; proposed flags for `CROSSREV_GIT_NAME` and `CROSSREV_GIT_EMAIL` are refused in automated mode because they are outside the allowlist. Credentials remain subject to the existing endpoint restrictions.

This allowlist covers setting override flags. The exhaustive list of permitted invocation controls is `--pr`, `--repo`, `--trigger`, `--continuation` and `--pass`, on commands that accept each. `--pr` and `--repo` select the pull request and repository. Review's `--continuation` identifies a cycle continuation; review and resolve accept and ignore `--pass` because pass numbering comes from pull-request state. `--trigger` retains the existing exception described below, including its effect on resolve's trusted marker author. Every flag outside the six setting overrides and these five invocation controls is refused in automated mode with a message naming ADR 0003. This closes the rule over parser-accepted flags, including those absent from printed usage; it adds no flag to a command that does not accept it.

The generated `pull_request` workflows are not a trusted invocation. GitHub runs them at the merge ref, `refs/pull/N/merge`, so a same-repository pull request can edit their action inputs, which become flags. The review workflow's `issue_comment` path instead runs from the default branch. See [GitHub's event reference](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request), `templates/crossrev-review.yml`, `templates/crossrev-resolve.yml` and `action.yml`. Refusing automated policy flags preserves ADR 0003's configuration boundary even when those flags originate in a pull request: endpoint and backlog redirection, changed convergence thresholds and changed limits must wait until the policy change merges. This rule does not make a pull-request-controlled workflow itself trusted.

`--trigger human` is an existing exception to automatic admission and, on resolve, trusted marker selection. Today it bypasses the automatic review admission checks for `max_passes_per_cycle`, `max_files_changed_per_pr` and `max_prs_per_day`, including with `mode: automated`; a cycle still obeys its own pass bound, and review continuations still enforce the pass cap. Human-triggered review and resolve also skip the automatic fork and draft admission checks. It does not bypass `min_fix_severity`.

On resolve, `--trigger human` also switches the trusted marker author from `CROSSREV_APP_SLUG` plus `[bot]` to the viewer returned by `ViewerLogin`, even when `mode: automated`. Review instead selects its trusted author from `mode`, so it still trusts the App in automated mode. See `internal/resolve/context.go` and `internal/review/context.go`.

This ADR leaves both the admission bypass and the resolve marker-author switch unchanged; it does not authenticate a human trigger. A same-repository writer can therefore still change a workflow's trigger input to claim an attended run. The resolve template omits `trigger` and the action defaults it to `automatic`, but a pull request can add `trigger: human` to the action inputs. The proposed refusal protects setting values from overrides outside the allowlist; it does not close the existing admission bypass or marker-author switch. Changing trigger authorization requires a separate decision.

Workflow-supplied environment values are as pull-request-editable as action inputs. Both generated event workflows set `CROSSREV_APP_SLUG` in the step's `env`, and both legs read it to select the trusted App marker author. A same-repository pull request can replace that value. The environment table's refusal covers proposed flags only; existing environment inputs remain compatible, including `CROSSREV_APP_SLUG`, `CROSSREV_GIT_NAME`, `CROSSREV_GIT_EMAIL` and `CROSSREV_HARNESS_INSTALL`. Refusing their flags does not protect against workflow edits to their environment values. Binding the App identity to a trusted source requires the same separate decision as trigger authorization.

Parity does not change base-revision reads, trusted marker authors, endpoint credential restrictions or backlog path containment.

Settings that are not user-facing are out of scope. Credentials stay in the environment and secrets, never in flags where process listings expose their values, and never in the effective-setting log. An endpoint's `token_env` is the name of a variable, not the credential it contains; that name remains a user-facing setting.

## Audit of the current gaps

This audit compares every documented config field in [configuration.md](../configuration.md) with the binary's printed usage on 2026-09-30 at revision `d2b6bbb3d92a034a838cb8aae001933b4c218684`. The following commands exit with an unknown-option error before running a leg:

```sh
go run ./cmd/crossrev review --bogus
go run ./cmd/crossrev resolve --bogus
go run ./cmd/crossrev cycle --bogus
```

Their usage lines print:

```text
Usage: crossrev review --pr <number> [--harness <one of: claude|codex|agy|grok|opencode>] [--model <id>] [--effort <level>] [--input-policy hunks_first|whole_when_fits] [--no-tips] [--keep-transcripts]
Usage: crossrev resolve --pr <number> [--harness <one of: claude|codex|agy|grok|opencode>] [--model <id>] [--effort <level>] [--trigger human|automatic] [--keep-transcripts]
Usage: crossrev cycle --pr <number> [--trigger human|automatic] [--model <id>] [--effort <level>] [--input-policy hunks_first|whole_when_fits] [--no-tips] [--keep-transcripts]
```

`Present` means a corresponding flag appears in that command's usage. `Missing` means no corresponding flag is printed for a setting that affects that command. `N/A` means not applicable to that command or excluded from this rule. The automated column states whether a flag override for that field is allowed or refused under the proposal, not whether it exists today. N/A excludes schema discriminators and settings used only during setup. Allowed harness overrides remain subject to endpoint and slot-id preservation. Parent mappings group the leaf fields below; `reviewers[].*` names fields in the reviewer slot list, and `endpoints.<name>.*` covers every endpoint definition.

| Config field | `review` | `resolve` | `cycle` | Automated override | Qualification |
|---|---|---|---|---|---|
| `version` | N/A | N/A | N/A | N/A | File schema discriminator, not a run setting; applies to both config files. |
| `mode` | Missing | Missing | Missing | Refused | Determines trusted state authors; an override cannot bypass automated safeguards. |
| `runner` | N/A | N/A | N/A | N/A | Workflow rendering and pairing availability for setup, not a leg's runner selection. Parity also applies to affected setup commands. |
| `policy.min_fix_severity` | Missing | Missing | Missing | Refused | Review posting and convergence, resolver fix threshold. |
| `policy.max_passes_per_cycle` | Missing | Missing | Missing | Refused | Pass accounting and automatic continuation. |
| `policy.max_files_changed_per_pr` | Missing | N/A | Missing | Refused | Automatic review admission. |
| `policy.max_prs_per_day` | Missing | N/A | Missing | Refused | Automatic review admission. |
| `git.hooks` | N/A | Missing | Missing | Refused | Resolve commit and push hooks. |
| `logs.retention_days` | Missing | Missing | Missing | Refused | Run directory retention. |
| `logs.keep_transcripts` | Present: `--keep-transcripts` | Present: `--keep-transcripts` | Present: `--keep-transcripts` | Allowed | Can force true only; cannot override configured true to false. |
| `reviewer.harness` | Present: `--harness` | Missing | Missing | Allowed | Resolve also reads reviewer identity for coverage verification; its `--harness` selects the resolver. |
| `reviewer.model` | Present: `--model` | Missing | Present: `--model` | Allowed | Cycle applies one value to both legs, without independent slot overrides. |
| `reviewer.effort` | Present: `--effort` | Missing | Present: `--effort` | Allowed | Same shared cycle override. |
| `reviewer.endpoint` | Missing | Missing | Missing | Refused | Reviewer selection and coverage producer identity. |
| `resolver.harness` | N/A | Present: `--harness` | Missing | Allowed | Cycle has no advertised `--harness`. |
| `resolver.model` | N/A | Present: `--model` | Present: `--model` | Allowed | Same shared cycle override. |
| `resolver.effort` | N/A | Present: `--effort` | Present: `--effort` | Allowed | Same shared cycle override. |
| `resolver.endpoint` | N/A | Missing | Missing | Refused | Resolver endpoint selection. |
| `coverage.store` | Missing | N/A | Missing | Refused | Review publishes generations; resolve reads the store named by the review handle. |
| `coverage.ref_namespace` | Missing | Missing | Missing | Refused | Ledger writes and reads. |
| `coverage.on_overflow` | Missing | Missing | Missing | Refused | Review publication and construction of marker stores for reads. |
| `reviewers` | Missing | Missing | Missing | Refused | Slot list overrides the singular shorthand; only one slot is currently supported. |
| `reviewers[].id` | Missing | Missing | Missing | Refused | Stable ledger slot identity. |
| `reviewers[].harness` | Present: `--harness` | Missing | Missing | Allowed | Review flag selects the active reviewer; resolve uses the slot for coverage verification. |
| `reviewers[].model` | Present: `--model` | Missing | Present: `--model` | Allowed | Cycle cannot independently address a slot and the resolver. |
| `reviewers[].effort` | Present: `--effort` | Missing | Present: `--effort` | Allowed | Same shared cycle override. |
| `reviewers[].endpoint` | Missing | Missing | Missing | Refused | Slot endpoint and coverage producer identity. |
| `review.input_policy` | Present: `--input-policy` | N/A | Present: `--input-policy` | Allowed | Review input shaping; logs its effective value and source as `flag`, `config` or `default`. |
| `backlog.destination` | Missing | Missing | Missing | Refused | Review context and resolve deferred-work destination. |
| `backlog.github_issues.labels` | N/A | Missing | Missing | Refused | Labels on filed issues. |
| `backlog.github_issues.tracking_label` | N/A | Missing | Missing | Refused | Matching earlier issues. |
| `backlog.github_issues.create_missing_labels` | N/A | Missing | Missing | Refused | Issue label creation. |
| `backlog.github_issues.comment_on_existing_issue` | N/A | Missing | Missing | Refused | Comments on matching issues. |
| `backlog.repository.layout` | Missing | Missing | Missing | Refused | Resolved backlog context and repository writes. |
| `backlog.repository.path` | Missing | Missing | Missing | Refused | Resolved backlog context and contained write target. |
| `enable_automation_hint` | Present: `--no-tips` | Missing | Present: `--no-tips` | Allowed | Can force false only; cannot override configured false to true. |
| `endpoints.<name>.base_url` | Missing | Missing | Missing | Refused | Definitions merge by name; the operator file wins. |
| `endpoints.<name>.token_env` | Missing | Missing | Missing | Refused | Variable name only; its secret value is excluded. |

Cycle has no `--harness` in its usage today. The parser does accept it, just as resolve accepts an unadvertised `--no-tips`; these are discoverability gaps rather than absent parser support. The audit deliberately uses printed usage as its contract. Parser behavior is in `internal/cli/parse.go`; the usage strings are in `internal/cli/flags.go`. Resolve's reviewer dependencies are in `internal/resolve/convergence.go`. Partial boolean overrides and shared cycle values do not yet satisfy full parity.

Review and resolve currently clear the configured endpoint and model when `--harness` is supplied, including in automated mode. See `internal/review/invoke.go` and `internal/resolve/context.go`. The `harness` action input in `action.yml` exposes this behavior to pull-request-edited workflows. This is an existing gap against the proposed protection of endpoint selection, even though the harness flag itself is allowed. In automated mode the implementation must preserve the base endpoint, or refuse the override with an ADR 0003 message if that endpoint cannot serve the requested harness. This draft changes no runtime behavior.

Path attributes in `.gitattributes` are a separate base-revision policy format under [ADR 0023](0023-generated-files-are-recognised-without-configuration.md), not config-file keys in this audit. Internal `CROSSREV_*` state is excluded.

### Documented environment overrides

The environment table in configuration.md also exposes user choices without equivalent config keys. Its automated column applies to proposed flag overrides during review, resolve and cycle; it does not withdraw existing environment inputs. N/A means a credential or a choice affecting only setup or installation. Non-secret user-facing choices need config and flag parity on their affected commands, even when none of the three audited commands applies.

| Variable | Existing override | Automated override | Flag requirement under this proposal |
|---|---|---|---|
| `CROSSREV_CODEX_AUTH` | Restored Codex credentials. | N/A | None; credential. |
| `CROSSREV_APP_SLUG` | Selects the GitHub App. | Refused | Needed for commands that select trusted App state; missing from the audited usage. |
| `CROSSREV_OWNER` | Selects the owner for App files. | Refused | Needed where owner selection applies; setup/auth already expose `--owner`. |
| `CROSSREV_REFRESH_APP_ID` | Refresh App identity. | N/A | None; part of the credential setup, outside run settings. |
| `CROSSREV_REFRESH_APP_PRIVATE_KEY` | Refresh App key. | N/A | None; credential. |
| `CROSSREV_HARNESS_INSTALL` | Selects per-run harness installation. | Refused | Needed where installation is controlled; no corresponding flag in the audited usage. |
| `CROSSREV_ASSUME_YES` | Answers installation and upgrade prompts. | N/A | Existing `--yes` on affected setup commands; N/A to these legs. |
| `CROSSREV_NO_TIPS` | Suppresses closing suggestions. | Allowed | Existing advertised `--no-tips` on review/cycle; resolve's usage omits it. Needs a way to set either value. |
| `CROSSREV_GIT_NAME` | Resolve commit author name. | Refused | Needed on resolve/cycle; missing. |
| `CROSSREV_GIT_EMAIL` | Resolve commit author email. | Refused | Needed on resolve/cycle; missing. |
| `CROSSREV_BIN_DIR` | Binary install location. | N/A | Needed for the installers it affects; N/A to the three commands. |
| `CROSSREV_REPO` | Download source repository. | N/A | Needed for the download installer; N/A to the three commands. |
| `CROSSREV_REF` | Download revision. | N/A | Needed for the download installer; N/A to the three commands. |

The documented `XDG_CONFIG_HOME` and `XDG_DATA_HOME` are standard directory conventions, not CrossRev settings requiring duplicate flags. Inherited `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN` are refused, not supported overrides. Endpoint selection needs flags for the non-secret config fields above; endpoint tokens remain environment/secret inputs.

## Options considered

**Keep flags only for common overrides.** The strongest counter-argument is flag sprawl and a second place to look for a setting's value. Full parity expands help text, validation and compatibility obligations, and the config alone no longer describes a particular run. CrossRev still chooses parity because an operator should be able to change one run without changing repository policy or a machine's persistent config. Effective-value source records make the run auditable, and flags belong only on commands the setting affects.

**Allow unrestricted flag precedence in automated mode.** Rejected because an invocation can bypass a safely read policy by changing a limit, an endpoint or a backlog target. Automated setting flags outside the exhaustive allowlist must be refused; allowlisted flags retain one-run precedence.

**Allow automated guard overrides that only tighten limits.** Rejected because pull-request workflows can supply the flags, and tightening has no consistent meaning across settings. Raising `min_fix_severity` permits fewer resolver edits but also makes convergence and posting ignore findings the base policy considers actionable. Endpoint selection, `base_url`, backlog targets, coverage storage and git hooks have no numeric ordering. Default-deny keeps those decisions at the base revision without inventing an ordering for each new key. Treating unspecified settings as harmless would also require a new judgment for each key, the two-tier rule ADR 0003 rejects. The six named exceptions are explicit; a new key gets no implicit permission.

## Consequences

- New user-facing settings must supply both config and flag forms on affected commands, with effective-value source records.
- Existing settings have the gaps recorded above; approval of this ADR does not implement or claim to close them.
- Automated setting overrides outside the six-flag allowlist are refused with an ADR 0003 message. Every new setting starts refused; local overrides remain unrestricted for valid non-secret values.
- Parity flags for refused settings work only where the base `mode` is `local`. An attended run in an `automated` repository also waits for a policy change to merge; running at a terminal or selecting `--trigger human` does not change that mode restriction.
- The existing human-trigger admission bypass and resolve marker-author switch are unchanged gaps. `--trigger human` can still select the viewer instead of the App in automated resolve; review continues to select trust by mode. This proposal does not secure trigger authorization.
- Workflow-editable `CROSSREV_APP_SLUG` remains an existing trusted-marker identity gap requiring the same separate decision as trigger authorization. Refusal of proposed flags does not restrict existing workflow-supplied environment inputs.
- Existing `--harness` endpoint clearing requires a runtime change before automated overrides satisfy this rule: preserve the base endpoint, or refuse an incompatible harness override with an ADR 0003 message.
- Credentials and internal state acquire no flags under this rule.
