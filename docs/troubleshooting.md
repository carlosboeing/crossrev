# Troubleshooting

Every failure mode here reports itself by name. This page is what the name means and what to do about it.

**Start here for anything about a specific pull request:**

```bash
crossrev status --pr 42
```

It reads the loop's state off the pull request, renders every pass with both legs, and always ends with something you can type.

## A missing dependency

```bash
crossrev doctor
```

`doctor` names the gap and the fix rather than just reporting a gap. `yq` is the usual one on macOS — `brew install yq`. It's preinstalled on both GitHub runner families.

`doctor` also verifies that `gh` is **authenticated**, not merely installed, because an unauthenticated `gh` fails at the first API call instead of at the check. And it reports which reviewer/resolver pairings your configured runner can serve — otherwise that stays invisible until a CI run fails to authenticate.

The composite action runs the same check as its first step, so a runner missing a dependency fails naming it rather than dying mid-leg with `command not found` three steps later.

## A leg stopped and left a claim

Every leg posts a **claim comment before doing any work**, carrying a marker that records the pass, the leg, the head SHA and a timestamp. That is what makes a retry safe: the marker and the thing it records are one HTTP call, so there is no window where a comment exists and its record doesn't.

Run the same command again. CrossRev finds the unfinished claim and resumes it, reusing the same comment rather than posting a second one. If the previous attempt already recorded its findings, it does not run the model again.

**A claim is abandoned rather than resumed in two cases**, and it says which:

- **The pull request has moved on** — the claim started against one head SHA and the branch is now at another. Resuming would reconcile against findings that no longer describe the code.
- **It is more than an hour old.** Coming back later would resume into a pull request that has changed underneath.

Either way it starts the pass again and reuses the comment.

### Locally, a lock says another run holds the pull request

Two terminals against the same pull request would interleave comments and replies, so a local run takes a lock and names the holder — pid, host, and when it started.

If that process is still running, wait for it or stop it. If it isn't, CrossRev takes the lock over by itself and says so; the dead run's marker records how far it got, and this run posts only the difference.

`crossrev status` uses the same lock to answer "is this actually still running?" rather than probing a bare pid — pids are recycled and every machine has its own, so a bare probe from a second machine can find a stranger's process and print "running now" over a leg that died.

## A halted loop

`crossrev/halted` means the loop stopped short and a human is needed. **Nothing about a halt is a judgement on the code.** Five things cause one:

| Cause | What to do |
|---|---|
| Reached `max_passes_per_cycle` | Read the findings. Run `crossrev review --pr N` for another attended pass, or raise the cap |
| Reached `max_prs_per_day` or `max_files_changed_per_pr` | An automatic trigger declined the pass. A person asking still gets one |
| The reviewer returned `blocked` | It could not complete the review at all. The comment says why |
| The resolve leg returned `blocked` | Same, on the other leg |
| A finding was `escalated` | It needs a human decision, so `crossrev/stop` went on and the thread stayed open |

Remove `crossrev/halted` once you've looked, then run the command `status` suggests.

**A declined pass leaves a marker too**, flagged `declined`, so `status` can render the refusal instead of inferring it from a label plus the prose of a comment. It doesn't count as a pass: raising the cap and re-running won't answer "already reviewed".

### `crossrev/stop`

Somebody applied it, and it outranks everything including a healthy verdict — checked first, every pass. It is an instruction, not a state. Remove it to continue; `status` names the leg that was owed when the brake went on.

## The loop went quiet in automated mode

Event-driven mode's failure mode is silence: a dropped label event and a converged pull request look identical from outside.

That's what the watchdog is for. It runs on a schedule, finds open pull requests carrying a `crossrev/awaiting-*` label past its timeout, and **retries once** by removing and re-applying the label. Re-applying a label GitHub already holds fires no event, which is why the removal is the mechanism.

A second failure is not a dropped event, so it applies `crossrev/halted`, comments saying how far the leg got, and stops. To restart it, remove `crossrev/halted` and `crossrev/watchdog-retried`, then apply the awaiting label again.

Run it by hand to see what it sees:

```bash
crossrev watchdog --repo owner/name
```

## A pull request CrossRev won't touch

