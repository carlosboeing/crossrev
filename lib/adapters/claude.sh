# shellcheck shell=bash
# lib/adapters/claude.sh
#
# Returns two things, not one: the payload, and execution metadata naming the
# harness, the resolved endpoint, the answering model where the harness reports
# one, and what the turn cost in tokens. Invoked with no GitHub credential in its
# environment, and with repository-provided harness customisation disabled.

# adapter_claude <prompt_file> <schema_file> <workdir> <model> <effort> <endpoint_name>
#
# Prints a JSON object: {payload, harness, endpoint, model_reported, tokens, ok, error}
adapter_claude() {
  local prompt_file="$1" schema_file="$2" workdir="$3"
  local model="${4:-}" effort="${5:-}" endpoint="${6:-}"

  command -v claude >/dev/null 2>&1 || ui_die \
    "the claude CLI is not installed, and this leg is configured to use it" \
    "Install it from https://claude.com/claude-code, or point this leg at another harness with --harness."

  local -a args=(-p --output-format json)

  # Claude Code takes the schema INLINE as a JSON string. Codex takes a file
  # path. Verified: handing Claude a path fails with a JSON parse error about
  # the leading slash, which reads like a corrupt schema rather than a wrong
  # argument type.
  [[ -n "$schema_file" ]] && args+=(--json-schema "$(cat "$schema_file")")

  # Model ids must be fully qualified. `--model sonnet-5` fails with "It may not
  # exist or you may not have access to it", which reads like an entitlement
  # problem rather than a typo.
  [[ -n "$model"  && "$model"  != "null" ]] && args+=(--model "$model")
  [[ -n "$effort" && "$effort" != "null" ]] && args+=(--effort "$effort")

  # One env invocation, built as a single array.
  #
  # GH_TOKEN and friends are stripped, not merely unset by convention — the agent
  # process must hold no GitHub credential even when the caller has one.
  #
  # Built this way rather than as a separate optional prefix because an empty
  # bash array expanded with a default yields one EMPTY word, not zero words:
  # `"${prefix[@]:-}" env … claude` runs the command named "" and fails with
  # "command not found" and an empty error string. That broke every invocation
  # with no endpoint configured, which is the default local case.
  local -a run=(env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN)
  # And every credential belonging to a harness that is not this one. The
  # workflow hands all of them to one process; only one of them is this leg's.
  local v
  while IFS= read -r v; do run+=(-u "$v"); done < <(cred_env_strip_for claude)
  local endpoint_label="vendor"
  if [[ -n "$endpoint" && "$endpoint" != "null" ]]; then
    local resolved url tok_env tok
    resolved="$(cfg_endpoint "$endpoint")" || return 1
    read -r url tok_env <<<"$resolved"
    tok="${!tok_env:-}"
    [[ -n "$tok" ]] || ui_die \
      "the endpoint '$endpoint' needs \$$tok_env, which is unset" \
      "Export it, or set it as a repository secret for CI. crossrev will not fall back to the vendor's own API."
    # Set inline on the invocation only. Never exported, never in a workflow
    # env: block. These variables are process-scoped, so a leg that leaks them
    # silently redirects the OTHER leg too — both legs run on one model, the
    # loop completes normally, and the cross-model property that justifies the
    # whole design is gone with no error anywhere.
    run+=("ANTHROPIC_BASE_URL=$url" "ANTHROPIC_AUTH_TOKEN=$tok")
    endpoint_label="$endpoint"
  fi
  run+=(claude "${args[@]}")

  local out err rc payload model_reported
  out="$(mktemp)"; err="$(mktemp)"

  # stdin from /dev/null: with a terminal attached the CLI waits for piped input.
  ( cd "$workdir" && "${run[@]}" "$(cat "$prompt_file")" ) >"$out" 2>"$err" </dev/null
  rc=$?

  if (( rc != 0 )) || [[ "$(jq -r '.is_error // false' "$out" 2>/dev/null)" == "true" ]]; then
    # Chosen on whether a message is there, not on jq's exit status. On an EMPTY
    # stdout jq exits 0 with no output, so a `jq … || head "$err"` fallback never
    # fires and the error becomes the empty string — exactly when stderr holds
    # the only diagnosis. Found by a reviewer in the agy adapter, which copied
    # this line.
    local msg; msg="$(jq -r '.result // empty' "$out" 2>/dev/null)"
    [[ -n "$msg" ]] || msg="$(head -c 400 "$err")"
    [[ -n "$msg" ]] || msg="claude exited $rc with no output on either stream"
    jq -cn --arg e "$msg" \
      '{ok:false, payload:null, harness:"claude", endpoint:null, model_reported:null,
        tokens:null, error:$e}'
    rm -f "$out" "$err"; return 1
  fi

  payload="$(jq -r '.result // empty' "$out")"
  # The harness's accounting of what served the turn, never the model's own
  # claim about itself — a substituted endpoint would get a self-report wrong in
  # precisely the case this exists to catch.
  model_reported="$(jq -r '(.modelUsage // {}) | keys | .[0] // empty' "$out")"

  # Every token the turn cost, summed across models and across the four ways
  # Claude Code counts them. Cache reads are included deliberately: they are
  # cheaper, not free, and a number that quietly omits them under-reports the
  # passes that reuse the most context.
  local tokens
  tokens="$(jq '[(.modelUsage // {}) | to_entries[] | .value
                 | (.inputTokens // 0) + (.outputTokens // 0)
                   + (.cacheReadInputTokens // 0) + (.cacheCreationInputTokens // 0)]
                | add // null' "$out" 2>/dev/null)" || tokens=null
  rm -f "$out" "$err"

  jq -cn --argjson p "$(jq -c . <<<"$payload" 2>/dev/null || echo null)" \
     --arg ep "$endpoint_label" --arg m "$model_reported" \
     --argjson t "${tokens:-null}" \
    '{ok:true, payload:$p, harness:"claude", endpoint:$ep,
      model_reported:(if $m == "" then null else $m end), tokens:$t, error:null}'
}
