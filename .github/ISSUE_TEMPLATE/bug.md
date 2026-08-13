---
name: Bug report
about: Something CrossRev does that it should not, or does not do that it should
title: ""
labels: bug
---

<!-- Not for security issues. Those go through SECURITY.md, privately. -->

## What happened

<!-- What you ran, and what it did. Paste the output rather than describing it. -->

## What you expected

## How to reproduce

<!-- The command, and the state it ran against. If a pull request is involved,
     say whether the run was local or automated, and which pass it was on. -->

## Environment

Paste the output of these three:

```
crossrev version
crossrev doctor
uname -sr && bash --version | head -1
```

Local run or automated? If automated: which runner, and which reviewer/resolver
pairing?

## The state on the pull request

<!-- `crossrev status --pr N` renders the whole loop state, so it is usually the
     most useful single thing to include. Redact anything private. -->
