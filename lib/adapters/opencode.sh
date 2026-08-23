# shellcheck shell=bash
# lib/adapters/opencode.sh — opencode, the fifth harness, and the first that
# does not constrain its own output.
#
# Same contract as the other four: payload plus execution metadata, and no
# GitHub credential in the environment.
#
# **There is no schema flag.** `run --format json` streams NDJSON events —
# step_start, tool_use, text, step_finish, and error on a failure — and the
# answer rides on `.part.text` of the text events. Nothing constrains that text
# to a schema, so this is the one harness where `schema_native: false` means
# what it says: the schema travels inside the prompt, and the extra attempt
# granted at lib/run.sh finally has a caller. The ladder below survives a fence
# and surrounding prose because models drift, not because any of this is
# enforced.
#
# **The permission defaults are inverted.** Most permissions default to allow,
# so unlike every other harness, an opencode leg holds `edit` and `bash`
# unless something takes them away. The isolation config below is that
# something, in one of two shapes chosen by the write flag. A reading leg
# keeps the `"*": "deny"` base rule with edit denied under it. A writing leg
# drops the base rule and grants edit outright, because measured against
# 1.18.21 the base rule suppresses edit in every form — shorthand or map — so
# no grant can survive beside it. The standing denials (bash, task, skill,
# webfetch, websearch, external_directory) hold in both shapes. The trade the
# write shape makes is recorded rather than hidden: without the base rule it
# does not fail closed on a permission key a future opencode adds, which the
# read shape catches by default. That is the price of a grant the base rule
# makes unreachable, and bash staying denied is what keeps the exposure small
# — files inside the worktree, not commands. A denied tool is absent from the
# session rather than refused at call time, and nothing prompts for approval,
# so there is nothing for a headless run to block on. OPENCODE_CONFIG merges
# on top of the operator's own global config and wins on these keys —
# measured twice, against a global `edit: allow` and again against
# ~/.opencode/opencode.json, which loads later.
#
# **stdin must be /dev/null.** With stdin held open the CLI blocks on it and
# prints nothing — five minutes of silence was measured, with zero bytes on
# either stream — which in CI means hanging until the workflow timeout with no
# diagnosis at all.
#
# The answering model and a whole-run token total come from
# `opencode export <sessionID>`, which reads the local session database and
# costs no model call. That makes this the third harness able to report which
# model answered, alongside claude and grok.

