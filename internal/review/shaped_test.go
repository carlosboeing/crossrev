package review_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

var errShaping = errors.New("shaping failed")

// TestShapedBatchRendersHunksAndRecordsTheirForms drives one batch whose
// units shaped: a small file reading in full and a large one reading as
// function-context hunks. The prompt carries each file's own numbered
// hunks and no separate whole-diff section, and each published record
// names the form it was shown under with the digest over the numbered
// hunk bytes.
func TestShapedBatchRendersHunksAndRecordsTheirForms(t *testing.T) {
	e := newEnv(t, "correctness")
	writeRequiredHead(e, "a.go", "package a\n")
	bigBody := strings.Repeat("package big\n\nfunc F() int { return 0 }\n", 400)
	writeRequiredHead(e, "b.go", bigBody)

	cannedA := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n package a\n+func A() {}\n"
	cannedB := "diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -50,4 +50,4 @@ func F() int {\n package big\n \n func F() int {\n-\treturn 0\n+\treturn 1\n"
	e.vcs.shapeFunc = func(unit intel.FileUnit) (vcs.ShapedFile, error) {
		switch unit.Path {
		case "a.go":
			return vcs.ShapedFile{Form: intel.FormFullText, Diff: []byte(cannedA)}, nil
		case "b.go":
			return vcs.ShapedFile{Form: intel.FormHunksContext, Diff: []byte(cannedB)}, nil
		}
		t.Fatalf("shaping reached unscripted path %q", unit.Path)
		return vcs.ShapedFile{}, nil
	}
	prompts := capturePrompt(e)
	published := capturePublished(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go", "b.go"}))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked", got.Outcome)
	}
	if len(*prompts) != 1 {
		t.Fatalf("prompts = %d, want 1", len(*prompts))
	}
	prompt := (*prompts)[0]
	for _, want := range []string{
		"Shown in full as one numbered hunk.",
		"Shown as the enclosing function of each change",
		"func A() {}",
		"return 1",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("shaped prompt lacks %q", want)
		}
	}
	if strings.Contains(prompt, "## The diff under review") {
		t.Error("shaped prompt still renders the separate diff section")
	}
	if len(*published) == 0 {
		t.Fatal("no complete generation published")
	}
	last := (*published)[len(*published)-1]
	for _, tc := range []struct {
		path   string
		canned string
		form   string
	}{
		{"a.go", cannedA, "full_text"},
		{"b.go", cannedB, "hunks_context"},
	} {
		record := suppliedRecordFor(t, last, tc.path)
		supplied, ok := record.Supplied.Get()
		if !ok {
			t.Fatalf("%s carries no supplied input", tc.path)
		}
		if supplied.Form != tc.form {
			t.Errorf("%s form = %q, want %q", tc.path, supplied.Form, tc.form)
		}
		numbered := diff.Parse([]byte(tc.canned), core.RevisionPair{}).Numbered()
		sum := sha256.Sum256(numbered)
		if want := hex.EncodeToString(sum[:]); supplied.Digest != want {
			t.Errorf("%s digest %s, want %s (the numbered hunk bytes)", tc.path, supplied.Digest, want)
		}
	}
}

// TestShapingFailureFailsThePass pins fail-closed shaping: a git failure
// behind one file's hunks stops the leg rather than reviewing the file
// from its body in silence.
func TestShapingFailureFailsThePass(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.vcs.shapeErr = errShaping
	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("Run passed silently over a shaping failure, want an error")
	}
	if got.Outcome != review.OutcomeError {
		t.Errorf("Outcome = %q, want error", got.Outcome)
	}
}
