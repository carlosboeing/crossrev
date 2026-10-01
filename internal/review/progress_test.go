package review_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// Without a live sink a pass that runs more than one batch queues each
// accepted batch in the report, the way every other leg line travels: a
// caller with no terminal still sees the counts, just with the closing
// report rather than while the pass runs.
func TestReviewReportsPerBatchProgressWithoutASink(t *testing.T) {
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
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	for _, want := range []string{
		"Batch 1 of 2 — covered 40 of 41 required files.",
		"Batch 2 of 2 — covered 41 of 41 required files.",
	} {
		if !containsString(ui.Texts(got.Messages), want) {
			t.Errorf("terminal lines miss %q; the leg said %q", want, ui.Texts(got.Messages))
		}
	}
}

// The same per-batch counts reach the pass comment, so a reader watching the
// pull request sees the pass advance without waiting for the summary. The
// claim is edited after every accepted batch — not only once findings exist,
// which left a clean pass silent until the summary landed.
func TestReviewReportsPerBatchProgressOnTheClaim(t *testing.T) {
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
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.edits) != 4 {
		t.Fatalf("claim edits = %d, want 4 (one per accepted batch plus the summary and complete edits)", len(e.forge.edits))
	}
	for i, want := range []string{
		"Batch 1 of 2 — covered 40 of 41 required files.",
		"Batch 2 of 2 — covered 41 of 41 required files.",
	} {
		if !strings.Contains(e.forge.edits[i], want) {
			t.Errorf("claim edit %d misses %q:\n%s", i, want, e.forge.edits[i])
		}
		if !strings.Contains(e.forge.edits[i], "No findings so far") {
			t.Errorf("claim edit %d does not say no findings were recorded:\n%s", i, e.forge.edits[i])
		}
	}
	if marker := decodeEditMarker(t, e.forge.edits[0]); marker.State != core.PassStarted {
		t.Errorf("progress edit marker state = %q, want started (the pass has not settled)", marker.State)
	}
	_ = got
}

// When an accepted batch names findings, the progress edit keeps the
// findings-recorded record beside the counts: a failure in a later batch
// still leaves them on the pull request, where the re-drive reads them back.
func TestReviewProgressEditKeepsTheFindingsRecord(t *testing.T) {
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
		{ExitCode: 0, Stdout: claudeStdout(findingAnswer(t, first[0], first[1:], "First batch finding"))},
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, rest))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !strings.Contains(e.forge.edits[0], "Batch 1 of 2 — covered 40 of 41 required files.") {
		t.Errorf("first progress edit carries no per-batch counts:\n%s", e.forge.edits[0])
	}
	if !strings.Contains(e.forge.edits[0], "Findings recorded") {
		t.Errorf("first progress edit lost the findings record:\n%s", e.forge.edits[0])
	}
	if n := len(decodeEditFindings(t, e.forge.edits[0])); n != 1 {
		t.Errorf("progress edit findings = %d, want 1 (the accepted batch-one finding)", n)
	}
	_ = got
}

// orderRunner records each session child start in order, so a test can pin
// a live progress line against the harness call that follows it. Version
// probes are not session children and are not recorded; neither is the
// served read-server session the self-test speaks to before the child.
type orderRunner struct {
	inner  exec.Runner
	onCall func()
}

func (r orderRunner) Run(ctx context.Context, spec exec.Spec) exec.Result {
	served := false
	for _, arg := range spec.Args {
		if arg == "__read-server" {
			served = true
		}
	}
	if !(len(spec.Args) == 1 && spec.Args[0] == "--version") && !served {
		r.onCall()
	}
	return r.inner.Run(ctx, spec)
}

// The batch-one progress line reaches the live sink before the batch-two
// harness call starts: a line that only flushed with the closing report
// would leave a long pass silent on the terminal while it runs, which is
// what queuing every line in Messages did. A wired sink reports each line
// once — queued again in the report, the terminal would print it twice.
func TestReviewEmitsPerBatchProgressBeforeTheNextBatchStarts(t *testing.T) {
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
	var events []string
	leg := e.leg(t)
	leg.Runner = orderRunner{inner: e.runner, onCall: func() { events = append(events, "harness") }}
	leg.Progress = func(line ui.Line) { events = append(events, "progress:"+line.String()) }
	got := leg.Run(context.Background(), e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	progressAt := -1
	harnessCalls := 0
	secondHarnessAt := -1
	for i, ev := range events {
		if ev == "harness" {
			harnessCalls++
			if harnessCalls == 2 {
				secondHarnessAt = i
			}
		}
		if progressAt == -1 && strings.HasPrefix(ev, "progress:Batch 1 of 2") {
			progressAt = i
		}
	}
	if progressAt == -1 {
		t.Fatalf("the batch-one line never reached the sink; events: %q", events)
	}
	if secondHarnessAt == -1 {
		t.Fatalf("the second batch never started; events: %q", events)
	}
	if progressAt > secondHarnessAt {
		t.Errorf("batch-one progress reached the sink after the batch-two harness call started; events: %q", events)
	}
	for _, line := range ui.Texts(got.Messages) {
		if strings.Contains(line, "Batch 1 of 2") || strings.Contains(line, "Batch 2 of 2") {
			t.Errorf("a wired sink queued %q in the report too, so the terminal would print it twice", line)
		}
	}
}
