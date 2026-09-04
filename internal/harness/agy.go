// agy.go — lib/adapters/agy.sh, Antigravity, the third harness.
//
// Same contract as the other two: payload plus execution metadata, and no
// GitHub credential in the environment.
//
// Two things about this CLI shape the adapter, and both were found by running it
// rather than by reading the help text.
//
// **`--print` takes the prompt as its value.** So every other flag has to come
// BEFORE it. Written the usual way — `agy --print --output-format json "..."` —
// the CLI treats the literal string "--output-format" as the prompt and answers
// a question about it, cheerfully, in prose. That failure costs a subscription
// call and looks like the model ignoring instructions.
//
// **It constrains its own output.** `--json-schema` takes a string or a path,
// and `--output-format json` returns the parsed object under
// `structured_output`. Verified against a two-field schema, which came back
// conforming. So this harness is schema-native alongside claude and codex, and
// the retry path stays dead code for it too.
//
// It reports no answering model — the JSON carries a conversation id, a status,
// usage counts and nothing identifying what served the turn — so ModelReported
// is nil, exactly as for codex. Layer one of the divergence guard covers it.

package harness

import (
	"encoding/json"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/runlog"
)

// Agy is the adapter for Antigravity.
type Agy struct{ base }

var _ Adapter = (*Agy)(nil)

// NotInstalled is lib/adapters/agy.sh:32-34.
func (a *Agy) NotInstalled() *Refusal {
	return a.notInstalled("Install Antigravity from https://antigravity.google, or point this leg at another harness with --harness.")
}

// Spec builds the child process (lib/adapters/agy.sh:36-94).
func (a *Agy) Spec(inv Invocation) (exec.Spec, error) {
	if inv.Endpoint.Named() {
		return exec.Spec{}, a.endpointRefusal(inv.Endpoint, endpointHostName, "")
	}

	// Order matters: everything before --print, and the prompt as its value.
	args := []string{"--output-format", "json", "--disable-slash-commands"}

	// Antigravity does not take the shell's working directory as its workspace.
	// It keeps its own project root, and without one it resolves against $HOME:
	// a relative `read_file app.ts` was refused as `read_file(/Users/<name>)`,
	// and a shell command ran from a scratch directory rather than the checkout.
	// So the leg could not see the code it was sent to work on, and the model
	// reached for `pwd` and `git status` to find out where it was — which the
	// permission layer then denied, because those were outside the workspace
	// too.
	//
	// That reads as "this harness cannot resolve without a shell grant", and it
	// is not. With the workspace named, `--mode accept-edits` alone edits the
	// file on the first turn. The fix is telling it where the work is, not
	// widening what it may do.
	args = append(args, "--add-dir", inv.Workdir)

	// Same shape as the other two: the resolve leg has to change files and this
	// grants exactly that. `--mode` takes accept-edits or plan, and plan changes
	// what the model does rather than what it may touch, so a reading leg passes
	// no mode at all and the default denies the write.
	// --dangerously-skip-permissions is the blanket bypass and is never passed.
	if inv.Write {
		args = append(args, "--mode", "accept-edits")
	}
	// Unlike Claude Code, this one takes the schema as a PATH.
	if inv.Schema.Present() {
		args = append(args, "--json-schema", inv.Schema.Path)
	}
	if wanted(inv.Model) {
		args = append(args, "--model", inv.Model)
	}
	if wanted(inv.Effort) {
		args = append(args, "--effort", inv.Effort)
	}
	args = append(args, "--print", inv.Prompt.Argument())

	return a.spec(inv, args), nil
}

// Envelope reads what the child produced (lib/adapters/agy.sh:98-146).
func (a *Agy) Envelope(_ Invocation, res exec.Result) Envelope {
	answer, _ := decodeOrdered(res.Stdout)
	status, _ := answer.member("status").asString()

	if res.ExitCode != 0 || status != "SUCCESS" {
		// The message is chosen on whether one is actually there, not on jq's
		// exit status. `jq … || legs_harness_error "$err"` reads as a fallback
		// and is not one: on an EMPTY stdout jq exits 0 with no output, so the
		// fallback never fires and the error becomes the empty string — which is
		// precisely the case where the only diagnosis lives on stderr.
		message := firstAlternative(answer, "error", "response")
		if message == "" {
			message = HarnessError(res.Stderr)
		}
		if message == "" {
			message = "agy exited " + itoa(res.ExitCode) + " with no output on either stream"
		}
		return failed(a.Name(), runlog.Redact(message))
	}

	// Buckets summed from the parts, cache reads included. The vendor's own
	// total_tokens excludes cache reads — on the measured run it reported 48,162
	// of the 133,830 the parts sum to, dropping 64 per cent of the work the leg
	// did — so no vendor total is read at all.
	return succeeded(a.Name(), vendorEndpoint, agyPayload(res.Stdout), ParseAgy(res.Stdout))
}

// agyPayload is `.structured_output // (.response | fromjson? // null)`.
//
// structured_output is the parsed object when a schema was given. The response
// string is the same JSON, and parsing it is the fallback for a run with no
// schema rather than a second-guess of the first.
func agyPayload(stdout []byte) json.RawMessage {
	if structured, ok := alternativeValue(rawMember(stdout, "structured_output")); ok {
		if payload, parsed := parseJSON(string(structured)); parsed {
			return payload
		}
	}
	response, ok := rawMember(stdout, "response")
	if !ok {
		return nil
	}
	var text string
	if err := json.Unmarshal(response, &text); err != nil {
		// `fromjson` on anything but a string is an error, and `?` swallows it.
		return nil
	}
	payload, parsed := parseJSON(text)
	if !parsed {
		return nil
	}
	return payload
}
