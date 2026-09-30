// grok.go — lib/adapters/grok.sh, Grok, the fourth harness.
//
// Same contract as the other three: payload plus execution metadata, and no
// GitHub credential in the environment.
//
// `--prompt-file` takes a path and turns on headless mode. `-p` / `--print` /
// `--single` consume the next argv as the prompt, so a flag written after them
// is answered as the question. That is the same class of failure the agy adapter
// already documents. `--json-schema` takes an inline JSON string, not a path;
// handing it a path fails with a parse error about the leading slash.
//
// Authentication rejections are classified as a credential failure naming Grok.
// That is the mitigation for a vendor silently switching archetype: the operator
// sees a credential that was consumed, not a harness that stopped working.

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

// Grok is the adapter for Grok.
type Grok struct{ base }

var _ Adapter = (*Grok)(nil)

// NotInstalled is lib/adapters/grok.sh:23-25.
func (a *Grok) NotInstalled() *Refusal {
	return a.notInstalled("Install Grok from https://x.ai/cli, or point this leg at another harness with --harness.")
}

// Spec builds the child process (lib/adapters/grok.sh:27-79).
func (a *Grok) Spec(inv Invocation) (exec.Spec, error) {
	if inv.Endpoint.Named() {
		// This adapter names the endpoint host out of the descriptor where the
		// other three spell it (lib/adapters/grok.sh:28-31).
		return exec.Spec{}, a.endpointRefusal(inv.Endpoint, a.doc.EndpointHost(), "")
	}

	// --prompt-file last: it takes a path, so remaining flags are not
	// swallowed, but putting the prompt after the rest matches the other
	// adapters' shape.
	//
	// A supplied review leg reviews the way the spike ran it: streaming-json
	// output with a tools allowlist holding neither shell nor read tools, so
	// the tripwire can parse tool_call and tool_call_update events. The
	// zero mode keeps the legacy json shape every stub test pins.
	suppliedReview := !inv.Write && inv.ReadMode == ReadModeSupplied
	outputFormat := "json"
	if inv.Write || suppliedReview {
		// A resolve leg streams its tool record so the tripwire can read it.
		outputFormat = "streaming-json"
	}
	args := []string{"--output-format", outputFormat, "--permission-mode", "dontAsk"}

	// dontAsk on both legs: the headless default can prompt and hang. The
	// resolve leg needs an explicit write grant on top; the review leg is denied
	// at both the permission-rule and sandbox layers. bypassPermissions,
	// --always-approve, --yolo and --dangerously-skip-permissions are a blanket
	// bypass and are never passed.
	//
	// A resolve leg edits without running commands: the --tools allowlist
	// holds the read and edit tools while leaving the shell out. A --deny
	// rule did not remove the shell in the measured run, so the allowlist is
	// what denies it. --sandbox workspace and --allow Edit/Write stay as the
	// filesystem and permission grants; --tools is the built-in tool list
	// (`grok --help`). The names are the Claude-style tool names the
	// orchestration mappings record (Read, Grep, Glob, Edit, Write), with no
	// Bash entry.
	if inv.Write {
		args = append(args, "--sandbox", "workspace", "--allow", "Edit", "--allow", "Write",
			"--tools", "Read,Grep,Glob,Edit,Write")
	} else if suppliedReview {
		// The allowlist is what denies: a --deny rule did not remove the
		// shell in the measured run. It grants no tool at all — Grep
		// returns file content and Glob enumerates paths over the
		// checkout, and neither carries a command the tripwire would
		// catch, so even those two stay out. An empty allowlist fails
		// closed if the CLI ever refuses it.
		args = append(args, "--sandbox", "read-only", "--deny", "Edit", "--deny", "Write",
			"--tools", "")
	} else {
		args = append(args, "--sandbox", "read-only", "--deny", "Edit", "--deny", "Write")
	}

	// --json-schema travels on the legacy review leg only. A supplied
	// review streams like a resolve leg: without the flag the model answers
	// in text and the answer is read out of the stream instead (see
	// grokPayload); the shape check downstream still validates that text
	// against the schema the prompt carries, and the call earns the second
	// shape attempt of a harness that does not constrain its own output.
	// With the flag and a prompt carrying the full diff, the model answers
	// in one structured turn and never calls a tool, so a streaming leg's
	// tool record would stay empty.
	// (grok --help: --json-schema implies --output-format json, which is
	// why the flag and the stream cannot travel together.)
	if inv.Schema.Present() && !inv.Write && !suppliedReview {
		if inv.Schema.Text == "" {
			return exec.Spec{}, &Refusal{
				Reason: "the grok adapter was given a schema path with no schema text",
				Action: "`--json-schema` takes inline JSON rather than a path, so the orchestrator has to read the file before the leg runs.",
				Kind:   ErrSchemaUnavailable,
			}
		}
		args = append(args, "--json-schema", inv.Schema.Argument())
	}
	// A supplied review cannot take the flag, so the schema travels inside
	// the prompt instead, the way opencode's does. The child reads the
	// composed copy, never the bare prompt file.
	promptPath := inv.Prompt.Path
	if suppliedReview && inv.Schema.Present() {
		if inv.Schema.Text == "" {
			return exec.Spec{}, &Refusal{
				Reason: "the grok adapter was given a schema path with no schema text",
				Action: "A supplied grok review carries the schema inside the prompt, so the orchestrator has to read the file before the leg runs.",
				Kind:   ErrSchemaUnavailable,
			}
		}
		composed, err := writeSuppliedPrompt(inv)
		if err != nil {
			return exec.Spec{}, err
		}
		promptPath = composed
	}
	if wanted(inv.Model) {
		args = append(args, "--model", inv.Model)
	}
	if wanted(inv.Effort) {
		args = append(args, "--reasoning-effort", inv.Effort)
	}
	if inv.Prompt.Path == "" {
		return exec.Spec{}, &Refusal{
			Reason: "the grok adapter was given no prompt file",
			Action: "`--prompt-file` takes a path, so the orchestrator has to write the prompt before the leg runs.",
			Kind:   ErrSchemaUnavailable,
		}
	}
	args = append(args, "--prompt-file", promptPath)

	return a.spec(inv, args), nil
}

