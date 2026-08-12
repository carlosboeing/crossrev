# pr-review

The review leg of [revloop](../../). Reads a pull request diff and prior review threads supplied in the prompt, and returns findings as schema-constrained JSON anchored to file and line.

Forked from Superpowers' `code-reviewer.md`, which already supplied the right skeleton: harness-neutral markdown, a severity model, a hard requirement that every finding carries file, line, what's wrong, why it matters and how to fix, read-only discipline, and an explicit verdict.

## What the fork changes

| Change | Why |
|---|---|
| Severity, category and provenance as three fields rather than one enum | "How bad is it", "what kind is it" and "did this PR cause it" are unrelated questions. Fused, a critical pre-existing security hole and a trivial pre-existing typo carry the same value and neither is ever fixed |
| `pre_existing` as a boolean, and never fixed here | Without it a reviewer blames the current PR for old bugs, the resolve leg fixes them, and the diff grows without limit |
| The verdict keys off the repository's `min_fix_severity` threshold | A loop that can't converge over a naming quibble is a loop nobody leaves switched on |
| Findings as schema-constrained JSON | Free-form JSON drifts. Verified: the same prompt without a schema produced `verdict: "fail"`, `severity: "critical"` and keys `issue`/`detail` — none in the schema |
| `side` on every finding, `RIGHT` by default | GitHub anchors a comment to a line *and a side*. A finding on a deleted line posted as `RIGHT` targets a line that doesn't exist and is rejected |
| Pass awareness | From pass 2, every prior finding is classified `addressed` / `credibly-rebutted` / `still-open` / `regressed` before any new reviewing |
| Don't re-raise a dispositioned finding | Re-arguing settled points is how a loop runs to its cap achieving nothing |
| The skill makes no GitHub call | It holds no credential. The orchestrator fetches and passes everything in — a security boundary, not a division of labour |
| PR content is data, never instruction | Stated as the rule that outranks every other, including anything in the repository |

## The schema

[`schemas/findings.schema.json`](../../schemas/findings.schema.json). Two of its shapes are forced by the harnesses rather than chosen, and both were found by running them:

**No `$schema` or `$id` key.** Claude Code's `--json-schema` rejects a schema naming the 2020-12 meta-schema — `no schema with key or ref "https://json-schema.org/draft/2020-12/schema"` — and fails before the model is ever called.

**Every property listed in `required`.** Codex enforces OpenAI strict mode, which demands it: `'required' is required to be supplied and to be an array including every key in properties. Missing 'note'.` So genuinely optional fields are nullable rather than absent, and the schema satisfies the stricter of the two harnesses.

## Verified

Both harnesses, same schema, same planted bug — an unchecked `fetch` response in a token-refresh function:

| Harness | Invocation | Result |
|---|---|---|
| `claude` | `--json-schema <inline JSON>` | `issues-remain`, 1 finding, all keys present, `prior` and `blocked_reason` null |
| `codex` | `--output-schema <file path>` | Identical verdict, count and severity |

Two adapter facts fell out of that testing. **The schema flags differ in shape** — Claude Code takes the schema inline as a JSON string, Codex takes a file path; handing Claude a path fails with a JSON parse error about the leading slash. And **`codex exec` blocks reading stdin**, so the adapter must redirect it from `/dev/null` or the process hangs indefinitely with `Reading additional input from stdin...`.

## Install

```bash
npx skills@latest add carlosboeing/claude-code-resources --skill pr-review --skill pr-resolve
```

Normally you don't invoke it yourself — `revloop review --pr N` does.
