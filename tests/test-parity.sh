#!/usr/bin/env bash
#
# Parity vectors: the Bash implementation must still produce exactly what
# tests/fixtures/parity records, on every platform that runs this suite.
#
# The fixtures were captured once by tests/capture-parity.sh, from the real
# library functions, with each file naming the platform, tr implementation and
# locale it was captured under. A native port will assert against the same
# files; this suite holds the Bash side of that contract until then. Any
# failure here is either an unintended behaviour change or a recapture made
# without reading its own diff.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/ui.sh
source "$HERE/../lib/ui.sh"
# shellcheck source=../lib/diff.sh
source "$HERE/../lib/diff.sh"
# shellcheck source=../lib/sandbox.sh
source "$HERE/../lib/sandbox.sh"
# shellcheck source=../lib/prompt.sh
source "$HERE/../lib/prompt.sh"
# shellcheck source=../lib/state.sh
source "$HERE/../lib/state.sh"
# shellcheck source=../lib/harnesses.sh
source "$HERE/../lib/harnesses.sh"
# shellcheck source=../lib/config.sh
source "$HERE/../lib/config.sh"
# shellcheck source=../lib/legs.sh
source "$HERE/../lib/legs.sh"

export GIT_AUTHOR_NAME="crossrev"
export GIT_AUTHOR_EMAIL="test@example.com"
export GIT_COMMITTER_NAME="crossrev"
export GIT_COMMITTER_EMAIL="test@example.com"

PARITY="$HERE/fixtures/parity"

