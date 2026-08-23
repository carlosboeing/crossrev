# Using CrossRev

## Three words, each meaning one thing

A **cycle** contains **passes**, and a pass has two **legs**. The review leg posts findings; the resolve leg answers each one. A cycle runs passes until the reviewer converges, a cap is reached, or somebody stops it.

## Locally, against a real pull request

Nothing to set up. No App, no secrets, no workflows — CrossRev uses the `gh` authentication you already have, so its comments appear as **you**.

```bash
crossrev         --pr 42    # a cycle: both legs, alternating, up to max_passes_per_cycle
crossrev cycle   --pr 42    # the same thing, spelled out
crossrev review  --pr 42    # one review leg: inline comments plus a summary
crossrev resolve --pr 42    # verify each finding, fix, reply, resolve, push
crossrev status  --pr 42    # where the loop is, and how to resume it
```

Options on `cycle`, `review` and `resolve`:

| Option | What it does |
|---|---|
| `--harness claude\|codex\|agy\|grok\|opencode` | Override the harness the config names for that leg, for this run. Not `kimi`, which is an endpoint rather than an adapter; not opencode on the resolve leg, which is review-only |
| `--repo owner/name` | Target a repository other than this checkout |
| `--no-tips` | Suppress the closing suggestion about automated mode |

**Start with `review` on its own.** It only writes comments, so it is the cheapest way to find out whether the findings are any good — which is the question that decides whether the rest is worth it.

### Before you point it at something you care about

**`resolve` and `cycle` commit and push to the pull request's branch.** That is the point of the tool. Three rails constrain it:

- **The push guard** runs in a dedicated worktree at the pull request's head revision — so your checkout branch does not matter — and refuses to push to the repository default branch. Your configured remote must push to the head repository — resolving a fork pull request locally needs a remote pointing at the fork. Asserted before anything leaves the machine, because branch protection fires only after a bad push is attempted.
- **`policy.max_passes_per_cycle`** caps the loop at 3 passes by default.
- **The `crossrev/stop` label** halts the loop, and outranks a healthy verdict. It is checked first, every pass.

To watch with no risk of a push at all, run `review` only.

**Fork pull requests can be reviewed locally, and resolved when maintainer edits are allowed.** `crossrev review` reads the diff and leaves comments without requiring write access. `crossrev resolve` pushes fixes directly to the fork branch if `maintainerCanModify` is enabled, and halts with an error if disabled. Automated mode refuses fork pull requests because GitHub withholds secrets from fork workflows.

## What a pass writes to the pull request

The review leg writes:

- **One inline comment per finding**, on the line it affects. Each heading carries a coloured circle for severity and the category as a word — `🔴 **High · Security** — <title>` — followed by what goes wrong and how to fix it.
- **One summary comment**, opening with exactly one native GitHub alert carrying the verdict, then a table of findings with a pictogram per category, and closing with a run-details table naming the agent that ran, how long it took and what it cost in tokens. One row, for that comment's own leg.
- **A hidden marker** inside the summary comment, and one inside each inline comment.

The resolve leg adds threaded replies, resolves the threads it settled, commits any fixes, files deferred defects to the configured backlog, and posts its own summary comment.

### The commits CrossRev makes

A pass that changes code makes one commit and pushes it to the pull request's own branch.

**The resolver writes the subject**, because only it knows what the change did. It is told to follow your repository's commit convention, which CrossRev works out from your twenty most recent commit subjects, read from the pull request's base revision so a branch cannot seed the style of the commit written onto it. A `.gitmessage` template is read from the same revision. A repository with fewer than five subjects to sample has no convention to read, and Conventional Commits is used instead.

CrossRev's own past commits are excluded from that sample, so it does not learn from the generic message this replaced.

**CrossRev composes the body**: one line per finding fixed, each with its location and a link to the review thread that settled it, then two trailers.

```
fix(api): check the response status before reading it

- Response used without checking ok
  src/api.ts:42 - https://github.com/acme/widget/pull/7/files#r2298471023

Crossrev-pr: acme/widget#7
Crossrev-pass: 1
```

The commits are also identifiable by author — they are made as `crossrev`, so `git log --author=crossrev` finds every one.

A subject that is not a single line, runs past 100 characters, or carries control characters is refused. The commit still happens, with a generic subject, and the run says so rather than passing it off as the resolver's own.

A pass that fixed nothing but recorded deferred work commits under `chore: record deferred crossrev findings`, which is already an accurate description of what happened.

### How a finding is named

Wherever CrossRev refers to a finding in something you read, it names it by **where the code is** — `path:line`, in a column headed Location — and links it.

