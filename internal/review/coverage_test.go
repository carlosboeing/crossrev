package review_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/review"
)

// batchAnswer returns a coverage-complete answer for n numbered units: every
// unit judged no_issue with the file-level evidence the semantic check
// requires for a judgement with something behind it.
func batchAnswer(t *testing.T, n int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"verdict":"issues-remain","blocked_reason":null,"findings":[],"coverage":[`)
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		b.WriteString(`{"unit_number":` + itoa2(i) + `,"disposition":"no_issue","finding_numbers":[],"evidence":[{"path":"` + batchPath(i) + `","revision":"` + headSHA + `","start_line":null,"end_line":null,"source":"git","note":null}],"reason":null}`)
	}
	b.WriteString(`],"examined_scope":"read the batch","known_limits":[]}`)
	return b.String()
}

// batchPath names the i-th required file in path order for the two-file
// batch tests below: a.go first, b.go second.
func batchPath(i int) string {
	if i == 2 {
		return "b.go"
	}
	return "a.go"
}

func itoa2(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// ledgerGenerations returns the complete generations the leg published,
// selected one generation at a time from the oldest manifest on.
func ledgerManifestIDs(t *testing.T, e *env) []int64 {
	t.Helper()
	var ids []int64
	for _, c := range e.forge.comments {
		if c.AuthorLogin != author {
			continue
		}
		if manifest, ok := prstate.DecodeCoverageManifest(c.Body); ok {
			ids = append(ids, c.ID)
			_ = manifest
		}
	}
	return ids
}

func ledgerComments(t *testing.T, e *env) []prstate.CoverageComment {
	t.Helper()
	var comments []prstate.CoverageComment
	for _, c := range e.forge.ledger.order {
		stored := e.forge.ledger.comments[c]
		comments = append(comments, prstate.CoverageComment{ID: stored.ID, Author: stored.Author, Body: stored.Body})
	}
	return comments
}

// ledgerGenerations returns every complete generation the leg published:
// one SelectGeneration per manifest, each over the ledger prefix through
// that manifest, so earlier generations are visible beside the current one.
func ledgerGenerations(t *testing.T, e *env) []prstate.Generation {
	t.Helper()
	comments := ledgerComments(t, e)
	var out []prstate.Generation
	for i, c := range comments {
		if _, ok := prstate.DecodeCoverageManifest(c.Body); !ok {
			continue
		}
		gen, err := prstate.SelectGeneration(comments[:i+1], author, core.RevisionPair{Base: mustRev(t, baseSHA), Head: mustRev(t, headSHA)}, core.FileEngineVersion)
		if err != nil {
			continue
		}
		out = append(out, gen)
	}
	return out
}

// TestReviewPublishesOneCompleteGenerationPerAcceptedBatch pins the C1 batch
// loop: two required files in one batch publish one complete generation per
// accepted batch, and the current generation covers both units.
func TestReviewPublishesOneCompleteGenerationPerAcceptedBatch(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	writeRequiredHead(e, "b.go", "package b\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 2))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	gens := ledgerGenerations(t, e)
	if len(gens) != 2 {
		t.Fatalf("complete generations = %d, want 2 (initial outstanding plus one per accepted batch)", len(gens))
	}
	last := gens[len(gens)-1]
	covered := 0
	for _, record := range last.Records {
		if record.Type == "unit" {
			covered++
		}
	}
	if covered != 2 {
		t.Fatalf("covered units in the current generation = %d, want 2", covered)
	}
}

// TestReviewRetriesSemanticOmissionOnce pins the one semantic retry with the
// missing unit named: the first answer omits unit 2 of 2, the leg asks once
// more quoting the missing number, and the accepted retry publishes.
func TestReviewRetriesSemanticOmissionOnce(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	writeRequiredHead(e, "b.go", "package b\n")
	omitSecond := `{"verdict":"issues-remain","blocked_reason":null,"findings":[],"coverage":[{"unit_number":1,"disposition":"no_issue","finding_numbers":[],"evidence":[],"reason":null}],"examined_scope":"read half the batch","known_limits":[]}`
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(omitSecond)},
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 2))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 2 {
		t.Fatalf("harness calls = %d, want 2 (one semantic retry)", e.runner.calls)
	}
	var warned bool
	for _, line := range got.Messages {
		if strings.Contains(line.Text, "missing unit number(s) 2") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("no retry naming the omitted unit; messages = %q", review.OutcomeInvoked)
	}
	gens := ledgerGenerations(t, e)
	if len(gens) == 0 {
		t.Fatal("no complete generation published after the accepted retry")
	}
}

// TestReviewRestartUsesOnlySameRevisionCoverage pins resumption: a restart at
// the same base, head and engine reuses the prior generation's accepted
// dispositions, while a moved head starts from zero accepted units.
func TestReviewRestartUsesOnlySameRevisionCoverage(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	first := runLeg(t, e, e.request(t))
	if first.Err != nil {
		t.Fatalf("first Run: %v", first.Err)
	}
	same := acceptedReuse(t, e, mustRev(t, baseSHA), mustRev(t, headSHA))
	movedHead := mustRev(t, "3333333333333333333333333333333333333333")
	moved := acceptedReuse(t, e, mustRev(t, baseSHA), movedHead)
	if same != 1 || moved != 0 {
		t.Fatalf("restart reuse = same:%d moved:%d, want same:1 moved:0", same, moved)
	}
}

// acceptedReuse counts the prior generation's dispositions the leg would
// reuse at the given revision pair under the current engine.
func acceptedReuse(t *testing.T, e *env, base, head core.Revision) int {
	t.Helper()
	gen, err := prstate.SelectGeneration(ledgerComments(t, e), author, core.RevisionPair{Base: base, Head: head}, core.FileEngineVersion)
	if err != nil {
		return 0
	}
	accepted := 0
	for _, record := range gen.Records {
		if record.Type == "unit" && record.Disposition.Present() {
			accepted++
		}
	}
	return accepted
}

var _ = context.Background
