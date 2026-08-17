# shellcheck shell=bash
# lib/adapters/agy.sh — Antigravity, the third harness.
#
# Same contract as the other two: payload plus execution metadata, and no GitHub
# credential in the environment.
#
# Two things about this CLI shape the adapter, and both were found by running it
# rather than by reading the help text.
#
# **`--print` takes the prompt as its value.** So every other flag has to come
# BEFORE it. Written the usual way — `agy --print --output-format json "..."` —
# the CLI treats the literal string "--output-format" as the prompt and answers a
# question about it, cheerfully, in prose. That failure costs a subscription call
# and looks like the model ignoring instructions.
#
# **It constrains its own output.** The amendment that asked for this adapter
# said there was no schema flag and to expect a fenced-JSON fallback. There is
# one: `--json-schema` takes a string or a path, and `--output-format json`
# returns the parsed object under `structured_output`. Verified against a
# two-field schema, which came back conforming. So this harness is schema-native
# alongside claude and codex, and the retry path stays dead code for it too.
#
# It reports no answering model — the JSON carries a conversation id, a status,
# usage counts and nothing identifying what served the turn — so model_reported
# is null, exactly as for codex. Layer one of the divergence guard covers it.

# adapter_agy <prompt_file> <schema_file> <workdir> <model> <effort> <endpoint_name> <write>
adapter_agy() {
  local prompt_file="$1" schema_file="$2" workdir="$3"
  local model="${4:-}" effort="${5:-}" endpoint="${6:-}" write="${7:-no}"

  command -v agy >/dev/null 2>&1 || ui_die \
    "the agy CLI is not installed, and this leg is configured to use it" \
    "Install Antigravity from https://antigravity.google, or point this leg at another harness with --harness."

  if [[ -n "$endpoint" && "$endpoint" != "null" ]]; then
    ui_die "the agy adapter cannot use the endpoint '$endpoint'" \
      "Named endpoints are Anthropic-compatible and reached through the claude adapter. Use harness: claude with endpoint: $endpoint, or drop the endpoint for this leg."
  fi

  # Order matters: everything before --print, and the prompt as its value.
  local -a args=(--output-format json --disable-slash-commands)

  # Same shape as the other two: the resolve leg has to change files and this
  # grants exactly that. `--mode` takes accept-edits or plan, and plan changes
  # what the model does rather than what it may touch, so a reading leg passes no
  # mode at all and the default denies the write.
  # --dangerously-skip-permissions is the blanket bypass and is never passed.
  [[ "$write" == "yes" ]] && args+=(--mode accept-edits)
  # Unlike Claude Code, this one takes the schema as a PATH.
  [[ -n "$schema_file" ]] && args+=(--json-schema "$schema_file")
  [[ -n "$model"  && "$model"  != "null" ]] && args+=(--model "$model")
  [[ -n "$effort" && "$effort" != "null" ]] && args+=(--effort "$effort")

  local out err rc
  out="$(mktemp)"; err="$(mktemp)"

  # No GitHub credential, and none belonging to another harness: this process
  # reads attacker-controlled text, and a credential it never receives is one no
  # injection can talk it into exfiltrating.
  local -a run=(env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN)
  local v
  while IFS= read -r v; do run+=(-u "$v"); done < <(cred_env_strip_for agy)

  ( cd "$workdir" && "${run[@]}" agy "${args[@]}" --print "$(cat "$prompt_file")" ) \
    >"$out" 2>"$err" </dev/null
  rc=$?

  local status; status="$(jq -r '.status // empty' "$out" 2>/dev/null)"
  if (( rc != 0 )) || [[ "$status" != "SUCCESS" ]]; then
    # The message is chosen on whether one is actually there, not on jq's exit
    # status. `jq … || head -c 400 "$err"` reads as a fallback and is not one: on
    # an EMPTY stdout jq exits 0 with no output, so the fallback never fires and
    # the error becomes the empty string — which is precisely the case where the
    # only diagnosis lives on stderr.
    local msg; msg="$(jq -r '.error // .response // empty' "$out" 2>/dev/null)"
    [[ -n "$msg" ]] || msg="$(head -c 400 "$err")"
    [[ -n "$msg" ]] || msg="agy exited $rc with no output on either stream"
    jq -cn --arg e "$msg" \
      '{ok:false, payload:null, harness:"agy", endpoint:null, model_reported:null,
        tokens:null, error:$e}'
    rm -f "$out" "$err"; return 1
  fi

  # structured_output is the parsed object when a schema was given. The response
  # string is the same JSON, and parsing it is the fallback for a run with no
  # schema rather than a second-guess of the first.
  local payload tokens
  payload="$(jq -c '.structured_output // (.response | fromjson? // null)' "$out" 2>/dev/null || echo null)"
  # It reports no answering model and does report usage, so this is the one number
  # it can contribute to the run-details table.
  tokens="$(jq -r '(.usage // {})
                   | (.total_tokens // ((.input_tokens // 0) + (.output_tokens // 0)))
                   | if . == 0 then "null" else tostring end' "$out" 2>/dev/null)" || tokens=null
  [[ -n "$tokens" ]] || tokens=null
  rm -f "$out" "$err"

  jq -cn --argjson p "${payload:-null}" --argjson t "$tokens" \
    '{ok:true, payload:$p, harness:"agy", endpoint:"vendor",
      model_reported:null, tokens:$t, error:null}'
}
