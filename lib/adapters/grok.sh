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
  local -a run=(env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN)
  local v
  while IFS= read -r v; do run+=(-u "$v"); done < <(cred_env_strip_for grok)

  ( cd "$workdir" && "${run[@]}" grok "${args[@]}" ) \
    >"$out" 2>"$err" </dev/null
  rc=$?

  # Redaction before anything else reads the files — the backstop for whatever
  # a failing CLI echoes, since the harness itself holds no GitHub credential.
  if (( keep_transcript )); then log_redact_file "$out"; log_redact_file "$err"; fi

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
    if grep -qiE 'not signed in|XAI_API_KEY' "$err" 2>/dev/null \
       || [[ "$msg" == *"Not signed in"* || "$msg" == *"XAI_API_KEY"* ]]; then
      msg="Grok rejected the credential. CrossRev classifies this as a credential failure, not a generic harness error. ${msg}"
    fi
    jq -cn --arg e "$msg" \
      '{ok:false, payload:null, harness:"grok", endpoint:null, model_reported:null,
        tokens:null, error:$e}'
    (( keep_transcript )) || rm -f "$out" "$err"; return 1
  fi

  local payload model_reported tokens
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
  model_reported="$(jq -r '(.modelUsage // {}) | keys | .[0] // empty' "$out" 2>/dev/null)"
  tokens="$(jq -r '(.usage // {})
                   | .total_tokens
                     // ((.input_tokens // 0) + (.output_tokens // 0)
                         + (.cache_read_input_tokens // 0)
                         + (.cache_creation_input_tokens // 0))
                   | if . == 0 then "null" else tostring end' "$out" 2>/dev/null)" || tokens=null
  [[ -n "$tokens" ]] || tokens=null
  (( keep_transcript )) || rm -f "$out" "$err"

  jq -cn --argjson p "${payload:-null}" --arg m "$model_reported" --argjson t "$tokens" \
    '{ok:true, payload:$p, harness:"grok", endpoint:"vendor",
      model_reported:(if $m == "" then null else $m end), tokens:$t, error:null}'
}