pass=0 fail=0
ok()    { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL  %s\n    expected: %s\n    actual:   %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
is()    { [[ "$2" == "$3" ]] && ok "$1" || notok "$1" "$3" "$2"; }

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

# Every fixture names where it was captured, or a silent recapture has nothing
# to be compared against later.
provenance_is_recorded() {
  local f name
  for f in "$PARITY"/*.json; do
    name="$(basename "$f")"
    [[ -n "$(jq -r '.captured.platform // empty' "$f")" ]] \
      && [[ -n "$(jq -r '.captured.tr_implementation // empty' "$f")" ]] \
      && [[ -n "$(jq -r '.captured.locale // empty' "$f")" ]] \
      && [[ -n "$(jq -r '.captured.awk_implementation // empty' "$f")" ]] \
      && ok "$name records the tools and locale it was captured under" \
      || notok "$name records the tools and locale it was captured under" "all four present" "$(jq -c .captured "$f")"
  done
}

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  is "finding id: $name" \
    "$(state_finding_id "$(jq -r .path <<<"$c")" "$(jq -jr .title <<<"$c")" "$(jq -r .anchor <<<"$c")")" \
    "$(jq -r .id <<<"$c")"
done < <(jq -c '.cases[]' "$PARITY/state_finding_id.json")

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  if [[ "$(jq -r .exists <<<"$c")" == "true" ]]; then
    cf="$workdir/content.ts"
    jq -jr .content <<<"$c" >"$cf"
    is "anchor: $name" "$(state_anchor "$cf" "$(jq -r .line <<<"$c")")" "$(jq -r .anchor <<<"$c")"
  else
    is "anchor: $name" "$(state_anchor "$workdir/does-not-exist.ts" "$(jq -r .line <<<"$c")")" ""
  fi
done < <(jq -c '.cases[]' "$PARITY/state_anchor.json")

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  is "marker codec: $name" \
    "$(state_marker_of "$(jq -jr .body <<<"$c")")" \
    "$(jq -r .decoded <<<"$c")"
done < <(jq -c '.cases[]' "$PARITY/marker_codec.json")

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  # A case records its input one of two ways. `input` is a JSON value, which jq
  # normalised when it was captured; `input_raw` is the text verbatim, which is
  # what the cases that pin the normalisation itself need. Feeding a normalised
  # input back would test nothing about the rewriting.
  if [[ "$(jq 'has("input_raw")' <<<"$c")" == "true" ]]; then
    inp="$(jq -r .input_raw <<<"$c")"
  else
    inp="$(jq -c .input <<<"$c")"
  fi
  is "marker encode: $name" \
    "$(state_marker_encode "$inp")" \
    "$(jq -r .encoded <<<"$c")"
done < <(jq -c '.cases[]' "$PARITY/marker_encode.json")

diff_corpus_file="$workdir/parity_corpus.diff"
jq -j .corpus "$PARITY/diff_views.json" >"$diff_corpus_file"

is "diff views: diff_number" \
  "$(diff_number "$diff_corpus_file")" \
  "$(jq -j .diff_number "$PARITY/diff_views.json")"

while IFS= read -r a; do
  name="$(jq -r .name <<<"$a")"
  path="$(jq -r .path <<<"$a")"
  side="$(jq -r .side <<<"$a")"
  line="$(jq -r .line <<<"$a")"
  bound="$(jq -r .bound <<<"$a")"
  is "diff anchor: $name" \
    "$(diff_anchor "$diff_corpus_file" "$path" "$side" "$line" "$bound")" \
    "$(jq -r .result <<<"$a")"
done < <(jq -c '.anchors[]' "$PARITY/diff_views.json")

# The malformed corpus. Shapes git does not produce and the awk still answers
# for: a hunk header that claims more lines than it holds, one that reads as no
# number at all, a section with no side lines, and a side line inside a hunk.
# Frozen because a port would otherwise hand-write these answers.
malformed_file="$workdir/parity_malformed.diff"
jq -j .malformed.corpus "$PARITY/diff_views.json" >"$malformed_file"

is "diff views: malformed diff_number" \
  "$(diff_number "$malformed_file")" \
  "$(jq -j .malformed.diff_number "$PARITY/diff_views.json")"

while IFS= read -r a; do
  name="$(jq -r .name <<<"$a")"
  path="$(jq -r .path <<<"$a")"
  side="$(jq -r .side <<<"$a")"
  line="$(jq -r .line <<<"$a")"
  bound="$(jq -r .bound <<<"$a")"
  is "diff anchor, malformed: $name" \
    "$(diff_anchor "$malformed_file" "$path" "$side" "$line" "$bound")" \
    "$(jq -r .result <<<"$a")"
done < <(jq -c '.malformed.anchors[]' "$PARITY/diff_views.json")

while IFS= read -r ex; do
  name="$(jq -r .name <<<"$ex")"
  ex_list=()
  while IFS= read -r item; do
    [[ -n "$item" ]] && ex_list+=("$item")
  done < <(jq -r '.exclusions[]' <<<"$ex")
  if [[ ${#ex_list[@]} -eq 0 ]]; then
    actual_ex="$(diff_exclude "$diff_corpus_file")"
  else
    actual_ex="$(diff_exclude "$diff_corpus_file" "${ex_list[@]}")"
  fi
  expected_ex="$(jq -j .output <<<"$ex")"
  is "diff exclude: $name" "$actual_ex" "$expected_ex"
done < <(jq -c '.excludes[]' "$PARITY/diff_views.json")

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  d="$(mktemp -d "$workdir/cfg_test_XXXXXX")"
  (
    cd "$d" && git init -q . && git commit -q --allow-empty -m init
    repo_yaml="$(jq -r '.repo_yaml // empty' <<<"$c")"
    op_yaml="$(jq -r '.operator_yaml // empty' <<<"$c")"
    base_sha="$(jq -r '.base_sha // empty' <<<"$c")"
    if [[ -n "$base_sha" ]]; then
      printf '%s\n' "$repo_yaml" > .crossrev.yml
      git add .crossrev.yml && git commit -q -m "base policy"
      real_base="$(git rev-parse HEAD)"
      rm -f .crossrev.yml
      git commit -q --allow-empty -m "head commit"
      XDG_CONFIG_HOME="$d/xdg"; export XDG_CONFIG_HOME
      cfg_load "$real_base"
    else
      if [[ -n "$repo_yaml" ]]; then
        mkdir -p .github
        printf '%s\n' "$repo_yaml" > .github/crossrev.yml
      fi
      if [[ -n "$op_yaml" ]]; then
        mkdir -p "$d/xdg/crossrev"
        printf '%s\n' "$op_yaml" > "$d/xdg/crossrev/config.yml"
      fi
      XDG_CONFIG_HOME="$d/xdg"; export XDG_CONFIG_HOME
      cfg_load
    fi
    printf '%s' "$CFG_MERGED"
  ) >"$d/actual_merged.json"
  expected_merged="$(jq -c .merged <<<"$c")"
  actual_merged="$(jq -c . <"$d/actual_merged.json")"
  is "config merge: $name" "$actual_merged" "$expected_merged"
done < <(jq -c '.cases[]' "$PARITY/config_merge.json")

# Each vector records the driver that produced it. cfg_load alone reaches only the
# values it validates itself; an endpoint or a backlog value is refused at the
# point a caller asks for it, and the base-revision arm of the version check
# composes a different message from the working-tree one.
while IFS= read -r ref; do
  name="$(jq -r .name <<<"$ref")"
  driver="$(jq -r '.driver // "load"' <<<"$ref")"
  d="$(mktemp -d "$workdir/cfg_refusal_XXXXXX")"
  (
    cd "$d" && git init -q .
    mkdir -p .github
    jq -j .config <<<"$ref" > .github/crossrev.yml
    XDG_CONFIG_HOME="$d/xdg"; export XDG_CONFIG_HOME

    case "$driver" in
      load_at_base)
        git add .github/crossrev.yml
        GIT_AUTHOR_NAME="crossrev" GIT_AUTHOR_EMAIL="test@example.com" \
        GIT_COMMITTER_NAME="crossrev" GIT_COMMITTER_EMAIL="test@example.com" \
        GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z" \
        git commit -q -m "base config"
        base_sha="$(git rev-parse HEAD)"
        rm -f .github/crossrev.yml
        GIT_AUTHOR_NAME="crossrev" GIT_AUTHOR_EMAIL="test@example.com" \
        GIT_COMMITTER_NAME="crossrev" GIT_COMMITTER_EMAIL="test@example.com" \
        GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z" \
        git commit -q --allow-empty -m "head commit"
        cfg_load "$base_sha" >/dev/null
        ;;
      call)
        git commit -q --allow-empty -m init
        # @sh quotes every element, so an empty argument survives as one.
        # Declared first because shellcheck cannot see an assignment through eval.
        call=()
        eval "call=( $(jq -r '.call | @sh' <<<"$ref") )"
        cfg_load >/dev/null && "${call[@]}" >/dev/null
        ;;
      *)
        git commit -q --allow-empty -m init
        cfg_load >/dev/null
        ;;
    esac
  ) >"$d/err.txt" 2>&1; rc=$?
  err="$(cat "$d/err.txt")"
  # A base-revision refusal names the revision it read. The SHA depends on how
  # this replay built its repository, not on the behaviour, so both sides carry
  # the placeholder that capture-parity.sh recorded.
  if [[ "$driver" == "load_at_base" ]]; then
    err="$(sed -E 's/[0-9a-f]{40}/<base_sha>/g' <<<"$err")"
  fi
  expected_err="$(jq -r .error <<<"$ref")"
  [[ $rc -ne 0 ]] && ok "config refusal exits non-zero: $name" \
    || notok "config refusal exits non-zero: $name" "non-zero exit" "$rc"
  is "config refusal error: $name" "$err" "$expected_err"
done < <(jq -c '.refusals[]' "$PARITY/config_merge.json")

while IFS= read -r l; do
  label="$(jq -r .label <<<"$l")"
  is "label colour: $label" "$(legs_label_colour "$label")" "$(jq -r .colour <<<"$l")"
  is "label description: $label" "$(legs_label_description "$label")" "$(jq -r .description <<<"$l")"
done < <(jq -c '.labels[]' "$PARITY/labels.json")

rebuild_prompt() {
  local fixture="$1" leg="$2" out="$3" skill diff meta threads
  skill="$workdir/skill.md"; diff="$workdir/diff.patch"
  jq -j '.inputs.skill' "$fixture" >"$skill"
  jq -j '.inputs.diff' "$fixture" >"$diff"
  meta="$(jq -c '.inputs.meta' "$fixture")"
  threads="$(jq -c '.inputs.threads' "$fixture")"
  if [[ "$leg" == "review" ]]; then
    prompt_review "$out" "$skill" "$diff" "$meta" \
      "$(jq -c '.inputs.prior' "$fixture")" "$threads"
  else
    prompt_resolve "$out" "$skill" "$diff" "$meta" \
      "$(jq -c '.inputs.findings' "$fixture")" "$threads" \
      "$(jq -c '.inputs.candidates' "$fixture")"
  fi
}

for leg in review resolve; do
  fixture="$PARITY/prompt_$leg.json"
  out="$workdir/prompt.txt"; expected="$workdir/expected.txt"
  rebuild_prompt "$fixture" "$leg" "$out"
  jq -j '.prompt' "$fixture" >"$expected"
  if cmp -s "$out" "$expected"; then
    ok "prompt_$leg assembles byte for byte ($(wc -c <"$out" | tr -d ' ') bytes)"
  else
    notok "prompt_$leg assembles byte for byte" "$(wc -c <"$expected" | tr -d ' ') bytes" \
      "$(wc -c <"$out" | tr -d ' ') bytes, first difference: $(cmp "$out" "$expected" 2>&1 | head -1)"
  fi
done

provenance_is_recorded

# The commit convention, over a real base revision.
#
# The resolve prompt fixture records an empty base_sha, so this section
# contributes nothing to it. Replaying these rebuilds each repository with the
# same pinned author and dates the capture used, so the base revision and every
# byte come back the same.
cc_replay() { # <n_repo> <n_mine> <template>
  local n_repo="$1" n_mine="$2" template="$3" d i base
  d="$(mktemp -d "$workdir/ccr_XXXXXX")"
  (
    cd "$d" && git init -q .
    _cc() {
      GIT_AUTHOR_NAME="capture" GIT_AUTHOR_EMAIL="$1" \
      GIT_COMMITTER_NAME="capture" GIT_COMMITTER_EMAIL="$1" \
      GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z" \
      git commit -q --allow-empty -m "$2"
    }
    for (( i = 1; i <= n_mine; i++ )); do
      _cc "crossrev@example.com" "chore(crossrev): a subject the leg must not learn from $i"
    done
    for (( i = 1; i <= n_repo; i++ )); do
      _cc "dev@example.com" "feat(api): add the $i-th endpoint"
    done
    if [[ -n "$template" ]]; then
      printf '%s' "$template" > .gitmessage
      git add .gitmessage
      _cc "dev@example.com" "chore: add a commit template"
    fi
    base="$(git rev-parse HEAD)"
    prompt_commit_convention "$base" "crossrev@example.com"
  )
}

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  if [[ "$name" == "no-base" ]]; then
    is "commit convention: $name" \
      "$(prompt_commit_convention "" "crossrev@example.com")" \
      "$(jq -r .rendered <<<"$c")"
    continue
  fi
  is "commit convention: $name" \
    "$(cc_replay "$(jq -r .repo_subjects <<<"$c")" "$(jq -r .own_subjects <<<"$c")" "$(jq -r .template <<<"$c")")" \
    "$(jq -r .rendered <<<"$c")"
done < <(jq -c '.cases[]' "$PARITY/prompt_commit_convention.json")

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
