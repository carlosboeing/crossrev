package harness_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// harnessErrorCases are what legs_harness_error is asked for on a real leg.
//
// Each is a whole stderr capture, because the function's rules are about
// position: the last two lines that look like a diagnosis, or the last three
// lines when none does.
var harnessErrorCases = []struct {
	name   string
	stderr string
}{
	{name: "empty", stderr: ""},
	{name: "one diagnosis line", stderr: "banner v1\nworkdir /tmp\nError: unauthorized\n"},
	{
		// Harnesses retry, and the final message is the one that stuck — codex
		// reports the same 401 nine times and only the last carries the reason
		// phrase.
		name:   "the last two of many",
		stderr: "Error: one\nError: two\nError: three\nError: four\n",
	},
	{
		// The banner is always at the top and never worth the budget, so the
		// fallback is the tail rather than the head.
		name:   "no keyword anywhere",
		stderr: "one\ntwo\nthree\nfour\nfive\n",
	},
	{name: "fewer lines than the tail asks for", stderr: "only this\n"},
	{name: "no trailing newline", stderr: "banner\nfatal: cut short"},
	{name: "a keyword in the middle of a word", stderr: "reinvalidated\nplain\n"},
	{name: "mixed case", stderr: "banner\nFATAL: shouted\n"},
	{name: "a blank line before the tail", stderr: "one\n\nthree\n"},
	{
		// The cap takes the END, and a cut that lands mid-line drops that
		// partial line — unless dropping it would leave nothing.
		name:   "over the cap, two lines",
		stderr: "Error: " + strings.Repeat("x", 500) + "\nfatal: " + strings.Repeat("y", 30) + "\n",
	},
	{
		name:   "over the cap on one line",
		stderr: "Error: " + strings.Repeat("z", 900) + "\n",
	},
	{
		name:   "over the cap with a short tail line",
		stderr: "Error: " + strings.Repeat("x", 500) + "\nfatal: " + strings.Repeat("y", 500) + "\n",
	},
	{name: "carriage returns", stderr: "banner\r\nError: with a carriage return\r\n"},
}

// The Go answer is the Bash answer, measured rather than described.
//
// legs_harness_error lives in lib/legs.sh rather than in one of the adapters,
// and every adapter calls it for the message a failing leg reports. It is run
// here rather than frozen because it takes a whole file and its rules are about
// position, so a frozen vector would freeze one shape of capture.
func TestHarnessErrorMatchesTheShell(t *testing.T) {
	for _, tool := range []string{"bash", "grep", "tail"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH, so the shell side cannot be run", tool)
		}
	}

	// Nothing is spliced into this script: the capture arrives as a file the
	// test wrote, and its path arrives as an argument.
	const script = `
set -uo pipefail
ROOT="$1"
export ROOT
# shellcheck source=/dev/null
source "$ROOT/lib/ui.sh"
# shellcheck source=/dev/null
source "$ROOT/lib/legs.sh"
printf '%s' "$(legs_harness_error "$2")"
`

	for _, tt := range harnessErrorCases {
		t.Run(tt.name, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "stderr")
			if err := os.WriteFile(capture, []byte(tt.stderr), 0o600); err != nil {
				t.Fatalf("writing the capture: %v", err)
			}
			cmd := exec.Command("bash", "-c", script, "bash", repoRoot, capture)
			out, err := cmd.Output()
			if err != nil && len(out) == 0 && tt.stderr != "" {
				t.Fatalf("running legs_harness_error: %v", err)
			}
			if got := harness.HarnessError([]byte(tt.stderr)); got != string(out) {
				t.Errorf("Go  = %q\nBash = %q", got, string(out))
			}
		})
	}
}

// A Refusal reports itself as its sentinel, carries both halves of a ui_die, and
// unwraps to whatever caused it.
func TestRefusalErrorsAreAskable(t *testing.T) {
	underlying := errors.New("the disk is full")
	refusal := &harness.Refusal{
		Reason: "the isolation config could not be written",
		Action: "Check that the scratch directory is writable.",
		Kind:   harness.ErrScratch,
		Err:    underlying,
	}

	if refusal.Error() != refusal.Reason {
		t.Errorf("Error() = %q, want the reason", refusal.Error())
	}
	if !errors.Is(refusal, harness.ErrScratch) {
		t.Error("the refusal does not match its own sentinel")
	}
	if errors.Is(refusal, harness.ErrNotInstalled) {
		t.Error("the refusal matches a sentinel it was not minted under")
	}
	if !errors.Is(refusal, underlying) {
		t.Error("the refusal does not unwrap to what caused it")
	}
}

// The sentinels are distinct, so a caller asking about one cannot be answered
// by another.
func TestSentinelsAreDistinct(t *testing.T) {
	sentinels := []error{
		harness.ErrNotInstalled,
		harness.ErrEndpointUnsupported,
		harness.ErrEndpointToken,
		harness.ErrHardening,
		harness.ErrSchemaUnavailable,
		harness.ErrScratch,
		harness.ErrEndpointLeaked,
		harness.ErrModelsConverged,
		harness.ErrDescriptor,
		harness.ErrNotJSON,
	}
	for i := range sentinels {
		for j := range sentinels {
			if i != j && errors.Is(sentinels[i], sentinels[j]) {
				t.Errorf("%v and %v are the same sentinel", sentinels[i], sentinels[j])
			}
		}
	}
}
