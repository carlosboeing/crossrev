#!/usr/bin/env bash
#
# Reads the Conventional Commit types since the last version tag and reports
# what the next version should be.
#
# The bump is not a fresh judgement made at release time. It was already made,
# once per commit, when someone chose `fix:` or `feat:`. Deciding it a second
# time from a diff produces a second opinion that can disagree with the first,
# so this reads the decisions rather than remaking them.
#
# Major is deliberately never chosen automatically. `v1.0.0` is gated on
# proving automated mode end to end, which is a conversation rather than a
# commit type, so a breaking change is reported and left to a human.

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1

current="$(tr -d '[:space:]' <VERSION)"
# `v[0-9]*.*.*` and not `v*`, because `v0` is the floating tag ADR 0009 reserves
# for the README's copy-paste example. It moves, so describing against it would
# silently measure from the wrong place.
version_tag="v$current"
if git rev-parse -q --verify "refs/tags/$version_tag" >/dev/null 2>&1; then
  # The checkout names its own release, so the tag comes from VERSION rather
  # than from reachability. A release cut on a pull request that was then
  # squash-merged is not an ancestor of HEAD — the tag stays on the pull
  # request's commit — so the range starts after the first-parent commit that
  # carried the release onto the main line: the oldest one whose tree equals
  # the tag's. VERSION is part of every tree and changes at each cut, so that
  # tree matches only commits that carried this release.
  last_tag="$version_tag"
  if git merge-base --is-ancestor "$version_tag" HEAD 2>/dev/null; then
    range="$version_tag..HEAD"
  else
    tag_tree="$(git rev-parse "$version_tag^{tree}" 2>/dev/null)"
    release_commit="$(git log --first-parent --format='%H %T' HEAD 2>/dev/null \
      | awk -v "tree=$tag_tree" '$2 == tree { found=$1 } END { print found }')"
    if [[ -n "$release_commit" ]]; then
      range="$release_commit..HEAD"
    else
      range="$version_tag..HEAD"
    fi
  fi
  since="since $last_tag"
else
  last_tag="$(git describe --tags --abbrev=0 --match='v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null)"
  if [[ -n "$last_tag" ]]; then
    range="$last_tag..HEAD"
    since="since $last_tag"
  else
    range="HEAD"
    since="across all history — no version tag found"
  fi
fi

subjects="$(git log --format=%s "$range" 2>/dev/null)"
if [[ -z "$subjects" ]]; then
  printf 'Nothing has landed %s. Current version is %s.\n' "$since" "$current"
  exit 0
fi

# `type!: …`, `type(scope)!: …`, or a BREAKING CHANGE footer anywhere in a body.
breaking="$(git log --format='%s%n%b' "$range" | grep -cE '^[a-z]+(\([^)]*\))?!:|^BREAKING CHANGE:' || true)"
feats="$(grep -cE '^feat(\([^)]*\))?:' <<<"$subjects" || true)"
fixes="$(grep -cE '^fix(\([^)]*\))?:' <<<"$subjects" || true)"
total="$(wc -l <<<"$subjects" | tr -d ' ')"

IFS=. read -r major minor patch <<<"$current"

printf '\n%s commit(s) %s\n' "$total" "$since"
printf '  %-3s breaking\n  %-3s feat\n  %-3s fix\n' "$breaking" "$feats" "$fixes"
printf '\ncurrent  %s\n' "$current"

if (( breaking > 0 )); then
  printf 'next     %d.%d.0   (minor, and read the note)\n' "$major" "$((minor + 1))"
  printf '\nA breaking change is present, and this does not choose major on its own.\n'
  printf 'While the version is 0.x, semver already allows a breaking change in a\n'
  printf 'minor bump. Going to 1.0.0 is gated on proving automated mode end to end\n'
  printf '(see docs/ROADMAP.md), which is a decision rather than a commit type.\n'
  exit 0
fi

if (( feats > 0 )); then
  printf 'next     %d.%d.0   (minor — a feat landed)\n' "$major" "$((minor + 1))"
elif (( fixes > 0 )); then
  printf 'next     %d.%d.%d   (patch — fixes only)\n' "$major" "$minor" "$((patch + 1))"
else
  printf 'next     %s   (unchanged — nothing user-facing landed)\n' "$current"
  printf '\nOnly docs, tests, chores or CI since %s. A release would publish a\n' "${last_tag:-the start}"
  printf 'tarball identical in behaviour to the one already on the registry.\n'
fi
