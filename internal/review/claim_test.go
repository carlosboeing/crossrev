package review_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/review"

	"github.com/carlosboeing/crossrev/internal/ui"
)

func TestClaimCreateFailureStopsBeforeAHarnessProcess(t *testing.T) {
	e := newEnv(t)
	e.forge.createErr = errors.New("gh could not post the comment")
	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("wanted a claim failure, got success")
	}
	if !strings.Contains(got.Err.Error(), "claim comment did not post") && !strings.Contains(got.Err.Error(), "could not post") {
		t.Errorf("err = %q, want it to name the failed claim", got.Err)
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatalf("harness started %d times after a claim failure", len(e.runner.Specs()))
	}
	events := e.log.all()
	for _, name := range events {
		if name == "harness" {
			t.Fatalf("events %v started a harness after the claim failed", events)
		}
	}
}

func TestClaimZeroIDStopsBeforeAHarnessProcess(t *testing.T) {
	e := newEnv(t)
	e.forge.zeroCreateID = true
	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("wanted a claim failure for CommentCreate (0, nil)")
	}
	if !strings.Contains(got.Err.Error(), "claim comment did not post") {
		t.Errorf("err = %q, want it to name the failed claim", got.Err)
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatalf("harness started %d times after a zero claim id", len(e.runner.Specs()))
	}
}

func TestClaimHappensBeforeTheHarness(t *testing.T) {
	e := newEnv(t)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	events := e.log.all()
	claimAt, harnessAt := -1, -1
	for i, name := range events {
		switch name {
		case "claim":
			if claimAt < 0 {
				claimAt = i
			}
		case "harness":
			if harnessAt < 0 {
				harnessAt = i
			}
		}
	}
	if claimAt < 0 {
		t.Fatalf("events %v never posted a claim", events)
	}
	if harnessAt < 0 {
		t.Fatalf("events %v never started a harness", events)
	}
	if claimAt > harnessAt {
		t.Fatalf("harness at %d before claim at %d: %v", harnessAt, claimAt, events)
	}
	if got.ClaimID == 0 {
		t.Error("Result.ClaimID is 0")
	}
	if len(e.forge.created) == 0 {
		t.Fatal("no claim body")
	}
	body := e.forge.created[0]
	if !strings.Contains(body, "**crossrev — reviewing, pass 1**") {
		t.Errorf("claim heading: %s", body)
	}
	if !strings.Contains(body, "Reading the diff and any earlier review threads.") {
		t.Errorf("claim prose: %s", body)
	}
	if !strings.Contains(body, `"leg":"review"`) || !strings.Contains(body, `"state":"started"`) {
		t.Errorf("claim marker: %s", body)
	}
	if strings.Contains(body, `"comment_id"`) {
		t.Error("the posted claim carried comment_id, which lib/run.sh deletes before encode")
	}
}

// The claim body, frozen at the coverage-slice marker advance.
//
// The review leg's opening comment was compared byte for byte against the
// shell's. The shell is removed, so the bytes the two implementations agreed
// on were frozen here; the marker version field follows the writer's current
// version (v2 since the coverage slice).
func TestClaimBodyIsFrozen(t *testing.T) {
	got := review.ClaimBody(1, 3, startedMarkerJSON())
	want := "**crossrev — reviewing, pass 1**\n\nReading the diff and any earlier review threads. This comment becomes the pass summary when the review finishes.\n\n<!-- crossrev: {\"v\":2,\"leg\":\"review\",\"pass\":1,\"state\":\"started\",\"ts\":1700000000,\"done_ts\":null,\"run_id\":\"local-test\",\"head_sha\":\"2c4a46cb321db01826d116b5ef2add6b0284d68c\",\"harness\":\"claude\",\"model\":null,\"effort\":null,\"endpoint\":null,\"model_reported\":null,\"tokens\":null,\"usage\":null,\"billing\":null,\"verdict\":null,\"blocked_reason\":null,\"findings\":[]} -->"
	if got != want {
		t.Errorf("Go claim body does not match the frozen body\nGo:\n%s\nwant:\n%s", got, want)
	}
}

