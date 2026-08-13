# Installing CrossRev

## The one-command install

```bash
curl -fsSL https://raw.githubusercontent.com/carlosboeing/crossrev/main/bootstrap.sh | bash
```

No token, no `gh`, no credential of any kind. The repository is public, so raw.githubusercontent serves the script anonymously.

`bootstrap.sh` clones CrossRev somewhere durable, then hands off to `install.sh`, which puts `crossrev` on your PATH. It asks before creating anything, and it is safe to re-run.

### What bootstrap decides, and how to override it

| Flag | Default | What it does |
|---|---|---|
| `--dir <path>` | `~/.local/share/crossrev` | Where the clone lives. Respects `XDG_DATA_HOME` |
| `--ref <tag\|branch\|sha>` | the default branch | Pin a known revision. Worth doing if you want "what did I install?" to have an answer |
| `--repo owner/name` | `carlosboeing/crossrev` | A fork |
| `--yes` | ask | Accept every prompt. Required when there is no terminal to ask at |
| `--skills` / `--no-skills` | ask | Decide the skills offer up front. Forwarded to `install.sh` |

`CROSSREV_REF` and `CROSSREV_REPO` set the last two through the environment instead.

**Bootstrap looks for an existing checkout before cloning anything.** It checks three places, cheapest first: the directory you are standing in, the checkout that an already-installed `crossrev` symlink resolves back to, and the destination it would clone into. Any of them that holds both `install.sh` and `bin/crossrev` is used as-is. Cloning over the top of a checkout you already have would be the rudest thing the script could do.

Pass `--dir` and it looks nowhere else. An explicit destination is an instruction, and searching anyway would answer a different question than the one you asked.

## From npm, to try it without cloning

```bash
npx crossrev-ai --pr 42        # nothing installed, nothing left behind
npm install -g crossrev-ai     # or put it on your PATH the npm way
```

**The package is `crossrev-ai`; the command it installs is `crossrev`.** npm refuses the plain name as too similar to `cross-env`, a check with no appeal, so the suffix is a constraint rather than a choice ([ADR 0011](adrs/0011-npm-as-a-second-install-route.md)). `npm install crossrev` will not find anything. Every other route — Homebrew, releases, the clone — uses the plain name, and what you type after installing is `crossrev` in all of them.

The package is the same bash the clone runs — `bin/`, `lib/`, `schemas/`, `skills/`, `templates/` and the `VERSION` file, with no build step and no dependencies. It needs Node only to install; nothing about the tool runs on it. macOS and Linux only, declared in the manifest, so Windows fails at install rather than at first run.

**`crossrev init` does not work from an npm install, and this is the one real difference.** `init` generates workflows that pin the composite action to a 40-character SHA, and it reads that SHA from CrossRev's own git checkout ([ADR 0009](adrs/0009-delivery-via-sha-pinned-composite-action.md)). An npm package has no `.git`, so `init` stops with an error naming the cause rather than writing a workflow pinned to nothing.

So: **npm is the local path, a clone is either path.** If you're setting up automated mode, use the bootstrap above. Full reasoning in [ADR 0011](adrs/0011-npm-as-a-second-install-route.md).

Updating is `npm update -g crossrev-ai`, which is the one thing npm does better than the clone — see [the checkout is the installation](#the-checkout-is-the-installation) for why the clone has no update command yet.

## Installing from a checkout you already have

Skip the bootstrap:

```bash
./install.sh
```

| Flag | What it does |
|---|---|
| `--bin-dir <dir>` | Where to put the symlink. Defaults to `~/.local/bin`, or `CROSSREV_BIN_DIR` |
| `--yes` | Don't ask before replacing an existing link |
| `--skills` / `--no-skills` | Decide the skills offer without being asked |

`install.sh` owns exactly one thing permanently: the symlink on your PATH. It reports what it replaced, because a link silently repointed at a different checkout keeps working while `git pull` in the old one changes nothing, and there is no error to explain why.

If the bin directory isn't on your PATH, it tells you the line to add to your shell profile rather than editing it for you.

## The checkout is the installation

`install.sh` symlinks; it never copies. At runtime `crossrev` resolves the symlink and reads its libraries, skills and templates from the checkout. Three consequences worth knowing:

- **`git pull` in the checkout is the update.** There is no `crossrev update` command, and no reinstall step. A [roadmap item](ROADMAP.md) tracks giving this a proper command.
- **Deleting or moving the checkout is the uninstall.** Remove the clone and the symlink on your PATH, and nothing is left except `~/.config/crossrev/` if you set up automated mode.
- **Editing the checkout takes effect immediately.** Handy when you're working on CrossRev itself, occasionally surprising otherwise.

## The two skills

`install.sh` **offers** to install `pr-review` and `pr-resolve` for your harnesses, and hands over to the [`skills` CLI](https://github.com/obra/skills) if you accept. By hand it is:

```bash
npx skills@latest add carlosboeing/crossrev
```

No `--skill` filters: `skills/` holds exactly those two, so naming them selects everything and can only go stale.

**The loop does not need them.** CrossRev reads both skills out of its checkout and reproduces their text into every prompt, so installing them is for invoking them by hand in an ordinary session. That's why it stays an offer — and it's the only step that wants Node, when everything else runs on git, bash and coreutils.

With no `npx` installed, or no terminal to ask at, the offer is skipped and the command printed. Neither is a failure, and the loop is unaffected.

One detail about the `skills` CLI, because it fails by reporting nothing rather than erroring: **it goes non-interactive when an agent is driving it**, printing "Agent detected". In that mode nobody is asked anything and the scope defaults to project — which, run from inside the clone, means into the clone. Present in the repository you were only installing from, absent everywhere you work. Pass `--global` whenever it isn't a human answering.

## Check it

```bash
crossrev doctor
```

`doctor` checks the dependencies, verifies `gh` is authenticated rather than merely installed, and reports which harnesses it can see. It also says which reviewer/resolver pairings your configured runner can actually serve — the half of "is this set up correctly" that otherwise stays invisible until a CI run fails to authenticate.

### What it needs

| Tool | Why |
|---|---|
| `git`, `gh` | Reading and writing the pull request. `gh` must be authenticated |
| `jq` | The findings and resolve payloads are JSON |
| `yq` | Both config layers are YAML, and `jq` cannot read YAML |
| `openssl` | Decoding a restored subscription credential to check its remaining lifetime |
| One of `claude`, `codex`, `agy` | Something has to do the reviewing |

`yq` is the one usually missing on macOS — `brew install yq`. It is preinstalled on both GitHub runner families.

## Local endpoints

`~/.config/crossrev/config.yml` holds the endpoints that only exist on your machine — an Ollama box on your LAN, a router on localhost. There's a commented example in the checkout:

```bash
mkdir -p ~/.config/crossrev
cp templates/operator-config.yml ~/.config/crossrev/config.yml
```

See [configuration.md](configuration.md) for what goes in it and how it merges with a repository's own config.
