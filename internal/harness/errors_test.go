package harness_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// harnessErrorCases are what the harness-error tail is asked for on a real leg.
//
// Each is a whole stderr capture, because the function's rules are about
// position: the last two lines that look like a diagnosis, or the last three
// lines when none does.
var harnessErrorCases = []struct {
	name   string
	stderr string
	want   string
}{
	{name: "empty", stderr: "", want: ""},
	{name: "one diagnosis line", stderr: "banner v1\nworkdir /tmp\nError: unauthorized\n",
		want: "Error: unauthorized"},
	{
		// Harnesses retry, and the final message is the one that stuck — codex
		// reports the same 401 nine times and only the last carries the reason
		// phrase.
		name:   "the last two of many",
		stderr: "Error: one\nError: two\nError: three\nError: four\n",
		want:   "Error: three\nError: four",
	},
	{
		// The banner is always at the top and never worth the budget, so the
		// fallback is the tail rather than the head.
		name:   "no keyword anywhere",
		stderr: "one\ntwo\nthree\nfour\nfive\n",
		want:   "three\nfour\nfive",
	},
	{name: "fewer lines than the tail asks for", stderr: "only this\n", want: "only this"},
	{name: "no trailing newline", stderr: "banner\nfatal: cut short", want: "fatal: cut short"},
	{name: "a keyword in the middle of a word", stderr: "reinvalidated\nplain\n", want: "reinvalidated"},
	{name: "mixed case", stderr: "banner\nFATAL: shouted\n", want: "FATAL: shouted"},
	{name: "a blank line before the tail", stderr: "one\n\nthree\n", want: "one\n\nthree"},
	{
		// The cap takes the END, and a cut that lands mid-line drops that
		// partial line — unless dropping it would leave nothing.
		name:   "over the cap, two lines",
		stderr: "Error: " + strings.Repeat("x", 500) + "\nfatal: " + strings.Repeat("y", 30) + "\n",
		want:   "fatal: " + strings.Repeat("y", 30),
	},
	{
		name:   "over the cap on one line",
		stderr: "Error: " + strings.Repeat("z", 900) + "\n",
		want:   strings.Repeat("z", 400),
	},
	{
		name:   "over the cap with a short tail line",
		stderr: "Error: " + strings.Repeat("x", 500) + "\nfatal: " + strings.Repeat("y", 500) + "\n",
		want:   strings.Repeat("y", 400),
	},
	{name: "carriage returns", stderr: "banner\r\nError: with a carriage return\r\n",
		want: "Error: with a carriage return\r"},
}

// The tail rules, frozen at the native cutover.
//
// The tail lived in lib/legs.sh as legs_harness_error, and this test ran it on
// every case above and compared the bytes. The shell is removed, so the answers
// measured then are frozen here: the last two lines that look like a diagnosis,
// or the last three lines when none does, cut to a 400-byte cap with a mid-line
// cut dropped unless dropping it would leave nothing.
func TestHarnessErrorKeepsItsTailRules(t *testing.T) {
	for _, tt := range harnessErrorCases {
		t.Run(tt.name, func(t *testing.T) {
			if got := harness.HarnessError([]byte(tt.stderr)); got != tt.want {
				t.Errorf("Go  = %q\nwant = %q", got, tt.want)
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
