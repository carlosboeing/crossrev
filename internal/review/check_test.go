package review_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/sandbox"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// checkEnv is a review leg with the check on: the bare default config,
// whose review.check is resolver.
func checkEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	// One concern: the check proves itself against a single review
	// call, and a second concern's call would only spend a script
	// entry. Cases proving concern interplay set their own config.
	e.cfg = mustConfig(t, "version: 2\nreview:\n  concerns: [correctness]\n")
	return e
}

// checkPayload builds a check answer from its decision entries.
func checkPayload(decisions ...string) string {
	return `{"decisions":[` + strings.Join(decisions, ",") + `]}`
}

func confirmDecision(position int) string {
	return `{"position":` + itoa2(position) + `,"decision":"confirmed","duplicate_of":null,"reason":"the defect reads real on the excerpt","severity":null,"pre_existing":null}`
}

func rejectDecision(position int, reason string) string {
	return `{"position":` + itoa2(position) + `,"decision":"rejected","duplicate_of":null,"reason":` + jsonQuote(reason) + `,"severity":null,"pre_existing":null}`
}

func duplicateDecision(position, target int, reason string) string {
	return `{"position":` + itoa2(position) + `,"decision":"duplicate","duplicate_of":` + itoa2(target) + `,"reason":` + jsonQuote(reason) + `,"severity":null,"pre_existing":null}`
}

func jsonQuote(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// postedReviewBodies answers what the leg posted as review comments.
func postedReviewBodies(e *env) []string {
	out := make([]string, 0, len(e.forge.reviewPosted))
	for _, posted := range e.forge.reviewPosted {
		out = append(out, posted.Body)
	}
	return out
}

// finalClaimBody answers the last claim edit: the complete pass comment.
func finalClaimBody(e *env) string {
	if len(e.forge.edits) == 0 {
		return ""
	}
	return e.forge.edits[len(e.forge.edits)-1]
}

// checkRecordOf decodes the marker's check record, failing when none is
// recorded.
func checkRecordOf(t *testing.T, marker prstate.Marker) prstate.CheckRecord {
	t.Helper()
	record, ok := marker.DecodeCheckRecord()
	if !ok {
		t.Fatalf("the marker carries no check record: %+v", marker)
	}
	return record
}

// The check judges each finding on the resolver's harness before
// anything posts: the confirmed candidate posts, the rejected one never
// does, and the summary names it.
func TestCheckConfirmsAndRejectsBeforePosting(t *testing.T) {
	e := checkEnv(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
		{ExitCode: 0, Stdout: claudeStdout(checkPayload(confirmDecision(1), rejectDecision(2, "the inferred type is exactly right")))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.reviewPosted) != 1 {
		t.Fatalf("posted %d finding comments, want 1 (the confirmed candidate)", len(e.forge.reviewPosted))
	}
	if !strings.Contains(e.forge.reviewPosted[0].Body, "Unchecked fetch response") {
		t.Errorf("posted body = %s, want the confirmed finding", e.forge.reviewPosted[0].Body)
	}
	if strings.Contains(e.forge.reviewPosted[0].Body, "Missing return type") {
		t.Errorf("posted body = %s, want the rejected finding absent", e.forge.reviewPosted[0].Body)
	}
	if got.Marker.Check.Value() != prstate.CheckRan {
		t.Errorf("check = %q, want ran", got.Marker.Check.Value())
	}
	record := checkRecordOf(t, got.Marker)
	if len(record.Decisions) != 2 || record.Harness != "claude" {
		t.Errorf("record = %+v, want 2 decisions from the claude checker", record)
	}
	if record.Digest == "" || record.Base == "" || record.Head == "" {
		t.Errorf("record carries no digest and revision: %+v", record)
	}
	checkedOut := got.Marker.DecodeCheckedOut()
	if len(checkedOut) != 1 || checkedOut[0].Decision != "rejected" || checkedOut[0].Position != 2 {
		t.Fatalf("checked_out = %+v, want the rejected candidate", checkedOut)
	}
	// The rejected entry never reaches the resolver input: posted:false
	// is the filter the resolve leg reads.
	findings := parseTestFindings(t, got.Marker.Findings)
	posted := map[string]bool{}
	for _, f := range findings {
		var postedFlag *bool
		if raw, ok := f["posted"]; ok {
			_ = json.Unmarshal(raw, &postedFlag)
		}
		posted[string(f["title"])] = postedFlag == nil || *postedFlag
	}
	if !posted[`"Unchecked fetch response"`] || posted[`"Missing return type"`] {
		t.Errorf("posted flags = %v, want only the confirmed finding posted", posted)
	}
	body := finalClaimBody(e)
	if !strings.Contains(body, "1 candidate rejected by the cross-model check:") {
		t.Errorf("summary names no rejection:\n%s", body)
	}
	if !strings.Contains(body, "the inferred type is exactly right") {
		t.Errorf("summary carries no reason:\n%s", body)
	}
	if strings.Contains(body, "Missing return type") && strings.Contains(body, "| low") {
		t.Errorf("the rejected finding reaches the findings table:\n%s", body)
	}
	joined := ui.Joined(got.Messages)
	if !strings.Contains(joined, "The cross-model check rejected 1 of 2 candidates; posting the 1 confirmed.") {
		t.Errorf("messages = %q, want the filtered line", joined)
	}
	log := readRunLog(t, e)
	named := false
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, " call 2 ") && strings.HasSuffix(line, "kind=check concern=- part=-") {
			named = true
		}
	}
	if !named {
		t.Errorf("run.log carries no kind=check call line:\n%s", log)
	}
	if !strings.Contains(log, "check result=ran candidates=2 confirmed=1 rejected=1 duplicate=0") {
		t.Errorf("run.log carries no check outcome:\n%s", log)
	}
	if !strings.Contains(log, "check decision position=2 decision=rejected") {
		t.Errorf("run.log carries no per-decision record:\n%s", log)
	}
}

