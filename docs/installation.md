# Installing CrossRev

## The one-command install

```bash
curl -fsSL https://raw.githubusercontent.com/carlosboeing/crossrev/main/bootstrap.sh | bash
```

No token, no `gh`, no credential of any kind. The repository is public, so raw.githubusercontent serves the script anonymously.

`bootstrap.sh` downloads the release binary for your platform, checks its digest against the release's `checksums.txt`, and installs it onto your PATH. It asks before replacing anything, and it is safe to re-run.

### What bootstrap decides, and how to override it

| Flag | Default | What it does |
|---|---|---|
| `--dir <path>` | `~/.local/bin` | Where the `crossrev` binary lands |
| `--ref <tag>` | the latest release | Pin a known release. Worth doing if you want "what did I install?" to have an answer |
| `--repo owner/name` | `carlosboeing/crossrev` | A fork |
| `--yes` | ask | Accept every prompt. Required when there is no terminal to ask at |

`CROSSREV_BIN_DIR`, `CROSSREV_REF` and `CROSSREV_REPO` set the destination, tag and fork through the environment instead.

**Only two platforms have a binary.** macOS on Apple Silicon and Linux on 64-bit Intel/AMD. Anything else is refused by name before any download starts.

**An existing install with identical bytes is kept without asking.** Anything else asks before it is replaced. The install is atomic: the download lands in a private directory, the digest is checked there, and a rename puts it on PATH. An interrupted run leaves the old binary or nothing, never half a file.

**The digest proves the transfer, not the publisher.** `checksums.txt` comes from the same release as the binary, so a matching digest means the file arrived intact and says nothing about who put it there.

Pass `--dir` and it installs there and nowhere else. An explicit destination is an instruction, and installing anywhere else would answer a different question than the one you asked.

## From npm, to try the last Bash version

**npm is paused, not dropped.** Publishing stops at the last Bash version until the platform packages exist ([ADR 0020](adrs/0020-the-first-native-release-ships-a-reduced-scope.md)). What is on the registry still runs, and what follows describes it.

```bash
npx crossrev-ai --pr 42        # nothing installed, nothing left behind
npm install -g crossrev-ai     # or put it on your PATH the npm way
```

**The package is `crossrev-ai`; the command it installs is `crossrev`.** npm refuses the plain name as too similar to `cross-env`, a check with no appeal, so the suffix is a constraint rather than a choice ([ADR 0011](adrs/0011-npm-as-a-second-install-route.md)). `npm install crossrev` will not find anything. Every other route — Homebrew, releases, the clone — uses the plain name, and what you type after installing is `crossrev` in all of them.

The package is the same bash the clone runs — `bin/`, `lib/`, `schemas/`, `skills/`, `templates/` and the `VERSION` file, with no build step and no dependencies. It needs Node only to install; nothing about the tool runs on it. macOS and Linux only, declared in the manifest, so Windows fails at install rather than at first run.

**`crossrev init` does not work from an npm install, and this is the one real difference.** `init` generates workflows that pin the composite action to a 40-character SHA, and it reads that SHA from CrossRev's own git checkout ([ADR 0009](adrs/0009-delivery-via-sha-pinned-composite-action.md)). An npm package has no `.git`, so `init` stops with an error naming the cause rather than writing a workflow pinned to nothing.

So: **npm is the local path, a release binary is either path.** If you're setting up automated mode, use the bootstrap above. Full reasoning in [ADR 0011](adrs/0011-npm-as-a-second-install-route.md).

Updating a release install is the bootstrap again, which keeps an identical binary without asking. Updating npm is `npm update -g crossrev-ai`, which is the one thing npm does better than a binary.

## Installing from a checkout you already have

Skip the bootstrap:

```bash
./install.sh
```

| Flag | What it does |
|---|---|
| `--bin-dir <dir>` | Where to put the binary. Defaults to `~/.local/bin`, or `CROSSREV_BIN_DIR` |
| `--yes` | Don't ask before replacing an existing binary |
| `--skills` / `--no-skills` | Decide the skills offer without being asked |

`install.sh` builds the binary from the checkout with `scripts/build-native.sh` and copies it onto your PATH. It reports what it replaced, because a binary silently overwritten by a different build keeps working while running code you did not expect, and there is no error to explain why.

If the bin directory isn't on your PATH, it tells you the line to add to your shell profile rather than editing it for you.

## The binary is the installation

The binary carries everything it needs: skills, templates and schemas are embedded at build time, so there is no checkout to keep beside it. Three consequences worth knowing:

- **Rebuild and re-run `install.sh` to update.** There is no `crossrev update` command. A [roadmap item](ROADMAP.md) tracks giving this a proper command.
- **Deleting the binary is the uninstall.** Remove it from your PATH, and nothing is left except `~/.config/crossrev/` if you set up automated mode.
- **Editing the checkout changes nothing until you reinstall.** Handy when you're working on CrossRev itself: the installed tool keeps running the build it was copied from, no matter what the checkout does next.

## The two skills

`install.sh` **offers** to install `pr-review` and `pr-resolve` for your harnesses, and hands over to the [`skills` CLI](https://github.com/obra/skills) if you accept. By hand it is:

```bash
npx skills@latest add carlosboeing/crossrev
```

No `--skill` filters: `skills/` holds exactly those two, so naming them selects everything and can only go stale.

**The loop does not need them.** CrossRev embeds both skills at build time and reproduces their text into every prompt, so installing them is for invoking them by hand in an ordinary session. That's why it stays an offer — and it's the only step that wants Node, when everything else runs on git, bash and coreutils.

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