Which link depends on what you are likely to be asking at that point.

| Where you see it | The link goes to |
|---|---|
| The review leg's findings table | The code, permalinked to the revision that was reviewed |
| The resolve leg's summary table | The review thread, where the reasoning and any dispute are |
| The deferred work list | The same thread |

A finding GitHub could not anchor to a line has no thread, so its location falls back to the code permalink.

CrossRev also gives every finding a 16-character id, used to match a reply to the finding it answers across passes. It is deliberately not shown to you: it lives inside a hidden marker, so it does not render and you cannot even search a page for it.

### Severity and category

Severity says how bad the defect is, and nothing else:

| Severity | Meaning |
|---|---|
| `high` | A bug that should be fixed before merging |
| `medium` | Worth fixing, not alarming |
| `low` | Minor |

Category is a closed set — `correctness`, `security`, `performance`, `maintainability`, `testing`, `docs`. Free text would turn the summary table to mush.

A finding also carries `pre_existing`, true when the defect would still be there if the pull request were reverted. Pre-existing defects are reported at any severity but never fixed, and they cannot keep the loop alive. A pull request that also fixes old bugs is one nobody can review.

### The five resolutions

Every finding gets a reply, whatever the resolve leg decides. Nothing is silently dropped.

| Resolution | What it means |
|---|---|
| `fixed` | Code changed |
| `skipped` | Not acting, by policy — typically below `min_fix_severity` |
| `deferred` | Real, worth doing, not here. Persisted to the backlog |
| `disputed` | Technically wrong for this codebase |
| `escalated` | Needs a human decision. Applies `crossrev/stop` and leaves the thread open |

### The six labels

The label row on a pull request reads at a glance, because no two of the six colours are adjacent on the wheel:

| Label | Colour | Meaning |
|---|---|---|
| `crossrev/awaiting-review` | blue | A review is owed |
| `crossrev/awaiting-resolution` | purple | The review landed, the resolve leg is owed |
| `crossrev/converged` | green | The loop finished on its own |
| `crossrev/halted` | orange | Stopped short, a human is needed |
| `crossrev/stop` | red | A human applied it, and the loop stops |
| `crossrev/pass-N` | grey | Which pass it is on |

**Red is reserved for `stop`**, the one label a human applies, so a red pill in a pull request list always means somebody pulled the brake — never that the loop had trouble. `crossrev/watchdog-retried` is a seventh, yellow, and it is bookkeeping rather than a loop state.

`crossrev init --upgrade` recolours labels minted before this palette, so there is no migration to run.

### The state lives on the pull request

Markers are HTML comments in comment bodies. They carry the pass number, the leg, the verdict, the findings, the head SHA and a timestamp, so **every pass is reconstructable from the pull request alone.** Nothing is cached locally, there is nothing to clean up, and a run that dies mid-flight loses nothing. `crossrev status --pr N` is just a rendering of what's already there.

### The run record on disk

Every run also writes locally, under `~/.local/state/crossrev/runs/<repo-slug>/pr-<n>/<run-id>/` (the slash in the repository name becomes a hyphen) — beside the worktrees a failed resolve leg keeps, and named by the same run id the marker carries.

`run.log` is one line per event — timestamp, phase, subprocess, exit code, duration — so a stall inside a step is attributable to that step, and a dead run says where it stopped. A failed leg also keeps the harness transcript there (`<leg>.attempt-N.stdout` and `.stderr`), so what the model actually did can be read afterwards instead of being deleted by the code that noticed the failure. Successful legs delete their transcripts; `--keep-transcripts` keeps them, for the failure that is a wrong answer rather than an error.

Two bounds hold. Nothing grows without limit: run directories older than `logs.retention_days` (default 14) are swept, logs and transcripts alike. And no credential reaches either file: the harness process holds no GitHub token, and captured output is redacted before it lands.

In automated mode the runner is discarded after the job, so the generated workflows upload the directory as a run artifact (`crossrev-run-<run-id>`, retained 14 days). The pull request marker stays the durable record there — the artifact is the diagnosis.

## When the loop stops

`crossrev status --pr N` reports one of five states and always ends with something you can type:

| State | What happened |
|---|---|
| awaiting review | A review leg is owed |
| awaiting resolution | The review landed; the resolve leg is owed |
| converged | Nothing at or above `min_fix_severity` remains. The loop finished on its own |
| halted | It stopped short — a cap, a blocked leg, an escalated finding, or a deferral whose record never landed. A human is needed |
| stopped | Somebody applied `crossrev/stop` |