// Positions count path order even when the reviewer lists files
// backwards: the b.go finding arrives first, a.go judges as position
// 1, and the rejection lands on a.go's entry.
func TestCheckPositionsCountPathOrder(t *testing.T) {
	e := checkEnv(t)
	findings := `[{"path":"b.go","line":1,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"Bee","why":"w","fix":"f"},` +
		`{"path":"a.go","line":1,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"Aye","why":"w","fix":"f"}]`
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(findings))},
		{ExitCode: 0, Stdout: claudeStdout(checkPayload(rejectDecision(1, "a.go is fine"), confirmDecision(2)))},
	}
	var checkPrompt string
	prompts := capturePrompt(e)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	for _, prompt := range *prompts {
		if strings.Contains(prompt, "You are the cross-model check") {
			checkPrompt = prompt
		}
	}
	if first, second := strings.Index(checkPrompt, "## Candidate 1 of 2"), strings.Index(checkPrompt, "## Candidate 2 of 2"); first < 0 || second < first {
		t.Fatalf("check prompt numbers no two candidates:\n%s", checkPrompt)
	} else if !strings.Contains(checkPrompt[first:second], "a.go:1") || !strings.Contains(checkPrompt[second:], "b.go:1") {
		t.Errorf("position 1 judges no a.go finding:\n%s", checkPrompt)
	}
	// Both findings anchor file-level off the fixture diff, so the
	// survivor posts as a file comment.
	if len(e.forge.filePosted) != 1 {
		t.Fatalf("posted %d file comments, want 1 (b.go)", len(e.forge.filePosted))
	}
	if !strings.Contains(e.forge.filePosted[0].Body, "Bee") {
		t.Errorf("posted body = %s, want the b.go finding", e.forge.filePosted[0].Body)
	}
	checkedOut := got.Marker.DecodeCheckedOut()
	if len(checkedOut) != 1 || checkedOut[0].Path != "a.go" || checkedOut[0].Position != 1 {
		t.Errorf("checked_out = %+v, want a.go at position 1", checkedOut)
	}
}

