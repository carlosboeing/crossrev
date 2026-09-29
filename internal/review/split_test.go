package review_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// partFindingAnswer answers one split-file part with a finding: finding
// number 1 names the returned finding, so an interrupted pass can prove
// the part's finding reached no publication.
func partFindingAnswer(t *testing.T, path, title string) string {
	t.Helper()
	return `{"verdict":"issues-remain","blocked_reason":null,"findings":[{"number":1,"path":"` + path + `","line":1,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"` + title + `","why":"A failed request reads as success","fix":"Check it"}],"coverage":[` +
		`{"unit_number":1,"verdict":"finding","finding_numbers":[1],"evidence":[{"path":"` + path + `","revision":"` + headSHA + `","start_line":1,"end_line":1,"source":"git","note":null}],"reason":null}` +
		`],"examined_scope":"read the slice","known_limits":[]}`
}

// unwrappedProse builds a handwritten Markdown body of about size bytes:
// long unwrapped lines a person wrote, which no built-in generated
// signal matches.
func unwrappedProse(size int) []byte {
	line := strings.Repeat("An unwrapped paragraph written by a person. ", 10) + "\n"
	var b strings.Builder
	for b.Len() < size {
		b.WriteString(line)
	}
	return []byte(b.String())
}

// TestReviewSplitsTwoMegabyteHandwrittenFile is the PR 270 shape: a 2 MB
// handwritten CHANGELOG.md splits into line chunks and completes with
// the merged verdict, every part's call inside the packing limit.
func TestReviewSplitsTwoMegabyteHandwrittenFile(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "CHANGELOG.md", string(unwrappedProse(2<<20)))
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(partAnswer(t, "CHANGELOG.md"))},
	}
	prompts := capturePrompt(e)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked (the file splits and merges)", got.Outcome)
	}
	if len(*prompts) < 2 {
		t.Fatalf("prompts = %d, want the 2 MB file split into chunks", len(*prompts))
	}
	for i, prompt := range *prompts {
		if len(prompt) > claudePackBytes() {
			t.Errorf("prompt %d is %d bytes, over the %d packing limit", i+1, len(prompt), claudePackBytes())
		}
		if !strings.Contains(prompt, "(part ") || !strings.Contains(prompt, "@@") {
			t.Errorf("prompt %d carries no positioned hunk slice", i+1)
		}
	}
	gens := ledgerGenerations(t, e)
	if len(gens) == 0 {
		t.Fatal("no complete generation after the merged parts")
	}
	record := suppliedRecordFor(t, gens[len(gens)-1], "CHANGELOG.md")
	verdict, ok := record.Verdict.Get()
	if !ok || verdict != "no_issue" {
		t.Errorf("merged verdict = %q, want no_issue", verdict)
	}
}

// TestReviewChangeLogReviewsAsHunksWithNoHalt pins the hunks_first half
// of the PR 270 shape: a handwritten file past the old single-prompt
// budget reviews in its hunk form with no halt and no split.
func TestReviewChangeLogReviewsAsHunksWithNoHalt(t *testing.T) {
	e := newEnv(t)
	body := strings.Repeat("package changelog\n", 250)
	writeRequiredHead(e, "CHANGELOG.md", body)
	var raw strings.Builder
	fmt.Fprintf(&raw, "diff --git a/CHANGELOG.md b/CHANGELOG.md\n--- a/CHANGELOG.md\n+++ b/CHANGELOG.md\n")
	for h := 0; h < 10; h++ {
		fmt.Fprintf(&raw, "@@ -%d,20 +%d,20 @@\n", h*20+1, h*20+1)
		raw.WriteString(strings.Repeat(strings.Repeat("x", 999)+"\n", 20))
	}
	e.vcs.shapeFunc = func(unit intel.FileUnit) (vcs.ShapedFile, error) {
		if unit.Path != "CHANGELOG.md" {
			t.Fatalf("shaping reached unscripted path %q", unit.Path)
		}
		return vcs.ShapedFile{Form: intel.FormHunksContext, Diff: []byte(raw.String())}, nil
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"CHANGELOG.md"}))},
	}
	prompts := capturePrompt(e)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked (hunks review with no halt)", got.Outcome)
	}
	if len(*prompts) != 1 {
		t.Fatalf("prompts = %d, want 1 (the hunk form fits one call)", len(*prompts))
	}
	if !strings.Contains((*prompts)[0], "the enclosing function of each change") {
		t.Error("the prompt does not read the file in its hunk form")
	}
	for _, label := range e.forge.labelsAdded {
		if strings.Contains(label, "halted") {
			t.Fatalf("hunk review applied a halted label: %q", label)
		}
	}
}

