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
| `--harness claude\|codex\|agy` | Override the harness the config names for that leg, for this run |
| `--repo owner/name` | Target a repository other than this checkout |
| `--no-tips` | Suppress the closing suggestion about automated mode |

**Start with `review` on its own.** It only writes comments, so it is the cheapest way to find out whether the findings are any good — which is the question that decides whether the rest is worth it.

### Before you point it at something you care about

**`resolve` and `cycle` commit and push to the pull request's branch.** That is the point of the tool. Three rails constrain it:

- **The branch guard** refuses to push unless your checkout is on the pull request's own head branch, that branch is not the repository default, and the head repository matches your origin. Asserted before anything leaves the machine, because branch protection fires only after a bad push is attempted and says nothing about which branch was targeted.
- **`policy.max_passes_per_cycle`** caps the loop at 3 passes by default.
- **The `crossrev/stop` label** halts the loop, and outranks a healthy verdict. It is checked first, every pass.

To watch with no risk of a push at all, run `review` only.

**Fork pull requests are refused, not skipped quietly.** GitHub withholds secrets from them, and a fork's head branch is not yours to push to. CrossRev says so and stops.

## What a pass writes to the pull request

The review leg writes:

- **One inline comment per finding**, on the line it affects. Each heading carries a coloured circle for severity and the category as a word — `🔴 **High · Security** — <title>` — followed by what goes wrong and how to fix it.
- **One summary comment**, opening with exactly one native GitHub alert carrying the verdict, then a table of findings with a pictogram per category, and closing with a run-details table naming the agent that ran, how long it took and what it cost in tokens. One row, for that comment's own leg.
- **A hidden marker** inside the summary comment, and one inside each inline comment.

The resolve leg adds threaded replies, resolves the threads it settled, commits any fixes, files deferred defects to the configured backlog, and posts its own summary comment.

### Severity and category

Severity says how bad the defect is, and nothing else:

| Severity | Meaning |
|---|---|
| `high` | A bug that should be fixed before merging |
| `medium` | Worth fixing, not alarming |
| `low` | Minor |

Category is a closed set — `correctness`, `security`, `performance`, `maintainability`, `testing`, `docs`. Free text would turn the summary table to mush.

A finding also carries `pre_existing`, true when the defect would still be there if the pull request were reverted. Pre-existing defects are reported at any severity but never fixed, and they cannot keep the loop alive. A pull request that also fixes old bugs is one nobody can review.

### The five dispositions

Every finding gets a reply, whatever the resolve leg decides. Nothing is silently dropped.

| Disposition | What it means |
|---|---|
| `fixed` | Code changed |
| `skipped` | Not acting, by policy — typically below `min_fix_severity` |
| `deferred` | Real, worth doing, not here. Persisted to the backlog |
| `rebutted` | Technically wrong for this codebase |
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

## When the loop stops

`crossrev status --pr N` reports one of five states and always ends with something you can type:

| State | What happened |
|---|---|
| awaiting review | A review leg is owed |
| awaiting resolution | The review landed; the resolve leg is owed |
| converged | Nothing at or above `min_fix_severity` remains. The loop finished on its own |
| halted | It stopped short — a cap, a blocked leg, or an escalated finding. A human is needed |
| stopped | Somebody applied `crossrev/stop` |

A resolve pass that ended blocked or escalated is complete but not settled, so it can be driven again: once whatever stopped it is fixed, `crossrev resolve --pr N` runs the resolver over the same findings instead of refusing. A pass that settled every finding stays finished. `status` names whichever command applies.

Converged does not mean "no findings". It means no finding this pull request introduced, at or above the threshold, remains. Findings below the threshold and pre-existing ones are reported and cannot keep the loop alive — a loop that cannot converge because of a naming quibble is one nobody leaves switched on.

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