// A duplicate folds onto its confirmed survivor: one comment posts,
// with the group's highest severity.
func TestCheckDuplicateFoldsOntoTheSurvivor(t *testing.T) {
	e := checkEnv(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
		{ExitCode: 0, Stdout: claudeStdout(checkPayload(confirmDecision(1), duplicateDecision(2, 1, "the same unchecked fetch")))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.reviewPosted) != 1 {
		t.Fatalf("posted %d finding comments, want 1 (the survivor)", len(e.forge.reviewPosted))
	}
	checkedOut := got.Marker.DecodeCheckedOut()
	if len(checkedOut) != 1 || checkedOut[0].Decision != "duplicate" {
		t.Fatalf("checked_out = %+v, want the folded duplicate", checkedOut)
	}
	body := finalClaimBody(e)
	if strings.Contains(body, "rejected by the cross-model check") {
		t.Errorf("summary lists a rejection for a duplicate fold:\n%s", body)
	}
}

// Corrections replace the reviewer's values before posting, and the
// originals stay on the finding for the record.
func TestCheckCorrectionsApplyBeforePosting(t *testing.T) {
	e := checkEnv(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
		{ExitCode: 0, Stdout: claudeStdout(checkPayload(
			`{"position":1,"decision":"confirmed","duplicate_of":null,"reason":"real, and older than the change","severity":"high","pre_existing":true}`,
			confirmDecision(2),
		))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.reviewPosted) != 2 {
		t.Fatalf("posted %d finding comments, want 2", len(e.forge.reviewPosted))
	}
	findings := parseTestFindings(t, got.Marker.Findings)
	first := findings[0]
	if string(first["severity"]) != `"high"` || string(first["pre_existing"]) != "true" {
		t.Errorf("corrected finding = %v, want the checker's values", first)
	}
	// The reviewer raised high already: an identical correction
	// records no original.
	if _, ok := first["original_severity"]; ok {
		t.Errorf("identical severity records an original: %v", first)
	}
	if string(first["original_pre_existing"]) != "false" {
		t.Errorf("corrected finding records no original pre_existing: %v", first)
	}
}

// A check that fails past its retries degrades: every candidate posts
// unchecked, and the summary and marker say so with the reason.
func TestCheckDegradedPostsAllWithTheNote(t *testing.T) {
	e := checkEnv(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
		{ExitCode: 0, Stdout: claudeStdout(`{"verdict":"issues-remain","findings":[]}`)},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.reviewPosted) != 2 {
		t.Fatalf("posted %d finding comments, want 2 (every candidate unchecked)", len(e.forge.reviewPosted))
	}
	if got.Marker.Check.Value() != prstate.CheckDegraded {
		t.Errorf("check = %q, want degraded", got.Marker.Check.Value())
	}
	if got.Marker.CheckReason.Value() != "answer_rejected" {
		t.Errorf("check_reason = %q, want answer_rejected", got.Marker.CheckReason.Value())
	}
	if _, ok := got.Marker.DecodeCheckRecord(); ok {
		t.Error("a degraded check records decisions")
	}
	body := finalClaimBody(e)
	if !strings.Contains(body, "check: degraded (answer_rejected): every candidate posted unchecked.") {
		t.Errorf("summary carries no degrade note:\n%s", body)
	}
	joined := ui.Joined(got.Messages)
	if !strings.Contains(joined, "the cross-model check degraded (answer_rejected), so every candidate posts unchecked") {
		t.Errorf("messages = %q, want the degrade warning", joined)
	}
}

// A quota failure on the checker degrades with the quota reason rather
// than failing the pass.
func TestCheckQuotaFailureDegrades(t *testing.T) {
	e := checkEnv(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
		{ExitCode: 0, Stdout: []byte("{\"type\":\"result\",\"result\":\"quota exceeded for this window\",\"is_error\":true}\n")},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.reviewPosted) != 2 {
		t.Fatalf("posted %d finding comments, want 2", len(e.forge.reviewPosted))
	}
	if got.Marker.Check.Value() != prstate.CheckDegraded || got.Marker.CheckReason.Value() != "quota" {
		t.Errorf("check = %q %q, want degraded quota", got.Marker.Check.Value(), got.Marker.CheckReason.Value())
	}
}

// A checker whose review isolation is unverified cannot check: the pass
// posts unchecked and records unavailable with the isolation reason,
// without starting a check call.
func TestCheckUnavailableOnUnverifiedIsolation(t *testing.T) {
	e := checkEnv(t)
	e.cfg = mustConfig(t, "version: 2\nresolver:\n  harness: grok\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 1 {
		t.Fatalf("harness calls = %d, want 1 (the review; no check call starts)", e.runner.calls)
	}
	if len(e.forge.reviewPosted) != 2 {
		t.Fatalf("posted %d finding comments, want 2", len(e.forge.reviewPosted))
	}
	if got.Marker.Check.Value() != prstate.CheckUnavailable || got.Marker.CheckReason.Value() != "review_isolation_unverified" {
		t.Errorf("check = %q %q, want unavailable review_isolation_unverified", got.Marker.Check.Value(), got.Marker.CheckReason.Value())
	}
	body := finalClaimBody(e)
	if !strings.Contains(body, "check: unavailable (review_isolation_unverified): every candidate posted unchecked.") {
		t.Errorf("summary carries no unavailable note:\n%s", body)
	}
}

// An unknown checker harness records unavailable without a call.
func TestCheckUnavailableOnUnknownHarness(t *testing.T) {
	e := checkEnv(t)
	e.cfg = mustConfig(t, "version: 2\nresolver:\n  harness: nope\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 1 {
		t.Fatalf("harness calls = %d, want 1 (the review; no check call starts)", e.runner.calls)
	}
	if got.Marker.Check.Value() != prstate.CheckUnavailable || got.Marker.CheckReason.Value() != "unknown_harness" {
		t.Errorf("check = %q %q, want unavailable unknown_harness", got.Marker.Check.Value(), got.Marker.CheckReason.Value())
	}
}

// Zero candidates make no check call and record no_candidates.
func TestCheckNoCandidatesMakesNoCall(t *testing.T) {
	e := checkEnv(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(convergedPayload())},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 1 {
		t.Fatalf("harness calls = %d, want 1 (the review; no check call)", e.runner.calls)
	}
	if got.Marker.Check.Value() != prstate.CheckNoCandidates {
		t.Errorf("check = %q, want no_candidates", got.Marker.Check.Value())
	}
	if !strings.Contains(readRunLog(t, e), "check result=no_candidates") {
		t.Errorf("run.log records no no_candidates outcome")
	}
}

// A check configured off posts everything with no second call.
func TestCheckOffPostsWithoutACall(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 2\nreview:\n  check: off\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 1 {
		t.Fatalf("harness calls = %d, want 1 (the review; no check call)", e.runner.calls)
	}
	if len(e.forge.reviewPosted) != 2 {
		t.Fatalf("posted %d finding comments, want 2", len(e.forge.reviewPosted))
	}
	if got.Marker.Check.Value() != prstate.CheckOff {
		t.Errorf("check = %q, want off", got.Marker.Check.Value())
	}
}

// A check that ran on the reviewer's own model is recorded as
// same_model, never as cross-model: the decisions stand, and the
// summary says what lineage the pass had.
func TestCheckSameModelRecordsSameModel(t *testing.T) {
	e := checkEnv(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdoutWithUsage(t, issuesPayload(twoFindings), "shared-model", 10, 0, 0, 5)},
		{ExitCode: 0, Stdout: claudeStdoutWithUsage(t, checkPayload(confirmDecision(1), rejectDecision(2, "wrong")), "shared-model", 20, 0, 0, 8)},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Marker.Check.Value() != prstate.CheckRan {
		t.Fatalf("check = %q, want ran", got.Marker.Check.Value())
	}
	if got.Marker.CheckReason.Value() != prstate.CheckReasonSameModel {
		t.Errorf("check_reason = %q, want same_model", got.Marker.CheckReason.Value())
	}
	if len(e.forge.reviewPosted) != 1 {
		t.Errorf("posted %d finding comments, want 1 (the decisions stand)", len(e.forge.reviewPosted))
	}
	body := finalClaimBody(e)
	if !strings.Contains(body, "check: ran (same_model): the checker answered as the model that reviewed, so this pass had no second lineage.") {
		t.Errorf("summary carries no same_model line:\n%s", body)
	}
	if strings.Contains(body, "cross-model") {
		t.Errorf("a same-model pass calls its check cross-model:\n%s", body)
	}
	joined := ui.Joined(got.Messages)
	if !strings.Contains(joined, "the cross-model check ran on the reviewer's own model") {
		t.Errorf("messages = %q, want the same_model warning", joined)
	}
}

// The checker's usage is priced with the checker's harness and model
// and kept off the review's own record: separate provenance, separate
// buckets.
func TestCheckUsageIsPricedWithTheChecker(t *testing.T) {
	e := checkEnv(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
		{ExitCode: 0, Stdout: claudeStdoutWithUsage(t, checkPayload(confirmDecision(1), confirmDecision(2)), "claude-opus-5", 1000, 0, 0, 100)},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	record := checkRecordOf(t, got.Marker)
	if len(record.Usage) == 0 || string(record.Usage) == "null" {
		t.Fatal("the check record carries no usage")
	}
	buckets := usageBuckets(t, record.Usage)
	if buckets["input_fresh"] != 1000 || buckets["output"] != 100 {
		t.Errorf("check usage buckets = %v, want the checker's", buckets)
	}
	var priced map[string]any
	if err := json.Unmarshal(record.Usage, &priced); err != nil {
		t.Fatalf("usage decode: %v", err)
	}
	// 1000 fresh and 100 output tokens of claude-opus-5 table-price
	// at 0.0075: the reviewer's empty model would price nothing, so
	// the figure proves the checker's model priced it.
	if priced["cost_usd"] != 0.0075 || priced["cost_source"] != "table" {
		t.Errorf("check usage prices as %v, want the checker's table cost", priced)
	}
	if len(got.Marker.Usage) != 0 && string(got.Marker.Usage) != "null" {
		t.Errorf("the review record carries usage %s, want the checker's kept separate", got.Marker.Usage)
	}
	if !strings.Contains(readRunLog(t, e), "kind=check concern=- part=-") {
		t.Error("run.log carries no kind=check call line")
	}
}

// A checker that runs a command trips the same tripwire the review leg
// does: the pass fails, and nothing posts.
func TestCheckTripwirePropagates(t *testing.T) {
	const sentinel = "touch /tmp/crossrev-check-tripwire-sentinel-9f31aa-probe"
	e := checkEnv(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
		{ExitCode: 0, Stdout: []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":` + jsonQuote(sentinel) + `}}]}}` + "\n" + string(claudeStdout(checkPayload(confirmDecision(1), confirmDecision(2)))))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("want the tripwire to fail the pass")
	}
	if !strings.Contains(got.Err.Error(), "review_leg_ran_command") {
		t.Fatalf("err = %v, want the review_leg_ran_command name", got.Err)
	}
	if len(e.forge.reviewPosted) != 0 {
		t.Errorf("posted %d finding comments past the tripwire", len(e.forge.reviewPosted))
	}
	if got.Marker.Check.Value() == prstate.CheckRan {
		t.Error("a tripped check records ran")
	}
}

// A quarantine restore failure under the check fails the pass the way
// the same failure under the review does, with the restore warning.
func TestCheckRestoreFailurePropagates(t *testing.T) {
	e := checkEnv(t)
	if err := os.WriteFile(e.dir+"/CLAUDE.md", []byte("quarantine me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stash := filepath.Join(e.dir, sandbox.QuarantineDir)
	modelCalls := 0
	e.runner.onSpec = func(exec.Spec) {
		// The hook fires for model calls only: probes and read
		// sessions return before it. The second model call is the
		// check's, and sealing its stash fails its restore.
		modelCalls++
		if modelCalls == 2 {
			if err := os.Chmod(stash, 0o555); err != nil {
				t.Errorf("sealing the stash: %v", err)
			}
		}
	}
	defer func() { _ = os.Chmod(stash, 0o755) }()
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
		{ExitCode: 0, Stdout: claudeStdout(checkPayload(confirmDecision(1), confirmDecision(2)))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("want the restore failure to fail the pass")
	}
	if !strings.Contains(got.Err.Error(), "could not be put back") {
		t.Fatalf("err = %v, want the restore failure", got.Err)
	}
	if len(e.forge.reviewPosted) != 0 {
		t.Errorf("posted %d finding comments past the restore failure", len(e.forge.reviewPosted))
	}
	joined := ui.Joined(got.Messages)
	if !strings.Contains(joined, "could not be put back") {
		t.Errorf("messages = %q, want the restore warning", joined)
	}
}

// Cancellation under the check propagates as cancellation, and the
// claim stays resumable: the next run checks again and posts once.
func TestCheckCancellationLeavesAResumableClaim(t *testing.T) {
	e := checkEnv(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
		{ExitCode: 0, Err: context.Canceled},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := e.request(t)
	leg := e.leg(t)
	got := leg.Run(ctx, req)
	if got.Err == nil {
		t.Fatal("want the cancellation to fail the run")
	}
	if !errors.Is(got.Err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", got.Err)
	}
	if len(e.forge.reviewPosted) != 0 {
		t.Fatalf("posted %d finding comments before the cancellation", len(e.forge.reviewPosted))
	}
	// The claim carries the recorded findings with no check: a resume
	// in the candidates state.
	if !strings.Contains(e.forge.edits[len(e.forge.edits)-1], "Findings recorded; posting them now.") {
		t.Fatalf("the claim carries no recorded findings: %q", e.forge.edits)
	}
	e.runner.script = append(e.runner.script, exec.Result{
		ExitCode: 0, Stdout: claudeStdout(checkPayload(confirmDecision(1), rejectDecision(2, "wrong on the excerpt"))),
	})
	second := runLeg(t, e, e.request(t))
	if second.Err != nil {
		t.Fatalf("resume: %v", second.Err)
	}
	if len(e.forge.reviewPosted) != 1 {
		t.Fatalf("posted %d finding comments, want 1 (the confirmed candidate, once)", len(e.forge.reviewPosted))
	}
	if second.Marker.Check.Value() != prstate.CheckRan {
		t.Errorf("check = %q, want ran", second.Marker.Check.Value())
	}
}

// seedCheckedClaim turns a completed pass marker into the open claim a
// resume would find: started, freshly timestamped, thread linkage
// stripped, the check record kept. mutate edits the marker document
// before it encodes.
func seedCheckedClaim(t *testing.T, final prstate.Marker, mutate func(doc map[string]json.RawMessage)) prstate.Marker {
	t.Helper()
	raw, err := final.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("marker decode: %v", err)
	}
	doc["state"] = json.RawMessage(`"started"`)
	delete(doc, "done_ts")
	doc["ts"] = json.RawMessage(strconv.FormatInt(frozenNow.Unix(), 10))
	var findings []map[string]json.RawMessage
	if err := json.Unmarshal(doc["findings"], &findings); err != nil {
		t.Fatalf("findings decode: %v", err)
	}
	for _, f := range findings {
		delete(f, "thread_id")
		delete(f, "root_comment_id")
	}
	doc["findings"], err = json.Marshal(findings)
	if err != nil {
		t.Fatalf("findings encode: %v", err)
	}
	if mutate != nil {
		mutate(doc)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marker encode: %v", err)
	}
	return parseMarker(t, string(out))
}

// runCheckedPass runs one full pass that records a check, and answers
// its marker.
func runCheckedPass(t *testing.T) prstate.Marker {
	t.Helper()
	e := checkEnv(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
		{ExitCode: 0, Stdout: claudeStdout(checkPayload(confirmDecision(1), rejectDecision(2, "the inferred type is exactly right")))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Marker.Check.Value() != prstate.CheckRan {
		t.Fatalf("check = %q, want ran", got.Marker.Check.Value())
	}
	return got.Marker
}

// An unchanged resume reuses the recorded check: no model call, the
// confirmed candidate posts once, the rejected one never does.
func TestCheckResumeReusesTheRecordedCheck(t *testing.T) {
	final := runCheckedPass(t)
	e := checkEnv(t)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, seedCheckedClaim(t, final, nil))}
	e.forge.nextID = 10001
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("resume: %v", got.Err)
	}
	if e.runner.calls != 0 {
		t.Fatalf("harness calls = %d, want 0 (the recorded check is reused)", e.runner.calls)
	}
	if len(e.forge.reviewPosted) != 1 {
		t.Fatalf("posted %d finding comments, want 1 (the confirmed candidate)", len(e.forge.reviewPosted))
	}
	if !strings.Contains(e.forge.reviewPosted[0].Body, "Unchecked fetch response") {
		t.Errorf("posted body = %s, want the confirmed finding", e.forge.reviewPosted[0].Body)
	}
	if got.Marker.Check.Value() != prstate.CheckRan {
		t.Errorf("check = %q, want ran", got.Marker.Check.Value())
	}
	if len(got.Marker.DecodeCheckedOut()) != 1 {
		t.Errorf("checked_out = %+v, want the rebuilt rejection", got.Marker.DecodeCheckedOut())
	}
	joined := ui.Joined(got.Messages)
	if !strings.Contains(joined, "Reusing the recorded cross-model check (2 decisions).") {
		t.Errorf("messages = %q, want the reuse line", joined)
	}
}

// A resume whose candidate set changed discards the recorded decisions
// and checks again.
func TestCheckResumeDiscardsOnChangedCandidates(t *testing.T) {
	final := runCheckedPass(t)
	e := checkEnv(t)
	claim := seedCheckedClaim(t, final, func(doc map[string]json.RawMessage) {
		var findings []map[string]json.RawMessage
		if err := json.Unmarshal(doc["findings"], &findings); err != nil {
			t.Fatalf("findings decode: %v", err)
		}
		findings[0]["title"] = json.RawMessage(`"Unchecked fetch response, retitled"`)
		doc["findings"], _ = json.Marshal(findings)
	})
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, claim)}
	e.forge.nextID = 10001
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(checkPayload(confirmDecision(1), confirmDecision(2)))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("resume: %v", got.Err)
	}
	if e.runner.calls != 1 {
		t.Fatalf("harness calls = %d, want 1 (the re-check)", e.runner.calls)
	}
	if len(e.forge.reviewPosted) != 2 {
		t.Fatalf("posted %d finding comments, want 2 (both confirmed on the re-check)", len(e.forge.reviewPosted))
	}
	if !strings.Contains(readRunLog(t, e), "discarded the recorded check: the candidate set changed") {
		t.Error("run.log records no discard with the candidate-set reason")
	}
	if checkRecordOf(t, got.Marker).Digest == checkRecordOf(t, final).Digest {
		t.Error("the re-check kept the discarded digest")
	}
}

// A resume whose revision moved discards the recorded decisions and
// checks again.
func TestCheckResumeDiscardsOnMovedRevision(t *testing.T) {
	final := runCheckedPass(t)
	e := checkEnv(t)
	claim := seedCheckedClaim(t, final, nil)
	// The record was written against another head: the candidates read
	// from a revision the checker never judged.
	record, ok := claim.DecodeCheckRecord()
	if !ok {
		t.Fatal("the seed carries no check record")
	}
	record.Head = "3333333333333333333333333333333333333333"
	raw, err := claim.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["check_record"], err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	claim = parseMarker(t, string(out))
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, claim)}
	e.forge.nextID = 10001
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(checkPayload(confirmDecision(1), confirmDecision(2)))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("resume: %v", got.Err)
	}
	if e.runner.calls != 1 {
		t.Fatalf("harness calls = %d, want 1 (the re-check)", e.runner.calls)
	}
	if !strings.Contains(readRunLog(t, e), "discarded the recorded check: the revision moved") {
		t.Error("run.log records no discard with the revision reason")
	}
}

