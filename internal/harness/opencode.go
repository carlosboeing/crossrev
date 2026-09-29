// opencode.go — lib/adapters/opencode.sh, the fifth harness, and the first that
// does not constrain its own output.
//
// Same contract as the other four: payload plus execution metadata, and no
// GitHub credential in the environment.
//
// **There is no schema flag.** `run --format json` streams NDJSON events —
// step_start, tool_use, text, step_finish, and error on a failure — and the
// answer rides on `.part.text` of the text events. Nothing constrains that text
// to a schema, so this is the one harness where `schema_native: false` means
// what it says: the schema travels inside the prompt, and the extra attempt the
// orchestrator grants finally has a caller. The ladder below survives a fence
// and surrounding prose because models drift, not because any of this is
// enforced.
//
// **The permission defaults are inverted.** Most permissions default to allow,
// so unlike every other harness, an opencode leg holds `edit` and `bash` unless
// something takes them away. The isolation config below is that something, and
// it is fail-closed in both directions the write flag names: `"*": "deny"` as the
// base rule, with every useful tool allowed under it, and exactly one key
// flipped by the leg — a reading leg denies `edit`, a writing leg allows it
// beside the rule, which at 1.18.21 writes files. An earlier contrary
// measurement had this build dropping the base rule to make room for the grant;
// re-measured, the rule plus an explicit edit allow wrote the file, so the grant
// lives under the rule after all — and the rule is what holds the surface no key
// names. Permission keys match as wildcard patterns against tool names, which
// covers custom tools and whatever a plugin or MCP server registers; without
// `"*": "deny"` all of those inherit opencode's allow-by-default, on the one leg
// that reads attacker-authored diff text holding a write grant. A denied tool is
// absent from the session rather than refused at call time, and nothing prompts
// for approval, so there is nothing for a headless run to block on.
//
// Three more doors are closed beside the permission block. `run --pure` keeps
// external plugins from loading at all — belt for the base rule's braces, since a
// plugin's registered tools would be denied anyway but its code would still run.
// And `OPENCODE_CONFIG_DIR` at an empty directory displaces the agents and
// commands that would otherwise load from beside the operator's global config;
// plugins are NOT displaced by it — opencode resolves them from its own
// directories regardless — which is why the base rule and --pure, not the empty
// directory, are what answer them.
//
// The last door is which agent runs. The leg passes --agent naming the agent
// the isolation config defines, whose own permission block mirrors the leg's
// grant. Agent rules are appended after all config-file rules with last match
// winning, so without the pin the leg runs the default build agent and a
// global agents.build allow widens it back. OPENCODE_DISABLE_PROJECT_CONFIG
// keeps project and parent-directory configs from merging at all, and any
// operator-exported OPENCODE_CONFIG beside the adapter's own is dropped
// before the child starts, so the isolation entries are the only ones.
//
// # 2.x keeps the config mechanism and changes everything around it
//
// opencode 2.x rejects `--pure` and `--dir` before any model call, takes no
// `--variant`, and answers the session record through `session export`
// rather than `export` (issue #272). Live flash-model legs at 2.0.15
// decide the shape below, and the second decision is the one that matters:
// OPENCODE_CONFIG, OPENCODE_CONFIG_CONTENT and OPENCODE_PERMISSION are each
// ignored by a run attached to the background service — `debug config` lists
// only the operator's global file with any of them set — while the file
// config is honoured by a run with `--standalone`, which is why a 2.x leg
// passes it. The permission
// keys themselves carry over: a review config denying `edit` held no writer
// under any name when the model was told to try `write`, `apply_patch`,
// `edit` and `bash` in turn, and a resolve config allowing `edit` wrote a
// file (through the `write` tool — in 2.x `edit` only replaces text) while
// `bash` stayed unavailable. `write` and `apply_patch` are still named
// beside `edit` rather than left to the base rule, so the grant survives
// whatever grouping a later 2.x draws between them. There is no 2.x
// equivalent of `--pure`: the base rule is what holds plugin and MCP tools
// on both majors, and on 2.x the plugin code itself loads. The effort rides
// `--model` as provider/model#variant, the `--model` help's own format, so
// an effort with no model to ride on refuses rather than starting without
// what it was configured with. The checkout travels as the child's working
// directory, which base.spec already sets to the workdir — 2.x takes no
// `--dir`, and the probe files of those live legs landed in the directory
// the process started in. `session export` answers the same `.info` shape
// the usage parser reads, so no parsing changed with the subcommand — but it
// runs `--standalone` too, because without it the child attaches to the
// shared background service and stalls instead of answering.
//
// The agent pin matters more on 2.x than on 1.x: the isolation config merges
// on top of the operator's global config, but an agent's rules are appended
// after all config-file rules with last match winning, so a global
// agents.build allow overrides the file's deny unless the leg runs as its
// own pinned agent. The pin holds on both majors, and live legs against a
// widening global config proved it: a review leg still wrote nothing and ran
// nothing, while a resolve leg wrote its file and still ran nothing.
//
// The answering model and a whole-run usage record come from
// `opencode export <sessionID>`, which reads the local session database and costs
// no model call. That is a SECOND child process, so it is a second Spec: Spec
// builds the run, ExportSpec builds the export, and MergeExport folds the answer
// in. The orchestrator drives both, which is what keeps every process start in
// one place.

