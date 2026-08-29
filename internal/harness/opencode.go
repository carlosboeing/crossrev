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
// Two more doors are closed beside the permission block. `run --pure` keeps
// external plugins from loading at all — belt for the base rule's braces, since a
// plugin's registered tools would be denied anyway but its code would still run.
// And `OPENCODE_CONFIG_DIR` at an empty directory displaces the agents and
// commands that would otherwise load from beside the operator's global config;
// plugins are NOT displaced by it — opencode resolves them from its own
// directories regardless — which is why the base rule and --pure, not the empty
// directory, are what answer them. OPENCODE_CONFIG itself merges on top of the
// operator's global config and wins on these keys — measured twice, against a
// global `edit: allow` and again against a later-loading config file.
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
)

// isolationConfig is the config jq builds at lib/adapters/opencode.sh:125-151.
//
// It is a template rather than a marshalled map because encoding/json sorts a
// map's keys, and the key order here is read by a person auditing what a leg was
// granted. One value varies, and %s is where the write flag lands.
//
// question and doom_loop are named denials rather than casualties of "*" so the
// intent survives anyone reading only this block; doom_loop otherwise falls back
// to ask — a prompt a headless leg cannot answer. task is denied for
// predictability (the model spawns a subagent unprompted, which multiplies token
// spend without being asked for) and skill because it is the door to the
// operator's own skill library. read stays a map, not the string "allow": a
// string would replace opencode's own *.env deny and let the model quote an
// untracked .env into a public comment.
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

	// --pure keeps external plugins out of the session entirely; see the header
	// for why the permission block alone does not answer them.
	args := []string{"run", "--pure", "--format", "json", "--dir", inv.Workdir}
	if wanted(inv.Model) {
		args = append(args, "--model", inv.Model)
	}
	if wanted(inv.Effort) {
		args = append(args, "--variant", inv.Effort)
	}
	args = append(args, prompt)

	return a.spec(inv, args, a.isolationEnv(inv)...), nil
}

// ExportSpec is the second child: `opencode export <sessionID>`, which reads the
// local session database and costs no model call
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
	return a.spec(inv, []string{"export", sessionID}, a.isolationEnv(inv)...), nil
}

func (a *Opencode) isolationEnv(inv Invocation) []string {
	return []string{
		opencodeConfigVar + "=" + filepath.Join(inv.Scratch, opencodeConfigFile),
		opencodeConfigDirVar + "=" + filepath.Join(inv.Scratch, opencodeConfigDir),
	}
}

func (a *Opencode) writeIsolation(inv Invocation) error {
	permission := "deny"
	if inv.Write {
		permission = "allow"
	}
	config := fmt.Sprintf(isolationConfig, permission)
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
	// that is a handoff, not a failure: a nil payload lets the orchestrator spend
	// the extra attempt a non-schema-native harness is granted before reporting.
	payload, _ := ExtractJSON(text)
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
