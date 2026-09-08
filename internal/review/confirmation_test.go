package review_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// TestConfirmationReceivesRepairDeltaBeforeFullScope pins the repair
// confirmation input: after a resolve pushes C from reviewed head B, the
// next review prompt carries the B-to-C delta ahead of the current full
// scope, and the pass writes the confirmation pair only after accepted
// current-head coverage.
func TestConfirmationReceivesRepairDeltaBeforeFullScope(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "repair.go", "package repair\n\nfunc Fixed() int { return 1 }\n")
	reviewedB := mustRev(t, baseSHA)
	e.vcs.repair = &fakeRepair{base: reviewedB.SHA(), head: headSHA, bytes: []byte("diff --git a/repair.go b/repair.go\n--- a/repair.go\n+++ b/repair.go\n@@ -1,3 +1,3 @@\n-func Broken() int { return 0 }\n+func Fixed() int { return 1 }\n")}
	seedResolveMarker(t, e, reviewedB.SHA(), headSHA)
	prompts := capturePrompt(e)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"repair.go"}))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(*prompts) == 0 {
		t.Fatal("no prompt captured")
	}
	prompt := (*prompts)[0]
	repairAt := strings.Index(prompt, "repair delta")
	scopeAt := strings.Index(prompt, "The files under review")
	if repairAt < 0 {
		t.Fatalf("prompt carries no repair delta ahead of the scope")
	}
	if scopeAt >= 0 && repairAt > scopeAt {
		t.Fatalf("repair delta follows the full scope; want it first")
	}
	base, _ := got.Marker.ConfirmationBaseSHA.Get()
	head, _ := got.Marker.ConfirmationHeadSHA.Get()
	if base == "" || head == "" {
		t.Fatalf("confirmation pair = %q/%q, want B and C after accepted coverage", base, head)
	}
}

// TestRepairInvalidatesEveryPriorDisposition pins invalidation: a repair
// that moves the head retires every earlier disposition, so the next review
// accounts for every current required file from zero accepted units.
func TestRepairInvalidatesEveryPriorDisposition(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	first := runLeg(t, e, e.request(t))
	if first.Err != nil {
		t.Fatalf("first Run: %v", first.Err)
	}
	movedHead := mustRev(t, "4444444444444444444444444444444444444444")
	_ = movedHead
	writeRequiredHead(e, "b.go", "package b\n")
	reused := acceptedAtHead(t, e)
	if reused != 0 {
		t.Fatalf("reused dispositions at the moved head = %d, want 0", reused)
	}
}

// TestOutsideDiffFindingKeepsResolutionIdentityWithoutAThread pins the
// outside-diff anchor: a finding on an advisory untouched path keeps its
// finding ID and resolution identity even when GitHub supplies no thread.
func TestOutsideDiffFindingKeepsResolutionIdentityWithoutAThread(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	// Untouched adjacent test: advisory context, not required work.
	writeHead(e, "a_test.go", "package a\n\nfunc TestNothing(t *testing.T) {}\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(outsideDiffAnswer(t))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	findings := parseTestFindings(t, got.Marker.Findings)
	if len(findings) == 0 {
		t.Fatal("no findings persisted")
	}
	seen := map[string]bool{}
	var outside int
	for _, f := range findings {
		var id string
		if raw, ok := f["id"]; ok {
			_ = json.Unmarshal(raw, &id)
		}
		if id == "" {
			t.Errorf("persisted finding carries no id: %v", f)
			continue
		}
		if seen[id] {
			t.Errorf("duplicate persisted finding id %q", id)
		}
		seen[id] = true
		var kind string
		if raw, ok := f["anchor_kind"]; ok {
			_ = json.Unmarshal(raw, &kind)
		}
		if kind == "outside_diff" {
			outside++
			var reason string
			if raw, ok := f["anchor_reason"]; ok {
				_ = json.Unmarshal(raw, &reason)
			}
			if reason == "" {
				t.Errorf("outside_diff finding %q carries no anchor_reason", id)
			}
		}
	}
	if outside != 1 {
		t.Fatalf("outside_diff findings = %d, want 1 (the advisory-path finding keeps identity without a thread)", outside)
	}
	if len(e.forge.threads) != 0 {
		t.Fatalf("threads = %d, want 0: the outside-diff finding posts with no thread", len(e.forge.threads))
	}
}

// capturePrompt records every rendered prompt the harness receives, in order.
// Set before runLeg: the runner calls onSpec per invocation.
func capturePrompt(e *env) *[]string {
	prompts := &[]string{}
	e.runner.onSpec = func(spec exec.Spec) {
		if len(spec.Args) > 0 {
			*prompts = append(*prompts, spec.Args[len(spec.Args)-1])
		}
	}
	return prompts
}

func acceptedAtHead(t *testing.T, e *env) int {
	t.Helper()
	comments := ledgerComments(t, e)
	gen, err := prstate.SelectGeneration(comments, author, core.RevisionPair{Base: mustRev(t, baseSHA), Head: mustRev(t, "4444444444444444444444444444444444444444")}, core.FileEngineVersion)
	if err != nil {
		return 0
	}
	accepted := 0
	for _, record := range gen.Records {
		if record.Type == prstate.CoverageRecordUnit && record.Disposition.Present() {
			accepted++
		}
	}
	return accepted
}

// seedResolveMarker posts a completed resolve marker that pushed commit C
// from reviewed head B, the way the resolve leg leaves it.
func seedResolveMarker(t *testing.T, e *env, reviewedHead, commit string) {
	t.Helper()
	head, err := core.NewRevision(reviewedHead)
	if err != nil {
		t.Fatalf("reviewed head: %v", err)
	}
	marker := prstate.Marker{
		Version:   core.MarkerVersion,
		Leg:       core.LegResolve,
		Pass:      1,
		State:     core.PassComplete,
		HeadSHA:   prstate.Some(head.SHA()),
		CommitSHA: prstate.Some(commit),
	}
	encoded, err := marker.Encode()
	if err != nil {
		t.Fatalf("encode resolve marker: %v", err)
	}
	e.forge.comments = append(e.forge.comments, forge.IssueComment{
		ID:          8001,
		AuthorLogin: author,
		Body:        "resolve done" + encoded,
	})
}

// outsideDiffAnswer names one finding on the advisory untouched path: unit
// 1 (a.go) carries it with file-level evidence for the required file, and
// the finding itself points at a_test.go.
func outsideDiffAnswer(t *testing.T) string {
	t.Helper()
	return `{"verdict":"issues-remain","blocked_reason":null,"findings":[{"number":1,"path":"a_test.go","line":1,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"Untested helper","why":"The helper has no test","fix":"Add one"}],` +
		`"coverage":[{"unit_number":1,"disposition":"finding","finding_numbers":[1],` +
		`"evidence":[{"path":"a.go","revision":"` + headSHA + `","start_line":null,"end_line":null,"source":"git","note":null}],"reason":null}],` +
		`"examined_scope":"read the batch","known_limits":[]}`
}