// grokSuppliedPromptFile is the composed prompt a supplied review reads:
// the review prompt with the schema appended under the instruction, beside
// the bare prompt the leg rendered.
const grokSuppliedPromptFile = "prompt.supplied"

// writeSuppliedPrompt composes the prompt a supplied review is read: the
// schema cannot travel as --json-schema on a streaming leg, so it travels
// inside the prompt under the same instruction opencode's carries.
func writeSuppliedPrompt(inv Invocation) (string, error) {
	if inv.Scratch == "" {
		return "", &Refusal{
			Reason: "the grok adapter was given no scratch directory",
			Action: "A supplied grok review carries the schema inside the prompt, so the orchestrator has to name a directory to compose one in.",
			Kind:   ErrScratch,
		}
	}
	composed := inv.Prompt.Text + fmt.Sprintf(schemaInstruction, inv.Schema.Argument())
	path := filepath.Join(inv.Scratch, grokSuppliedPromptFile)
	if err := os.WriteFile(path, []byte(composed), 0o600); err != nil {
		return "", &Refusal{
			Reason: "the grok adapter could not write its composed prompt",
			Action: "Check that the scratch directory is writable.",
			Kind:   ErrScratch,
			Err:    err,
		}
	}
	return path, nil
}

// grokCredentialRejection matches the stderr of a run Grok refused to
// authenticate (lib/adapters/grok.sh:94).
var grokCredentialRejection = regexp.MustCompile(`(?i)not signed in|XAI_API_KEY`)

