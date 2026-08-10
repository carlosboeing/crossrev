# Changelog

All notable changes to revloop. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `bin/revloop` entrypoint with `doctor`, `version` and `help`.
- `revloop auth install` — runs the install half on its own, for the closed tab or the new repository a year later.
- A one-shot local listener on the redirect, so the browser lands on a real page and the terminal carries on by itself. `nc -k` keeps the socket open across connections: serving one connection and re-binding was measured losing the redirect whenever anything else connected first, because the re-bind gap returns connection-refused.
- Installation verification — `auth login` opens the install page with the account prefilled, then signs an RS256 JWT with the stored key and polls until GitHub confirms the installation. "Registered" no longer reports success for an App that can reach no repository.
- `revloop auth login` — registers a GitHub App through the [App Manifest flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest), prefilling the three permissions and disabling the webhook so the form offers nothing to get wrong.
- `revloop auth status` — which Apps are configured, for which owners, and whether each private key is still mode 0600.
- `install.sh` — symlinks `bin/revloop` onto PATH and reports what's missing. Skills are left to the `skills` CLI.
- `lib/ui.sh` — the six output rules from the design, enforced by the helper signatures rather than remembered per call site. `ui_warn` and `ui_die` both require a second argument, so a warning always states its consequence and an error always states the next action.
- `lib/preflight.sh` — dependency checks that name what's missing and how to install it, normalised to one `<tool> <version>` format across seven CLIs that each report themselves differently.

### Notes

- `revloop auth rotate` is deliberately absent rather than stubbed. GitHub exposes no API for generating an App private key — it's a web-UI action — so the honest implementation needs a guided browser flow.
