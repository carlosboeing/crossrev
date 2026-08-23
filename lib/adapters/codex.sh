# shellcheck shell=bash
# lib/adapters/codex.sh
#
# Same contract as the claude adapter: payload plus execution metadata, no
# GitHub credential in the environment.
#
# The event stream reports no model. `codex exec --json` carries token counts
# on turn.completed and nothing identifying the model, so model_reported comes
# from the newest session rollout when one can be read, and stays null when it
# cannot — a miss never fails a leg whose answer already exists. Halting
# whenever the model is unreported would be the stricter rule and it is the
# wrong one — it would disqualify this adapter on the evidence that Codex does
# not emit the field. Layer one of the divergence guard already catches the
# failures reachable from a config mistake.

# adapter_codex <prompt_file> <schema_file> <workdir> <model> <effort> <endpoint_name> <write>
adapter_codex() {
  local prompt_file="$1" schema_file="$2" workdir="$3"
  local model="${4:-}" effort="${5:-}" endpoint="${6:-}" write="${7:-no}"

  command -v codex >/dev/null 2>&1 || ui_die \
    "the codex CLI is not installed, and this leg is configured to use it" \
    "Install it from $(harness_get codex .install.hint), or point this leg at another harness with --harness."

  if [[ -n "$endpoint" && "$endpoint" != "null" ]]; then
    ui_die "the codex adapter cannot use the endpoint '$endpoint'" \
      "Named endpoints are Anthropic-compatible and reached through the claude adapter. Use harness: claude with endpoint: $endpoint, or drop the endpoint for this leg."
  fi

  local out_file err events rc
  # When run_invoke names a transcript base the capture files ARE the record:
  # the event stream (codex's stdout) and stderr land in the run directory
  # alongside the payload file, are redacted in place, and their deletion is
  # the orchestrator's decision. Without one the adapter runs as it always
  # has — anonymous temp files, deleted on both paths.
  local keep_transcript=0
  if [[ -n "${CROSSREV_TRANSCRIPT_BASE:-}" ]]; then
    keep_transcript=1
    out_file="$CROSSREV_TRANSCRIPT_BASE.payload"
    err="$CROSSREV_TRANSCRIPT_BASE.stderr"
    events="$CROSSREV_TRANSCRIPT_BASE.stdout"
  else
    out_file="$(mktemp)"; err="$(mktemp)"; events="$(mktemp)"
  fi

  # `--json` streams events on stdout while `-o` still writes the final payload to
  # a file, so this buys the token counts without changing where the payload comes
  # from. Codex carries them on turn.completed and nothing else does.
  local -a args=(exec --skip-git-repo-check --json -o "$out_file")
  local sandbox_args
  if ! sandbox_args="$(sandbox_args_for codex)" || [[ -z "$sandbox_args" ]]; then
    ui_die "could not resolve hardening arguments for codex" \
      "sandbox_args_for in lib/sandbox.sh must return --ignore-user-config for codex. Refusing to run codex unhardened."
  fi
  args+=("$sandbox_args")

  # `codex exec` sandboxes to read-only by default, so a resolve leg on this
  # harness could verify a finding and then fail to apply the fix. workspace-write
  # confines writes to the checkout, which is exactly what the leg needs;
  # danger-full-access and --dangerously-bypass-approvals-and-sandbox are on the
  # wrong side of the line between editing files and running arbitrary commands.
  #
  # A reading leg is pinned read-only rather than left to the default, because
  # codex reads a user config that can set one. Saying it costs nothing and means
  # a machine-level setting cannot quietly hand the review leg a writable tree.
  if [[ "$write" == "yes" ]]; then
    args+=(--sandbox workspace-write)
  else
    args+=(--sandbox read-only)
  fi

  # Codex takes the schema as a FILE PATH, where Claude Code takes it inline.
  [[ -n "$schema_file" ]] && args+=(--output-schema "$schema_file")
  [[ -n "$model" && "$model" != "null" ]] && args+=(-m "$model")
  # No --effort flag; it is a config override.
  [[ -n "$effort" && "$effort" != "null" ]] && args+=(-c "model_reasoning_effort=$effort")

  # No GitHub credential, and no credential belonging to another harness. By this
  # point the codex credential lives in CODEX_HOME, so the raw copy the workflow
  # passed in is a second one nothing needs — and this is the process that reads
  # attacker-controlled text.
  local -a run=(env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN)
  local v
  while IFS= read -r v; do run+=(-u "$v"); done < <(cred_env_strip_for codex)

  # stdin from /dev/null is required, not defensive. Without it `codex exec`
  # blocks indefinitely on "Reading additional input from stdin..." and the leg
  # hangs with no output and no error.
  ( cd "$workdir" && "${run[@]}" codex "${args[@]}" "$(cat "$prompt_file")" ) \
    >"$events" 2>"$err" </dev/null
  rc=$?

  if (( rc != 0 )) || [[ ! -s "$out_file" ]]; then
    jq -cn --arg e "$(log_redact_str "$(legs_harness_error "$err")")" \
      '{ok:false, payload:null, harness:"codex", endpoint:null, model_reported:null,
        effort_reported:null, tokens:null, usage:null, error:$e}'
    # The capture files are the record, so they are filtered here — after every
    # value has been read from them, never before. Redacting first would rewrite
    # the payload this adapter parses, so identical harness output would yield
    # different answers depending on whether a run directory exists.
    if (( keep_transcript )); then
      log_redact_file "$out_file"; log_redact_file "$err"; log_redact_file "$events"
    else rm -f "$out_file" "$err" "$events"; fi
    return 1
  fi

  # The last turn.completed event, if one is there. Parsed leniently on purpose:
  # a missing count renders as a dash under a footnote, which is a better outcome
  # than an adapter that fails because a vendor renamed a field.
  #
  # `cached_input_tokens` is deliberately NOT added: it is the cached subset of
  # `input_tokens`, not a figure alongside it. Codex's own summary line reads
  # `total=T input=I (+ C cached) output=O`, where the cached count sits inside
  # the input figure it qualifies. Adding it would overstate every run that hits
  # the prompt cache, which is exactly the context-heavy pass this table exists
  # to report on. The parser keeps that rule and turns the rest into buckets:
  # fresh input is the subtraction, writes land unsplit, and no vendor total is
  # read.
  local payload usage tokens got model_reported effort_reported
  payload="$(jq -c . "$out_file" 2>/dev/null || echo null)"
  usage="$(usage_parse_codex_events "$events")"
  [[ -n "$usage" ]] || usage=null
  tokens="$(jq -r '.total // "null"' <<<"$usage")"

  # The event stream names neither model nor effort; the newest session rollout
  # carries both. Any failure here is a miss — null and null — never a failed
  # leg: the payload has already been read by the time this runs.
  model_reported=""; effort_reported=""
  got="$(usage_read_codex_rollout)" || got='{"model":null,"effort":null}'
  [[ -n "$got" ]] && {
    model_reported="$(jq -r '.model // empty' <<<"$got")"
    effort_reported="$(jq -r '.effort // empty' <<<"$got")"
  }

  # The capture files are the record, so they are filtered here — after every
  # value has been read from them, never before. Redacting first would rewrite
  # the payload this adapter parses, so identical harness output would yield
  # different answers depending on whether a run directory exists.
  if (( keep_transcript )); then
    log_redact_file "$out_file"; log_redact_file "$err"; log_redact_file "$events"
  else rm -f "$out_file" "$err" "$events"; fi

  jq -cn --argjson p "$payload" --argjson t "${tokens:-null}" \
     --argjson u "${usage:-null}" --arg m "$model_reported" --arg e "$effort_reported" \
    '{ok:true, payload:$p, harness:"codex", endpoint:"vendor",
      model_reported:(if $m == "" then null else $m end),
      effort_reported:(if $e == "" then null else $e end),
      tokens:(if $t == "null" then null else $t end),
      usage:$u, error:null}'
}
