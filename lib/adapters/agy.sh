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

# adapter_agy <prompt_file> <schema_file> <workdir> <model> <effort> <endpoint_name>
adapter_agy() {
  local prompt_file="$1" schema_file="$2" workdir="$3"
  local model="${4:-}" effort="${5:-}" endpoint="${6:-}"

  command -v agy >/dev/null 2>&1 || ui_die \
    "the agy CLI is not installed, and this leg is configured to use it" \
    "Install Antigravity from https://antigravity.google, or point this leg at another harness with --harness."

  if [[ -n "$endpoint" && "$endpoint" != "null" ]]; then
    ui_die "the agy adapter cannot use the endpoint '$endpoint'" \
      "Named endpoints are Anthropic-compatible and reached through the claude adapter. Use harness: claude with endpoint: $endpoint, or drop the endpoint for this leg."
  fi

  # Order matters: everything before --print, and the prompt as its value.
  local -a args=(--output-format json --disable-slash-commands)
  # Unlike Claude Code, this one takes the schema as a PATH.
  [[ -n "$schema_file" ]] && args+=(--json-schema "$schema_file")
  [[ -n "$model"  && "$model"  != "null" ]] && args+=(--model "$model")
  [[ -n "$effort" && "$effort" != "null" ]] && args+=(--effort "$effort")

  local out err rc
  out="$(mktemp)"; err="$(mktemp)"

  ( cd "$workdir" && env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN \
      agy "${args[@]}" --print "$(cat "$prompt_file")" ) >"$out" 2>"$err" </dev/null
  rc=$?

  local status; status="$(jq -r '.status // empty' "$out" 2>/dev/null)"
  if (( rc != 0 )) || [[ "$status" != "SUCCESS" ]]; then
    jq -cn --arg e "$(jq -r '.error // .response // empty' "$out" 2>/dev/null || head -c 400 "$err")" \
      '{ok:false, payload:null, harness:"agy", endpoint:null, model_reported:null, error:$e}'
    rm -f "$out" "$err"; return 1
  fi

  # structured_output is the parsed object when a schema was given. The response
  # string is the same JSON, and parsing it is the fallback for a run with no
  # schema rather than a second-guess of the first.
  local payload
  payload="$(jq -c '.structured_output // (.response | fromjson? // null)' "$out" 2>/dev/null || echo null)"
  rm -f "$out" "$err"

  jq -cn --argjson p "${payload:-null}" \
    '{ok:true, payload:$p, harness:"agy", endpoint:"vendor",
      model_reported:null, error:null}'
}