func startedMarkerJSON() string {
	return `{"v":2,"leg":"review","pass":1,"state":"started","ts":1700000000,"done_ts":null,"run_id":"local-test","head_sha":"2c4a46cb321db01826d116b5ef2add6b0284d68c","harness":"claude","model":null,"effort":null,"endpoint":null,"model_reported":null,"tokens":null,"usage":null,"billing":null,"verdict":null,"blocked_reason":null,"findings":[]}`
}

func TestClaimWriteCapabilityIsFalse(t *testing.T) {
	e := newEnv(t)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	specs := e.runner.Specs()
	if len(specs) == 0 {
		t.Fatal("no harness spec")
	}
	args := strings.Join(specs[0].Args, " ")
	if strings.Contains(args, "acceptEdits") {
		t.Errorf("review spec granted write: %v", specs[0].Args)
	}
	if core.WriteCapabilityFor(core.RoleReviewer) != core.WriteNo {
		t.Fatal("WriteCapabilityFor(reviewer) is not no")
	}
}

// A redrive rewrites its claim comment in place, which leaves no new
// comment on the pull request — and wrote nothing anywhere else either, so
// the write the retry-safety marker depends on was untraceable: the stub's
// event log showed only [harness], and the run log carried no claim line.
// The redrive reports what it posted both places.
func TestClaimRedriveReportsWhatItPosted(t *testing.T) {
	e := newEnv(t)
	raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"complete","ts":1699950000,"comment_id":9001,"run_id":"x","head_sha":%q,"verdict":"blocked","findings":[]}`, headSHA)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	events := e.log.all()
	claimAt, harnessAt := -1, -1
	for i, name := range events {
		switch name {
		case "claim":
			if claimAt < 0 {
				claimAt = i
			}
		case "harness":
			if harnessAt < 0 {
				harnessAt = i
			}
		}
	}
	if claimAt < 0 {
		t.Fatalf("events %v never posted a claim", events)
	}
	if harnessAt < 0 {
		t.Fatalf("events %v never started a harness", events)
	}
	if claimAt > harnessAt {
		t.Fatalf("harness at %d before claim at %d: %v", harnessAt, claimAt, events)
	}
	log := readRunLog(t, e)
	if !strings.Contains(log, "claim redrive pass=1 comment=9001") {
		t.Errorf("the run log does not report the redrive's claim:\n%s", log)
	}
}

// The run log is the operator record; the record a pull request reader has
// is the pass comment. The redrive claim edit is rewritten twice before the
// pass finishes — "Findings recorded" mid-pass, then the summary — so the
// summary has to name the redrive itself, or nothing the reader sees says
// the pass ran again.
//
// The review has files to read, so the pass settles through the coverage
// loop: the pass marker there is rebuilt from the pre-claim snapshot, and
// the copy has to carry the redrive onto it. The single-prompt path never
// rebuilds the marker and cannot catch the drop.
func TestClaimRedriveNamesTheRedriveInTheSummary(t *testing.T) {
	e := newEnv(t)
	raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"complete","ts":1699950000,"comment_id":9001,"run_id":"x","head_sha":%q,"verdict":"blocked","findings":[]}`, headSHA)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked", got.Outcome)
	}
	if !got.Marker.Redriven.Value() {
		t.Error("the settled marker lost the redrive")
	}
	claimEdit := ""
	for _, body := range e.forge.edits {
		if strings.Contains(body, "Driving the pass again") {
			claimEdit = body
		}
	}
	if claimEdit == "" {
		t.Fatal("the redrive never posted its claim edit")
	}
	if !strings.Contains(claimEdit, `"redriven":true`) {
		t.Errorf("the redrive claim carries no redriven key:\n%s", claimEdit)
	}
	var summary string
	for i, id := range e.forge.editIDs {
		if id == 9001 {
			summary = e.forge.edits[i]
		}
	}
	if summary == "" {
		t.Fatal("the redrive never edited comment 9001")
	}
	if !strings.Contains(summary, "This pass ran again on this comment: the previous attempt could not be completed.") {
		t.Errorf("the redriven summary names no redrive:\n%s", summary)
	}
	if !strings.Contains(summary, `"redriven":true`) {
		t.Errorf("the redriven marker carries no redriven key:\n%s", summary)
	}
}

