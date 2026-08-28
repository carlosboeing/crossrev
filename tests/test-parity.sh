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
# Colour is decided when lib/ui.sh is sourced, from whether stdout is a terminal
# at that moment. A vector holding refusal text would therefore carry escape
# codes when captured from a terminal and none when captured through a pipe, so
# the same behaviour would freeze two different ways. NO_COLOR settles it before
# the decision is made.
export NO_COLOR=1
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
# shellcheck source=../lib/log.sh
source "$HERE/../lib/log.sh"
# shellcheck source=../lib/credentials.sh
source "$HERE/../lib/credentials.sh"
# shellcheck source=../lib/usage.sh
source "$HERE/../lib/usage.sh"

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


# --- redaction ---------------------------------------------------------------

# The _b64 fields are read rather than the plain ones. A body carrying bytes
# that are not valid UTF-8 cannot round-trip through a JSON string, and that is
# exactly the body log_redact pins LC_ALL=C for.
unb64() { printf '%s' "$1" | openssl base64 -d -A; }

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  text="$(unb64 "$(jq -r .text_b64 <<<"$c")"; printf 'x')"; text="${text%x}"
  got="$(log_redact_str "$text"; printf 'x')"; got="${got%x}"
  want="$(unb64 "$(jq -r .redacted_b64 <<<"$c")"; printf 'x')"; want="${want%x}"
  is "redact string: $name" "$got" "$want"
  if pub="$(log_redact_publish "$text"; printf 'x')"; then rc=0; else rc=$?; fi
  pub="${pub%x}"
  wantpub="$(unb64 "$(jq -r .published_b64 <<<"$c")"; printf 'x')"; wantpub="${wantpub%x}"
  is "redact publish: $name" "$pub" "$wantpub"
  is "redact publish rc: $name" "$rc" "$(jq -r .published_rc <<<"$c")"
done < <(jq -c '.cases[]' "$PARITY/redaction.json")

is "redact publish notice text" "$LOG_REDACT_NOTICE" "$(jq -r .notice "$PARITY/redaction.json")"

# --- legs_github_slug --------------------------------------------------------

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  url="$(jq -jr .url <<<"$c")"
  got="$(legs_github_slug "$url")" && rc=0 || rc=$?
  is "github slug rc: $name" "$rc" "$(jq -r .rc <<<"$c")"
  if [[ "$rc" -eq 0 ]]; then
    is "github slug: $name" "$got" "$(jq -r .slug <<<"$c")"
  fi
done < <(jq -c '.cases[]' "$PARITY/github_slug.json")

# --- credentials -------------------------------------------------------------

is "cred minimum freshness seconds" "$CRED_MIN_SECONDS" \
  "$(jq -r .cred_min_seconds "$PARITY/credentials.json")"

while IFS= read -r c; do
  h="$(jq -r .harness <<<"$c")"
  is "credential strip set: $h" \
    "$(cred_env_strip_for "$h" | jq -Rs 'split("\n") | map(select(length > 0))' | jq -cS .)" \
    "$(jq -cS .strip <<<"$c")"
done < <(jq -c '.strip_sets[]' "$PARITY/credentials.json")

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  jwt="$(jq -jr .jwt <<<"$c")"
  got="$(cred_jwt_claims "$jwt")" && rc=0 || rc=$?
  is "jwt claims rc: $name" "$rc" "$(jq -r .rc <<<"$c")"
  if [[ "$rc" -eq 0 ]]; then
    is "jwt claims: $name" "$got" "$(jq -r .claims <<<"$c")"
  fi
done < <(jq -c '.jwt_cases[]' "$PARITY/credentials.json")

while IFS= read -r c; do
  s="$(jq -r .seconds <<<"$c")"
  is "credential duration: $s" "$(_cred_human_duration "$s")" "$(jq -r .human <<<"$c")"
done < <(jq -c '.duration_cases[]' "$PARITY/credentials.json")

# --- usage -------------------------------------------------------------------

is "usage zero record" "$(usage_zero)" "$(jq -c .zero "$PARITY/usage.json")"
is "usage zero record with total" "$(usage_with_total "$(usage_zero)")" \
  "$(jq -c .zero_with_total "$PARITY/usage.json")"

# A price table the vectors were not captured against would fail every price
# case with no explanation, so it is named once rather than discovered twenty
# times.
is "price table version" "$(jq -r '.version // ""' "$HERE/../lib/prices.json")" \
  "$(jq -r .price_table_version "$PARITY/usage.json")"

usage_replay_file="$workdir/usage_input"
while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  jq -jr .input <<<"$c" >"$usage_replay_file"
  case "$(jq -r .parser <<<"$c")" in
    claude)   got="$(usage_parse_claude "$usage_replay_file")" ;;
    codex)    got="$(usage_parse_codex_events "$usage_replay_file")" ;;
    grok)     got="$(usage_parse_grok "$usage_replay_file")" ;;
    agy)      got="$(usage_parse_agy "$usage_replay_file")" ;;
    opencode) got="$(usage_parse_opencode_export "$usage_replay_file")" ;;
  esac
  is "usage parse: $name" "$got" "$(jq -r .record <<<"$c")"
