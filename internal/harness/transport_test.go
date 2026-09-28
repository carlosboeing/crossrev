package harness_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// A 200 KiB prompt stays off the argument vector where the descriptor says
// stdin, on both legs: the prompt travels as the child's stdin instead, and
// no codex or claude argument may exceed 4 KiB — except claude's
// --json-schema value. Claude Code takes the schema inline as a JSON string
// (a path fails with a parse error, claude.go), and stdin already holds the
// prompt, so the real leg schemas travel there at 11,720 and 5,734 bytes by
// design. The test exercises those real schemas and pins that one value to
// them. A stdin holding the prompt reaches EOF after it, so the open-stdin
// block the adapters used to close with </dev/null cannot recur.
func TestStdinTransportKeepsLongPromptsOffArgv(t *testing.T) {
	doc := descriptors(t)

	for _, name := range []string{"codex", "claude"} {
		for _, write := range []bool{false, true} {
			leg := "review"
			if write {
				leg = "resolve"
			}
			t.Run(name+"/"+leg, func(t *testing.T) {
				adapter, known := harness.For(doc, name)
				if !known {
					t.Fatalf("the descriptor names no %s adapter", name)
				}
				inv := legSchema(t, longPromptInvocation(t, name, write), write)

				spec, err := adapter.Spec(inv)
				if err != nil {
					t.Fatalf("building the spec: %v", err)
				}
				for i, arg := range spec.Args {
					if name == "claude" && i > 0 && spec.Args[i-1] == "--json-schema" {
						// The one exemption: see the test comment.
						continue
					}
					if len(arg) > maxArgBytes {
						t.Errorf("an argument carries %d bytes, over the %d-byte ceiling", len(arg), maxArgBytes)
					}
				}
				if got := string(spec.Stdin); got != inv.Prompt.Argument() {
					t.Errorf("stdin carries %d bytes, want the %d-byte prompt", len(got), len(inv.Prompt.Argument()))
				}
				switch name {
				case "codex":
					// `codex exec` reads the prompt from stdin when the
					// positional prompt is `-` rather than text.
					if last := spec.Args[len(spec.Args)-1]; last != "-" {
						t.Errorf("the last argument carries %d bytes, want the stdin marker -", len(last))
					}
				case "claude":
					if !slices.Contains(spec.Args, "-p") {
						t.Errorf("the invocation lost its -p: %v", spec.Args)
					}
					if slices.Contains(spec.Args, inv.Prompt.Text) {
						t.Error("the prompt is still a positional argument; -p takes it from stdin")
					}
					if !hasFlagPair(spec.Args, "--json-schema", inv.Schema.Argument()) {
						t.Error("the --json-schema value is not the real leg schema")
					}
				}
			})
		}
	}
}

// Stdin carries the prompt as the shell spelled it: the adapters used to pass
// `"$(cat "$prompt_file")"` on argv, and command substitution removes every
// trailing newline. The transport changes; the bytes the model reads do not.
func TestStdinTransportCarriesThePromptAsTheShellSpelledIt(t *testing.T) {
	doc := descriptors(t)

	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			adapter, known := harness.For(doc, name)
			if !known {
				t.Fatalf("the descriptor names no %s adapter", name)
			}
			inv := invocation(t, name, false)
			text := inv.Prompt.Text + "\n\n"
			if err := os.WriteFile(inv.Prompt.Path, []byte(text), 0o600); err != nil {
				t.Fatalf("rewriting the prompt: %v", err)
			}
			inv.Prompt.Text = text

			spec, err := adapter.Spec(inv)
			if err != nil {
				t.Fatalf("building the spec: %v", err)
			}
			if got, want := string(spec.Stdin), inv.Prompt.Argument(); got != want {
				t.Errorf("stdin = %q, want the prompt with its trailing newlines removed %q", got, want)
			}
		})
	}
}

// The other three transports keep their shape under the same 200 KiB prompt:
// grok names the prompt file, agy and opencode still pass the prompt on argv,
// and none of them holds stdin open with anything on it. agy's review leg
// wraps the prompt in its no-commands directive, so only its resolve leg
// carries the prompt verbatim.
func TestFileAndArgvTransportsKeepTheirShape(t *testing.T) {
	doc := descriptors(t)

	for _, name := range []string{"grok", "agy", "opencode"} {
		for _, write := range []bool{false, true} {
			leg := "review"
			if write {
				leg = "resolve"
			}
			t.Run(name+"/"+leg, func(t *testing.T) {
				adapter, known := harness.For(doc, name)
				if !known {
					t.Fatalf("the descriptor names no %s adapter", name)
				}
				inv := longPromptInvocation(t, name, write)

				spec, err := adapter.Spec(inv)
				if err != nil {
					t.Fatalf("building the spec: %v", err)
				}
				if spec.Stdin != nil {
					t.Errorf("stdin carries %d bytes; only the stdin transports hold the prompt there", len(spec.Stdin))
				}
				switch name {
				case "grok":
					if !hasFlagPair(spec.Args, "--prompt-file", inv.Prompt.Path) {
						t.Errorf("the prompt file is not named: %v", spec.Args)
					}
					if slices.Contains(spec.Args, inv.Prompt.Text) {
						t.Error("the prompt text reached argv; it travels by file")
					}
				case "agy":
					at := slices.Index(spec.Args, "--print")
					if at < 0 {
						t.Fatalf("the invocation carries no --print: %v", spec.Args)
					}
					got := spec.Args[at+1]
					if write {
						if got != inv.Prompt.Argument() {
							t.Error("the resolve prompt is not the --print value")
						}
						break
					}
					if !strings.HasPrefix(got, "You have no shell command tool") {
						t.Error("the review prompt does not open with the no-commands directive")
					}
					if !strings.Contains(got, inv.Prompt.Argument()) {
						t.Error("the review prompt lost the prompt inside its directive")
					}
					if !strings.HasSuffix(got, "answer anyway.") {
						t.Error("the review prompt does not close with the no-commands directive")
					}
				case "opencode":
					last := spec.Args[len(spec.Args)-1]
					if !strings.HasPrefix(last, inv.Prompt.Text) {
						t.Error("the composed prompt does not open with the prompt text")
					}
				}
			})
		}
	}
}