// A redriven pass that halts keeps the redrive on its halted marker. The
// halt record is built by the same claim-to-marker copy as the covered
// path, so the halted comment's marker still says the pass ran again —
// which is what the next attempt re-drives from.
func TestClaimRedriveKeepsTheNoticeOnAHaltedPass(t *testing.T) {
	e := newEnv(t)
	raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"complete","ts":1699950000,"comment_id":9001,"run_id":"x","head_sha":%q,"verdict":"blocked","findings":[]}`, headSHA)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
	// Oversized files split rather than halting now, so the halt is the
	// 400-file pass budget carrying the last file.
	var paths []string
	for i := 0; i <= 400; i++ {
		path := fmt.Sprintf("file%03d.go", i)
		writeRequiredHead(e, path, "package x\n")
		paths = append(paths, path)
	}
	for i := 0; i < 400; i += 40 {
		end := i + 40
		e.runner.script = append(e.runner.script, exec.Result{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, paths[i:end]))})
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeHalted {
		t.Fatalf("Outcome = %q, want halted", got.Outcome)
	}
	if !got.Marker.Redriven.Value() {
		t.Error("the halted marker lost the redrive")
	}
	var halted string
	for i, id := range e.forge.editIDs {
		if id == 9001 {
			halted = e.forge.edits[i]
		}
	}
	if halted == "" {
		t.Fatal("the redrive never edited comment 9001")
	}
	if !strings.Contains(halted, `"redriven":true`) {
		t.Errorf("the halted marker carries no redriven key:\n%s", halted)
	}
}

// An accepted batch checkpoints its findings onto the claim mid-pass, and
// that checkpoint copies the pass marker under construction — so it has to
// carry the redrive too, or the comment misstates the pass between the
// first accepted batch and the summary.
func TestClaimRedriveKeepsTheNoticeOnTheMidPassCheckpoint(t *testing.T) {
	e := newEnv(t)
	raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"complete","ts":1699950000,"comment_id":9001,"run_id":"x","head_sha":%q,"verdict":"blocked","findings":[]}`, headSHA)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(findingAnswer(t, "a.go", nil, "Mid-pass finding"))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked", got.Outcome)
	}
	checkpoint := ""
	for _, body := range e.forge.edits {
		if strings.Contains(body, "Findings recorded; the remaining batches are still running.") {
			checkpoint = body
		}
	}
	if checkpoint == "" {
		t.Fatal("the accepted batch never checkpointed its findings")
	}
	if !strings.Contains(checkpoint, `"redriven":true`) {
		t.Errorf("the mid-pass checkpoint carries no redriven key:\n%s", checkpoint)
	}
}

