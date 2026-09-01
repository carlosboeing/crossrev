# shellcheck shell=bash
# lib/adapters/claude.sh
#
# Returns two things, not one: the payload, and execution metadata naming the
# harness, the resolved endpoint, the answering model where the harness reports
# one, a normalized usage record of token buckets (lib/usage.sh), and what the
# turn cost in tokens. Invoked with no GitHub credential in its environment,
# and with repository-provided harness customisation disabled.

# adapter_claude <prompt_file> <schema_file> <workdir> <model> <effort> <endpoint_name> <write>
#
# `write` is yes or no, derived from the leg rather than configured. Prints a JSON
# object: {payload, harness, endpoint, model_reported, effort_reported, tokens,
# usage, ok, error}
adapter_claude() {
  local prompt_file="$1" schema_file="$2" workdir="$3"
  local model="${4:-}" effort="${5:-}" endpoint="${6:-}" write="${7:-no}"

  command -v claude >/dev/null 2>&1 || ui_die \
    "the claude CLI is not installed, and this leg is configured to use it" \
    "Install it from https://claude.com/claude-code, or point this leg at another harness with --harness."

  local -a args=(-p --output-format json)

  # A resolve leg has to change files, and headless Claude Code denies a write
  # tool unless something grants it. Locally that something is the operator's own
  # ~/.claude/settings.json; a runner is a fresh container with no such file, so
  # the leg verified findings, worked out the fix and then could not apply it.
  #
  # It has to be this flag rather than a settings file: lib/sandbox.sh quarantines
  # every path a harness auto-loads configuration from, so a settings file written
  # into the workspace would be moved out of the way before claude started — and a
  # grant that survived the quarantine would be the hole the quarantine exists to
  # close.
  #
  # acceptEdits, not bypassPermissions: the line worth holding is between editing
  # files and running arbitrary commands, and the resolve leg only needs the first.
  #
  # A reading leg passes no mode at all. There is no permission mode meaning
  # "deny" — plan mode changes what the model does rather than what it may touch —
  # and the headless default already denies the write, which is the behaviour that
  # exposed this in the first place.
  [[ "$write" == "yes" ]] && args+=(--permission-mode acceptEdits)

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
  # Four names, not three. `gh` reads GH_ENTERPRISE_TOKEN and, when that is
  # unset, GITHUB_ENTERPRISE_TOKEN — `gh help environment` lists both, "in order
  # of precedence". A strip list naming only the first left the second in the
  # agent's environment on a GitHub Enterprise Server setup, which is the one
  # kind of installation where it is the credential in use.
  local -a run=(env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN -u GITHUB_ENTERPRISE_TOKEN)
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
      "Export it, or set it as a repository secret for CI. CrossRev will not fall back to the vendor's own API."
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
  # When run_invoke names a transcript base the capture files ARE the record:
  # they live in the run directory, are redacted in place, and their deletion
  # is the orchestrator's decision rather than this adapter's. Without one the
  # adapter runs exactly as it always has — anonymous temp files, deleted on
  # both paths.
  local keep_transcript=0
  if [[ -n "${CROSSREV_TRANSCRIPT_BASE:-}" ]]; then
    keep_transcript=1
    out="$CROSSREV_TRANSCRIPT_BASE.stdout"; err="$CROSSREV_TRANSCRIPT_BASE.stderr"
  else
    out="$(mktemp)"; err="$(mktemp)"
  fi

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
    [[ -n "$msg" ]] || msg="$(legs_harness_error "$err")"
    [[ -n "$msg" ]] || msg="claude exited $rc with no output on either stream"
    msg="$(log_redact_str "$msg")"
    jq -cn --arg e "$msg" \
      '{ok:false, payload:null, harness:"claude", endpoint:null, model_reported:null,
        effort_reported:null, tokens:null, usage:null, error:$e}'
    # The capture files are the record, so they are filtered here — after every
    # value has been read from them, never before. Redacting first would rewrite
    # the payload this adapter parses, so identical harness output would yield
    # different answers depending on whether a run directory exists.
    if (( keep_transcript )); then log_redact_file "$out"; log_redact_file "$err"
    else rm -f "$out" "$err"; fi
    return 1
  fi

  payload="$(jq -r '.result // empty' "$out")"

  # Buckets, not one number: the normalized usage record comes from four sums
  # across modelUsage plus the write-TTL split and thinking count that only
  # top-level .usage carries. The answering model is the canonicalModel of the
  # key holding the largest token share — `keys | .[0]` sorts lexically, and a
  # session where Haiku helped an Opus run would otherwise name Haiku.
  local usage model_reported tokens
  usage="$(usage_parse_claude "$out")"
  [[ -n "$usage" ]] || usage=null
  model_reported="$(jq -r '.models[0].id // empty' <<<"$usage")"
  tokens="$(jq -r '.total // "null"' <<<"$usage")"

  # The capture files are the record, so they are filtered here — after every
  # value has been read from them, never before. Redacting first would rewrite
  # the payload this adapter parses, so identical harness output would yield
  # different answers depending on whether a run directory exists.
  if (( keep_transcript )); then log_redact_file "$out"; log_redact_file "$err"
  else rm -f "$out" "$err"; fi

  jq -cn --argjson p "$(jq -c . <<<"$payload" 2>/dev/null || echo null)" \
     --arg ep "$endpoint_label" --arg m "$model_reported" \
     --argjson t "${tokens:-null}" --argjson u "${usage:-null}" \
    '{ok:true, payload:$p, harness:"claude", endpoint:$ep,
      model_reported:(if $m == "" then null else $m end),
      effort_reported:null,
      tokens:(if $t == "null" then null else $t end),
      usage:$u, error:null}'
}
