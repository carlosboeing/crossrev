package harness_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// The complete argument vector, element for element, for every adapter and
// both legs.
//
// # Why the whole sequence and not a flag pair
//
// The tests around this one assert `Args[:3]`, one flag's value, and the last
// element. A reordering in the MIDDLE of an argv passes all of them, and argv
// order is not cosmetic for these CLIs: codex reads `--sandbox` positionally
// against `-o`, grok's `--allow`/`--deny` pairs must follow their `--sandbox`,
// and Antigravity's own flag order is called out in lib/adapters/agy.sh:56 as a
// per-CLI fact. The order in this table was proved by running the five Bash
// adapters with argument logging and diffing; this test is what keeps it.
//
// # The expected vectors are the Bash lines, with their citations
//
// Each row below is the concatenation of that adapter's `args=(…)` and
// `args+=(…)` lines in source order, plus whatever the invocation line appends.
// A placeholder stands for a value the invocation supplies; resolvePlaceholder
// is the only place they are turned back into strings, so a row reads as the
// shell line it came from.
func TestEveryAdapterBuildsTheWholeArgv(t *testing.T) {
	doc := descriptors(t)

	tests := []struct {
		harness string
		write   bool
		// where cites the Bash lines this vector is assembled from.
		where string
		want  []string
	}{
		{
			harness: "claude", write: false,
			where: "lib/adapters/claude.sh:23,49,54,55,111",
			want: []string{"-p", "--output-format", "json",
				"--json-schema", "<schema-text>", "--model", "<model>", "--effort", "high", "<prompt>"},
		},
		{
			harness: "claude", write: true,
			where: "lib/adapters/claude.sh:23,43,49,54,55,111",
			want: []string{"-p", "--output-format", "json",
				"--permission-mode", "acceptEdits",
				"--json-schema", "<schema-text>", "--model", "<model>", "--effort", "high", "<prompt>"},
		},
		{
			harness: "codex", write: false,
			where: "lib/adapters/codex.sh:50,56,70,74,75,77,95",
			want: []string{"exec", "--skip-git-repo-check", "--json", "-o", "<payload>",
				"--ignore-user-config", "--sandbox", "read-only",
				"--output-schema", "<schema-path>", "-m", "<model>",
				"-c", "model_reasoning_effort=high", "<prompt>"},
		},
		{
			harness: "codex", write: true,
			where: "lib/adapters/codex.sh:50,56,68,74,75,77,95",
			want: []string{"exec", "--skip-git-repo-check", "--json", "-o", "<payload>",
				"--ignore-user-config", "--sandbox", "workspace-write",
				"--output-schema", "<schema-path>", "-m", "<model>",
				"-c", "model_reasoning_effort=high", "<prompt>"},
		},
		{
			harness: "agy", write: false,
			where: "lib/adapters/agy.sh:42,56,65,66,67,94",
			want: []string{"--output-format", "json", "--disable-slash-commands",
				"--add-dir", "<workdir>", "--json-schema", "<schema-path>",
				"--model", "<model>", "--effort", "high", "--print", "<prompt>"},
		},
		{
			harness: "agy", write: true,
			where: "lib/adapters/agy.sh:42,56,63,65,66,67,94",
			want: []string{"--output-format", "json", "--disable-slash-commands",
				"--add-dir", "<workdir>", "--mode", "accept-edits",
				"--json-schema", "<schema-path>", "--model", "<model>",
				"--effort", "high", "--print", "<prompt>"},
		},
		{
			harness: "grok", write: false,
			where: "lib/adapters/grok.sh:36,46,49,50,51,52,79",
			want: []string{"--output-format", "json", "--permission-mode", "dontAsk",
				"--sandbox", "read-only", "--deny", "Edit", "--deny", "Write",
				"--json-schema", "<schema-text>", "--model", "<model>",
				"--reasoning-effort", "high", "--prompt-file", "<prompt-path>"},
		},
		{
			harness: "grok", write: true,
			where: "lib/adapters/grok.sh:36,44,49,50,51,52,79",
			want: []string{"--output-format", "json", "--permission-mode", "dontAsk",
				"--sandbox", "workspace", "--allow", "Edit", "--allow", "Write",
				"--json-schema", "<schema-text>", "--model", "<model>",
				"--reasoning-effort", "high", "--prompt-file", "<prompt-path>"},
		},
		{
			// The write flag reaches opencode through the isolation config it
			// writes, never through argv, so both legs share one vector. That
			// is asserted rather than assumed: an adapter that started spelling
			// the difference in argv would fail one of these two rows.
			harness: "opencode", write: false,
			where: "lib/adapters/opencode.sh:155,156,157,187",
			want: []string{"run", "--pure", "--format", "json", "--dir", "<workdir>",
				"--model", "<model>", "--variant", "high", "<prompt-with-schema>"},
		},
		{
			harness: "opencode", write: true,
			where: "lib/adapters/opencode.sh:155,156,157,187",
			want: []string{"run", "--pure", "--format", "json", "--dir", "<workdir>",
				"--model", "<model>", "--variant", "high", "<prompt-with-schema>"},
		},
	}

	seen := map[string]bool{}
	for _, tt := range tests {
		leg := "review"
		if tt.write {
			leg = "resolve"
		}
		t.Run(tt.harness+"/"+leg, func(t *testing.T) {
			adapter, known := harness.For(doc, tt.harness)
			if !known {
				t.Fatalf("the descriptor names no %s adapter", tt.harness)
			}
			inv := invocation(t, tt.harness, tt.write)
			spec, err := adapter.Spec(inv)
			if err != nil {
				t.Fatalf("building the spec: %v", err)
			}

			want := make([]string, 0, len(tt.want))
			for _, element := range tt.want {
				want = append(want, resolvePlaceholder(t, element, inv))
			}
			if !slices.Equal(spec.Args, want) {
				t.Errorf("argv differs from %s\n  got : %s\n  want: %s",
					tt.where, describeArgv(spec.Args, inv), describeArgv(want, inv))
			}
		})
		seen[tt.harness] = true
	}

	// A harness added to the descriptor without a row here would otherwise
	// have no argv assertion at all.
	for _, name := range doc.Names() {
		if !seen[name] {
			t.Errorf("%s has no argv vector in this table", name)
		}
	}
}