done < <(jq -c '.parse_cases[]' "$PARITY/usage.json")

while IFS= read -r c; do
  r="$(jq -jr .reported <<<"$c")"
  is "price key: ${r:-<empty>}" "$(usage_price_key "$r")" "$(jq -r .key <<<"$c")"
done < <(jq -c '.price_key_cases[]' "$PARITY/usage.json")

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  is "usage price: $name" \
    "$(usage_price "$(jq -r .usage <<<"$c")" "$(jq -r .model <<<"$c")")" \
    "$(jq -r .priced <<<"$c")"
done < <(jq -c '.price_cases[]' "$PARITY/usage.json")

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  h="$(jq -r .harness <<<"$c")"; e="$(jq -r .endpoint <<<"$c")"
  if [[ "$(jq -r .anthropic_api_key_set <<<"$c")" == "true" ]]; then
    got="$(ANTHROPIC_API_KEY="sk-ant-test" usage_billing_for "$h" "$e")"
  else
    got="$(ANTHROPIC_API_KEY="" usage_billing_for "$h" "$e")"
  fi
  is "usage billing: $name" "$got" "$(jq -r .billing <<<"$c")"
done < <(jq -c '.billing_cases[]' "$PARITY/usage.json")

while IFS= read -r c; do
  v="$(jq -jr .value <<<"$c")"
  is "cost format: ${v:-<empty>}" "$(usage_format_cost "$v")" "$(jq -r .formatted <<<"$c")"
done < <(jq -c '.format_cost_cases[]' "$PARITY/usage.json")

while IFS= read -r c; do
  cs="$(jq -jr .cost_source <<<"$c")"; b="$(jq -jr .billing <<<"$c")"
  is "usage footnote: ${cs:-<empty>}/${b:-<empty>}" \
    "$(usage_footnote "$cs" "$b")" "$(jq -r .footnote <<<"$c")"
done < <(jq -c '.footnote_cases[]' "$PARITY/usage.json")


# --- push targets ------------------------------------------------------------

push_replay() { # dir <config lines>
  local d="$1"; shift
  local line
  (
    cd "$d" && git init -q . 2>/dev/null
    for line in "$@"; do
      git config --add "${line%%$'\t'*}" "${line#*$'\t'}"
    done
    LEGS_PUSH_REPO=""
    legs_resolve_push_repo origin 2>"$d/err"
    printf '%s' "$LEGS_PUSH_REPO"
  )
}

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  cfg=(); while IFS= read -r line; do cfg+=("$line"); done < <(jq -r '.config[]' <<<"$c")
  d="$(mktemp -d "$workdir/prr_XXXXXX")"
  got="$(push_replay "$d" ${cfg+"${cfg[@]}"})" && rc=0 || rc=$?
  is "push target rc: $name" "$rc" "$(jq -r .rc <<<"$c")"
  is "push target repo: $name" "$got" "$(jq -r .push_repo <<<"$c")"
  is "push target message: $name" "$(cat "$d/err" 2>/dev/null || true)" "$(jq -r .stderr <<<"$c")"
done < <(jq -c '.cases[]' "$PARITY/push_target.json")

# --- run-log paths and quarantine paths --------------------------------------

is "local run id shape" \
  "$(GITHUB_RUN_ID="" log_run_id | sed -E 's/^local-[0-9]+$/local-<pid>/')" \
  "$(jq -r .local_run_id_shape "$PARITY/paths.json")"

while IFS= read -r c; do
  name="$(jq -r .name <<<"$c")"
  is "run dir: $name" \
    "$(XDG_STATE_HOME="$(jq -r .xdg_state_home <<<"$c")" \
       HOME="$(jq -r .home <<<"$c")" \
       GITHUB_RUN_ID="$(jq -r .github_run_id <<<"$c")" \
       log_run_dir "$(jq -r .repo <<<"$c")" "$(jq -r .pr <<<"$c")")" \
    "$(jq -r .dir <<<"$c")"
done < <(jq -c '.run_dirs[]' "$PARITY/paths.json")

is "quarantine directory name" "$CROSSREV_QUARANTINE" \
  "$(jq -r .quarantine_dir "$PARITY/paths.json")"
is "quarantined paths" \
  "$(_sandbox_paths | jq -Rs 'split("\n")|map(select(length>0))' | jq -cS .)" \
  "$(jq -cS .quarantined_paths "$PARITY/paths.json")"

while IFS= read -r c; do
  h="$(jq -r .harness <<<"$c")"
  is "sandbox args: $h" "$(sandbox_args_for "$h")" "$(jq -r .args <<<"$c")"
done < <(jq -c '.sandbox_args[]' "$PARITY/paths.json")

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
(( fail == 0 ))