package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/runlog"
)

// Opencode is the adapter for opencode.
type Opencode struct{ base }

var _ Adapter = (*Opencode)(nil)

// NotInstalled is lib/adapters/opencode.sh:85-87.
func (a *Opencode) NotInstalled() *Refusal {
	return a.notInstalled("Install it with: npm install -g opencode-ai, or point this leg at another harness with --harness.")
}

// The two files the adapter writes under Invocation.Scratch, and the variables
// that point opencode at them.
const (
	opencodeConfigFile   = "config.json"
	opencodeConfigDir    = "config-home"
	opencodeConfigVar    = "OPENCODE_CONFIG"
	opencodeConfigDirVar = "OPENCODE_CONFIG_DIR"
	// opencodeDisableProjectConfigVar skips project config discovery, so no
	// opencode.json(c) from the checkout or a parent directory loads beside
	// the isolation config. It is defense in depth beside the agent pin:
	// agent rules take precedence over every merged config, while this
	// variable keeps those configs from merging at all.
	opencodeDisableProjectConfigVar = "OPENCODE_DISABLE_PROJECT_CONFIG"
	// opencodeAgentName is the agent the isolation config defines and every
	// leg runs as via --agent. Without it the leg runs the default build
	// agent, whose global rules are appended after the config-file rules
	// with last match winning — a global agents.build allow then widens the
	// leg back. The agent is a primary one: newer runtimes silently fall
	// back to the default agent on a subagent dispatch, dropping the
	// permission block outright.
	opencodeAgentName = "crossrev"
)

// isolationConfig is the config jq builds at lib/adapters/opencode.sh:125-151.
//
// It is a template rather than a marshalled map because encoding/json sorts a
// map's keys, and the key order here is read by a person auditing what a leg was
// granted. One value varies, and each %s is where the write flag lands — the
// first three in the top-level block, the second three in the pinned agent's.
//
// question and doom_loop are named denials rather than casualties of "*" so the
// intent survives anyone reading only this block; doom_loop otherwise falls back
// to ask — a prompt a headless leg cannot answer. task is denied for
// predictability (the model spawns a subagent unprompted, which multiplies token
// spend without being asked for) and skill because it is the door to the
// operator's own skill library. read stays a map, not the string "allow": a
// string would replace opencode's own *.env deny and let the model quote an
// untracked .env into a public comment. write and apply_patch are named beside
// edit because 2.x splits creating a file from replacing text across them:
// each mirrors the leg's grant, so a review leg holds no writer under any
// name whatever grouping a 2.x draws between them, and the keys match nothing
// on 1.x, where they are inert patterns.
//
// The agent block defines the agent the leg runs as via --agent, carrying a
// full mirror of the top-level block. Agent rules take precedence over every
// merged config, so a key missing here would fall back to whatever the
// operator's global file says — the mirror leaves no such key.
const isolationConfig = `{
  "$schema": "https://opencode.ai/config.json",
  "permission": {
    "*": "deny",
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
    "edit": "%s",
    "write": "%s",
    "apply_patch": "%s",
    "bash": "deny",
    "task": "deny",
    "skill": "deny",
    "webfetch": "deny",
    "websearch": "deny",
    "external_directory": "deny",
    "question": "deny",
    "doom_loop": "deny"
  },
  "agent": {
    "crossrev": {
      "mode": "primary",
      "permission": {
        "*": "deny",
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
        "edit": "%s",
        "write": "%s",
        "apply_patch": "%s",
        "bash": "deny",
        "task": "deny",
        "skill": "deny",
        "webfetch": "deny",
        "websearch": "deny",
        "external_directory": "deny",
        "question": "deny",
        "doom_loop": "deny"
      }
    }
  }
}
`