// The descriptor says how each harness is handed a prompt, and each adapter
// has to do what its own entry says.
func TestPromptTransportMatchesTheDescriptor(t *testing.T) {
	doc := descriptors(t)

	for _, name := range doc.Names() {
		t.Run(name, func(t *testing.T) {
			adapter, _ := harness.For(doc, name)
			inv := invocation(t, name, false)

			spec, err := adapter.Spec(inv)
			if err != nil {
				t.Fatalf("building the spec: %v", err)
			}
			entry, _ := doc.For(name)
			switch entry.PromptTransport {
			case "stdin":
				if got := string(spec.Stdin); got != inv.Prompt.Argument() {
					t.Error("a stdin harness is handed the prompt on stdin")
				}
				if slices.Contains(spec.Args, inv.Prompt.Text) {
					t.Error("a stdin harness carries none of the prompt on argv")
				}
			case "file":
				if !slices.Contains(spec.Args, inv.Prompt.Path) {
					t.Error("a file harness is handed the prompt path")
				}
				if slices.Contains(spec.Args, inv.Prompt.Text) {
					t.Error("a file harness carries none of the prompt text on argv")
				}
			case "argv":
				if !strings.Contains(strings.Join(spec.Args, "\n"), inv.Prompt.Text) {
					t.Error("an argv harness carries the prompt on argv")
				}
			default:
				t.Fatalf("the descriptor names an unknown prompt transport %q", entry.PromptTransport)
			}
		})
	}
}

// The claude stub reads the prompt from stdin and falls back to the last argv
// entry, so a direct stub invocation with stdin closed keeps working. This is
// the fallback half: argv prompt, nil stdin, and the canned payload answered.
func TestStdinTransportFallsBackToArgv(t *testing.T) {
	adapter := claudeAdapter(t)
	inv := invocation(t, "claude", false)

	spec := exec.Spec{
		Path: "claude",
		Args: []string{"-p", "--output-format", "json", inv.Prompt.Text},
		Dir:  inv.Workdir,
		Env:  inv.Env,
	}
	res := runAgainstStub(t, spec, payloadFile(t, "CROSSREV_HARNESS_PAYLOAD", cannedPayload))
	if res.Err != nil {
		t.Fatalf("running the stub: %v", res.Err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("the stub refused the invocation: exit %d, stderr %s", res.ExitCode, res.Stderr)
	}
	envelope := adapter.Envelope(inv, res)
	if !envelope.OK {
		t.Fatalf("the envelope reports a failure: %+v", envelope)
	}
	if got := string(envelope.Payload); got != cannedPayload {
		t.Errorf("payload = %s, want %s", got, cannedPayload)
	}
}

const (
	// longPromptBytes is the prompt size the transport ceiling is measured
	// against: large enough that no argv entry may hold it whole.
	longPromptBytes = 200 * 1024
	// maxArgBytes is the longest argument a stdin transport may build.
	maxArgBytes = 4 * 1024
)

// legSchema replaces the fixture's small schema with the real schema the
// leg hands its harness: the findings schema on a review leg, the resolve
// schema on a resolve leg. Both exceed the 4 KiB ceiling, so a passing test
// proves the ceiling against what production actually passes.
func legSchema(t *testing.T, inv harness.Invocation, write bool) harness.Invocation {
	t.Helper()
	schema := validate.FindingsSchema()
	if write {
		schema = validate.ResolveSchema()
	}
	if err := os.WriteFile(inv.Schema.Path, schema, 0o600); err != nil {
		t.Fatalf("writing the leg schema: %v", err)
	}
	inv.Schema.Text = string(schema)
	return inv
}

// longPromptInvocation is invocation with a 200 KiB prompt, written to the
// prompt file as well so the fixture stays coherent.
func longPromptInvocation(t *testing.T, name string, write bool) harness.Invocation {
	t.Helper()
	inv := invocation(t, name, write)
	big := strings.Repeat("p", longPromptBytes)
	if err := os.WriteFile(inv.Prompt.Path, []byte(big), 0o600); err != nil {
		t.Fatalf("writing the long prompt: %v", err)
	}
	inv.Prompt.Text = big
	return inv
}
