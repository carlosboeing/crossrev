package review_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/review"
)

// This file pins the split path's counting: a file in N parts makes N
// calls, and the pass folds each call's envelope once, sums each call's
// usage once, and prints one progress line per call. Every script entry
// carries distinct usage so a double fold shows as an exact surplus.

// splitUsageScript scripts up to 40 part answers with distinct per-call
// usage: call i reports in=1000+i, read=100+i, write=200+i, out=300+i.
func splitUsageScript(t *testing.T, path string, n int) []exec.Result {
	t.Helper()
	script := make([]exec.Result, 0, n)
	for i := 0; i < n; i++ {
		script = append(script, exec.Result{ExitCode: 0, Stdout: claudeStdoutWithUsage(t,
			partAnswer(t, path), "claude-test", int64(1000+i), int64(100+i), int64(200+i), int64(300+i))})
	}
	return script
}

// splitUsageSums answers the exact per-bucket sums of the first n script
// entries: what the marker carries when every call folds once.
func splitUsageSums(n int) (fresh, read, write, out int64) {
	for i := 0; i < n; i++ {
		fresh += int64(1000 + i)
		read += int64(100 + i)
		write += int64(200 + i)
		out += int64(300 + i)
	}
	return fresh, read, write, out
}

// A split file's every part call folds its envelope exactly once: the
// marker's fresh input is the parts' sum, not the sum plus the final
// part's envelope again.
func TestReviewSplitFoldsEachEnvelopeOnce(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "huge.go", "package huge\n"+strings.Repeat("// filler line to exceed the prompt budget\n", 8000))
	e.runner.script = splitUsageScript(t, "huge.go", 40)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked (the file splits and merges)", got.Outcome)
	}
	n := e.runner.calls
	if n < 2 {
		t.Fatalf("harness calls = %d, want at least 2 parts", n)
	}
	wantFresh, _, _, _ := splitUsageSums(n)
	buckets := usageBuckets(t, got.Marker.Usage)
	if buckets["input_fresh"] != wantFresh {
		t.Errorf("marker input_fresh = %d, want %d (the %d part calls summed once)", buckets["input_fresh"], wantFresh, n)
	}
}

// A split file's usage buckets sum once per call: cache and output totals
// match the parts' exact sums, with no final-call surplus.
func TestReviewSplitSumsUsageOncePerCall(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "huge.go", "package huge\n"+strings.Repeat("// filler line to exceed the prompt budget\n", 8000))
	e.runner.script = splitUsageScript(t, "huge.go", 40)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked (the file splits and merges)", got.Outcome)
	}
	n := e.runner.calls
	if n < 2 {
		t.Fatalf("harness calls = %d, want at least 2 parts", n)
	}
	_, wantRead, wantWrite, wantOut := splitUsageSums(n)
	buckets := usageBuckets(t, got.Marker.Usage)
	for key, want := range map[string]int64{
		"cache_read":          wantRead,
		"cache_write_unsplit": wantWrite,
		"output":              wantOut,
	} {
		if buckets[key] != want {
			t.Errorf("marker usage[%s] = %d, want %d (the %d part calls summed once)", key, buckets[key], want, n)
		}
	}
}

// A split file prints one progress line per call: every "Batch i of N"
// appears exactly once, and the merged file's completion line carries the
// final covered count rather than a stale one beside it.
func TestReviewSplitPrintsOneProgressLinePerCall(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "huge.go", "package huge\n"+strings.Repeat("// filler line to exceed the prompt budget\n", 8000))
	e.runner.script = splitUsageScript(t, "huge.go", 40)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked (the file splits and merges)", got.Outcome)
	}
	n := e.runner.calls
	if n < 2 {
		t.Fatalf("harness calls = %d, want at least 2 parts", n)
	}
	var lines []string
	for _, line := range got.Messages {
		if strings.Contains(line.Text, "Batch ") {
			lines = append(lines, line.Text)
		}
	}
	if len(lines) != n {
		t.Fatalf("progress lines = %d, want %d (one per call):\n%s", len(lines), n, strings.Join(lines, "\n"))
	}
	for i := 1; i <= n; i++ {
		want := fmt.Sprintf("Batch %d of %d", i, n)
		var hits int
		for _, line := range lines {
			if strings.Contains(line, want) {
				hits++
			}
		}
		if hits != 1 {
			t.Errorf("%q appears %d times, want exactly once", want, hits)
		}
	}
	last := fmt.Sprintf("Batch %d of %d — covered 1 of 1 required files.", n, n)
	var closed bool
	for _, line := range lines {
		if strings.Contains(line, last) {
			closed = true
		}
	}
	if !closed {
		t.Errorf("no progress line closes the merged file with %q:\n%s", last, strings.Join(lines, "\n"))
	}
}