// schemaInstruction is the paragraph appended to the prompt for the one harness
// that does not constrain its own output (lib/adapters/opencode.sh:105).
//
// The Bash writes a copy of the prompt to a temporary file and then passes
// `$(cat "$prompt_copy")` as argv, deleting the copy immediately after
// (lib/adapters/opencode.sh:100-108 and :190). The file is a vehicle rather than
// an input — nothing reads it by path — so the Go builds the string.
const schemaInstruction = "\n\nThis harness does not constrain your output. The answer text itself is what is parsed, so return a single JSON object matching exactly this schema, with no markdown fence and no commentary:\n\n```json\n%s\n```\n"

// Spec builds the child process, and writes the isolation config it names
// (lib/adapters/opencode.sh:89-188).
func (a *Opencode) Spec(inv Invocation) (exec.Spec, error) {
	if inv.Endpoint.Named() {
		return exec.Spec{}, a.endpointRefusal(inv.Endpoint, endpointHostName,
			"opencode has its own provider layer, so an endpoint name means nothing to it.")
	}
	if inv.Scratch == "" {
		return exec.Spec{}, &Refusal{
			Reason: "the opencode adapter was given no scratch directory",
			Action: "Its permission grant travels in a config file outside the workspace, so the orchestrator has to name a directory to write one into.",
			Kind:   ErrScratch,
		}
	}

	// The schema travels inside the prompt, under an instruction that also
	// corrects the skill's "the harness constrains your output" claim — true for
	// the other four, false here. This keeps prompt building unaware of which
	// harness will read what it built — the same class of per-CLI fact as
	// Antigravity's flag order or Codex's schema path.
	// The composition order matters, and this is the one adapter where it is
	// not File.Argument. The Bash builds a copy — `cat "$prompt_file"` RAW,
	// then the printf block — and only the finished copy goes through
	// `"$(cat "$leg_prompt")"` at lib/adapters/opencode.sh:187. So the prompt
	// file's own trailing newline survives into the middle of the composed
	// string, and the block's closing `\n` after the fence is what gets
	// stripped. Trimming the prompt first would lose a newline the model sees
	// and keep one it does not.
	prompt := inv.Prompt.Text
	if inv.Schema.Present() {
		if inv.Schema.Text == "" {
			return exec.Spec{}, &Refusal{
				Reason: "the opencode adapter was given a schema path with no schema text",
				Action: "This harness takes no schema flag, so the schema is reproduced inside the prompt and the orchestrator has to read the file before the leg runs.",
				Kind:   ErrSchemaUnavailable,
			}
		}
		prompt += fmt.Sprintf(schemaInstruction, inv.Schema.Argument())
	}
	prompt = strings.TrimRight(prompt, "\n")

	if err := a.writeIsolation(inv); err != nil {
		return exec.Spec{}, err
	}

	if inv.CLIMajor == 2 {
		return a.spec2x(inv, prompt)
	}

	// --pure keeps external plugins out of the session entirely; see the header
	// for why the permission block alone does not answer them. --agent runs
	// the session as the agent the isolation config defines, whose rules are
	// appended last and take precedence over any merged global.
	args := []string{"run", "--pure", "--format", "json", "--agent", opencodeAgentName, "--dir", inv.Workdir}
	if wanted(inv.Model) {
		args = append(args, "--model", inv.Model)
	}
	if wanted(inv.Effort) {
		args = append(args, "--variant", inv.Effort)
	}
	args = append(args, prompt)

	return a.opencodeSpec(inv, args, a.isolationEnv(inv)...), nil
}

