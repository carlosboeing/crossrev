package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// forwardedFlag is one flag the composite action appends to the argument list
// it runs `bin/crossrev` with (action.yml:84-89).
type forwardedFlag struct {
	name      string
	takesArgs bool
}

// actionForwards matches `args+=(--pr "${{ inputs.pr }}")` and
// `args+=(--no-tips)`. The second capture is empty for a flag that carries no
// value, which is how the test learns which of the two shapes each flag is
// without being told.
var actionForwards = regexp.MustCompile(`args\+=\((--[a-z-]+)([^)]*)\)`)

// actionValues is the value each forwarded flag is exercised with. A flag added
// to action.yml with no entry here fails the test by name rather than being
// skipped.
var actionValues = map[string]string{
	"--pr":      "1",
	"--harness": "codex",
	"--trigger": "automatic",
}

func readActionYAML(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../action.yml")
	if err != nil {
		t.Fatalf("reading action.yml: %v", err)
	}
	return string(raw)
}

// TestFlagsMatchTheActionForwards is the drift guard between action.yml and
// this parser.
//
// The composite action forwards a fixed set of flags to whichever leg it was
// told to run (action.yml:84-89), and every one of those legs has its own
// argument loop. A flag added to the action and to four of the five loops
// fails only on the fifth, in CI, on somebody else's repository. So the flags
// and the legs are both read out of action.yml here, and each pairing is
// parsed.
func TestFlagsMatchTheActionForwards(t *testing.T) {
	yaml := readActionYAML(t)

	var flags []forwardedFlag
	for _, m := range actionForwards.FindAllStringSubmatch(yaml, -1) {
		flags = append(flags, forwardedFlag{name: m[1], takesArgs: strings.TrimSpace(m[2]) != ""})
	}
	if len(flags) == 0 {
		t.Fatal("no forwarded flag was found in action.yml, so this rule proves nothing")
	}

	legs := actionLegs(t, yaml)
	if len(legs) == 0 {
		t.Fatal("no leg was found in the action's leg input, so this rule proves nothing")
	}

	var args []string
	for _, flag := range flags {
		args = append(args, flag.name)
		if !flag.takesArgs {
			continue
		}
		value, ok := actionValues[flag.name]
		if !ok {
			t.Fatalf("action.yml forwards %s and this test has no value to exercise it with", flag.name)
		}
		args = append(args, value)
	}

	for _, leg := range legs {
		t.Run(leg, func(t *testing.T) {
			io, _, errs := captureIO()
			got, err := Parse(append([]string{leg}, args...), io, nil)
			if err != nil {
				t.Fatalf("the action forwards %q to %s and the parser refused it: %v\n%s", args, leg, err, errs)
			}
			if string(got.Command) != leg {
				t.Errorf("command = %q, want %q", got.Command, leg)
			}
		})
	}
}

// actionLegs reads the legs out of the description of the action's `leg` input
// rather than repeating them here, so renaming one in the action fails the
// pairing rather than leaving it unexercised.
func actionLegs(t *testing.T, yaml string) []string {
	t.Helper()
	known := make(map[string]bool)
	for _, name := range AllCommands() {
		known[string(name)] = true
	}

	description := ""
	lines := strings.Split(yaml, "\n")
	for i, line := range lines {
		if line != "  leg:" {
			continue
		}
		for _, body := range lines[i+1:] {
			if !strings.HasPrefix(body, "    ") {
				break
			}
			if trimmed := strings.TrimSpace(body); strings.HasPrefix(trimmed, "description:") {
				description = strings.TrimPrefix(trimmed, "description:")
				break
			}
		}
		break
	}
	if description == "" {
		t.Fatal("action.yml has no description on its leg input to read the legs from")
	}

	var legs []string
	for _, word := range strings.FieldsFunc(description, func(r rune) bool {
		return r < 'a' || r > 'z'
	}) {
		if known[word] {
			legs = append(legs, word)
		}
	}
	return legs
}

// TestFlagsHarnessHintNamesWhatIsInstalled pins the two shapes of the
// `--harness` hint. Bash renders the list only when jq is present and
// `harness_names` answers (lib/run.sh:926-931, :1742-1747); here the caller
// hands the names in, and an empty list is the fallback.
func TestFlagsHarnessHintNamesWhatIsInstalled(t *testing.T) {
	for _, tc := range []struct {
		name      string
		harnesses []string
		want      string
	}{
		{"with the list", []string{"claude", "codex"}, "unknown option for review: --bogus"},
		{"without it", nil, "unknown option for review: --bogus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			io, _, errs := captureIO()
			_, err := Parse([]string{"review", "--bogus"}, io, tc.harnesses)
			if err == nil {
				t.Fatal("Parse accepted --bogus")
			}
			printed := errs.String()
			if !strings.Contains(printed, tc.want) {
				t.Errorf("stderr = %q, want it to name the option", printed)
			}
			hint := "--harness <harness>"
			if len(tc.harnesses) > 0 {
				hint = "--harness <one of: " + strings.Join(tc.harnesses, "|") + ">"
			}
			if !strings.Contains(printed, hint) {
				t.Errorf("stderr = %q, want it to carry %q", printed, hint)
			}
		})
	}
}