// A resume with the check turned off discards the recorded decisions
// and posts everything, with no model call.
func TestCheckResumeDiscardsOnChangedSetting(t *testing.T) {
	final := runCheckedPass(t)
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 2\nreview:\n  check: off\n")
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, seedCheckedClaim(t, final, nil))}
	e.forge.nextID = 10001
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("resume: %v", got.Err)
	}
	if e.runner.calls != 0 {
		t.Fatalf("harness calls = %d, want 0", e.runner.calls)
	}
	if len(e.forge.reviewPosted) != 2 {
		t.Fatalf("posted %d finding comments, want 2 (the discarded rejection posts)", len(e.forge.reviewPosted))
	}
	if got.Marker.Check.Value() != prstate.CheckOff {
		t.Errorf("check = %q, want off", got.Marker.Check.Value())
	}
	if _, ok := got.Marker.DecodeCheckRecord(); ok {
		t.Error("the discarded record survives")
	}
	if len(got.Marker.DecodeCheckedOut()) != 0 {
		t.Errorf("checked_out = %+v, want it cleared", got.Marker.DecodeCheckedOut())
	}
	findings := parseTestFindings(t, got.Marker.Findings)
	for _, f := range findings {
		if raw, ok := f["posted"]; ok && string(raw) == "false" {
			t.Errorf("a discarded stamp still holds a finding back: %v", f)
		}
	}
}

