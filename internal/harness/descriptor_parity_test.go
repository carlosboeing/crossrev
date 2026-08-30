package harness_test

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// Every shape the validator can meet, answered by both implementations.
//
// # Why this exists alongside TestValidatorMessagesMatchTheShell
//
// That test walks the mutations tests/test-harnesses.sh builds, and every one
// of them is a document the shell REJECTS — it fails outright when the shell
// accepts one. Three defects lived in exactly that blind spot: a
// `credential.billing` of null or false, which the shell accepts and Go
// refused; a `quarantine` entry that is not a string, which the shell refuses
// and Go accepted; and a literal `null` document, which the shell answers with
// the version sentence. None of them could be expressed as a case there.
//
// So this compares the two answers whatever they are, accepted included, over a
// sweep of malformed shapes rather than a list of known faults.
//
// # Divergences are data here, not silence
//
// Seventeen shapes make jq raise a type error, which harness_validate's
// `2>/dev/null || printf` turns into its unparseable-JSON sentence. Go names
// the fault instead. descriptor.go's header sets out the family and the reason;
// this table is the enforcement, so a NEW divergence fails the test and a
// declared one has to be written down before it passes.
func TestValidatorAnswersTheShellOnEveryShape(t *testing.T) {
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH, so the shell side cannot be run", tool)
		}
	}

	const unparseable = "the descriptor is not parseable JSON"

	// declared maps a case name to the answer the SHELL gives when the two
	// disagree. Go's answer is not pinned here: the point is that the shell
	// reached its type-error sentence and Go reached a real check, and pinning
	// Go's wording would make this a change-detector for message edits the
	// other tests already cover.
	declared := map[string]string{
		"harnesses as a bare string":            unparseable,
		"harnesses as a number":                 unparseable,
		"harnesses as an array of numbers":      unparseable,
		"a harness entry that is not an object": unparseable,
		"not_driven as a bare string":           unparseable,
		"not_driven as a number":                unparseable,
		"not_driven as an array of numbers":     unparseable,
		"secret as a number":                    unparseable,
		"secret as true":                        unparseable,
		"secret as false":                       unparseable,
		"env_names holding a number":            unparseable,
		"staging env as a number":               unparseable,
		"credential as a string":                unparseable,
		"install as a string":                   unparseable,
		"install command as a number":           unparseable,
		"pinned version as a number":            unparseable,
		"quarantine_shared as a string":         unparseable,
	}

	for _, tt := range parityShapes(t) {
		t.Run(tt.name, func(t *testing.T) {
			want := shellValidate(t, tt.document)
			got := harness.Validate(tt.document)
			if got == want {
				if _, isDeclared := declared[tt.name]; isDeclared {
					t.Errorf("%q is declared as a divergence and no longer is; remove it from the table and from descriptor.go's header", tt.name)
				}
				return
			}
			shellAnswer, isDeclared := declared[tt.name]
			if !isDeclared {
				t.Fatalf("undeclared divergence\n  bash: %q\n  go  : %q\nfix it, or declare it in this table and in descriptor.go's header", want, got)
			}
			if want != shellAnswer {
				t.Errorf("the shell now answers %q, and the declaration records %q", want, shellAnswer)
			}
		})
	}
}

// shellValidate runs harness_validate over the real lib/harnesses.sh.
//
// Nothing is spliced into the script: the descriptor arrives on stdin and the
// repository root arrives as an argument.
func shellValidate(t *testing.T, descriptor []byte) string {
	t.Helper()
	const script = `
set -uo pipefail
ROOT="$1"
export ROOT
# shellcheck source=/dev/null
source "$ROOT/lib/ui.sh"
# shellcheck source=/dev/null
source "$ROOT/lib/harnesses.sh"
harness_validate "$(cat)"
`
	cmd := exec.Command("bash", "-c", script, "bash", repoRoot)
	cmd.Stdin = strings.NewReader(string(descriptor))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running harness_validate: %v", err)
	}
	// `jq -r` terminates its answer with a newline, and every caller reads the
	// function through `$(…)`, which strips it (lib/harnesses.sh:101).
	return strings.TrimSuffix(string(out), "\n")
}

type parityShape struct {
	name     string
	document []byte
}

