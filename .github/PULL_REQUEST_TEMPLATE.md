<!-- Thanks for contributing to CrossRev. Keep pull requests small and focused. -->

## Summary

<!--
What changes, and why, in two or three sentences before any detail.
Cite files in this repository by path, and the ADR when relevant. Cite
nothing outside it -- no private repository, no path on your own machine.
Restate the reasoning here rather than pointing at where it was written down.
-->

## Related issue

<!-- Closes #NNN, or "n/a" for a standalone change. -->

## How was this tested?

<!-- Paste the output. Do not just assert. -->

- [ ] `bash tests/run.sh` — all suites passed
- [ ] `bash scripts/lint.sh` — lint clean
- [ ] Ran it against a real pull request, if the change touches a leg

## Checklist

- [ ] Conventional Commit subject (`type(scope): description`, imperative, <= 72 chars)
- [ ] Nothing in the body names a private repository, a workbench path, or a path on a personal machine
- [ ] No new runtime, package manager or dependency beyond bash, `gh`, `jq`, `yq`, `openssl`
- [ ] No GitHub credential reaches the agent process
- [ ] Policy still read from the pull request's base revision, never its head
- [ ] Markers and the `crossrev/*` label namespace still lowercase
- [ ] `CrossRev` in anything a person reads; lowercase only in the closed list (ADR 0010)
- [ ] CHANGELOG / ROADMAP / docs / ADR updated in the same commit set if user-facing behaviour changed
