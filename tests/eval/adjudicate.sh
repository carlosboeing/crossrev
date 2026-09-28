#!/usr/bin/env bash
#
# tests/eval/adjudicate.sh — the blind adjudication-sheet generator.
#
# Reads one offline full-loop results directory (as written by
# tests/eval/run-loop.sh) and writes a sheet with the arm labels removed,
# so a judge scores runs without knowing which arm produced which:
#
#   bash tests/eval/adjudicate.sh --results-dir <dir>
#
# Writes <dir>/adjudication.tsv (blind_id, case, terminal_label, legs,
# findings, resolutions, markers — paths relative to the results directory)
# and <dir>/adjudication-key.json (blind_id to case and arm), kept beside
# the sheet rather than in it. Rows are shuffled; blind ids are assigned
# after the shuffle so neither order nor id leaks the arm.

set -uo pipefail

RESULTS=""
while (( $# )); do
  case "$1" in
    --results-dir) [[ $# -ge 2 ]] || { printf 'adjudicate: --results-dir needs a value\n' >&2; exit 2; }
      RESULTS="$2"; shift 2 ;;
    -h|--help) printf 'Usage: adjudicate.sh --results-dir <dir>\n'; exit 0 ;;
    *) printf 'adjudicate: unknown option: %s\n' "$1" >&2; exit 2 ;;
  esac
done
[[ -n "$RESULTS" ]] || { printf 'adjudicate: --results-dir is required\n' >&2; exit 2; }
[[ -d "$RESULTS" ]] || { printf 'adjudicate: not a directory: %s\n' "$RESULTS" >&2; exit 2; }

entries="$(
  for result in "$RESULTS"/*/result.json "$RESULTS"/*/*/result.json; do
    [[ -f "$result" ]] || continue
    jq -r --arg r "$result" --arg root "$RESULTS/" \
      'select(.case != null and .arm != null)
       | [$r, .case, .arm, (.terminal // "none"),
          ((.legs // []) | join("+")),
          ((.artifacts // {}) | [.findings, .resolutions, .markers] | map(. // "") | join(","))]
        | @tsv' "$result" 2>/dev/null || true
  done
)"
[[ -n "$entries" ]] || { printf 'adjudicate: no arm results under %s\n' "$RESULTS" >&2; exit 2; }

# Shuffle without assuming GNU tools: a $RANDOM prefix with the input line
# number as the tiebreak, so the stock sort on either platform handles it.
shuffled="$(i=0; while IFS= read -r line; do
  i=$((i+1)); printf '%d-%06d\t%s\n' "$RANDOM" "$i" "$line"
done <<<"$entries" | sort -t "$(printf '\t')" -k1,1n | cut -f2-)"

sheet="$RESULTS/adjudication.tsv"
key="$RESULTS/adjudication-key.json"
keytmp="$(mktemp)"
# shellcheck disable=SC2064
trap "rm -f '$keytmp'" EXIT
# The scored copies live under blind/ under blind names: the sheet must not
# leak the arm through an artifact path, so it references the copies.
blinddir="$RESULTS/blind"
mkdir -p "$blinddir"
printf 'blind_id\tcase\tterminal_label\tlegs\tfindings,resolutions,markers\n' >"$sheet"
: >"$keytmp"
n=0
while IFS=$'\t' read -r path case arm terminal legs artifacts; do
  [[ -n "${path:-}" ]] || continue
  n=$((n+1))
  blind="$(printf 'entry-%03d' "$n")"
  rel_findings=""; rel_resolutions=""; rel_markers=""
  IFS=',' read -r rel_findings rel_resolutions rel_markers <<<"$artifacts"
  for kind in findings resolutions markers; do
    src=""
    case "$kind" in
      findings) src="$rel_findings" ;;
      resolutions) src="$rel_resolutions" ;;
      markers) src="$rel_markers" ;;
    esac
    if [[ -n "$src" && -f "$RESULTS/$src" ]]; then
      cp "$RESULTS/$src" "$blinddir/$blind-$kind.json"
    else
      printf 'null\n' >"$blinddir/$blind-$kind.json"
    fi
  done
  printf '%s\t%s\t%s\t%s\t%s,%s,%s\n' \
    "$blind" "$case" "$terminal" "$legs" \
    "blind/$blind-findings.json" "blind/$blind-resolutions.json" "blind/$blind-markers.json" >>"$sheet"
  jq -cn --arg b "$blind" --arg c "$case" --arg a "$arm" \
    '{($b): {case:$c, arm:$a}}' >>"$keytmp"
done <<<"$shuffled"
jq -S -s 'add' "$keytmp" >"$key"
printf 'adjudicate: %d entries in %s\n' "$n" "$sheet"