// resolvePlaceholder turns one table element into the string the adapter should
// have produced.
//
// The prompt and schema placeholders answer what `"$(cat "$file")"` answers,
// which is the file with its trailing newlines removed — not File.Text.
func resolvePlaceholder(t *testing.T, element string, inv harness.Invocation) string {
	t.Helper()
	switch element {
	case "<prompt>":
		return commandSubstitution(t, inv.Prompt.Path)
	case "<prompt-path>":
		return inv.Prompt.Path
	case "<schema-text>":
		return commandSubstitution(t, inv.Schema.Path)
	case "<schema-path>":
		return inv.Schema.Path
	case "<workdir>":
		return inv.Workdir
	case "<payload>":
		return inv.PayloadPath
	case "<model>":
		return inv.Model
	case "<prompt-with-schema>":
		return opencodeComposedPrompt(t, inv)
	default:
		return element
	}
}

// commandSubstitution is `"$(cat "$file")"`, run by the shell rather than
// reproduced in Go.
//
// Reproducing it would make this test assert the same rule File.Argument
// implements, which proves nothing. Bash is the oracle for what Bash does.
func commandSubstitution(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("bash", "-c", `printf '%s' "$(cat "$1")"`, "bash", path).Output()
	if err != nil {
		t.Fatalf("running the command substitution: %v", err)
	}
	return string(out)
}

// opencodeComposedPrompt is lib/adapters/opencode.sh:100-108 and :187, run by
// the shell: a copy holding the RAW prompt file followed by the schema
// instruction, and only the finished copy passed through `"$(cat …)"`.
//
// The order is the whole point. Trimming the prompt before appending loses the
// prompt file's own trailing newline from the middle of the composed string and
// keeps the block's closing newline at the end, which is the opposite of what
// the shell sends on both counts.
func opencodeComposedPrompt(t *testing.T, inv harness.Invocation) string {
	t.Helper()
	const script = `
set -uo pipefail
prompt_file="$1"; schema_file="$2"
copy="$(mktemp)"
{
  cat "$prompt_file"
  printf '\n\nThis harness does not constrain your output. The answer text itself is what is parsed, so return a single JSON object matching exactly this schema, with no markdown fence and no commentary:\n\n` + "```" + `json\n%s\n` + "```" + `\n' "$(cat "$schema_file")"
} >"$copy"
printf '%s' "$(cat "$copy")"
rm -f "$copy"
`
	out, err := exec.Command("bash", "-c", script, "bash", inv.Prompt.Path, inv.Schema.Path).Output()
	if err != nil {
		t.Fatalf("composing the opencode prompt: %v", err)
	}
	return string(out)
}

