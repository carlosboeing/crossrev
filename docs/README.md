# CrossRev documentation

Start with [installation](installation.md), then [usage](usage.md). Everything else is reference you reach for when you need it.

| Page | What's in it |
|---|---|
| [installation.md](installation.md) | Getting CrossRev onto your machine, updating it, removing it |
| [usage.md](usage.md) | Running the loop locally, what it writes to a pull request, which model runs each leg |
| [configuration.md](configuration.md) | `.github/crossrev.yml` field by field, machine-local endpoints, environment variables |
| [credentials.md](credentials.md) | Which secrets automated mode needs, what each one holds, and why Codex needs a second App |
| [troubleshooting.md](troubleshooting.md) | The failure modes, each with the name it reports itself under |
| [architecture.md](architecture.md) | How the loop is built: the two legs, the orchestrator, the adapters, the marker and label contract |
| [adrs/](adrs/) | Decision records — what was decided, what was considered, what it costs |
| [ROADMAP.md](ROADMAP.md) | What's next, and what's deliberately deferred |

**CrossRev is a work in progress.** Every command is built and covered by an offline test suite, and the local path has been run against real pull requests. Automated mode has never had its workflows installed in a repository, which is why the version is `0.x`. The docs say so wherever it matters rather than only here.
