#!/usr/bin/env bash
#
# Write permission, scoped to the leg that needs it.
#
# The resolve leg's whole job is to change files, and until it was granted
# permission it could verify a finding, work out the correct fix and then fail to
# apply it. That is what the first real automated run did. Locally it had always
# worked, because headless Claude Code reads the operator's own settings and
# those already permit Edit and Write; a hosted runner is a fresh container with
# no such file, so the identical command was denied.
#
# The grant has to be a CLI flag rather than a settings file. lib/sandbox.sh
# quarantines every path a harness auto-loads configuration from — settings,
# instructions, hooks, MCP definitions, agents — because a pull request branch
# otherwise configures the thing reviewing it. A settings file written into the
# workspace to grant permission would be moved out of the way before the harness
# started, and any mechanism that survived the quarantine would have reopened the
# hole the quarantine exists to close. opencode is the exception in mechanism,
# not in spirit: its grant travels as OPENCODE_CONFIG pointing at a file the
# adapter writes OUTSIDE the workspace, so there is nothing for a branch to
# move and nothing auto-loaded from the checkout to win on precedence —
# measured twice against permissive global configs.
#
# The review leg must NOT carry the grant. It has no reason to write, and write
# access widens the blast radius of a prompt injection carried in a diff for no
# benefit at all. That absence is a security property, so it is asserted here
# rather than assumed.
#
# The adapters are enumerated off lib/adapters/ rather than listed, for the same
# reason tests/test-action.sh reads its flags off action.yml: a fourth adapter
# added without the capability is exactly the change that brings this back.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/ui.sh
source "$HERE/../lib/ui.sh"
# shellcheck source=../lib/harnesses.sh
source "$HERE/../lib/harnesses.sh"
# shellcheck source=../lib/credentials.sh
source "$HERE/../lib/credentials.sh"
# shellcheck source=../lib/sandbox.sh
source "$HERE/../lib/sandbox.sh"
# An adapter's no-output path reads the useful end of stderr through
# legs_harness_error, and the recorder below drives codex and agy straight down
# it. bin/crossrev sources lib/legs.sh ahead of the adapters for that reason
# (line 46, against 58 to 62). Without the same line here the probes run that
# path against an undefined function: probe() discards the command-not-found and
# the argv log the assertions read is unchanged, so the suite passes over it.
# shellcheck source=../lib/legs.sh
source "$HERE/../lib/legs.sh"
for _a in "$HERE"/../lib/adapters/*.sh; do
  # shellcheck source=/dev/null
  source "$_a"
done
# Sourced last so its assertion helpers and counters are the ones in force.
# shellcheck source=harness.sh
source "$HERE/harness.sh"

# ---------------------------------------------------------------------------
# A recorder in place of every harness CLI
# ---------------------------------------------------------------------------
#
# It logs the argument list and exits. Each adapter then takes its own error
# path, which is fine and deliberate: this suite reads the log, never the
# envelope, so one recorder covers a harness whose output shape it knows nothing
# about — including the next one somebody adds.
FAKE="$(mktemp -d)"
ARGV_PROBE="$(mktemp)"
PROMPT_FILE="$(mktemp)"; printf 'prompt\n' >"$PROMPT_FILE"

cat >"$FAKE/_record" <<'REC'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$CROSSREV_ARGV_LOG"
# One harness carries its grant in a config file named by the environment
# rather than in a flag. Summarising the permission block onto the same log
# keeps the class assertion below honest for it: two legs that differ only in
# what they may touch must log differently, wherever the grant rides.
if [[ -n "${OPENCODE_CONFIG:-}" ]]; then
  printf 'permission ' >>"$CROSSREV_ARGV_LOG"
  jq -r '.permission | to_entries | map(.key + "=" + (.value | tostring)) | join(" ")' \
    "$OPENCODE_CONFIG" >>"$CROSSREV_ARGV_LOG"
fi
exit 0
REC
chmod +x "$FAKE/_record"

HARNESSES=""
for _a in "$HERE"/../lib/adapters/*.sh; do
  _h="$(basename "$_a" .sh)"
  HARNESSES="$HARNESSES $_h"
  cp "$FAKE/_record" "$FAKE/$_h"
done

# The enumeration is derived, so one that quietly returned nothing would make
# every assertion below vacuous.
has "the adapters are enumerated off lib/adapters/" "$HARNESSES" "claude"
has "and all three are there"                       "$HARNESSES" "codex"
has "including the third harness"                   "$HARNESSES" "agy"

# probe <harness> <write> -> the argument list that harness CLI was invoked with
#
# Every token holding a path is flattened to the word PATH, and that is what
# makes the comparison below mean anything: the codex adapter passes `-o` a fresh
# mktemp file, so two raw argument lists differ on every invocation whatever the
# adapter did with the capability, and the check would have passed against the
# unfixed code. No flag or flag value in play here contains a slash.
probe() {
  local h="$1" write="$2"
  : >"$ARGV_PROBE"
  ( export CROSSREV_ARGV_LOG="$ARGV_PROBE"
    PATH="$FAKE:$PATH"
    "adapter_$h" "$PROMPT_FILE" "" "$PWD" "" "" "" "$write" ) >/dev/null 2>&1
  sed -E 's#[^ ]*/[^ ]*#PATH#g' "$ARGV_PROBE"
}

