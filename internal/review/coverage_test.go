package review_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/policy"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
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
		b.WriteString(`{"unit_number":` + itoa2(i) + `,"verdict":"no_issue","finding_numbers":[],"evidence":[{"path":"` + batchPath(i) + `","revision":"` + headSHA + `","start_line":null,"end_line":null,"source":"git","note":null}],"reason":null}`)
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

// batchAnswerFor answers n numbered units over the given paths in order, all
// no_issue. Paths beyond the two-file helper are fileNN.go in order.
func batchAnswerFor(t *testing.T, paths []string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"verdict":"issues-remain","blocked_reason":null,"findings":[],"coverage":[`)
	for i, path := range paths {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"unit_number":` + itoa2(i+1) + `,"verdict":"no_issue","finding_numbers":[],"evidence":[{"path":"` + path + `","revision":"` + headSHA + `","start_line":null,"end_line":null,"source":"git","note":null}],"reason":null}`)
	}
	b.WriteString(`],"examined_scope":"read the batch","known_limits":[]}`)
	return b.String()
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

// ledgerGenerations returns every complete generation the leg published, in
// publication order: the initial outstanding generation plus one per
// accepted batch.
func ledgerGenerations(t *testing.T, e *env) []prstate.Generation {
	t.Helper()
	store, ok := e.forge.store.(*storetest.FakeStore)
	if !ok {
		t.Fatalf("fixture store is %T, want *storetest.FakeStore", e.forge.store)
	}
	return store.Published()
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

// Corrupt coverage bytes fail the pass closed: the leg reports the error
// rather than re-reviewing from zero over a ledger it cannot read, and no
// model call goes out.
func TestReviewFailsClosedOnCorruptCoverage(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	// An open claim carrying a claim that is not a valid handle: a
	// generation number with no ref behind it. Corrupt state, never "no
	// coverage".
	seedStartedClaim(t, e, prstate.Marker{CoverageGen: prstate.Some(7)})
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))}}

	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("corrupt coverage did not fail the pass")
	}
	if e.runner.calls != 0 {
		t.Errorf("harness calls = %d, want 0 (fail closed before the first model call)", e.runner.calls)
	}
}

// TestReviewRetriesSemanticOmissionOnce pins the one semantic retry with the
// missing unit named: the first answer omits unit 2 of 2, the leg asks once
// more quoting the missing number, and the accepted retry publishes.
func TestReviewRetriesSemanticOmissionOnce(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	writeRequiredHead(e, "b.go", "package b\n")
	omitSecond := `{"verdict":"issues-remain","blocked_reason":null,"findings":[],"coverage":[{"unit_number":1,"verdict":"no_issue","finding_numbers":[],"evidence":[],"reason":null}],"examined_scope":"read half the batch","known_limits":[]}`
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
// verdicts, while a moved head starts from zero accepted units.
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

// acceptedReuse counts the prior generation's verdicts the leg would
// reuse at the given revision pair under the current engine.
func acceptedReuse(t *testing.T, e *env, base, head core.Revision) int {
	t.Helper()
	accepted := 0
	for _, gen := range ledgerGenerations(t, e) {
		if gen.Revision.Base.SHA() != base.SHA() || gen.Revision.Head.SHA() != head.SHA() || gen.Engine != core.FileEngineVersion {
			continue
		}
		for _, record := range gen.Records {
			if record.Type == "unit" && record.Verdict.Present() {
				accepted++
			}
		}
	}
	return accepted
}

var _ = context.Background

// TestReviewWriterDowngradesUncoveredConvergedVerdict pins route 1: an
// accepted converged answer with one file unexaminable (could_not_review)
// cannot complete green — the writer records blocked with the debt named,
// applies no converged label, and never falls back to the model verdict.
func TestReviewWriterDowngradesUncoveredConvergedVerdict(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	writeRequiredHead(e, "b.go", "package b\n")
	unexaminable := `{"verdict":"converged","blocked_reason":null,"findings":[],"coverage":[` +
		`{"unit_number":1,"verdict":"no_issue","finding_numbers":[],` +
		`"evidence":[{"path":"a.go","revision":"` + headSHA + `","start_line":null,"end_line":null,"source":"git","note":null}],"reason":null},` +
		`{"unit_number":2,"verdict":"could_not_review","finding_numbers":[],` +
		`"evidence":[{"path":"b.go","revision":"` + headSHA + `","start_line":null,"end_line":null,"source":"git","note":null}],` +
		`"reason":"binary content could not be read, fallback search found nothing"}],` +
		`"examined_scope":"read the batch","known_limits":["b.go is binary"]}`
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(unexaminable)},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 1 {
		t.Fatalf("harness calls = %d, want 1 (accepted, not retried)", e.runner.calls)
	}
	for _, label := range e.forge.labelsAdded {
		if label == policy.LabelConverged {
			t.Fatalf("converged label applied with one file unexamined: %v", e.forge.labelsAdded)
		}
	}
	if verdict := got.Marker.Verdict.Value(); verdict != "blocked" {
		t.Errorf("marker verdict = %q, want blocked (downgraded from converged)", verdict)
	}
	if reason, _ := got.Marker.BlockedReason.Get(); !strings.Contains(reason, "could not be examined") {
		t.Errorf("blocked reason = %q, want the coverage debt named", reason)
	}
	// The downgrade must reach the stored bytes, not just the in-memory
	// marker: downstream readers reload the claim comment.
	if len(e.forge.edits) == 0 {
		t.Fatal("no claim edits stored")
	}
	stored := e.forge.edits[len(e.forge.edits)-1]
	if !strings.Contains(stored, `"verdict":"blocked"`) {
		t.Errorf("stored claim lacks the downgraded verdict")
	}
	if strings.Contains(stored, `"verdict":"converged"`) {
		t.Errorf("stored claim still carries the converged verdict")
	}
}