// describeArgv renders an argv with the long values named, so a failure reads
// as the sequence rather than as two walls of JSON.
func describeArgv(args []string, inv harness.Invocation) string {
	named := make([]string, 0, len(args))
	for _, argument := range args {
		switch {
		case argument == inv.Workdir:
			named = append(named, "<workdir>")
		case argument == inv.PayloadPath:
			named = append(named, "<payload>")
		case argument == inv.Prompt.Path:
			named = append(named, "<prompt-path>")
		case argument == inv.Schema.Path:
			named = append(named, "<schema-path>")
		case strings.Contains(argument, "```json"):
			named = append(named, fmt.Sprintf("<prompt-with-schema %d bytes ending %q>",
				len(argument), tail(argument)))
		case strings.HasPrefix(argument, "{"):
			named = append(named, "<schema-text>")
		case strings.Contains(argument, " "):
			named = append(named, fmt.Sprintf("<text %d bytes ending %q>", len(argument), tail(argument)))
		default:
			named = append(named, argument)
		}
	}
	return strings.Join(named, " ")
}

func tail(text string) string {
	if len(text) <= 12 {
		return text
	}
	return text[len(text)-12:]
}

// A file's trailing newline never reaches a command line.
//
// Every adapter reads its prompt and its inline schema through
// `"$(cat "$file")"`, and command substitution removes every trailing newline.
// Both shipped schemas end in one, so passing File.Text verbatim sent the model
// a byte the shell never sent — and for opencode that byte lands inside the
// ```json fence in the prompt itself.
func TestFileArgumentDropsTheTrailingNewlineTheShellDrops(t *testing.T) {
	for _, text := range []string{
		"plain",
		"one trailing\n",
		"three trailing\n\n\n",
		"",
		"\n",
		"trailing spaces kept  \n",
		"inner\nnewlines\nkept\n",
	} {
		t.Run(fmt.Sprintf("%q", text), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "document")
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatalf("writing the document: %v", err)
			}
			file := harness.File{Path: path, Text: text}
			if got, want := file.Argument(), commandSubstitution(t, path); got != want {
				t.Errorf("Argument() = %q, and \"$(cat …)\" answers %q", got, want)
			}
		})
	}
}

// The prompt file's own trailing newline survives into the middle of the
// opencode prompt, and the instruction's closing newline does not survive at
// the end.
//
// This is the one adapter that composes rather than passes through, and the
// order decides both bytes. lib/adapters/opencode.sh:100-108 writes a copy that
// is `cat "$prompt_file"` RAW followed by the printf block, and only the copy
// goes through `"$(cat "$leg_prompt")"` at :187. Trimming the prompt first and
// appending after loses one newline from the middle and keeps one at the end —
// the opposite of the shell on both counts, and the fenced ```json block the
// model reads is what carries the difference.
//
// The invocation the rest of this file uses has no trailing newline on its
// prompt, so it cannot see this. A prompt written by lib/prompt.sh does.
func TestOpencodeComposesTheRawPromptBeforeTheSubstitution(t *testing.T) {
	doc := descriptors(t)
	adapter, known := harness.For(doc, "opencode")
	if !known {
		t.Fatal("no opencode adapter")
	}

	for _, promptText := range []string{
		reviewPrompt,
		reviewPrompt + "\n",
		reviewPrompt + "\n\n\n",
	} {
		t.Run(fmt.Sprintf("%q", tail(promptText)), func(t *testing.T) {
			inv := invocation(t, "opencode", false)
			if err := os.WriteFile(inv.Prompt.Path, []byte(promptText), 0o600); err != nil {
				t.Fatalf("rewriting the prompt: %v", err)
			}
			inv.Prompt.Text = promptText

			spec, err := adapter.Spec(inv)
			if err != nil {
				t.Fatalf("building the spec: %v", err)
			}
			got := spec.Args[len(spec.Args)-1]
			want := opencodeComposedPrompt(t, inv)
			if got == want {
				return
			}
			t.Errorf("the composed prompt differs from lib/adapters/opencode.sh:100-108,187\n  got  %d bytes, ending %q\n  want %d bytes, ending %q",
				len(got), tail(got), len(want), tail(want))
			if strings.TrimRight(got, "\n") == strings.TrimRight(want, "\n") {
				t.Log("the difference is only the trailing newlines")
			}
		})
	}
}