differs() { [[ "$2" != "$3" ]] && ok "$1" || notok "$1" "two different invocations" "$2"; }

# ---------------------------------------------------------------------------
# The class: every adapter distinguishes a writing leg from a reading one
# ---------------------------------------------------------------------------
for _h in $HARNESSES; do
  writing="$(probe "$_h" yes)"
  reading="$(probe "$_h" no)"

  differs "the $_h adapter invokes a writing leg differently from a reading one" \
    "$writing" "$reading"

  # The line worth holding is between editing files and running arbitrary
  # commands. Every harness offers a mode on the wrong side of it, and none of
  # them is what a resolve leg needs.
  hasnt "the $_h write grant stops short of Claude Code's blanket bypass" \
    "$writing" "bypassPermissions"
  hasnt "the $_h write grant stops short of an unsandboxed codex" \
    "$writing" "danger-full-access"
  hasnt "and nothing prefixed --dangerously reaches the $_h CLI" \
    "$writing" "--dangerously"
done

# ---------------------------------------------------------------------------
# The flags themselves, per harness
# ---------------------------------------------------------------------------
#
# Checked against the installed CLIs rather than their documentation. claude
# takes `--permission-mode` from acceptEdits, auto, bypassPermissions, manual,
# dontAsk and plan; codex takes `--sandbox` from read-only, workspace-write and
# danger-full-access; agy takes `--mode` from accept-edits and plan. The narrow
# one in each case.
has  "a writing claude leg accepts edits"        "$(probe claude yes)" "--permission-mode acceptEdits"
# There is no claude permission mode that means "deny": plan mode changes what
# the model does rather than what it may touch. So a reading leg passes no mode
# at all and headless Claude Code's own default denies the write — which is
# precisely the behaviour that exposed this bug, working as intended.
hasnt "a reading claude leg asks for no permission mode at all" "$(probe claude no)" "--permission-mode"

has  "a writing codex leg gets a workspace-writable sandbox" "$(probe codex yes)" "--sandbox workspace-write"
# Explicit rather than left to the default, because codex reads a user config
# that can set one. read-only is already the default; saying so costs nothing and
# means a machine-level setting cannot quietly hand the review leg a writable
# tree.
has  "a reading codex leg is pinned read-only"   "$(probe codex no)" "--sandbox read-only"
has  "a reading codex leg ignores user config"   "$(probe codex no)" "--ignore-user-config"
has  "a writing codex leg ignores user config"   "$(probe codex yes)" "--ignore-user-config"
hasnt "a reading claude leg ignores no user config" "$(probe claude no)" "--ignore-user-config"
hasnt "a reading agy leg ignores no user config"    "$(probe agy no)" "--ignore-user-config"

# An unresolvable codex hardening argument halts loudly rather than running unhardened.
err_missing="$(
  (
    PATH="$FAKE:$PATH"
    sandbox_args_for() { return 1; }
    adapter_codex "$PROMPT_FILE" "" "$PWD" "" "" "" "no"
  ) 2>&1 || true
)"
has "a missing codex hardening argument fails loudly" \
  "$err_missing" "could not resolve hardening arguments for codex"

has  "a writing agy leg accepts edits"           "$(probe agy yes)" "--mode accept-edits"
hasnt "a reading agy leg asks for no mode at all" "$(probe agy no)" "--mode"

# opencode's grant is the config shape, not a flag, and every shape keeps the
# fail-closed base rule — it is what denies tools no key names. The write
# shape flips edit beside it; the read shape denies edit under it.
has  "a writing opencode leg grants edit"           "$(probe opencode yes)" "edit=allow"
has  "under the fail-closed base rule"              "$(probe opencode yes)" "*=deny"
has  "a reading opencode leg denies edit"           "$(probe opencode no)" "edit=deny"
has  "with the same fail-closed base rule"          "$(probe opencode no)" "*=deny"
for _k in bash task skill webfetch websearch external_directory; do
  has "with $_k denied in the write shape" "$(probe opencode yes)" "$_k=deny"
  has "with $_k denied in the read shape"  "$(probe opencode no)"  "$_k=deny"
done
has "with the write shape running without external plugins" "$(probe opencode yes)" "--pure"
has "and the read shape running without them too"           "$(probe opencode no)"  "--pure"

# agy's --print takes the prompt as its VALUE, so a mode flag written after it
# becomes the prompt. The stub refuses that order; this is the assertion that
# the new flag went in on the right side of it.
agy_writing="$(probe agy yes)"
is "the agy mode flag comes before --print" \
  "$(printf '%s' "${agy_writing%%--print*}" | grep -c -- '--mode')" "1"

# ---------------------------------------------------------------------------
# The wiring: which leg gets it, through the real orchestrator
# ---------------------------------------------------------------------------
ID_A="a1b2c3d4"