// TestReviewInterruptedSplitLeavesNothing pins the interruption rule: a
// pass that dies mid-split leaves no part verdict or finding in the
// generation or on the claim, so the next run restarts the file from
// part 1.
func TestReviewInterruptedSplitLeavesNothing(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "huge.go", "package huge\n"+strings.Repeat("// filler line to exceed the prompt budget\n", 8000))
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(partFindingAnswer(t, "huge.go", "Interrupted part finding"))},
		{ExitCode: 1, Stderr: []byte("harness died")},
	}
	initial := runLeg(t, e, e.request(t))
	if initial.Err == nil {
		t.Fatal("first Run: want the part-two failure to stop the leg")
	}
	// The accepted part-one verdict and finding reached no publication:
	// no generation judges the file, and no claim edit names the finding.
	for _, gen := range ledgerGenerations(t, e) {
		for _, record := range gen.Records {
			if record.Type != prstate.CoverageRecordUnit {
				continue
			}
			if record.PathIndex >= 0 && record.PathIndex < len(gen.Paths) && gen.Paths[record.PathIndex] == "huge.go" {
				if _, ok := record.Verdict.Get(); ok {
					t.Errorf("generation %d judges huge.go from an interrupted split", gen.Gen)
				}
			}
		}
	}
	for _, body := range e.forge.edits {
		if strings.Contains(body, "Interrupted part finding") {
			t.Error("a claim edit carries the interrupted part's finding")
		}
	}
	// The next run restarts the file from part 1 and completes it.
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(partAnswer(t, "huge.go"))},
	}
	resumed := runLeg(t, e, e.request(t))
	if resumed.Err != nil {
		t.Fatalf("resumed Run: %v", resumed.Err)
	}
	if resumed.Outcome != review.OutcomeInvoked {
		t.Fatalf("resumed Outcome = %q, want invoked", resumed.Outcome)
	}
	gens := ledgerGenerations(t, e)
	record := suppliedRecordFor(t, gens[len(gens)-1], "huge.go")
	if verdict, ok := record.Verdict.Get(); !ok || verdict == "" {
		t.Error("the resumed pass carries no merged verdict for huge.go")
	}
}

// TestReviewArgvPacksUnder120KiBWhileCodexPacksTo390000 pins the
// transport split: the same files that fill one codex call split across
// argv calls, and every prompt on either harness fits its own budget.
func TestReviewArgvPacksUnder120KiBWhileCodexPacksTo390000(t *testing.T) {
	inputs := func(t *testing.T, wrap func(string) []byte) *env {
		e := newEnv(t)
		for i := 0; i < 4; i++ {
			writeRequiredHead(e, fmt.Sprintf("f%d.go", i), "package x\n"+strings.Repeat("// filler line to price the transport\n", 1300))
		}
		acceptAll(e)
		for i := 0; i < 8; i++ {
			e.runner.script = append(e.runner.script, exec.Result{ExitCode: 0, Stdout: wrap(convergedPayload())})
		}
		return e
	}
	agy := inputs(t, func(payload string) []byte { return agyStructuredStdout(t, payload, 7, 3) })
	agyReq := agy.request(t)
	agyReq.HarnessOverride = "agy"
	agyPrompts := capturePrompt(agy)
	if got := runLeg(t, agy, agyReq); got.Err != nil {
		t.Fatalf("agy Run: %v", got.Err)
	}
	argvPack := intel.ComputeLimits(128000, true).PackBytes
	if agy.runner.calls < 2 {
		t.Fatalf("agy calls = %d, want at least 2 (200 KB of files past the 120 KiB packing limit)", agy.runner.calls)
	}
	for i, prompt := range *agyPrompts {
		if len(prompt) > argvPack {
			t.Errorf("agy prompt %d is %d bytes, over the %d argv packing limit", i+1, len(prompt), argvPack)
		}
	}
	codex := inputs(t, claudeStdout)
	codexReq := codex.request(t)
	codexReq.HarnessOverride = "codex"
	prompts := capturePrompt(codex)
	// Codex writes its answer to the -o payload file rather than stdout,
	// so the runner plays the child and writes the file itself, chained
	// after the prompt capture.
	prev := codex.runner.onSpec
	codex.runner.onSpec = func(spec exec.Spec) {
		if prev != nil {
			prev(spec)
		}
		for i, arg := range spec.Args {
			if arg == "-o" && i+1 < len(spec.Args) {
				if err := os.WriteFile(spec.Args[i+1], []byte(convergedPayload()), 0o600); err != nil {
					t.Errorf("writing the codex payload file: %v", err)
				}
			}
		}
	}
	if got := runLeg(t, codex, codexReq); got.Err != nil {
		t.Fatalf("codex Run: %v", got.Err)
	}
	if codex.runner.calls != 1 {
		t.Errorf("codex calls = %d, want 1 (the same files fit the 390,000-byte packing limit)", codex.runner.calls)
	}
	for i, prompt := range *prompts {
		if len(prompt) > intel.ComputeLimits(258400, false).PackBytes {
			t.Errorf("codex prompt %d is %d bytes, over the 390,000-byte packing limit", i+1, len(prompt))
		}
	}
}

