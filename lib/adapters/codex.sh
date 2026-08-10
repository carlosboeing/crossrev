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

# adapter_codex <prompt_file> <schema_file> <workdir> <model> <effort> <endpoint_name>
adapter_codex() {
  local prompt_file="$1" schema_file="$2" workdir="$3"
  local model="${4:-}" effort="${5:-}" endpoint="${6:-}"

  command -v codex >/dev/null 2>&1 || ui_die \
    "the codex CLI is not installed, and this leg is configured to use it" \
    "Install it with: npm install -g @openai/codex, or point this leg at another harness with --harness."

  if [[ -n "$endpoint" && "$endpoint" != "null" ]]; then
    ui_die "the codex adapter cannot use the endpoint '$endpoint'" \
      "Named endpoints are Anthropic-compatible and reached through the claude adapter. Use harness: claude with endpoint: $endpoint, or drop the endpoint for this leg."
  fi

  local out_file err rc
  out_file="$(mktemp)"; err="$(mktemp)"

  local -a args=(exec --skip-git-repo-check -o "$out_file")
  # Codex takes the schema as a FILE PATH, where Claude Code takes it inline.
  [[ -n "$schema_file" ]] && args+=(--output-schema "$schema_file")
  [[ -n "$model" && "$model" != "null" ]] && args+=(-m "$model")
  # No --effort flag; it is a config override.
  [[ -n "$effort" && "$effort" != "null" ]] && args+=(-c "model_reasoning_effort=$effort")

  # stdin from /dev/null is required, not defensive. Without it `codex exec`
  # blocks indefinitely on "Reading additional input from stdin..." and the leg
  # hangs with no output and no error.
  ( cd "$workdir" && env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN \
      codex "${args[@]}" "$(cat "$prompt_file")" ) >/dev/null 2>"$err" </dev/null
  rc=$?

  if (( rc != 0 )) || [[ ! -s "$out_file" ]]; then
    jq -cn --arg e "$(head -c 400 "$err")" \
      '{ok:false, payload:null, harness:"codex", endpoint:null, model_reported:null, error:$e}'
    rm -f "$out_file" "$err"; return 1
  fi

  local payload; payload="$(jq -c . "$out_file" 2>/dev/null || echo null)"
  rm -f "$out_file" "$err"

  jq -cn --argjson p "$payload" \
    '{ok:true, payload:$p, harness:"codex", endpoint:"vendor",
      model_reported:null, error:null}'
}