// TestReviewFailsClosedWhenFileEnumerationFails pins the enumeration failure
// mode: a git error listing the changed files stops the leg rather than
// falling through to the frozen single-prompt path, which carries no
// coverage obligation and would converge with no ledger.
func TestReviewFailsClosedWhenFileEnumerationFails(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.vcs.changedErr = errors.New("git ls-tree exploded")
	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("Run: want the enumeration failure returned, not bypassed")
	}
	if got.Outcome != review.OutcomeError {
		t.Errorf("Outcome = %q, want error", got.Outcome)
	}
	if !strings.Contains(got.Err.Error(), "git ls-tree exploded") {
		t.Errorf("Err = %v, want the enumeration failure named", got.Err)
	}
	if e.runner.calls != 0 {
		t.Errorf("harness calls = %d, want 0 (no review without a required set)", e.runner.calls)
	}
	for _, label := range e.forge.labelsAdded {
		if label == policy.LabelConverged {
			t.Fatalf("converged label applied with no coverage ledger: %v", e.forge.labelsAdded)
		}
	}
}

// capturePublished records each coverage generation candidate the leg hands
// to publication, in order. The candidates are the in-memory values, before
// any store encodes them, so a test reading them here sees what the leg
// supplied rather than what publication persisted. Set before runLeg; the
// observer resets when the test ends.
func capturePublished(t *testing.T) *[]prstate.Generation {
	t.Helper()
	published := &[]prstate.Generation{}
	review.ObservePublishedCandidate = func(candidate prstate.Generation) {
		*published = append(*published, candidate)
	}
	t.Cleanup(func() { review.ObservePublishedCandidate = nil })
	return published
}

// suppliedRecordFor returns the published record for one required path: the
// record whose path index names it. It fails the test when the generation
// carries no such record, so a lookup miss cannot read as a missing field.
func suppliedRecordFor(t *testing.T, gen prstate.Generation, path string) prstate.Record {
	t.Helper()
	for _, record := range gen.Records {
		if record.PathIndex >= 0 && record.PathIndex < len(gen.Paths) && gen.Paths[record.PathIndex] == path {
			return record
		}
	}
	t.Fatalf("no record for %q in generation %d (%d records)", path, gen.Gen, len(gen.Records))
	return prstate.Record{}
}

// bodyHandedTo extracts one numbered file's fenced content out of the prompt
// the stub harness was actually given: the bytes under the unit's section
// header, between the opening fence and the closing one. It reads the
// harness input, not the fixture and not the scope, so a digest derived from
// it agrees with the record only when the record describes what the model
// received. The prompt fence trims one trailing newline, so the fixture body
// carries none and the extraction is byte-exact.
func bodyHandedTo(t *testing.T, prompt, path string) []byte {
	t.Helper()
	header := "### 1. `" + path + "`"
	at := strings.Index(prompt, header)
	if at < 0 {
		t.Fatalf("prompt carries no numbered section for %q", path)
	}
	const fence = "````\n"
	open := strings.Index(prompt[at:], fence)
	if open < 0 {
		t.Fatalf("prompt section for %q carries no fenced content", path)
	}
	rest := prompt[at+open+len(fence):]
	close := strings.Index(rest, "\n````")
	if close < 0 {
		t.Fatalf("prompt section for %q has an unterminated fence", path)
	}
	return []byte(rest[:close])
}

