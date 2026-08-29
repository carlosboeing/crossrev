// codex.go — lib/adapters/codex.sh.
//
// Same contract as the claude adapter: payload plus execution metadata, no
// GitHub credential in the environment.
//
// The event stream reports no model. `codex exec --json` carries token counts
// on turn.completed and nothing identifying the model, so ModelReported comes
// from the rollout this session wrote — matched by the session id the events
// carry, never by whichever file is newest — and stays nil when no rollout
// matches. A miss never fails a leg whose answer already exists. Halting
// whenever the model is unreported would be the stricter rule and it is the
// wrong one: it would disqualify this adapter on the evidence that Codex does
// not emit the field. Layer one of the divergence guard already catches the
// failures reachable from a config mistake.

package harness

import (
	"os"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/runlog"
)

// Codex is the adapter for Codex.
type Codex struct{ base }

var _ Adapter = (*Codex)(nil)

// NotInstalled is lib/adapters/codex.sh:22-24, which is the one adapter that
// reads its install hint out of the descriptor rather than spelling a URL.
func (a *Codex) NotInstalled() *Refusal {
	return a.notInstalled("Install it from " + a.descriptor.Install.Hint +
		", or point this leg at another harness with --harness.")
}

// Spec builds the child process (lib/adapters/codex.sh:26-95).
func (a *Codex) Spec(inv Invocation) (exec.Spec, error) {
	if inv.Endpoint.Named() {
		return exec.Spec{}, a.endpointRefusal(inv.Endpoint, endpointHostName, "")
	}
	if inv.PayloadPath == "" {
		return exec.Spec{}, &Refusal{
			Reason: "the codex adapter was given nowhere to write the payload",
			Action: "`codex exec -o` writes the answer to a file, so the orchestrator has to name one.",
			Kind:   ErrScratch,
		}
	}

	// `--json` streams events on stdout while `-o` still writes the final
	// payload to a file, so this buys the token counts without changing where
	// the payload comes from. Codex carries them on turn.completed and nothing
	// else does.
	args := []string{"exec", "--skip-git-repo-check", "--json", "-o", inv.PayloadPath}

	hardening := a.doc.SandboxArgs(a.Name())
	if len(hardening) == 0 {
		return exec.Spec{}, &Refusal{
			Reason: "could not resolve hardening arguments for codex",
			Action: "The descriptor's sandbox_args for codex must name --ignore-user-config. Refusing to run codex unhardened.",
			Kind:   ErrHardening,
		}
	}
	// The Bash appends the joined string as ONE argv entry
	// (lib/adapters/codex.sh:56), which works while the list holds one element
	// and would pass `--a --b` as a single argument if a second were added. Each
	// element is its own argument here, which is the same argv today.
	args = append(args, hardening...)

	// `codex exec` sandboxes to read-only by default, so a resolve leg on this
	// harness could verify a finding and then fail to apply the fix.
	// workspace-write confines writes to the checkout, which is exactly what the
	// leg needs; danger-full-access and --dangerously-bypass-approvals-and-sandbox
	// are on the wrong side of the line between editing files and running
	// arbitrary commands.
	//
	// A reading leg is pinned read-only rather than left to the default, because
	// codex reads a user config that can set one. Saying it costs nothing and
	// means a machine-level setting cannot quietly hand the review leg a
	// writable tree.
	if inv.Write {
		args = append(args, "--sandbox", "workspace-write")
	} else {
		args = append(args, "--sandbox", "read-only")
	}

	// Codex takes the schema as a FILE PATH, where Claude Code takes it inline.
	if inv.Schema.Present() {
		args = append(args, "--output-schema", inv.Schema.Path)
	}
	if wanted(inv.Model) {
		args = append(args, "-m", inv.Model)
	}
	// No --effort flag; it is a config override.
	if wanted(inv.Effort) {
		args = append(args, "-c", "model_reasoning_effort="+inv.Effort)
	}
	args = append(args, inv.Prompt.Argument())

	return a.spec(inv, args), nil
}

// Envelope reads what the child produced (lib/adapters/codex.sh:99-166).
func (a *Codex) Envelope(inv Invocation, res exec.Result) Envelope {
	// The error is deliberately dropped: a failed read answers a nil payload,
	// so the length test below already covers it, and a second clause for the
	// same case would be error handling that cannot change an outcome.
	payload, _ := os.ReadFile(inv.PayloadPath) //nolint:gosec // the orchestrator named this path
	if res.ExitCode != 0 || len(payload) == 0 {
		// No "exited N with no output" fallback here, unlike the other four:
		// the Bash builds this message from stderr alone, so an empty stderr
		// answers the empty string (lib/adapters/codex.sh:100).
		return failed(a.Name(), runlog.Redact(HarnessError(res.Stderr)))
	}

	compact, parsed := parseJSON(string(payload))
	if !parsed {
		compact = nil
	}

	usage := ParseCodexEvents(res.Stdout)
	envelope := succeeded(a.Name(), vendorEndpoint, compact, usage)

	// The event stream names neither model nor effort; this session's rollout
	// carries both. Any failure here is a miss — nil and nil — never a failed
	// leg: the payload has already been read by the time this runs.
	model, effort := ReadCodexRollout(inv.CodexHome, CodexSessionID(res.Stdout))
	if model != "" {
		envelope.ModelReported = &model
	}
	if effort != "" {
		envelope.EffortReported = &effort
	}
	return envelope
}
