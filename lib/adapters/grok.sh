# shellcheck shell=bash
# lib/adapters/grok.sh — Grok, the fourth harness.
#
# Same contract as the other three: payload plus execution metadata, and no
# GitHub credential in the environment.
#
# `--prompt-file` takes a path and turns on headless mode. `-p` / `--print` /
# `--single` consume the next argv as the prompt, so a flag written after them
# is answered as the question. That is the same class of failure the agy
# adapter already documents. `--json-schema` takes an inline JSON string, not a
# path; handing it a path fails with a parse error about the leading slash.
#
# Authentication rejections are classified as a credential failure naming Grok.
# That is the mitigation for a vendor silently switching archetype: the
# operator sees a credential that was consumed, not a harness that stopped
# working.

# adapter_grok <prompt_file> <schema_file> <workdir> <model> <effort> <endpoint_name> <write>
adapter_grok() {
  local prompt_file="$1" schema_file="$2" workdir="$3"
  local model="${4:-}" effort="${5:-}" endpoint="${6:-}" write="${7:-no}"

  command -v grok >/dev/null 2>&1 || ui_die \
    "the grok CLI is not installed, and this leg is configured to use it" \
    "Install Grok from https://x.ai/cli, or point this leg at another harness with --harness."

  if [[ -n "$endpoint" && "$endpoint" != "null" ]]; then
    local host
    host="$(harness_field .endpoint_host)"
    ui_die "the grok adapter cannot use the endpoint '$endpoint'" \
      "Named endpoints are Anthropic-compatible and reached through the $host adapter. Use harness: $host with endpoint: $endpoint, or drop the endpoint for this leg."
  fi

  # --prompt-file last: it takes a path, so remaining flags are not swallowed,
  # but putting the prompt after the rest matches the other adapters' shape.
  local -a args=(--output-format json --permission-mode dontAsk)

  # dontAsk on both legs: headless default can prompt and hang. The resolve
  # leg needs an explicit write grant on top; the review leg is denied at both
  # the permission-rule and sandbox layers. bypassPermissions, --always-approve,
  # --yolo and --dangerously-skip-permissions are a blanket bypass and are
  # never passed.
  if [[ "$write" == "yes" ]]; then
    args+=(--sandbox workspace --allow Edit --allow Write)
  else
    args+=(--sandbox read-only --deny Edit --deny Write)
  fi

  [[ -n "$schema_file" ]] && args+=(--json-schema "$(cat "$schema_file")")
  [[ -n "$model"  && "$model"  != "null" ]] && args+=(--model "$model")
  [[ -n "$effort" && "$effort" != "null" ]] && args+=(--reasoning-effort "$effort")
  args+=(--prompt-file "$prompt_file")

  local out err rc
  # When run_invoke names a transcript base the capture files ARE the record:
  # they live in the run directory, are redacted in place, and their deletion
  # is the orchestrator's decision. Without one — anonymous temp files,
  # deleted on both paths, as before.
  local keep_transcript=0
  if [[ -n "${CROSSREV_TRANSCRIPT_BASE:-}" ]]; then
    keep_transcript=1
    out="$CROSSREV_TRANSCRIPT_BASE.stdout"; err="$CROSSREV_TRANSCRIPT_BASE.stderr"
  else
    out="$(mktemp)"; err="$(mktemp)"
  fi

  # No GitHub credential, and none belonging to another harness: this process
  # reads attacker-controlled text, and a credential it never receives is one no
  # injection can talk it into exfiltrating.
  # Four names, not three. `gh` reads GH_ENTERPRISE_TOKEN and, when that is
  # unset, GITHUB_ENTERPRISE_TOKEN — `gh help environment` lists both, "in order
  # of precedence". A strip list naming only the first left the second in the
  # agent's environment on a GitHub Enterprise Server setup, which is the one
  # kind of installation where it is the credential in use.
  local -a run=(env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN -u GITHUB_ENTERPRISE_TOKEN)
  local v
  while IFS= read -r v; do run+=(-u "$v"); done < <(cred_env_strip_for grok)

  ( cd "$workdir" && "${run[@]}" grok "${args[@]}" ) \
    >"$out" 2>"$err" </dev/null
  rc=$?

  if (( rc != 0 )); then
    # The message is chosen on whether one is actually there, not on jq's exit
    # status. `jq … || legs_harness_error "$err"` reads as a fallback and is not
    # one: on an EMPTY stdout jq exits 0 with no output, so the fallback never
    # fires and the error becomes the empty string — which is precisely the case
    # where the only diagnosis lives on stderr.
    local msg
    msg="$(jq -r '.error // .text // empty' "$out" 2>/dev/null)"
    [[ -n "$msg" ]] || msg="$(legs_harness_error "$err")"
    [[ -n "$msg" ]] || msg="grok exited $rc with no output on either stream"
    msg="$(log_redact_str "$msg")"
    if grep -qiE 'not signed in|XAI_API_KEY' "$err" 2>/dev/null \
       || [[ "$msg" == *"Not signed in"* || "$msg" == *"XAI_API_KEY"* ]]; then
      msg="Grok rejected the credential. CrossRev classifies this as a credential failure, not a generic harness error. ${msg}"
    fi
    jq -cn --arg e "$msg" \
      '{ok:false, payload:null, harness:"grok", endpoint:null, model_reported:null,
        effort_reported:null, tokens:null, usage:null, error:$e}'
    # The capture files are the record, so they are filtered here — after every
    # value has been read from them, never before. Redacting first would rewrite
    # the payload this adapter parses, so identical harness output would yield
    # different answers depending on whether a run directory exists.
    if (( keep_transcript )); then log_redact_file "$out"; log_redact_file "$err"
    else rm -f "$out" "$err"; fi
    return 1
  fi

  local payload usage tokens model_reported
  # Live grok 1.0.5 with --json-schema puts the constrained object on
  # structuredOutput. .text is the model's prose, and on a schema run it is often
  # several draft JSON objects concatenated — fromjson rejects that, which is
  # how a successful turn was reported as "the payload is not a JSON object".
  # structured_output is the snake_case sibling agy uses; keep it as a fallback
  # in case a later grok release matches that spelling. .text remains last for
  # a run with no schema.
  payload="$(jq -c '
    .structuredOutput
    // .structured_output
    // (.text
        | if type == "object" or type == "array" then .
          elif type == "string" then (fromjson? // null)
          else null end)' "$out" 2>/dev/null || echo null)"
  # Buckets summed from the parts: grok's own total_tokens happens to
  # reconcile today, but reading it would trust a vendor field the identity
  # replaces. The harness cost rides along; the answering model comes from the
  # models list the parser built out of modelUsage — its entries carry call
  # counts rather than token totals, so there is no share to rank and first is
  # the only report.
  usage="$(usage_parse_grok "$out")"
  [[ -n "$usage" ]] || usage=null
  tokens="$(jq -r '.total // "null"' <<<"$usage")"
  model_reported="$(usage_model_reported_from_models "$(jq -c '.models // []' <<<"$usage")")"

  # The capture files are the record, so they are filtered here — after every
  # value has been read from them, never before. Redacting first would rewrite
  # the payload this adapter parses, so identical harness output would yield
  # different answers depending on whether a run directory exists.
  if (( keep_transcript )); then log_redact_file "$out"; log_redact_file "$err"
  else rm -f "$out" "$err"; fi

  jq -cn --argjson p "${payload:-null}" --arg m "$model_reported" \
     --argjson t "${tokens:-null}" --argjson u "${usage:-null}" \
    '{ok:true, payload:$p, harness:"grok", endpoint:"vendor",
      model_reported:(if $m == "" then null else $m end),
      effort_reported:null,
      tokens:(if $t == "null" then null else $t end),
      usage:$u, error:null}'
}
