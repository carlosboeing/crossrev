package review_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// A pass that runs more than one batch reports each accepted batch on the
// terminal: without it a long pass prints nothing between the run header and
// the verdict, and the operator cannot tell whether it is advancing.
func TestReviewReportsPerBatchProgressOnTheTerminal(t *testing.T) {
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
