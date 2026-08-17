# shellcheck shell=bash
# lib/adapters/codex.sh
#
# Same contract as the claude adapter: payload plus execution metadata, no
# GitHub credential in the environment.
#
# Codex reports no model at all. `codex exec --json` carries token counts on
# turn.completed and nothing identifying the model, so this adapter writes
# model_reported: null rather than echoing back the model it requested. Halting
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
    "Install it with: npm install -g @openai/codex, or point this leg at another harness with --harness."

  if [[ -n "$endpoint" && "$endpoint" != "null" ]]; then
    ui_die "the codex adapter cannot use the endpoint '$endpoint'" \
      "Named endpoints are Anthropic-compatible and reached through the claude adapter. Use harness: claude with endpoint: $endpoint, or drop the endpoint for this leg."
  fi

  local out_file err events rc
  out_file="$(mktemp)"; err="$(mktemp)"; events="$(mktemp)"

  # `--json` streams events on stdout while `-o` still writes the final payload to
  # a file, so this buys the token counts without changing where the payload comes
  # from. Codex carries them on turn.completed and nothing else does.
  local -a args=(exec --skip-git-repo-check --json -o "$out_file")

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
    jq -cn --arg e "$(legs_harness_error "$err")" \
      '{ok:false, payload:null, harness:"codex", endpoint:null, model_reported:null,
        tokens:null, error:$e}'
    rm -f "$out_file" "$err" "$events"; return 1
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
  # to report on.
  local payload tokens
  payload="$(jq -c . "$out_file" 2>/dev/null || echo null)"
  tokens="$(jq -s -r '
    [ .[] | select(.type == "turn.completed") | .usage // empty ] | last
    | if . == null then "null"
      else ((.input_tokens // 0) + (.output_tokens // 0) | tostring) end' \
    "$events" 2>/dev/null)" || tokens=null
  [[ -n "$tokens" ]] || tokens=null
  rm -f "$out_file" "$err" "$events"

  jq -cn --argjson p "$payload" --argjson t "$tokens" \
    '{ok:true, payload:$p, harness:"codex", endpoint:"vendor",
      model_reported:null, tokens:$t, error:null}'
}