| Refusal | Why |
|---|---|
| It comes from a **fork** | GitHub withholds secrets from fork pull requests, so the loop would run unauthenticated rather than not at all — and a fork's head branch isn't this repository's to push to. Review it by hand |
| It isn't **open** | CrossRev only runs on open pull requests |
| It's a **draft**, and the trigger was automatic | Mark it ready for review, or ask for a review explicitly. A person asking always gets one |
| It's **already reviewed at this head SHA** | Push a revision, or run the resolve leg |

## The resolve leg won't push

The branch guard asserts three things before anything leaves the machine, and names which one failed:

- Your checkout is on the pull request's own head branch. Check it out first.
- That branch is not the repository's default branch. Re-open the pull request from a feature branch.
- The pull request's head repository matches your origin.

Branch protection is a backstop rather than a control: it fires after a bad push is attempted and says nothing about which branch was targeted. This checks the target beforehand.

## Both legs ran the same model

CrossRev refuses to continue when it detects this, because it's the failure the whole cross-model design exists to prevent and it otherwise completes normally with no error anywhere.

Two layers catch it:

- **The environment.** `ANTHROPIC_BASE_URL` or `ANTHROPIC_AUTH_TOKEN` set in the environment CrossRev inherited redirect the harness process-wide. Unset them — CrossRev sets them itself per invocation.
- **The answering model.** Where a harness reports which model answered, CrossRev compares the two legs. Where it doesn't report one, the marker records that the check didn't run rather than implying it passed.

Check the `endpoint` block, and check that no endpoint variable is exported.

## An endpoint isn't resolving

An endpoint a leg names but nothing defines is a hard failure, never a fallback. Define it under `endpoints:` in the repository config, or in `~/.config/crossrev/config.yml` if it's machine-local.

Falling back to the vendor's own API would mean running Claude while the config says Ollama — the same silent substitution as above, arriving through a different door.

Each endpoint needs both a `base_url` and a `token_env`. The variable name is required rather than assumed because it genuinely differs by service.

## A credential problem in CI

| Symptom | Cause |
|---|---|
| A leg stops saying the credential has under an hour left | Deliberate. Refreshing mid-flight is what breaks the rotation chain — the refresher workflow is the only writer. Let the refresher run |
| A Codex leg fails to authenticate | The stored `auth.json` was probably refreshed elsewhere, invalidating this copy. Re-seed the secret from a fresh `codex login` |
| `crossrev init` refuses your pairing | The runner cannot serve it. It names the credential lifetime and both fixes |
| The loop stops after one leg | Something is using the default `GITHUB_TOKEN` for a write that advances the loop. GitHub deliberately doesn't trigger another workflow from those writes; the App token is what chains them |

See [credentials.md](credentials.md) for the full model.

## The harness wrote to a quarantined path

CrossRev moves repository-provided harness configuration — `.claude/`, `.agents/`, `CLAUDE.md`, `.mcp.json` and the rest — out of the checkout before any model invocation, and restores it before anything is committed.

If the harness wrote to one of those paths while it was quarantined, that write was made blind and is discarded on restore. CrossRev **warns rather than staying silent**, because a finding the resolver "fixed" by writing there is reported as fixed and lands in no commit. Check those findings by hand.

## The skills offer was skipped

Not a failure. `install.sh` skips it and prints the command when there's no `npx` installed, or no terminal to ask at — a script, a CI step, a container with no controlling terminal. Dying there would fail an install that already succeeded, over an optional extra.

**The loop is unaffected either way.** CrossRev reproduces both skills into every prompt from its own checkout; installing them is only for invoking them by hand.

```bash
npx skills@latest add carlosboeing/crossrev --global
```

Pass `--global` explicitly whenever it isn't a human answering — the CLI's own default scope is the project you happen to be standing in.

## A label wouldn't apply

In automated mode this is fatal rather than cosmetic: the chain is label-driven, so a label that can't be applied leaves the next workflow with no event to hear. Check the token's Issues permission and GitHub's availability, then retry.

Absence itself isn't the failure mode — GitHub's add-labels endpoint creates a missing label with default metadata, which is why `crossrev init` mints the six deliberately, with their colours and descriptions.

## Still stuck

Run `crossrev status --pr N` and read the markers on the pull request — they are the whole state, they're human-readable, and they say what each leg claimed and when. Then [open an issue](https://github.com/carlosboeing/crossrev/issues).