// spec2x builds the child process for opencode 2.x, which rejects the 1.x
// flags and ignores the isolation config unless the run is standalone (see
// the header). The checkout travels as the child's working directory, which
// base.spec already sets to the workdir, and the effort rides the model as
// provider/model#variant.
func (a *Opencode) spec2x(inv Invocation, prompt string) (exec.Spec, error) {
	args := []string{"run", "--standalone", "--format", "json", "--agent", opencodeAgentName}
	switch {
	case wanted(inv.Model) && wanted(inv.Effort):
		args = append(args, "--model", inv.Model+"#"+inv.Effort)
	case wanted(inv.Model):
		args = append(args, "--model", inv.Model)
	case wanted(inv.Effort):
		return exec.Spec{}, &Refusal{
			Reason: "the opencode adapter was given an effort with no model on opencode 2.x",
			Action: "opencode 2.x takes no --variant flag: the effort rides --model as provider/model#variant, so name a model beside the effort or clear the effort for this leg.",
			Kind:   ErrEffortWithoutModel,
		}
	}
	args = append(args, prompt)

	return a.opencodeSpec(inv, args, a.isolationEnv(inv)...), nil
}

// VersionProbe is `opencode --version`, which reports "opencode vX.Y.Z" and
// costs no model call.
//
// Its working directory is the scratch directory rather than the checkout:
// --version needs no checkout, and the resolve leg runs this probe before the
// quarantine has moved repository-provided harness configuration, so starting
// it outside the worktree is what keeps the probe from reading any.
func (a *Opencode) VersionProbe(inv Invocation) exec.Spec {
	probe := a.spec(inv, []string{"--version"})
	probe.Dir = inv.Scratch
	return probe
}

// VersionRefusal refuses an install outside the major versions this adapter
// drives, before the leg starts — and one it cannot read a version from.
//
// The rule is 1.x and 2.x, which is what every refusal here names. Each major
// takes its own flags and its own session-export subcommand, and the spec is
// built from the major the gate confirmed. Below 1.x nothing is recorded, so
// it is refused on the same terms rather than run blind.
//
// A probe that names no version at all is refused too: the gate fails closed,
// because an install CrossRev cannot confirm is not one it drives. The refusal
// names the range in every shape rather than guessing what an unreadable
// banner hid.
func (a *Opencode) VersionRefusal(probe []byte) *Refusal {
	token := versionToken.FindString(string(probe))
	if token == "" {
		return &Refusal{
			Reason: "the opencode CLI did not report a version, and CrossRev supports opencode 1.x and 2.x (issue #272)",
			Action: "CrossRev cannot confirm this install is one it drives, so the leg is refused rather than started (https://github.com/carlosboeing/crossrev/issues/272). Install the supported CLI with: " + a.descriptor.Install.Command + ", or point this leg at another harness with --harness.",
			Kind:   ErrVersionUnsupported,
		}
	}
	if major := majorVersion(token); major == 1 || major == 2 {
		return nil
	}
	return &Refusal{
		Reason: "the opencode CLI reports version " + token + ", and CrossRev supports opencode 1.x and 2.x (issue #272)",
		Action: "Nothing outside 1.x and 2.x is recorded (https://github.com/carlosboeing/crossrev/issues/272). Install the supported CLI with: " + a.descriptor.Install.Command + ", or point this leg at another harness with --harness.",
		Kind:   ErrVersionUnsupported,
	}
}