// TestReviewInputPoliciesShareFixtures runs the same shaped files under
// both input policies: hunks_first reads the hunk form, whole_when_fits
// reads the file whole where its rendered form fits the per-call budget,
// and both converge.
func TestReviewInputPoliciesShareFixtures(t *testing.T) {
	inputs := func(t *testing.T, policy string) (*env, *[]string) {
		e := newEnv(t)
		if policy != "" {
			e.cfg = mustConfig(t, "reviewers:\n  - harness: claude\nreview:\n  input_policy: "+policy+"\n")
		}
		writeRequiredHead(e, "a.go", "package a\n")
		writeRequiredHead(e, "b.go", strings.Repeat("package b\n", 150))
		canned := "diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,2 +1,3 @@\n package b\n+func B() {}\n package b\n"
		e.vcs.shapeFunc = func(unit intel.FileUnit) (vcs.ShapedFile, error) {
			if unit.Path != "b.go" {
				return vcs.ShapedFile{}, nil
			}
			return vcs.ShapedFile{Form: intel.FormHunksContext, Diff: []byte(canned)}, nil
		}
		e.runner.script = []exec.Result{
			{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go", "b.go"}))},
		}
		return e, capturePrompt(e)
	}
	hunks, hunksPrompts := inputs(t, "")
	if got := runLeg(t, hunks, hunks.request(t)); got.Err != nil {
		t.Fatalf("hunks_first Run: %v", got.Err)
	} else if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("hunks_first Outcome = %q, want invoked", got.Outcome)
	}
	whole, wholePrompts := inputs(t, "whole_when_fits")
	if got := runLeg(t, whole, whole.request(t)); got.Err != nil {
		t.Fatalf("whole_when_fits Run: %v", got.Err)
	} else if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("whole_when_fits Outcome = %q, want invoked", got.Outcome)
	}
	if len(*hunksPrompts) != 1 || len(*wholePrompts) != 1 {
		t.Fatalf("prompts = %d and %d, want 1 call under each policy", len(*hunksPrompts), len(*wholePrompts))
	}
	if !strings.Contains((*hunksPrompts)[0], "the enclosing function of each change") {
		t.Error("hunks_first does not read the hunk form")
	}
	wholePrompt := (*wholePrompts)[0]
	if strings.Contains(wholePrompt, "the enclosing function of each change") {
		t.Error("whole_when_fits still reads the hunk form for a file that fits whole")
	}
	if !strings.Contains(wholePrompt, "package b") {
		t.Error("whole_when_fits does not send the file whole")
	}
}

// TestReviewSharedContextOverHardHalts pins the one remaining halt:
// shared context alone past the hard limit schedules no call, halts with
// shared_context_exceeds_window, and applies the halted label.
func TestReviewSharedContextOverHardHalts(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	writeBase(e, "REVIEW.md", "# Repository review guide\n"+strings.Repeat("A standing instruction every prompt carries. ", 20000))
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeHalted {
		t.Fatalf("Outcome = %q, want halted (shared context alone over the hard limit)", got.Outcome)
	}
	if got.Reason != "shared_context_exceeds_window" {
		t.Errorf("Reason = %q, want shared_context_exceeds_window", got.Reason)
	}
	if e.runner.calls != 0 {
		t.Errorf("harness calls = %d, want 0 (halt before the first call)", e.runner.calls)
	}
	var halted bool
	for _, label := range e.forge.labelsAdded {
		if label == "crossrev/halted" {
			halted = true
		}
	}
	if !halted {
		t.Errorf("labels added = %v, want crossrev/halted", e.forge.labelsAdded)
	}
}

// TestReviewSharedContextInBandRunsOverBudget pins the band: shared
// context between 0.75 x P and H still runs and records over_budget on
// the generations with a terminal warning, packing against P until
// shared context passes it.
func TestReviewSharedContextInBandRunsOverBudget(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	writeBase(e, "REVIEW.md", "# Repository review guide\n"+strings.Repeat("A standing instruction every prompt carries. ", 6000))
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked (the band runs)", got.Outcome)
	}
	gens := ledgerGenerations(t, e)
	if len(gens) == 0 {
		t.Fatal("no complete generation published")
	}
	var recorded bool
	for _, gen := range gens {
		for _, limit := range gen.ScopeReport.KnownLimits {
			if limit == "over_budget" {
				recorded = true
			}
		}
	}
	if !recorded {
		t.Error("no generation records over_budget")
	}
	var warned, switched bool
	for _, line := range got.Messages {
		if line.Kind != ui.KindWarn || !strings.Contains(line.Text, "over budget") {
			continue
		}
		warned = true
		// In this band every call still packs against the packing
		// limit: the hint must say so, not claim the hard limit.
		if strings.Contains(line.Action, "packing limit until") &&
			strings.Contains(line.Action, "only then against the hard limit") {
			switched = true
		}
	}
	if !warned {
		t.Error("the pass ran over budget with no terminal warning")
	}
	if !switched {
		t.Error("the over-budget hint does not match the packing switch")
	}
}

