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
	args := []string{"--output-format", "json", "--permission-mode", "dontAsk"}

	// dontAsk on both legs: the headless default can prompt and hang. The
	// resolve leg needs an explicit write grant on top; the review leg is denied
	// at both the permission-rule and sandbox layers. bypassPermissions,
	// --always-approve, --yolo and --dangerously-skip-permissions are a blanket
	// bypass and are never passed.
	if inv.Write {
		args = append(args, "--sandbox", "workspace", "--allow", "Edit", "--allow", "Write")
	} else {
		args = append(args, "--sandbox", "read-only", "--deny", "Edit", "--deny", "Write")
	}

	if inv.Schema.Present() {
		if inv.Schema.Text == "" {
			return exec.Spec{}, &Refusal{
				Reason: "the grok adapter was given a schema path with no schema text",
				Action: "`--json-schema` takes inline JSON rather than a path, so the orchestrator has to read the file before the leg runs.",
				Kind:   ErrSchemaUnavailable,
			}
		}
		args = append(args, "--json-schema", inv.Schema.Text)
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
	args = append(args, "--prompt-file", inv.Prompt.Path)

	return a.spec(inv, args), nil
}

// grokCredentialRejection matches the stderr of a run Grok refused to
// authenticate (lib/adapters/grok.sh:94).
var grokCredentialRejection = regexp.MustCompile(`(?i)not signed in|XAI_API_KEY`)

// Envelope reads what the child produced (lib/adapters/grok.sh:83-149).
func (a *Grok) Envelope(_ Invocation, res exec.Result) Envelope {
	answer, _ := decodeOrdered(res.Stdout)

	if res.ExitCode != 0 {
		message, _ := answer.member("error").asString()
		if message == "" {
			message, _ = answer.member("text").asString()
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
	usage := ParseGrok(res.Stdout)
	envelope := succeeded(a.Name(), vendorEndpoint, grokPayload(res.Stdout, answer), usage)
	if usage != nil {
		if model := ModelReportedFromModels(usage.Models); model != "" {
			envelope.ModelReported = &model
		}
	}
	return envelope
}

// grokPayload is the ladder at lib/adapters/grok.sh:118-124.
//
// Live grok 1.0.5 with --json-schema puts the constrained object on
// structuredOutput. .text is the model's prose, and on a schema run it is often
// several draft JSON objects concatenated — fromjson rejects that, which is how
// a successful turn was reported as "the payload is not a JSON object".
// structured_output is the snake_case sibling agy uses; it stays as a fallback in
// case a later grok release matches that spelling. .text remains last for a run
// with no schema.
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
		payload, parsed := parseJSON(text.text)
		if !parsed {
			return nil
		}
		return payload
	default:
		return nil
	}
}