// The batch path checks too: merged batch findings are candidates, and
// only the confirmed ones post.
func TestCheckBatchPathConfirmsBeforePosting(t *testing.T) {
	e := checkEnv(t)
	writeRequiredHead(e, "app.go", "package app\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(findingAnswer(t, "app.go", nil, "Unchecked fetch"))},
		{ExitCode: 0, Stdout: claudeStdout(checkPayload(confirmDecision(1)))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 2 {
		t.Fatalf("harness calls = %d, want 2 (the batch and the check)", e.runner.calls)
	}
	// The batch finding anchors file-level (line 1 is context in the
	// fixture diff), so it posts as a file comment.
	if len(e.forge.filePosted) != 1 {
		t.Fatalf("posted %d file comments, want 1", len(e.forge.filePosted))
	}
	if got.Marker.Check.Value() != prstate.CheckRan {
		t.Errorf("check = %q, want ran", got.Marker.Check.Value())
	}
	if record := checkRecordOf(t, got.Marker); len(record.Decisions) != 1 {
		t.Errorf("record decisions = %d, want 1", len(record.Decisions))
	}
}

// A batch finding outside the diff still reads an excerpt: the
// committed lines around its anchor at head.
func TestCheckBatchOutsideDiffReadsCommittedLines(t *testing.T) {
	e := checkEnv(t)
	writeRequiredHead(e, "app.go", "package app\n")
	writeRequiredHead(e, "other.go", "package other\nline three\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(findingAnswer(t, "other.go", []string{"app.go"}, "Stale comment"))},
		{ExitCode: 0, Stdout: claudeStdout(checkPayload(confirmDecision(1)))},
	}
	prompts := capturePrompt(e)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Marker.Check.Value() != prstate.CheckRan {
		t.Fatalf("check = %q, want ran", got.Marker.Check.Value())
	}
	var checkPrompt string
	for _, prompt := range *prompts {
		if strings.Contains(prompt, "You are the cross-model check") {
			checkPrompt = prompt
		}
	}
	if checkPrompt == "" {
		t.Fatal("no check prompt was rendered")
	}
	if !strings.Contains(checkPrompt, "other.go lines 1-2 at head") {
		t.Errorf("check prompt carries no committed-lines excerpt:\n%s", checkPrompt)
	}
	if !strings.Contains(checkPrompt, "The anchored lines changed between base and head: no.") {
		t.Errorf("check prompt carries no unchanged-lines fact:\n%s", checkPrompt)
	}
}

// An oversized checked-out record fails the marker write loudly: no
// partial write lands, and no decision is dropped to fit.
func TestCheckOversizedRecordFailsTheMarkerWrite(t *testing.T) {
	e := checkEnv(t)
	const n = 150
	var findings, decisions strings.Builder
	findings.WriteString("[")
	for i := 1; i <= n; i++ {
		if i > 1 {
			findings.WriteString(",")
			decisions.WriteString(",")
		}
		fmt.Fprintf(&findings, `{"path":"app.go","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"Defect %d","why":"w","fix":"f"}`, i)
		fmt.Fprintf(&decisions, `{"position":%d,"decision":"rejected","duplicate_of":null,"reason":%s,"severity":null,"pre_existing":null}`, i, jsonQuote(strings.Repeat("r", 300)))
	}
	findings.WriteString("]")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(findings.String()))},
		{ExitCode: 0, Stdout: claudeStdout(checkPayload(decisions.String()))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("want the oversized marker write to fail the pass")
	}
	// Loud, as today: the retention ladder sheds coverage payloads
	// only, so an unsheddable marker fails the write instead of
	// dropping a decision to fit.
	if !strings.Contains(got.Err.Error(), "coverage ledger exhausted") {
		t.Fatalf("err = %v, want the loud write failure", got.Err)
	}
	if len(e.forge.reviewPosted) != 0 {
		t.Errorf("posted %d finding comments past the failed write", len(e.forge.reviewPosted))
	}
	// No partial write anywhere: no truncated checked-out list and
	// no shed record reaches the claim, however the pass ended.
	for _, edit := range e.forge.edits {
		if strings.Contains(edit, "checked_out") || strings.Contains(edit, "check_record") {
			t.Error("a partial check write landed on the claim")
		}
	}
}

// A rejected candidate raised again on a later pass is checked again:
// the new pass records its own decisions.
func TestCheckRechecksARaisedAgainCandidate(t *testing.T) {
	final := runCheckedPass(t)
	if len(final.DecodeCheckedOut()) != 1 {
		t.Fatalf("checked_out = %+v, want the first pass's rejection", final.DecodeCheckedOut())
	}
	e := checkEnv(t)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))},
		{ExitCode: 0, Stdout: claudeStdout(checkPayload(confirmDecision(1), confirmDecision(2)))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 2 {
		t.Fatalf("harness calls = %d, want 2 (the review and the fresh check)", e.runner.calls)
	}
	if len(e.forge.reviewPosted) != 2 {
		t.Fatalf("posted %d finding comments, want 2 (both confirmed on the fresh check)", len(e.forge.reviewPosted))
	}
	if got.Marker.Check.Value() != prstate.CheckRan {
		t.Errorf("check = %q, want ran", got.Marker.Check.Value())
	}
}