// Envelope reads what the child produced (lib/adapters/grok.sh:83-149).
func (a *Grok) Envelope(inv Invocation, res exec.Result) Envelope {
	// A resolve leg streams NDJSON; the terminal end event carries the usage
	// envelope while the text deltas accumulate to the constrained answer.
	// A supplied review leg streams the same way. Any other review leg keeps
	// the single json object.
	streaming := inv.Write || (!inv.Write && inv.ReadMode == ReadModeSupplied)
	stdout := res.Stdout
	var streamText string
	if streaming {
		if end := grokStreamEnd(res.Stdout); end != nil {
			stdout = end
		}
		streamText = grokStreamText(res.Stdout)
	}
	answer, _ := decodeOrdered(stdout)

	if res.ExitCode != 0 {
		message := firstAlternative(answer, "error", "text")
		if message == "" && streaming {
			message = grokStreamError(res.Stdout)
		}
		if message == "" {
			message = HarnessError(res.Stderr)
		}
		if message == "" {
			message = "grok exited " + itoa(res.ExitCode) + " with no output on either stream"
		}
		message = runlog.Redact(message)
		// The stderr test is case-insensitive and the message test is not,
		// because the Bash runs `grep -qiE` over the capture file and a
		// case-sensitive `[[ … == *"Not signed in"* ]]` over the message.
		if grokCredentialRejection.Match(res.Stderr) ||
			strings.Contains(message, "Not signed in") ||
			strings.Contains(message, "XAI_API_KEY") {
			message = "Grok rejected the credential. CrossRev classifies this as a credential failure, not a generic harness error. " + message
		}
		return failed(a.Name(), message)
	}

	// Buckets summed from the parts: grok's own total_tokens happens to
	// reconcile today, but reading it would trust a vendor field the identity
	// replaces. The harness cost rides along; the answering model comes from the
	// models list the parser built out of modelUsage — its entries carry call
	// counts rather than token totals, so there is no share to rank and first is
	// the only report.
	usage := ParseGrok(stdout)
	payload := grokPayload(stdout, answer)
	if streaming && payload == nil && streamText != "" {
		// The resolve leg runs without --json-schema, so the streamed
		// answer is prose around the payload rather than the payload.
		if parsed, ok := ExtractJSON(streamText); ok {
			payload = parsed
		}
	}
	envelope := succeeded(a.Name(), vendorEndpoint, payload, usage)
	if usage != nil {
		if model := ModelReportedFromModels(usage.Models); model != "" {
			envelope.ModelReported = &model
		}
	}
	return envelope
}

// grokStreamEnd is the terminal end event of a streaming-json run: the last
// line whose top-level type is "end". Each line decodes alone so one malformed
// line skips rather than taking the whole stream down.
func grokStreamEnd(stdout []byte) []byte {
	var last []byte
	for _, line := range strings.Split(string(stdout), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		event, err := decodeOrdered([]byte(trimmed))
		if err != nil {
			continue
		}
		if kind, _ := event.member("type").asString(); kind != "end" {
			continue
		}
		last = []byte(trimmed)
	}
	return last
}

// grokStreamText concatenates every text event's data, the accumulated answer
// a streaming-json run carries in place of the json object's text field.
func grokStreamText(stdout []byte) string {
	var answer strings.Builder
	for _, line := range strings.Split(string(stdout), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		event, err := decodeOrdered([]byte(trimmed))
		if err != nil {
			continue
		}
		if kind, _ := event.member("type").asString(); kind != "text" {
			continue
		}
		if data, ok := event.member("data").asString(); ok {
			answer.WriteString(data)
		}
	}
	return answer.String()
}

// grokStreamError is the first error event's message on a failed
// streaming-json run.
func grokStreamError(stdout []byte) string {
	for _, line := range strings.Split(string(stdout), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		event, err := decodeOrdered([]byte(trimmed))
		if err != nil {
			continue
		}
		if kind, _ := event.member("type").asString(); kind != "error" {
			continue
		}
		if message, ok := event.member("message").asString(); ok && message != "" {
			return message
		}
	}
	return ""
}

// grokPayload is the ladder at lib/adapters/grok.sh:118-124.
//
// Live grok 1.0.5 with --json-schema puts the constrained object on
// structuredOutput. .text is the model's prose, and on a schema run it is often
// several draft JSON objects concatenated — fromjson rejects that, which is how
// a successful turn was reported as "the payload is not a JSON object".
// structured_output is the snake_case sibling agy uses; it stays as a fallback in
// case a later grok release matches that spelling. .text remains last for a run
// with no schema — and the resolve leg is always such a run now, so its text
// goes through the fenced-or-spanned ladder rather than a bare parse: without
// the flag nothing constrains the answer to JSON alone.
func grokPayload(stdout []byte, answer node) json.RawMessage {
	for _, key := range []string{"structuredOutput", "structured_output"} {
		if value, ok := alternativeValue(rawMember(stdout, key)); ok {
			if payload, parsed := parseJSON(string(value)); parsed {
				return payload
			}
		}
	}
	text := answer.member("text")
	switch text.kind {
	case kindObject, kindArray:
		if value, ok := rawMember(stdout, "text"); ok {
			if payload, parsed := parseJSON(string(value)); parsed {
				return payload
			}
		}
		return nil
	case kindString:
		payload, parsed := ExtractJSON(text.text)
		if !parsed {
			return nil
		}
		return payload
	default:
		return nil
	}
}
