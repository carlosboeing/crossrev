# Changelog

All notable changes to revloop. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `revloop review --pr N` — one review pass. Claims before working, posts one inline comment per finding on the line it affects, records everything in a hidden marker on its own summary comment, and hands the loop to the address leg by label.
- `revloop address --pr N` — verifies every finding whatever its severity, commits and pushes what it fixed, replies in-thread, resolves what it settled, and persists deferred defects to a sink before resolving their threads.
- `revloop run --pr N` — the whole loop in one process, up to `max_passes`. A thin driver over the same legs the workflows invoke, because state lives on the pull request rather than in process memory.
- `revloop status --pr N` — position *and* interruption, naming the command that resumes a leg that died mid-flight and saying whether a re-run would resume it or abandon it as stale.
- `revloop init` — the upgrade to automated mode, behind an itemised plan naming every path, secret and label, where deferred work will go and where that answer came from, and anything it would overwrite. `--dry-run`, `--yes` and `--upgrade`.
- `revloop watchdog` — finds pull requests stuck waiting on a leg, retries once by re-firing the label, then halts and comments why. Re-applying a label GitHub already holds fires no event, so the retry removes it first.
- `lib/github.sh` — the whole GitHub boundary in one file: inline comments, threaded replies, GraphQL thread resolution, labels, issues, both dedupe tiers, the commit and the push. One file because that is what makes it stubbable.
- `lib/validate.sh` — structural checks in jq on what a harness returned. Deliberately not general JSON Schema validation, which bash cannot do.
- `lib/prompt.sh` — what each leg is given. Reproduces the skill text into the prompt rather than relying on harness discovery, because the quarantine moves the discovery paths out of the checkout.
- **Runner-aware `init`.** Which harnesses are reachable in CI is a property of the runner, so `init` refuses a pairing its runner cannot serve — naming the credential's measured lifetime and both fixes — instead of installing workflows that fail at the first API call. The generated workflows differ by runner: a hosted one installs the harness per run and receives credentials as secrets, a self-hosted one does neither.
- **`revloop-token-refresh.yml` and `revloop auth refresh`** — the single writer. Using a refresh token consumes it, so one scheduled job on its own concurrency group refreshes Codex's credential and every leg only reads. The vendor's token endpoint and client id are read out of the stored credential's own claims and its issuer's OpenID discovery document, never hardcoded: the endpoint is not where the obvious guess puts it.
- **Legs restore read-only.** A restored credential lands in a throwaway home that is discarded when the leg finishes, on the fatal paths as well as the clean one, so a harness that refreshes and writes back writes somewhere nothing reads again. A leg holding under an hour of token life stops and names both ways out rather than refreshing in flight.
- **`revloop auth login --role refresher`** — a second App per owner carrying `Secrets: write` and nothing else, through the existing manifest flow. `init` derives whether a pairing needs one and never asks; a configuration that does not need it never hears of it and no key is created. Keys are now stored per owner *and* role at `<owner>.<role>.pem`, with the pre-role `<owner>.pem` still read as the loop's.
- **`init` captures `claude setup-token`** rather than asking for a paste, redacting anything token-shaped on its way to the terminal, and records the creation date — the only moment that information exists, since the token cannot be read back. `revloop auth status` warns as the year closes.
- **`revloop auth rotate`** — guided rather than absent. GitHub has no API for generating an App key, so this opens the right page, picks the downloaded `.pem` out of `~/Downloads`, proves it authenticates as that App, and only then replaces the old one.
- `lib/adapters/agy.sh` — Antigravity as a third harness. It constrains its own output natively, so it is schema-native alongside claude and codex; the amendment that asked for it expected a fenced-JSON fallback and was wrong.
- `lib/credentials.sh` — reading a token's expiry, issuer and client id from its own claims, restoring one read-only, and the refusal threshold.
- `templates/` — the workflows, the starter policy config, and a commented example operator config.
- `scripts/lint.sh` — syntax plus `shellcheck -S warning` across everything, in one command.
- A stubbed-`gh` test suite: 330 offline assertions across thirteen files, no network, no model, no pull request. `tests/stub/codex` is a tripwire rather than a stub, because a fixture whose config failed to load reached the real billed CLI once before it existed. `tests/stub/agy` is a tripwire of a different kind: it exits non-zero if a flag follows `--print`, which is the mis-parse the real CLI answers cheerfully in prose.
- `action.yml` — a composite action manifest for the day revloop is public. Its `app-token` input has no default on purpose: `GITHUB_TOKEN` writes do not trigger workflows, so defaulting it would stall the chain after pass 1 while looking healthy.
- `bin/revloop` entrypoint with `doctor`, `version` and `help`.

### Fixed

- The claude adapter never ran the CLI at all when no endpoint was configured — the default local case. An empty bash array expanded with a default yields one *empty* word, so `"${prefix[@]:-}" env … claude` ran the command named `""` and failed with "command not found" and an empty error string.
- Marker reads passed `--arg` to `gh api --jq`, which takes a bare expression: the jq program became literally `--arg`. It only ever worked by falling through to a second, correct call.
- `git ls-remote` failing was read as "nobody else pushed", so the check for a concurrent push to the branch silently did nothing on an unreachable remote. It now says the check did not run.
- Adapter stderr was discarded, so a fatal error inside an adapter — an endpoint that resolves nowhere, for instance — exited the process with nothing printed.
- The single-harness fallback could resolve a leg to `kimi`, which has no adapter behind it: Kimi is reached through the claude adapter as a named endpoint. The leg then died on `adapter_kimi: command not found` rather than on the missing harness it was actually short of. `revloop doctor` reported the same three names for the same reason.
- `revloop auth install` — runs the install half on its own, for the closed tab or the new repository a year later.
- A one-shot local listener on the redirect, so the browser lands on a real page and the terminal carries on by itself. `nc -k` keeps the socket open across connections: serving one connection and re-binding was measured losing the redirect whenever anything else connected first, because the re-bind gap returns connection-refused.
- Installation verification — `auth login` opens the install page with the account prefilled, then signs an RS256 JWT with the stored key and polls until GitHub confirms the installation. "Registered" no longer reports success for an App that can reach no repository.
- `revloop auth login` — registers a GitHub App through the [App Manifest flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest), prefilling the three permissions and disabling the webhook so the form offers nothing to get wrong.
- `revloop auth status` — which Apps are configured, for which owners, and whether each private key is still mode 0600.
- `install.sh` — symlinks `bin/revloop` onto PATH and reports what's missing. Skills are left to the `skills` CLI.
- `lib/ui.sh` — the six output rules from the design, enforced by the helper signatures rather than remembered per call site. `ui_warn` and `ui_die` both require a second argument, so a warning always states its consequence and an error always states the next action.
- `lib/preflight.sh` — dependency checks that name what's missing and how to install it, normalised to one `<tool> <version>` format across seven CLIs that each report themselves differently.

### Notes

- Two claims inherited from the subscription-credentials amendment failed verification and are corrected here rather than carried forward. Codex's access token lives **10 days**, not ~12 hours — the earlier figure was time-remaining on an already-old token read as a lifetime, where `iat` to `exp` on a stored credential is exactly 864000 seconds. And `agy` **does** have `--json-schema`, so the Antigravity adapter is schema-native rather than falling back to fenced JSON.