# Concatenated answer text in, extracted JSON out. Four rungs, stopping at the
# first that parses: the text as-is, a stripped markdown fence, the span from
# the first { to the last }, then nothing. Prints compact JSON, or nothing —
# nothing is the adapter's signal to hand a null payload to the orchestrator's
# shape check rather than fail here, so prose that merely forgot the braces
# earns the retry a schema-less harness is budgeted.
_opencode_extract_json() {
  jq -Rcs '
    def rung: try fromjson catch empty;
    def unfence:
      sub("^```[a-zA-Z0-9_-]*[ \\t]*\\r?\\n"; "")
      | sub("\\r?\\n[ \\t]*```[ \\t]*$"; "");
    def spanned:
      (index("{")) as $i | (rindex("}")) as $j
      | select($i != null and $j != null and $j >= $i)
      | .[$i : $j + 1];
    (rung // (unfence | rung) // (spanned | rung))
  ' 2>/dev/null
}

# adapter_opencode <prompt_file> <schema_file> <workdir> <model> <effort> <endpoint_name> <write>
adapter_opencode() {
  local prompt_file="$1" schema_file="$2" workdir="$3"
  local model="${4:-}" effort="${5:-}" endpoint="${6:-}" write="${7:-no}"

  command -v opencode >/dev/null 2>&1 || ui_die \
    "the opencode CLI is not installed, and this leg is configured to use it" \
    "Install it with: npm install -g opencode-ai, or point this leg at another harness with --harness."

  if [[ -n "$endpoint" && "$endpoint" != "null" ]]; then
    ui_die "the opencode adapter cannot use the endpoint '$endpoint'" \
      "Named endpoints are Anthropic-compatible and reached through the claude adapter. opencode has its own provider layer, so an endpoint name means nothing to it. Use harness: claude with endpoint: $endpoint, or drop the endpoint for this leg."
  fi

  # The schema travels inside the prompt: a copy of the prompt with the schema
  # in a fenced block under an instruction that also corrects the skill's
  # "the harness constrains your output" claim — true for the other four,
  # false here. This keeps lib/prompt.sh unaware of which harness will read
  # what it built — the same class of per-CLI fact as Antigravity's flag
  # order or Codex's schema path.
  local leg_prompt="$prompt_file" prompt_copy=""
  if [[ -n "$schema_file" ]]; then
    prompt_copy="$(mktemp)"
    {
      cat "$prompt_file"
      printf '\n\nThis harness does not constrain your output. The answer text itself is what is parsed, so return a single JSON object matching exactly this schema, with no markdown fence and no commentary:\n\n```json\n%s\n```\n' "$(cat "$schema_file")"
    } >"$prompt_copy"
    leg_prompt="$prompt_copy"
  fi

  # Isolation, layered over whatever the operator already has, in one of the
  # two shapes the header describes: the write flag chooses between the
  # fail-closed read shape and the edit-granting resolve shape. question and
  # doom_loop are named denials in both shapes rather than casualties of "*":
  # the write shape has no "*" to hide them under, and doom_loop otherwise
  # falls back to ask — a prompt a headless leg cannot answer. task is denied
  # for predictability (the model spawns a subagent unprompted, which
  # multiplies token spend without being asked for) and skill because it is
  # the door to the operator's own skill library. read stays a map in both:
  # the string "allow" would replace opencode's own *.env deny and let the
  # model quote an untracked .env into a public comment. OPENCODE_CONFIG_DIR
  # at an empty directory removes the agents, commands and plugins that would
  # otherwise load from beside the operator's global config.
  local iso cfg_dir star_rule="true" edit_perm="deny"
  if [[ "$write" == "yes" ]]; then
    star_rule="false"
    edit_perm="allow"
  fi
  iso="$(mktemp -d)"
  cfg_dir="$iso/config-home"
  mkdir "$cfg_dir"
  jq -n --arg edit "$edit_perm" --argjson star "$star_rule" '
    {
      "$schema": "https://opencode.ai/config.json",
      "permission": ((if $star then {"*": "deny"} else {} end)
        + {
            "read": {
              "*": "allow",
              "*.env": "deny",
              "*.env.*": "deny",
              "*.env.example": "allow"
            },
            "glob": "allow",
            "grep": "allow",
            "list": "allow",
            "lsp": "allow",
            "todowrite": "allow",
            "edit": $edit,
            "bash": "deny",
            "task": "deny",
            "skill": "deny",
            "webfetch": "deny",
            "websearch": "deny",
            "external_directory": "deny",
            "question": "deny",
            "doom_loop": "deny"
          })
    }' >"$iso/config.json"

  local -a args=(run --format json --dir "$workdir")
  [[ -n "$model"  && "$model"  != "null" ]] && args+=(--model "$model")
  [[ -n "$effort" && "$effort" != "null" ]] && args+=(--variant "$effort")

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

  # No GitHub credential, none belonging to another harness, and the isolation
  # config riding along on the same invocation. The unset flags come before
  # every VAR=value: env stops parsing options at the first assignment, so a
  # -u written after one becomes the command it tries to run.
  local -a run=(env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN)
  local v
  while IFS= read -r v; do run+=(-u "$v"); done < <(cred_env_strip_for opencode)
  run+=("OPENCODE_CONFIG=$iso/config.json" "OPENCODE_CONFIG_DIR=$cfg_dir")

  # stdin from /dev/null is required, not defensive: see the header.
  ( cd "$workdir" && "${run[@]}" opencode "${args[@]}" "$(cat "$leg_prompt")" ) \
    >"$out" 2>"$err" </dev/null
  rc=$?
  rm -f "$prompt_copy"
  # $iso stays until after export: ${run[@]} names OPENCODE_CONFIG and
  # OPENCODE_CONFIG_DIR under it, and export is the same invocation shape.

  # An authentication rejection has a shape of its own: an error event on
  # stdout, AI_APICallError naming Unauthorized behind it on stderr. Naming it
  # matters more than usual here, because opencode falls through to a DIFFERENT
  # provider when the configured one cannot authenticate — measured — so "the
  # harness failed" sends the reader looking in the wrong place entirely. A
  # bare error event is not that shape — rate limits, overloads, tool failures
  # — and falls through to the generic harness-error branch below.
  local auth_rejected=0
  grep -q 'AI_APICallError' "$err" 2>/dev/null \
    && grep -Eqi 'Unauthorized|(^|[^0-9])401([^0-9]|$)' "$err" 2>/dev/null && auth_rejected=1

  if (( auth_rejected )); then
    local auth_msg="opencode rejected its credential. CrossRev classifies this as a credential failure, not a generic harness error."
    local detail; detail="$(legs_harness_error "$err")"
    [[ -n "$detail" ]] && auth_msg="$auth_msg $detail"
    auth_msg="$(log_redact_str "$auth_msg")"
    jq -cn --arg e "$auth_msg" \
      '{ok:false, payload:null, harness:"opencode", endpoint:null, model_reported:null,
        tokens:null, error:$e}'
    rm -rf "$iso"
    if (( keep_transcript )); then log_redact_file "$out"; log_redact_file "$err"
    else rm -f "$out" "$err"; fi
    return 1
  fi

  if (( rc != 0 )); then
    # The message is chosen on whether one is actually there, not on jq's exit
    # status — on EMPTY stdout the fallback would otherwise be the empty
    # string, precisely when stderr holds the only diagnosis.
    local msg; msg="$(legs_harness_error "$err")"
    [[ -n "$msg" ]] || msg="opencode exited $rc with no output on either stream"
    msg="$(log_redact_str "$msg")"
    jq -cn --arg e "$msg" \
      '{ok:false, payload:null, harness:"opencode", endpoint:null, model_reported:null,
        tokens:null, error:$e}'
    rm -rf "$iso"
    if (( keep_transcript )); then log_redact_file "$out"; log_redact_file "$err"
    else rm -f "$out" "$err"; fi
    return 1
  fi

  # No text event at all is a different fault from a malformed answer: the run
  # finished and said nothing, and the diagnosis should say so rather than
  # dressing it up as a schema mismatch. Join-output, not raw-output: -r
  # terminates every event with a newline, and a seam inside a JSON string is
  # then an unescaped control character that fails every extraction rung.
  local text
  text="$(jq -Rj 'fromjson? | select(.type == "text") | .part.text // empty' "$out" 2>/dev/null)"
  if [[ -z "$text" ]]; then
    local empty_msg="opencode produced no answer: the run finished without a single text event."
    jq -cn --arg e "$empty_msg" \
      '{ok:false, payload:null, harness:"opencode", endpoint:null, model_reported:null,
        tokens:null, error:$e}'
    rm -rf "$iso"
    if (( keep_transcript )); then log_redact_file "$out"; log_redact_file "$err"
    else rm -f "$out" "$err"; fi
    return 1
  fi

  # Extraction can legitimately miss — prose with no braces anywhere — and
  # that is a handoff, not a failure: payload null lets run_invoke spend the
  # extra attempt a non-schema-native harness is granted before reporting.
  local payload
  payload="$(_opencode_extract_json <<<"$text")"
  [[ -n "$payload" ]] || payload="null"

  # One export call supplies both the answering model and the whole-run token
  # total, summed the way the CLI sums its own steps: input + output +
  # reasoning + cache.read. Telemetry, not the answer — if the export fails,
  # both fall back to null and the review stands.
  local sid exported model_reported="" tokens="null"
  sid="$(jq -Rr 'fromjson? | .sessionID // empty' "$out" 2>/dev/null | head -n 1)"
  if [[ -n "$sid" ]]; then
    exported="$("${run[@]}" opencode export "$sid" </dev/null 2>/dev/null)" || exported=""
    if [[ -n "$exported" ]]; then
      model_reported="$(jq -r '.info.model.id // empty' <<<"$exported" 2>/dev/null)"
      tokens="$(jq -r '((.info.tokens // {})
                        | ((.input // 0) + (.output // 0) + (.reasoning // 0) + (.cache.read // 0)))
                       | if . == 0 then "null" else tostring end' <<<"$exported" 2>/dev/null)" || tokens=null
      [[ -n "$tokens" ]] || tokens=null
    fi
  fi
  rm -rf "$iso"

  if (( keep_transcript )); then log_redact_file "$out"; log_redact_file "$err"
  else rm -f "$out" "$err"; fi

  jq -cn --argjson p "$payload" --arg m "$model_reported" --argjson t "$tokens" \
    '{ok:true, payload:$p, harness:"opencode", endpoint:"vendor",
      model_reported:(if $m == "" then null else $m end), tokens:$t, error:null}'
}