// ExportSpec is the second child: `opencode export <sessionID>` on 1.x and
// `opencode session export <sessionID>` on 2.x, which reads the local
// session database and costs no model call
// (lib/adapters/opencode.sh:266).
//
// It carries the same environment as the run, isolation config included, because
// the Bash reuses the same `${run[@]}` array. The scratch directory therefore
// has to outlive the run itself (lib/adapters/opencode.sh:191-192).
func (a *Opencode) ExportSpec(inv Invocation, sessionID string) (exec.Spec, error) {
	if sessionID == "" {
		return exec.Spec{}, &Refusal{
			Reason: "the opencode adapter has no session id to export",
			Action: "The session id rides on every event the run emits, so a stream that carried none has nothing to export.",
			Kind:   ErrScratch,
		}
	}
	args := []string{"export", sessionID}
	if inv.CLIMajor == 2 {
		// --standalone for the same reason the run carries it: without it
		// the child attaches to the shared background service, where the
		// export stalls instead of answering.
		args = []string{"session", "export", "--standalone", sessionID}
	}
	return a.opencodeSpec(inv, args, a.isolationEnv(inv)...), nil
}

func (a *Opencode) isolationEnv(inv Invocation) []string {
	return []string{
		opencodeConfigVar + "=" + filepath.Join(inv.Scratch, opencodeConfigFile),
		opencodeConfigDirVar + "=" + filepath.Join(inv.Scratch, opencodeConfigDir),
		opencodeDisableProjectConfigVar + "=1",
	}
}

// opencodeSpec builds the child through base.spec after dropping any
// inherited entry for a variable the adapter sets itself. The leg allowlist
// inherits an operator-exported OPENCODE_CONFIG beside the adapter's own,
// and whichever duplicate the runtime honours first would then decide what
// the leg may do — so the isolation entries are the only ones.
func (a *Opencode) opencodeSpec(inv Invocation, args []string, additions ...string) exec.Spec {
	owned := map[string]bool{
		opencodeConfigVar:               true,
		opencodeConfigDirVar:            true,
		opencodeDisableProjectConfigVar: true,
	}
	env := make([]string, 0, len(inv.Env))
	for _, entry := range inv.Env {
		if variable, _, found := strings.Cut(entry, "="); found && owned[variable] {
			continue
		}
		env = append(env, entry)
	}
	inv.Env = env
	return a.spec(inv, args, additions...)
}

func (a *Opencode) writeIsolation(inv Invocation) error {
	permission := "deny"
	if inv.Write {
		permission = "allow"
	}
	config := fmt.Sprintf(isolationConfig,
		permission, permission, permission,
		permission, permission, permission)
	if !json.Valid([]byte(config)) {
		// Unreachable while the template above is a constant, and cheap enough
		// to keep: the file is what stands between a review leg and a write
		// grant, and a malformed one is ignored rather than refused by opencode.
		return &Refusal{
			Reason: "the opencode isolation config did not build as valid JSON",
			Action: "This is a CrossRev bug. The permission block is what denies a review leg the write tool, so the leg is refused rather than run without it.",
			Kind:   ErrScratch,
		}
	}
	if err := os.MkdirAll(filepath.Join(inv.Scratch, opencodeConfigDir), 0o700); err != nil {
		return &Refusal{
			Reason: "the opencode adapter could not create its config directory",
			Action: "Check that the scratch directory is writable.",
			Kind:   ErrScratch,
			Err:    err,
		}
	}
	path := filepath.Join(inv.Scratch, opencodeConfigFile)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		return &Refusal{
			Reason: "the opencode adapter could not write its isolation config",
			Action: "Check that the scratch directory is writable.",
			Kind:   ErrScratch,
			Err:    err,
		}
	}
	return nil
}

// The shape of an authentication rejection: an error event on stdout, and
// AI_APICallError naming Unauthorized behind it on stderr
// (lib/adapters/opencode.sh:202-203). The first grep is case-sensitive and the
// second is not, which is what the Bash does.
var (
	opencodeAPICallError = regexp.MustCompile(`AI_APICallError`)
	opencodeUnauthorized = regexp.MustCompile(`(?im)Unauthorized|(^|[^0-9])401([^0-9]|$)`)
)