// TestSuppliedDigestMatchesTheBytesHandedToTheHarness pins the point of the
// supplied field: the digest must match the bytes the harness got, not the
// bytes on disk and not the rendered prompt. The expected value is parsed
// out of the prompt the stub harness was actually given — hashing the
// fixture again on both sides would prove nothing — and the actual value is
// the candidate the leg handed to publication, which the v1 codec drops on
// the wire and store read-back can never observe.
func TestSuppliedDigestMatchesTheBytesHandedToTheHarness(t *testing.T) {
	e := newEnv(t)
	// No trailing newline: the prompt fence trims one, so this keeps the
	// extraction byte-exact (see bodyHandedTo).
	body := "package a\n\nconst HandedOver = true"
	writeRequiredHead(e, "a.go", body)
	prompts := capturePrompt(e)
	published := capturePublished(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(*prompts) != 1 {
		t.Fatalf("prompts = %d, want 1 (one file, one batch)", len(*prompts))
	}
	handed := bodyHandedTo(t, (*prompts)[0], "a.go")
	if string(handed) != body {
		t.Fatalf("extracted %q from the prompt, want the %q the scope read", handed, body)
	}
	if len(*published) == 0 {
		t.Fatal("no complete generation published")
	}
	record := suppliedRecordFor(t, (*published)[len(*published)-1], "a.go")
	supplied, ok := record.Supplied.Get()
	if !ok {
		t.Fatal("a judged record carries no supplied input")
	}
	sum := sha256.Sum256(handed)
	if want := hex.EncodeToString(sum[:]); supplied.Digest != want {
		t.Fatalf("supplied digest %s, want %s — the digest does not describe what the model received", supplied.Digest, want)
	}
	if supplied.Form != prstate.SuppliedFormFullText {
		t.Fatalf("form %q for a file supplied in full", supplied.Form)
	}
}

// TestAnUnavailableFileRecordsDiffOnly pins that binary, unreadable or
// quarantined content stays required with a named access limit and no body:
// the record must say diff_only rather than claim a full-text supply.
func TestAnUnavailableFileRecordsDiffOnly(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "blob.bin", "GIF89a\x00\x01binary-bytes")
	prompts := capturePrompt(e)
	published := capturePublished(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"blob.bin"}))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(*prompts) != 1 {
		t.Fatalf("prompts = %d, want 1 (one file, one batch)", len(*prompts))
	}
	if strings.Contains((*prompts)[0], "binary-bytes") {
		t.Fatal("prompt carried the binary body the batch block withholds")
	}
	if len(*published) == 0 {
		t.Fatal("no complete generation published")
	}
	record := suppliedRecordFor(t, (*published)[len(*published)-1], "blob.bin")
	supplied, ok := record.Supplied.Get()
	if !ok {
		t.Fatal("a judged binary record carries no supplied input")
	}
	if supplied.Form != prstate.SuppliedFormDiffOnly {
		t.Fatalf("form %q for a file that reached the model through the diff slice alone", supplied.Form)
	}
	if want := core.BodyDigestHex(nil); supplied.Digest != want {
		t.Fatalf("diff_only digest %s, want %s (no body bytes were handed over)", supplied.Digest, want)
	}
}

// TestReviewRefusesStaleGenerationWhenHeadMoves pins the publication
// freshness check: a push landing during the model invocation retires the
// accepted batch's candidate, so the generation is never committed and no
// convergence is published for the old head.
func TestReviewRefusesStaleGenerationWhenHeadMoves(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	moved := mustRev(t, "5555555555555555555555555555555555555555")
	e.runner.onSpec = func(exec.Spec) { e.forge.pr.HeadRefOid = moved }
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("Run: want the stale candidate refused after the head moved")
	}
	if !strings.Contains(got.Err.Error(), "moved") {
		t.Errorf("Err = %v, want the moved revision named", got.Err)
	}
	for _, label := range e.forge.labelsAdded {
		if label == policy.LabelConverged {
			t.Fatalf("converged label applied for the old head: %v", e.forge.labelsAdded)
		}
	}
	gens := ledgerGenerations(t, e)
	if len(gens) != 2 {
		t.Fatalf("published generations = %d, want 2 (the initial plus the stale candidate, whose handle was retired before the commit point)", len(gens))
	}
	if gen, _ := got.Marker.CoverageGen.Get(); gen != 1 {
		t.Fatalf("the marker names generation %d, want 1 (the initial checkpoint; the stale candidate's handle was retired before the commit point)", gen)
	}
}

// TestReviewRefusesConvergenceWhenHeadMovesAfterCoverage pins the freshness
// re-check at the label move: a push landing after the last generation
// committed still refuses convergence, because the label — not the ledger —
// is what drives the loop.
func TestReviewRefusesConvergenceWhenHeadMovesAfterCoverage(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	moved := mustRev(t, "5555555555555555555555555555555555555555")
	e.forge.onPullRequest = func(int) {
		if len(ledgerGenerations(t, e)) >= 2 {
			e.forge.pr.HeadRefOid = moved
		}
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("Run: want convergence refused after the head moved past the covered revision")
	}
	if !strings.Contains(got.Err.Error(), "moved") {
		t.Errorf("Err = %v, want the moved revision named", got.Err)
	}
	for _, label := range e.forge.labelsAdded {
		if label == policy.LabelConverged {
			t.Fatalf("converged label applied for the old head: %v", e.forge.labelsAdded)
		}
	}
	gens := ledgerGenerations(t, e)
	if len(gens) != 2 {
		t.Fatalf("complete generations = %d, want 2 (coverage committed before the push landed)", len(gens))
	}
}