A resolve pass that ended blocked or escalated is complete but not settled, so it can be driven again. Once whatever stopped it is fixed, `crossrev resolve --pr N` runs the resolver over the same findings instead of refusing. The same goes for a pass that left a deferral unpersisted, and for one whose claimed fix reached no commit. A pass that settled every finding stays finished. `status` names whichever command applies.

A resolve pass can also finish the loop itself. A pass that settled every finding without pushing a commit — each disputed, skipped, or deferred and tracked — converges on the spot: the head never moved, so a re-review would find nothing new and decline. A pass that pushed hands back to the reviewer, because there is something new to see.

Converged does not mean "no findings". It means no finding this pull request introduced, at or above the threshold, remains. Findings below the threshold and pre-existing ones are reported and cannot keep the loop alive — a loop that cannot converge because of a naming quibble is one nobody leaves switched on.

One exception keeps the green honest: a pass that raises nothing new while an escalated finding is still open does not converge. The reviewer does not re-raise a settled finding, so the pass is empty precisely because the loop is waiting on a person — and `halted` stays on the pull request until that person settles the thread and a later pass verifies the settlement.

## Which models run

With no config file anywhere, the defaults are `codex` reviewing and `claude` resolving, in `local` mode, with nothing persisted. Override a leg for one run without touching the repository:

```bash
crossrev review --pr 42 --harness claude
```

Repository policy lives in `.github/crossrev.yml`, and **it is read from the base revision, never the branch under review** — so a config committed on the pull request branch has no effect until it merges. A pull request cannot rewrite the loop that reviews it. See [configuration.md](configuration.md).

**Cross-vendor pairings are the strongest.** A bug one model family misses reviewing, it misses resolving too. CrossRev asserts that the two legs really did differ — in binary, resolved base URL or model — and refuses to run when endpoint variables are already set in the environment, because those redirect the harness process-wide and the loop would otherwise complete normally with the wrong model on both legs.

## Automated mode

```bash
crossrev auth login    # register and install the GitHub App, two browser approvals
crossrev init          # prints an itemised plan, asks once, then sets it up
```

`init` is the most consequential command here — it registers a GitHub identity, writes secrets and adds workflow files. It prints every path, secret and label it would touch, flags anything it would overwrite, and stops there under `--dry-run`. `--upgrade` re-renders existing workflows and recolours existing labels.

`init` generates up to four workflows: the review leg, the resolve leg, a watchdog on a schedule, and — only when the pairing needs one — a credential refresher. Each pins CrossRev's composite action at a full 40-character SHA with the tag as a trailing comment.

**Automated mode is unproven end to end.** No repository has had these workflows installed yet. That is what the `0.x` version records, and it is the [roadmap's](ROADMAP.md) first item.

### Pull requests on a public repository

On a public repository, CrossRev's automated mode reviews pull requests from branches in the repository — your team's, and Dependabot's — but not contributions from forks. GitHub withholds secrets from fork workflows, so CrossRev refuses them in CI rather than running unauthenticated.

| Case | Works |
|---|---|
| Maintainer and collaborator pull requests | Yes — push access means the branch is in the repository |
| Dependabot and Renovate | Yes — they push to in-repo branches (`dependabot/...`), not forks |
| Public repositories where all contributors have push access | Yes |
| Outside-contributor pull requests | No |

Two workarounds exist for outside-contributor pull requests:

- **Copy the contributor's branch into the repository and open an internal pull request from it.** CrossRev then reviews it normally. The review comments land on the copy, so the contributor never sees inline comments on their own pull request.
- **Add the contributor as a collaborator so they push branches rather than forking.** A trust decision unrelated to CrossRev, and only sensible for repeat contributors.

### The watchdog

Event-driven mode's failure mode is silence: a dropped label event and a converged pull request look identical from outside. So the watchdog goes looking, on a schedule.

```bash
crossrev watchdog --repo owner/name --timeout 1800
```

It finds open pull requests carrying a `crossrev/awaiting-*` label, and for each one past the timeout it **retries once** by removing and re-applying the label — re-applying a label GitHub already holds fires no event, which is why the removal matters. A second failure is not a dropped event, so it applies `crossrev/halted`, comments saying how far the leg got, and stops.

## Where deferred work goes

A real finding CrossRev chose not to fix has to go somewhere, or it disappears when the pull request merges — an unresolved thread on a merged pull request appears in no GitHub view.

```bash
crossrev config backlog    # the resolved destination
```

`github_issues` files an issue with the repository's own labels. `repository` writes into the checkout, either one file per finding (`layout: folder`) or appended to one list (`layout: file`). `none` leaves the thread open. `auto` reads the repository's `## Project Map` declaration and then sniffs for a backlog convention already in use. Details in [configuration.md](configuration.md).