// Envelope reads what the child produced (lib/adapters/opencode.sh:201-285).
//
// The answering model and the usage record are NOT here: they come from the
// export, which is a second process. MergeExport folds them in.
func (a *Opencode) Envelope(_ Invocation, res exec.Result) Envelope {
	// Naming an authentication rejection matters more than usual here, because
	// opencode falls through to a DIFFERENT provider when the configured one
	// cannot authenticate — measured — so "the harness failed" sends the reader
	// looking in the wrong place entirely. A bare error event is not that shape
	// — rate limits, overloads, tool failures — and falls through to the generic
	// harness-error branch below.
	if opencodeAPICallError.Match(res.Stderr) && opencodeUnauthorized.Match(res.Stderr) {
		message := "opencode rejected its credential. CrossRev classifies this as a credential failure, not a generic harness error."
		if detail := HarnessError(res.Stderr); detail != "" {
			message += " " + detail
		}
		return failed(a.Name(), runlog.Redact(message))
	}

	if res.ExitCode != 0 {
		message := HarnessError(res.Stderr)
		if message == "" {
			message = "opencode exited " + itoa(res.ExitCode) + " with no output on either stream"
		}
		return failed(a.Name(), runlog.Redact(message))
	}

	// No text event at all is a different fault from a malformed answer: the run
	// finished and said nothing, and the diagnosis should say so rather than
	// dressing it up as a schema mismatch.
	text := opencodeText(res.Stdout)
	if text == "" {
		// Not redacted, because it is a constant this adapter wrote.
		return failed(a.Name(), "opencode produced no answer: the run finished without a single text event.")
	}

	// Extraction can legitimately miss — prose with no braces anywhere — and
	// that is a handoff, not a failure: the orchestrator then spends the extra
	// attempt a non-schema-native harness is granted before reporting.
	//
	// The miss is carried as the JSON literal `null`, which is what
	// lib/adapters/opencode.sh:257-258 substitutes:
	//
	//	payload="$(_opencode_extract_json <<<"$text")"
	//	[[ -n "$payload" ]] || payload="null"
	//
	// A Go nil is not the same value. The validator rejects `null` as "the
	// payload is not a JSON object" and accepts an absent one, so prose with no
	// object in it was published as an empty review instead of being retried.
	payload, _ := ExtractJSON(text)
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	return succeeded(a.Name(), vendorEndpoint, payload, nil)
}

// SessionID is the id every event of the run carries
// (lib/adapters/opencode.sh:264).
//
// `jq -Rr 'fromjson? | .sessionID // empty' | head -n 1` stops at the FIRST
// line that produces output, and `//` produces output for the empty string
// because only null and false are falsy in jq. So an event carrying
// `"sessionID": ""` ends the search with nothing, and the export is skipped —
// measured, against a file whose first event carries an empty id and whose
// second carries a real one:
//
//	{"type":"text","sessionID":""}       -> (empty)
//	{"type":"text"}                      -> ses_real, from the next line
//	{"type":"text","sessionID":null}     -> ses_real, from the next line
//
// Scanning past the empty one found an id the shell does not export against.
// The consequence is not symmetric: the export is telemetry, so the shell loses
// a usage record and keeps the review, while a Go run that found an id the
// shell would not have exports for a session the shell never named.
//
// A line that parses to something that is not an object — `5` — makes jq raise
// a type error for that input only; jq reports it on the stderr this call
// discards and carries on to the next line. Measured, and the same as skipping.
func (a *Opencode) SessionID(res exec.Result) string {
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		event, err := decodeOrdered([]byte(line))
		if err != nil {
			// `fromjson?` skips a line that is not one JSON value.
			continue
		}
		id := event.member("sessionID")
		if !id.truthy() {
			continue
		}
		// Truthy, so this line ended the search whatever it holds. A
		// non-string answers the empty string, which is the declared
		// divergence firstAlternative records.
		text, _ := id.asString()
		return text
	}
	return ""
}

// MergeExport folds the export's answer into an envelope
// (lib/adapters/opencode.sh:265-273).
//
// One export call supplies the answering model and the whole-run usage record.
// Telemetry, not the answer — if the export failed, both stay nil and the review
// stands, which is why this takes the bytes rather than a Result and an error.
func (a *Opencode) MergeExport(envelope *Envelope, exported []byte) {
	if envelope == nil || len(exported) == 0 {
		return
	}
	if model := OpencodeModelID(exported); model != "" {
		envelope.ModelReported = &model
	}
	usage := ParseOpencodeExport(exported)
	if usage == nil {
		return
	}
	envelope.Usage = usage
	envelope.Tokens = usage.Total
}

