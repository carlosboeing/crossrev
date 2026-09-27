package review_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// pointerPass builds the two-batch advisory pass: 41 required files packing
// into 40 + 1, each adding one unique changed-line term whose untouched
// holder carries a distinct line number.
func pointerPass(t *testing.T) (*env, []string) {
	t.Helper()
	e := newEnv(t)
	var diff strings.Builder
	e.vcs.searchResults = map[string][]vcs.SearchHit{}
	for i := 1; i <= 41; i++ {
		path := fmt.Sprintf("file%02d.go", i)
		term := fmt.Sprintf("UniqueTerm%02d", i)
		writeRequiredHead(e, path, "package x\n")
		fmt.Fprintf(&diff, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -0,0 +1 @@\n+// %s marker\n",
			path, path, path, path, term)
		e.vcs.searchResults[term] = []vcs.SearchHit{{Path: fmt.Sprintf("docs/holder%02d.md", i), Lines: []int{i}}}
	}
	e.vcs.changedLines = []byte(diff.String())
	acceptAll(e)
	prompts := capturePrompt(e)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked", got.Outcome)
	}
	if len(*prompts) != 2 {
		t.Fatalf("prompts = %d, want 2 (41 files pack into 40 + 1)", len(*prompts))
	}
	return e, *prompts
}

// TestReviewBatchPointersArePerCall requires each batch prompt to carry only
// its own files' advisory pointers, with holder line numbers: the first
// batch names holders 01 to 40 and the second names holder 41 alone.
func TestReviewBatchPointersArePerCall(t *testing.T) {
	e, prompts := pointerPass(t)
	if e.vcs.searchCalls != 1 {
		t.Errorf("SearchAll calls = %d, want 1 (one blob pass serves both batches)", e.vcs.searchCalls)
	}
	for i := 1; i <= 40; i++ {
		want := fmt.Sprintf("docs/holder%02d.md:%d", i, i)
		if !strings.Contains(prompts[0], want) {
			t.Errorf("batch-one prompt lacks its own pointer %q", want)
		}
		if strings.Contains(prompts[1], fmt.Sprintf("docs/holder%02d.md", i)) {
			t.Errorf("batch-two prompt carries batch one's holder%02d", i)
		}
	}
	if !strings.Contains(prompts[1], "docs/holder41.md:41") {
		t.Error("batch-two prompt lacks its own pointer docs/holder41.md:41")
	}
	if strings.Contains(prompts[0], "docs/holder41.md") {
		t.Error("batch-one prompt carries batch two's holder41")
	}
}

// TestReviewBatchPromptCountsCappedHolderLines requires a holder whose
// blob-pass lines were capped to render its retained pointers with the
// counted rest on the prompt's remainder line.
func TestReviewBatchPromptCountsCappedHolderLines(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "file01.go", "package x\n")
	e.vcs.searchResults = map[string][]vcs.SearchHit{
		"UniqueTerm01": {{Path: "docs/noisy.md", Lines: []int{1, 2}, OmittedLines: 98}},
	}
	e.vcs.changedLines = []byte("diff --git a/file01.go b/file01.go\n--- a/file01.go\n+++ b/file01.go\n@@ -0,0 +1 @@\n+// UniqueTerm01 marker\n")
	acceptAll(e)
	prompts := capturePrompt(e)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked", got.Outcome)
	}
	if len(*prompts) != 1 {
		t.Fatalf("prompts = %d, want 1 (one file packs into one batch)", len(*prompts))
	}
	prompt := (*prompts)[0]
	for _, want := range []string{"docs/noisy.md:1", "docs/noisy.md:2"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks retained pointer %q", want)
		}
	}
	if !strings.Contains(prompt, "…and 98 more pointers") {
		t.Error("prompt lacks the remainder line counting the 98 capped holder lines")
	}
}

// TestReviewBatchPromptOmitsWholePassAdvisoryList requires the old repeated
// path list to be absent: no batch prompt carries all 41 holders the way the
// whole-pass list repeated in every call.
func TestReviewBatchPromptOmitsWholePassAdvisoryList(t *testing.T) {
	_, prompts := pointerPass(t)
	for n, prompt := range prompts {
		held := 0
		for i := 1; i <= 41; i++ {
			if strings.Contains(prompt, fmt.Sprintf("docs/holder%02d.md", i)) {
				held++
			}
		}
		if held == 41 {
			t.Errorf("batch %d prompt carries all 41 holders: the whole-pass list is repeated", n+1)
		}
	}
}