review_marker() {
  jq -cn --arg sha "$FIX_HEAD" --argjson ts "$(( $(date +%s) - 192 ))" --arg a "$ID_A" '
    {v:1, leg:"review", pass:1, state:"complete", ts:$ts, done_ts:($ts + 192), run_id:"1",
     head_sha:$sha, harness:"claude", model:"reviewer-model", model_reported:"reviewer-model",
     effort:null, endpoint:null, tokens:41205, verdict:"issues-remain",
     findings:[
       {id:$a, path:"app.ts", line:2, side:"RIGHT", severity:"high", category:"correctness",
        pre_existing:false, title:"Unchecked fetch response", why:"w", fix:"f",
        anchor:"", thread_id:"T_A", resolution:null, tracked_as:null}]}'
}

REVIEW_PAYLOAD='{"verdict":"issues-remain","blocked_reason":null,"prior":null,"findings":[
  {"path":"app.ts","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,
   "title":"Unchecked fetch response","why":"A failed request looks like a success","fix":"Check response.ok"}
]}'

RESOLVE_PAYLOAD='{"blocked":false,"blocked_reason":null,"summary":"Checked the response.",
  "resolutions":[{"finding_number":1,"resolution":"fixed","reply":"Checked response.ok.",
                   "persist":null,"duplicate_of":null}]}'

# --- a review leg -----------------------------------------------------------
fixture_repo; stub_reset
routes_baseline "$(printf '[]' | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9001}'
route '*reviewThreads*' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}'
CROSSREV_REVIEW_PAYLOAD="$(printf '%s' "$REVIEW_PAYLOAD" | payload)"; export CROSSREV_REVIEW_PAYLOAD
"$CROSSREV" review --pr 42 >/dev/null 2>&1
review_argv="$(cat "$ARGV_LOG")"

has  "the review leg really did reach the harness" "$review_argv" "--output-format json"
hasnt "and it was granted no permission to write"  "$review_argv" "acceptEdits"
hasnt "nor any other permission mode"              "$review_argv" "--permission-mode"

# --- a resolve leg over that review -----------------------------------------
fixture_repo; stub_reset
routes_baseline "$(marker_comment 9001 "$(review_marker)" | jq -cs . | payload)"
route 'api --method POST repos/*/issues/42/comments*' '{"id":9002}'
route '*reviewThreads*' "$(threads_response "$(thread_node T_A app.ts 2 false "$ID_A")")"
route '*resolveReviewThread*' '{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}'
route 'api --method POST repos/*/pulls/42/comments/*/replies*' '{"id":6001}'
CROSSREV_RESOLVE_PAYLOAD="$(printf '%s' "$RESOLVE_PAYLOAD" | payload)"; export CROSSREV_RESOLVE_PAYLOAD
CROSSREV_RESOLVE_EDIT="$(mktemp)"
printf 'printf "export const ok = 2\\n" >app.ts\n' >"$CROSSREV_RESOLVE_EDIT"
export CROSSREV_RESOLVE_EDIT
"$CROSSREV" resolve --pr 42 >/dev/null 2>&1
resolve_argv="$(cat "$ARGV_LOG")"

has "the resolve leg really did reach the harness" "$resolve_argv" "--output-format json"
has "and it may write to the working tree"         "$resolve_argv" "--permission-mode acceptEdits"

# Every GitHub credential name is stripped by every adapter.
#
# Asserted statically rather than through a run, because the failure this
# catches is a name nobody wrote down: a strip list is only as good as the list,
# and `gh` reads four names where CrossRev long stripped three. A new adapter
# copying an existing one inherits whatever that one names, so the check has to
# be over all of them at once.
#
# The names come from `gh help environment`. GH_ENTERPRISE_TOKEN and
# GITHUB_ENTERPRISE_TOKEN are both read, in that order of precedence, so a list
# holding only the first leaves the second in the agent's environment on a
# GitHub Enterprise Server installation.
for adapter in "$HERE"/../lib/adapters/*.sh; do
  name="$(basename "$adapter" .sh)"
  strip_line="$(grep -m1 'local -a run=(env -u' "$adapter" || true)"
  for credential in GH_TOKEN GITHUB_TOKEN GH_ENTERPRISE_TOKEN GITHUB_ENTERPRISE_TOKEN; do
    has "the $name adapter strips \$$credential" "$strip_line" "-u $credential"
  done
done

# The config layer refuses the same four as an endpoint's token_env, because an
# endpoint hands its value to the harness under a vendor variable name — past a
# strip list that removes the GitHub name and never sees the value again.
#
# Two lists in two files, so they are asserted equal rather than each asserted
# correct. A fifth name added to the adapters and not here would leave the
# config layer accepting the one thing the adapters strip.
is "the config layer names the same four credentials the adapters strip" \
  "$(grep -m1 '^CFG_FORGE_CREDENTIALS=' "$HERE/../lib/config.sh" \
     | sed 's/.*="//; s/"$//' | tr ' ' '\n' | sort | tr '\n' ' ')" \
  "GH_ENTERPRISE_TOKEN GH_TOKEN GITHUB_ENTERPRISE_TOKEN GITHUB_TOKEN "

finish