// opencodeText is `jq -Rj 'fromjson? | select(.type == "text") | .part.text //
// empty'` (lib/adapters/opencode.sh:241): every text event's text, concatenated.
//
// Join-output, not raw-output: `-r` terminates every event with a newline, and a
// seam inside a JSON string is then an unescaped control character that fails
// every extraction rung.
//
// Read line by line rather than as one stream, because `-R` is what the Bash
// passes: a single malformed line is skipped, where a slurped stream would take
// the whole read down.
func opencodeText(stdout []byte) string {
	var answer strings.Builder
	for _, line := range strings.Split(string(stdout), "\n") {
		event, err := decodeOrdered([]byte(line))
		if err != nil {
			continue
		}
		if kind, _ := event.member("type").asString(); kind != "text" {
			continue
		}
		if text, ok := event.member("part").member("text").asString(); ok {
			answer.WriteString(text)
		}
	}
	return answer.String()
}

// fenceOpening is the `^```[a-zA-Z0-9_-]*[ \t]*\r?\n` of
// lib/adapters/opencode.sh:70.
var fenceOpening = regexp.MustCompile("^```[a-zA-Z0-9_-]*[ \t]*\r?\n")

// fenceClosing is the same line's closing pattern with its `$` left off, because
// Go's `$` and jq's do not agree about a trailing newline. Measured: jq's
// `sub("b$"; "X")` rewrites "a\nb\n" to "a\nX\n", so Oniguruma's `$` matches
// before a final newline the way Perl's does, and Go's matches only at the end
// of the text. unfence applies that rule itself.
var fenceClosing = regexp.MustCompile("\r?\n[ \t]*```[ \t]*")

// ExtractJSON is _opencode_extract_json (lib/adapters/opencode.sh:66-78).
//
// Concatenated answer text in, extracted JSON out. Three rungs, stopping at the
// first that parses to something jq would call truthy: the text as-is, a stripped
// markdown fence, then the span from the first `{` to the last `}`. Nothing is
// the adapter's signal to hand a null payload to the orchestrator's shape check
// rather than fail here, so prose that merely forgot the braces earns the retry
// a schema-less harness is budgeted.
//
// A rung that parses to `null` or `false` falls through, because that is what
// jq's `//` does with them. Measured: text of `null` and of `false` both answer
// nothing, and `7` answers `7`.
func ExtractJSON(text string) (json.RawMessage, bool) {
	for _, rung := range []string{text, unfence(text), spanned(text)} {
		if rung == "" {
			continue
		}
		payload, parsed := parseJSON(rung)
		if !parsed {
			continue
		}
		trimmed := strings.TrimSpace(string(payload))
		if trimmed == "null" || trimmed == "false" {
			continue
		}
		return payload, true
	}
	return nil, false
}

// unfence strips a markdown fence from both ends, as two independent `sub`
// calls that each replace at most one match.
func unfence(text string) string {
	text = fenceOpening.ReplaceAllString(text, "")

	// jq's `$` matches at the end of the string OR before a final newline. Go's
	// regexp has no lookahead to express that, so the closing pattern is matched
	// without an anchor and the first match that ENDS in one of those two places
	// is the one jq would have found.
	for _, span := range fenceClosing.FindAllStringIndex(text, -1) {
		atEnd := span[1] == len(text)
		beforeFinalNewline := span[1] == len(text)-1 && text[len(text)-1] == '\n'
		if atEnd || beforeFinalNewline {
			return text[:span[0]] + text[span[1]:]
		}
	}
	return text
}

// spanned is the third rung: the substring from the first `{` to the last `}`.
func spanned(text string) string {
	opening := strings.Index(text, "{")
	closing := strings.LastIndex(text, "}")
	if opening < 0 || closing < opening {
		return ""
	}
	return text[opening : closing+1]
}