// parityShapes is the shipped descriptor with one edit applied, plus the
// non-object roots that carry no descriptor at all.
//
// The shapes are chosen by TYPE rather than by fault: for each field the
// validator reads, the values a hand-edited descriptor plausibly holds — a
// null, a false, a number, a bare string, an object where an array belongs. A
// list of known faults is what the mutations above already are.
func parityShapes(t *testing.T) []parityShape {
	t.Helper()

	base := func() map[string]any {
		var document map[string]any
		if err := json.Unmarshal(harness.DescriptorJSON(), &document); err != nil {
			t.Fatalf("decoding the descriptor: %v", err)
		}
		return document
	}
	harnessAt := func(document map[string]any, at int) map[string]any {
		return document["harnesses"].([]any)[at].(map[string]any)
	}
	credentialAt := func(document map[string]any, at int) map[string]any {
		return harnessAt(document, at)["credential"].(map[string]any)
	}
	stagingAt := func(document map[string]any, at int) map[string]any {
		staging, ok := credentialAt(document, at)["staging"].(map[string]any)
		if !ok {
			staging = map[string]any{"kind": "none"}
			credentialAt(document, at)["staging"] = staging
		}
		return staging
	}
	installAt := func(document map[string]any, at int) map[string]any {
		return harnessAt(document, at)["install"].(map[string]any)
	}
	appendTo := func(list any, value any) []any { return append(list.([]any), value) }

	var shapes []parityShape
	raw := func(name, document string) {
		shapes = append(shapes, parityShape{name: name, document: []byte(document)})
	}
	edit := func(name string, apply func(map[string]any)) {
		document := base()
		apply(document)
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatalf("encoding %s: %v", name, err)
		}
		shapes = append(shapes, parityShape{name: name, document: encoded})
	}

	raw("the shipped descriptor", string(harness.DescriptorJSON()))
	raw("a literal null document", "null")
	raw("a literal array document", "[1,2]")
	raw("a literal string document", `"str"`)
	raw("a literal number document", "5")
	raw("a literal true document", "true")
	raw("an empty object document", "{}")

	edit("version as a string", func(d map[string]any) { d["version"] = "1" })
	edit("version as a fraction", func(d map[string]any) { d["version"] = 1.5 })
	edit("version absent", func(d map[string]any) { delete(d, "version") })
	edit("version as null", func(d map[string]any) { d["version"] = nil })

	edit("harnesses as a bare string", func(d map[string]any) { d["harnesses"] = "review" })
	edit("harnesses as a number", func(d map[string]any) { d["harnesses"] = float64(3) })
	edit("harnesses as an array of numbers", func(d map[string]any) { d["harnesses"] = []any{1.0, 2.0} })
	edit("harnesses as null", func(d map[string]any) { d["harnesses"] = nil })
	edit("harnesses as false", func(d map[string]any) { d["harnesses"] = false })
	edit("a harness entry that is not an object", func(d map[string]any) {
		d["harnesses"] = appendTo(d["harnesses"], "x")
	})
	edit("a harness entry with no name", func(d map[string]any) {
		d["harnesses"] = []any{map[string]any{}}
	})
	edit("a harness name that is a number", func(d map[string]any) { harnessAt(d, 0)["name"] = float64(5) })
	edit("a harness name that is null", func(d map[string]any) { harnessAt(d, 0)["name"] = nil })
	edit("a harness name that is absent", func(d map[string]any) { delete(harnessAt(d, 0), "name") })

	edit("not_driven as a bare string", func(d map[string]any) { d["not_driven"] = "x" })
	edit("not_driven as a number", func(d map[string]any) { d["not_driven"] = float64(1) })
	edit("not_driven as an array of numbers", func(d map[string]any) { d["not_driven"] = []any{1.0, 2.0} })
	edit("not_driven as null", func(d map[string]any) { d["not_driven"] = nil })
	edit("not_driven as false", func(d map[string]any) { d["not_driven"] = false })
	edit("not_driven as an object", func(d map[string]any) { d["not_driven"] = map[string]any{} })

	edit("billing as null", func(d map[string]any) { credentialAt(d, 0)["billing"] = nil })
	edit("billing as false", func(d map[string]any) { credentialAt(d, 0)["billing"] = false })
	edit("billing as true", func(d map[string]any) { credentialAt(d, 0)["billing"] = true })
	edit("billing as an empty string", func(d map[string]any) { credentialAt(d, 0)["billing"] = "" })
	edit("billing as a number", func(d map[string]any) { credentialAt(d, 0)["billing"] = float64(5) })
	edit("billing as an object", func(d map[string]any) {
		credentialAt(d, 0)["billing"] = map[string]any{"a": 1.0}
	})
	edit("billing as subscription", func(d map[string]any) { credentialAt(d, 0)["billing"] = "subscription" })

	edit("quarantine holding a number", func(d map[string]any) {
		harnessAt(d, 0)["quarantine"] = appendTo(harnessAt(d, 0)["quarantine"], float64(123))
	})
	edit("quarantine holding false", func(d map[string]any) {
		harnessAt(d, 0)["quarantine"] = appendTo(harnessAt(d, 0)["quarantine"], false)
	})
	edit("quarantine holding true", func(d map[string]any) {
		harnessAt(d, 0)["quarantine"] = appendTo(harnessAt(d, 0)["quarantine"], true)
	})
	edit("quarantine holding null", func(d map[string]any) {
		harnessAt(d, 0)["quarantine"] = appendTo(harnessAt(d, 0)["quarantine"], nil)
	})
	edit("quarantine holding an object", func(d map[string]any) {
		harnessAt(d, 0)["quarantine"] = appendTo(harnessAt(d, 0)["quarantine"], map[string]any{"a": 1.0})
	})
	edit("quarantine holding an array", func(d map[string]any) {
		harnessAt(d, 0)["quarantine"] = appendTo(harnessAt(d, 0)["quarantine"], []any{"x"})
	})
	edit("quarantine as a string", func(d map[string]any) { harnessAt(d, 0)["quarantine"] = "x" })
	edit("quarantine_shared holding a number", func(d map[string]any) {
		d["quarantine_shared"] = appendTo(d["quarantine_shared"], float64(7))
	})
	edit("quarantine_shared holding null", func(d map[string]any) {
		d["quarantine_shared"] = appendTo(d["quarantine_shared"], nil)
	})
	edit("quarantine_shared as a string", func(d map[string]any) { d["quarantine_shared"] = "x" })
	edit("quarantine_shared as null", func(d map[string]any) { d["quarantine_shared"] = nil })
	edit("quarantine_shared as false", func(d map[string]any) { d["quarantine_shared"] = false })
	edit("staging path as a number", func(d map[string]any) { stagingAt(d, 0)["path"] = float64(9) })

	edit("secret as a number", func(d map[string]any) { credentialAt(d, 0)["secret"] = float64(5) })
	edit("secret as true", func(d map[string]any) { credentialAt(d, 0)["secret"] = true })
	edit("secret as false", func(d map[string]any) { credentialAt(d, 0)["secret"] = false })
	edit("secret as null", func(d map[string]any) { credentialAt(d, 0)["secret"] = nil })
	edit("env_names holding a number", func(d map[string]any) {
		credentialAt(d, 0)["env_names"] = []any{float64(5)}
	})
	edit("env_names holding null", func(d map[string]any) { credentialAt(d, 0)["env_names"] = []any{nil} })
	edit("env_names as a string", func(d map[string]any) { credentialAt(d, 0)["env_names"] = "X" })
	edit("staging env as a number", func(d map[string]any) { stagingAt(d, 0)["env"] = float64(2) })

	edit("credential as null", func(d map[string]any) { harnessAt(d, 0)["credential"] = nil })
	edit("credential absent", func(d map[string]any) { delete(harnessAt(d, 0), "credential") })
	edit("credential as a string", func(d map[string]any) { harnessAt(d, 0)["credential"] = "x" })

	edit("legs as null", func(d map[string]any) { harnessAt(d, 0)["legs"] = nil })
	edit("legs as a number", func(d map[string]any) { harnessAt(d, 0)["legs"] = float64(1) })
	edit("legs as an object", func(d map[string]any) { harnessAt(d, 0)["legs"] = map[string]any{} })
	edit("legs holding null", func(d map[string]any) { harnessAt(d, 0)["legs"] = []any{nil} })
	edit("legs holding a number", func(d map[string]any) { harnessAt(d, 0)["legs"] = []any{5.0} })

	edit("install as null", func(d map[string]any) { harnessAt(d, 0)["install"] = nil })
	edit("install absent", func(d map[string]any) { delete(harnessAt(d, 0), "install") })
	edit("install as a string", func(d map[string]any) { harnessAt(d, 0)["install"] = "x" })
	edit("install url as a number", func(d map[string]any) { installAt(d, 0)["url"] = float64(1) })
	edit("install url as false", func(d map[string]any) { installAt(d, 0)["url"] = false })
	edit("install command as a number", func(d map[string]any) { installAt(d, 0)["command"] = float64(1) })
	edit("pinned version as a number", func(d map[string]any) { installAt(d, 0)["pinned_version"] = float64(1) })

	edit("two faults, version before shape", func(d map[string]any) {
		d["version"] = float64(2)
		d["harnesses"] = map[string]any{}
	})
	return shapes
}
