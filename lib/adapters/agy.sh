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

  # Antigravity does not take the shell's working directory as its workspace. It
  # keeps its own project root, and without one it resolves against $HOME: a
  # relative `read_file app.ts` was refused as `read_file(/Users/<name>)`, and a
  # shell command ran from ~/.gemini/antigravity-cli/scratch rather than the
  # checkout. So the leg could not see the code it was sent to work on, and the
  # model reached for `pwd` and `git status` to find out where it was — which the
  # permission layer then denied, because those were outside the workspace too.
  #
  # That reads as "this harness cannot resolve without a shell grant", and it is
  # not. With the workspace named, `--mode accept-edits` alone edits the file on
  # the first turn. The fix is telling it where the work is, not widening what it
  # may do.
  args+=(--add-dir "$workdir")

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
  while IFS= read -r v; do run+=(-u "$v"); done < <(cred_env_strip_for agy)

  ( cd "$workdir" && "${run[@]}" agy "${args[@]}" --print "$(cat "$prompt_file")" ) \
    >"$out" 2>"$err" </dev/null
  rc=$?

  local status; status="$(jq -r '.status // empty' "$out" 2>/dev/null)"
  if (( rc != 0 )) || [[ "$status" != "SUCCESS" ]]; then
    # The message is chosen on whether one is actually there, not on jq's exit
    # status. `jq … || legs_harness_error "$err"` reads as a fallback and is not
    # one: on an EMPTY stdout jq exits 0 with no output, so the fallback never
    # fires and the error becomes the empty string — which is precisely the case
    # where the only diagnosis lives on stderr.
    local msg; msg="$(jq -r '.error // .response // empty' "$out" 2>/dev/null)"
    [[ -n "$msg" ]] || msg="$(legs_harness_error "$err")"
    [[ -n "$msg" ]] || msg="agy exited $rc with no output on either stream"
    msg="$(log_redact_str "$msg")"
    jq -cn --arg e "$msg" \
      '{ok:false, payload:null, harness:"agy", endpoint:null, model_reported:null,
        effort_reported:null, tokens:null, usage:null, error:$e}'
    # The capture files are the record, so they are filtered here — after every
    # value has been read from them, never before. Redacting first would rewrite
    # the payload this adapter parses, so identical harness output would yield
    # different answers depending on whether a run directory exists.
    if (( keep_transcript )); then log_redact_file "$out"; log_redact_file "$err"
    else rm -f "$out" "$err"; fi
    return 1
  fi

  # structured_output is the parsed object when a schema was given. The response
  # string is the same JSON, and parsing it is the fallback for a run with no
  # schema rather than a second-guess of the first.
  local payload usage tokens
  payload="$(jq -c '.structured_output // (.response | fromjson? // null)' "$out" 2>/dev/null || echo null)"
  # Buckets summed from the parts, cache reads included. The vendor's own
  # total_tokens excludes cache reads — on the measured run it reported 48,162
  # of the 133,830 the parts sum to, dropping 64 per cent of the work the leg
  # did — so no vendor total is read at all.
  usage="$(usage_parse_agy "$out")"
  [[ -n "$usage" ]] || usage=null
  tokens="$(jq -r '.total // "null"' <<<"$usage")"
  # The capture files are the record, so they are filtered here — after every
  # value has been read from them, never before. Redacting first would rewrite
  # the payload this adapter parses, so identical harness output would yield
  # different answers depending on whether a run directory exists.
  if (( keep_transcript )); then log_redact_file "$out"; log_redact_file "$err"
  else rm -f "$out" "$err"; fi

  jq -cn --argjson p "${payload:-null}" --argjson t "${tokens:-null}" \
     --argjson u "${usage:-null}" \
    '{ok:true, payload:$p, harness:"agy", endpoint:"vendor",
      model_reported:null,
      effort_reported:null,
      tokens:(if $t == "null" then null else $t end),
      usage:$u, error:null}'
}