// Packing can empty the required set after the claim exists — every
// remaining path recognised as generated and oversized — and that pass
// settles from the rebuilt marker too, so its summary has to name the
// redrive the same way a covered pass does.
func TestClaimRedriveNamesTheRedriveWhenNothingIsLeftToReview(t *testing.T) {
	e := newEnv(t)
	raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"complete","ts":1699950000,"comment_id":9001,"run_id":"x","head_sha":%q,"verdict":"blocked","findings":[]}`, headSHA)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
	writeRequiredHead(e, "gen/big.ts", oversizedHeaderBody())
	writeRequiredHead(e, "src/webAssets.ts", oversizedHeaderBody())
	e.runner.onSpec = func(exec.Spec) { t.Error("the harness ran for a pass with nothing to review") }
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeHalted {
		t.Fatalf("Outcome = %q, want halted", got.Outcome)
	}
	var summary string
	for i, id := range e.forge.editIDs {
		if id == 9001 {
			summary = e.forge.edits[i]
		}
	}
	if summary == "" {
		t.Fatal("the redrive never edited comment 9001")
	}
	if !strings.Contains(summary, "This pass ran again on this comment: the previous attempt could not be completed.") {
		t.Errorf("the redriven summary names no redrive:\n%s", summary)
	}
	if !strings.Contains(summary, `"redriven":true`) {
		t.Errorf("the redriven marker carries no redriven key:\n%s", summary)
	}
}

// A redriven pass that dies mid-run records the failure from the pass
// marker under construction, so the fatal record has to name the redrive
// too — the reader otherwise sees a blocked summary with no hint the pass
// ran again.
func TestAFailedRedrivenPassStillNamesTheRedrive(t *testing.T) {
	e := newEnv(t)
	raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"complete","ts":1699950000,"comment_id":9001,"run_id":"x","head_sha":%q,"verdict":"blocked","findings":[]}`, headSHA)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{{ExitCode: 1, Stderr: []byte("harness died")}}
	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("a harness that exits 1 did not fail the leg")
	}
	var fatal string
	for i, id := range e.forge.editIDs {
		if id == 9001 {
			fatal = e.forge.edits[i]
		}
	}
	if fatal == "" {
		t.Fatal("the failed redrive never edited comment 9001")
	}
	if !strings.Contains(fatal, `"verdict":"blocked"`) {
		t.Errorf("the fatal record is not a blocked summary:\n%s", fatal)
	}
	if !strings.Contains(fatal, "This pass ran again on this comment: the previous attempt could not be completed.") {
		t.Errorf("the fatal record names no redrive:\n%s", fatal)
	}
	if !strings.Contains(fatal, `"redriven":true`) {
		t.Errorf("the fatal marker carries no redriven key:\n%s", fatal)
	}
}

// A redrive that posted its claim and died before settling resumes from
// the open claim, which already carries the redrive: the snapshot marker
// is the claim itself here, so the settled summary names the redrive
// without any copy having to restore it. This pins the resume path while
// the redrive tests above pin the rebuild.
func TestAResumedRedriveStillNamesTheRedrive(t *testing.T) {
	e := newEnv(t)
	raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"started","ts":1700000000,"comment_id":9001,"run_id":"x","head_sha":%q,"harness":"claude","redriven":true,"findings":[]}`, headSHA)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked", got.Outcome)
	}
	var summary string
	for i, id := range e.forge.editIDs {
		if id == 9001 {
			summary = e.forge.edits[i]
		}
	}
	if summary == "" {
		t.Fatal("the resumed pass never edited comment 9001")
	}
	if !strings.Contains(summary, "This pass ran again on this comment: the previous attempt could not be completed.") {
		t.Errorf("the resumed summary names no redrive:\n%s", summary)
	}
	if !strings.Contains(summary, `"redriven":true`) {
		t.Errorf("the resumed marker carries no redriven key:\n%s", summary)
	}
}

func TestClaimWarnsWhenTheDailyReviewBackstopCannotReadComments(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 2\npolicy:\n  max_prs_per_day: 3\n")
	e.forge.repoCommentsErr = errors.New("comments unavailable")
	req := e.request(t)
	req.Trigger = review.TriggerAutomatic
	got := runLeg(t, e, req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	want := "could not read repository comments while checking max_prs_per_day\n   The backstop rounds down to zero rather than stopping a healthy automatic review early. Check GitHub availability and the token's issues read permission."
	if !containsString(ui.Texts(got.Messages), want) {
		t.Errorf("messages = %q, want warning %q", got.Messages, want)
	}
}
