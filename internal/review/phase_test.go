package review_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// TestPhaseAndCallLinesForATwoCallPass pins the run-log instrumentation: one
// phase line per preparation step, and one call line per accepted call.
func TestPhaseAndCallLinesForATwoCallPass(t *testing.T) {
	e := newEnv(t)
	var first, rest []string
	for i := 1; i <= 41; i++ {
		path := fmt.Sprintf("file%02d.go", i)
		writeRequiredHead(e, path, "package x\n")
		if i <= 40 {
			first = append(first, path)
		} else {
			rest = append(rest, path)
		}
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, first))},
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, rest))},
	}
	prompts := capturePrompt(e)

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(*prompts) != 2 {
		t.Fatalf("prompts sent = %d, want 2 (one per batch)", len(*prompts))
	}

	log := readRunLog(t, e)
	for _, want := range []string{
		"phase enumerate ms=",
		"phase reads ms=",
		"phase diff ms=",
		"phase search ms=",
		"phase pack ms=",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the run log has no %q:\n%s", want, log)
		}
	}
	// The term walk reads no -U0 lines from the stub, so each changed path
	// contributes itself alone: 41 terms.
	if !strings.Contains(log, "phase terms terms=41 ms=") {
		t.Errorf("the run log has no terms line with the term count:\n%s", log)
	}

	callLines := runLogLines(log, "call")
	if len(callLines) != 2 {
		t.Fatalf("call lines = %d, want 2 (one per accepted call):\n%s", len(callLines), log)
	}
	// prompt_bytes measures the rendered prompt. The captured prompt went
	// through the harness stdin transport, which carries Argument() — the
	// prompt with trailing newlines trimmed (internal/harness/adapter.go) —
	// so it reads one byte short of the rendered batch prompt ending in "\n".
	for i, want := range []string{
		fmt.Sprintf("1 prompt_bytes=%d supplied_bytes=400 fresh=0 cached=0 output=0 reads=0 commands=0 model=- ms=0", len((*prompts)[0])+1),
		fmt.Sprintf("2 prompt_bytes=%d supplied_bytes=10 fresh=0 cached=0 output=0 reads=0 commands=0 model=- ms=0", len((*prompts)[1])+1),
	} {
		if callLines[i] != want {
			t.Errorf("call line %d = %q, want %q", i+1, callLines[i], want)
		}
	}
}

// TestFailedTwoCallPassKeepsBothCallTranscripts pins failure-path keeping
// under the per-call stems: the accepted first call's transcript survives
// the second call's failure, and only the accepted call gets a call line.
func TestFailedTwoCallPassKeepsBothCallTranscripts(t *testing.T) {
	e := newEnv(t)
	var first []string
	for i := 1; i <= 41; i++ {
		path := fmt.Sprintf("file%02d.go", i)
		writeRequiredHead(e, path, "package x\n")
		if i <= 40 {
			first = append(first, path)
		}
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(findingAnswer(t, first[0], first[1:], "First batch finding"))},
		{ExitCode: 1, Stderr: []byte("harness died\n")},
	}

	if got := runLeg(t, e, e.request(t)); got.Err == nil {
		t.Fatal("Run: want the batch-two failure to stop the leg")
	}

	dir := runDir(t, e)
	for _, name := range []string{
		"review.call-1.attempt-1.stdout",
		"review.call-2.attempt-1.stderr",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("kept transcript %s: %v", name, err)
		}
	}
	if body, err := os.ReadFile(filepath.Join(dir, "review.call-2.attempt-1.stderr")); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(body), "harness died") {
		t.Errorf("the failed call's transcript does not hold the harness error: %q", body)
	}

	log := readRunLog(t, e)
	if len(runLogLines(log, "call")) != 1 || !strings.Contains(log, "call 1 ") {
		t.Errorf("the run log does not carry exactly the accepted call's line:\n%s", log)
	}
}

// runLogLines answers the detail halves of the run-log lines carrying the
// given event: everything after the timestamp and the event word.
func runLogLines(log, event string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSuffix(log, "\n"), "\n") {
		if _, detail, found := strings.Cut(line, " "+event+" "); found {
			out = append(out, detail)
		}
	}
	return out
}

// TestReviewCallStemsAreSweptWithTheLeg pins that the whole-leg transcript
// clear finds the per-call stems: a successful pass leaves no call file
// behind, under either the old or the new stem.
func TestReviewCallStemsAreSweptWithTheLeg(t *testing.T) {
	e := newEnv(t)
	var first, rest []string
	for i := 1; i <= 41; i++ {
		path := fmt.Sprintf("file%02d.go", i)
		writeRequiredHead(e, path, "package x\n")
		if i <= 40 {
			first = append(first, path)
		} else {
			rest = append(rest, path)
		}
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, first))},
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, rest))},
	}

	if got := runLeg(t, e, e.request(t)); got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}

	for _, pattern := range []string{"review.call-*", "review.attempt-*"} {
		left, err := filepath.Glob(filepath.Join(runDir(t, e), pattern))
		if err != nil {
			t.Fatal(err)
		}
		if len(left) != 0 {
			t.Errorf("transcripts left behind after a clean two-call pass (%s): %v", pattern, left)
		}
	}
}
