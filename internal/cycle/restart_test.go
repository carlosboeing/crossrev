package cycle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cycle"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// restartRun drives the real command against one fixture pull request: the
// labels and the markers go on the way the loop leaves them, and the forge
// answers from there.
func restartRun(t *testing.T, labels []string, markers []map[string]any, configure ...func(*restartForge)) (*restartForge, *bytes.Buffer, error) {
	t.Helper()
	forgeLabels := make([]forge.Label, 0, len(labels))
	for _, name := range labels {
		forgeLabels = append(forgeLabels, forge.Label{Name: name})
	}
	comments := make([]forge.IssueComment, 0, len(markers))
	for i, marker := range markers {
		raw, err := json.Marshal(marker)
		if err != nil {
			t.Fatalf("marker %d: %v", i, err)
		}
		body, err := prstate.EncodeMarker(raw)
		if err != nil {
			t.Fatalf("encoding marker %d: %v", i, err)
		}
		comments = append(comments, forge.IssueComment{
			ID:          int64(9001 + i),
			AuthorLogin: restartAuthor,
			Body:        "Summary." + body,
			CreatedAt:   "2026-08-11T00:00:00Z",
		})
	}
	base, err := core.NewRevision(restartBase)
	if err != nil {
		t.Fatalf("base revision: %v", err)
	}
	f := &restartForge{
		pr: forge.PullRequest{
			Number:     restartPR,
			BaseRefOid: base,
			Labels:     forgeLabels,
			State:      "OPEN",
		},
		comments: comments,
	}
	for _, apply := range configure {
		apply(f)
	}
	slug, err := core.ParseSlug(restartRepo)
	if err != nil {
		t.Fatalf("slug: %v", err)
	}
	var out bytes.Buffer
	r := &cycle.Restart{
		Forge: f,
		Show:  restartShow,
		Out:   &ui.IO{Out: &out},
	}
	err = r.Run(context.Background(), slug, restartPR)
	return f, &out, err
}

// restartMarker builds one marker payload. Every field the decision reads is
// an argument; the rest is the envelope the loop writes around it.
func restartMarker(t *testing.T, leg core.Leg, pass int, state core.PassState, extra map[string]any) map[string]any {
	t.Helper()
	m := map[string]any{
		"v": 1, "leg": string(leg), "pass": pass, "state": state.String(),
		"ts": int64(1755400000), "run_id": "1", "head_sha": restartHead,
		"harness": "claude", "model": "m", "model_reported": "m",
	}
	for key, value := range extra {
		m[key] = value
	}
	return m
}

// TestRestartReviewHaltedRestartsTheReviewLeg: a complete review marker with a
// blocked verdict is the review leg's halt, so the restart hands the pull
// request back to the reviewer.
func TestRestartReviewHaltedRestartsTheReviewLeg(t *testing.T) {
	f, _, err := restartRun(t, []string{"crossrev/halted"}, []map[string]any{
		restartMarker(t, core.LegReview, 1, core.PassComplete, map[string]any{"verdict": "blocked"}),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{
		"label-add 42 crossrev/awaiting-review",
		"label-remove 42 crossrev/halted",
	}
	if got := strings.Join(f.calls, "\n"); got != strings.Join(want, "\n") {
		t.Errorf("transcript\n--- got ---\n%s\n--- want ---\n%s", got, strings.Join(want, "\n"))
	}
}

// TestRestartResolveHaltedRestartsTheResolveLeg: a completed review whose
// resolve pass ended blocked is the resolve leg's halt, and the resolve leg is
// the one re-driven once whatever blocked it is fixed.
func TestRestartResolveHaltedRestartsTheResolveLeg(t *testing.T) {
	f, _, err := restartRun(t, []string{"crossrev/halted"}, []map[string]any{
		restartMarker(t, core.LegReview, 1, core.PassComplete, map[string]any{"verdict": "issues-remain"}),
		restartMarker(t, core.LegResolve, 1, core.PassComplete, map[string]any{"blocked": true}),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{
		"label-add 42 crossrev/awaiting-resolution",
		"label-remove 42 crossrev/halted",
	}
	if got := strings.Join(f.calls, "\n"); got != strings.Join(want, "\n") {
		t.Errorf("transcript\n--- got ---\n%s\n--- want ---\n%s", got, strings.Join(want, "\n"))
	}
}

// TestRestartClearsTheWatchdogsBookkeeping pins the watchdog halt in one
// transcript: the open resolve claim is the record of which leg was retried,
// and both labels the watchdog left come off after the awaiting label goes
// back on, in that order.
func TestRestartClearsTheWatchdogsBookkeeping(t *testing.T) {
	f, out, err := restartRun(t,
		[]string{"crossrev/halted", "crossrev/watchdog-retried"},
		[]map[string]any{
			restartMarker(t, core.LegReview, 1, core.PassComplete, map[string]any{"verdict": "issues-remain"}),
			restartMarker(t, core.LegResolve, 1, core.PassStarted, nil),
		})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{
		"label-add 42 crossrev/awaiting-resolution",
		"label-remove 42 crossrev/halted",
		"label-remove 42 crossrev/watchdog-retried",
	}
	if got := strings.Join(f.calls, "\n"); got != strings.Join(want, "\n") {
		t.Errorf("transcript\n--- got ---\n%s\n--- want ---\n%s", got, strings.Join(want, "\n"))
	}
	if !strings.Contains(out.String(), "crossrev/awaiting-resolution") {
		t.Errorf("output = %q, want it to name the label that went on", out.String())
	}
}

// TestRestartReappliesAnAwaitingLabelAlreadyOn: the workflows listen for the
// labeled event, which an add against a label already on the pull request
// does not fire, so the restart takes it off and puts it back.
func TestRestartReappliesAnAwaitingLabelAlreadyOn(t *testing.T) {
	f, _, err := restartRun(t,
		[]string{"crossrev/halted", "crossrev/awaiting-review"},
		[]map[string]any{
			restartMarker(t, core.LegReview, 1, core.PassComplete, map[string]any{"verdict": "blocked"}),
		})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{
		"label-remove 42 crossrev/awaiting-review",
		"label-add 42 crossrev/awaiting-review",
		"label-remove 42 crossrev/halted",
	}
	if got := strings.Join(f.calls, "\n"); got != strings.Join(want, "\n") {
		t.Errorf("transcript\n--- got ---\n%s\n--- want ---\n%s", got, strings.Join(want, "\n"))
	}
}

// TestRestartFailedAddLeavesTheHaltInPlace: the add is the restart, so when
// it fails the halt labels stay on and a second restart still sees a halted
// pull request rather than refusing it as one that never halted.
func TestRestartFailedAddLeavesTheHaltInPlace(t *testing.T) {
	addErr := errors.New("label write refused")
	f, _, err := restartRun(t,
		[]string{"crossrev/halted"},
		[]map[string]any{
			restartMarker(t, core.LegReview, 1, core.PassComplete, map[string]any{"verdict": "blocked"}),
		},
		func(f *restartForge) { f.addErr = addErr })
	if !errors.Is(err, addErr) {
		t.Fatalf("Run = %v, want the label add's error", err)
	}
	want := []string{"label-add 42 crossrev/awaiting-review"}
	if got := strings.Join(f.calls, "\n"); got != strings.Join(want, "\n") {
		t.Errorf("transcript\n--- got ---\n%s\n--- want ---\n%s", got, strings.Join(want, "\n"))
	}
}

// TestRestartRefusesAStoppedPullRequest pins the brake: red is applied by a
// person and no command clears it, so a stopped pull request is refused with
// the manual removal named, and nothing is written.
func TestRestartRefusesAStoppedPullRequest(t *testing.T) {
	f, _, err := restartRun(t, []string{"crossrev/stop", "crossrev/halted"}, nil)
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("Run = %v, want a ui.FatalError", err)
	}
	if !strings.Contains(fatal.Reason, "crossrev/stop") || !strings.Contains(fatal.Action, "crossrev/stop") {
		t.Errorf("refusal = %q / %q, want it to name the stop label twice", fatal.Reason, fatal.Action)
	}
	if !strings.Contains(fatal.Action, "gh pr edit 42 --remove-label crossrev/stop") {
		t.Errorf("action = %q, want the manual removal", fatal.Action)
	}
	if len(f.calls) != 0 {
		t.Errorf("a stopped pull request was written to: %v", f.calls)
	}
}

// TestRestartRefusesAStoppedPullRequestWithoutAHalt: a pull request carrying
// only crossrev/stop is not halted, and the refusal must not describe it as
// one — the brake is what is named, and the manual removal is the remedy.
func TestRestartRefusesAStoppedPullRequestWithoutAHalt(t *testing.T) {
	f, _, err := restartRun(t, []string{"crossrev/stop"}, nil)
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("Run = %v, want a ui.FatalError", err)
	}
	if !strings.Contains(fatal.Reason, "crossrev/stop") {
		t.Errorf("reason = %q, want it to name the stop label", fatal.Reason)
	}
	if strings.Contains(fatal.Reason, "halted") {
		t.Errorf("reason = %q, want no claim of a halt the pull request does not carry", fatal.Reason)
	}
	if !strings.Contains(fatal.Action, "gh pr edit 42 --remove-label crossrev/stop") {
		t.Errorf("action = %q, want the manual removal", fatal.Action)
	}
	if len(f.calls) != 0 {
		t.Errorf("a stopped pull request was written to: %v", f.calls)
	}
}

// TestRestartRefusesAPullRequestThatIsNotHalted pins the refusal saying what
// applies instead: an awaiting label means the loop is waiting on a leg, and
// the remedy is running that leg, not restarting anything.
func TestRestartRefusesAPullRequestThatIsNotHalted(t *testing.T) {
	f, _, err := restartRun(t, []string{"crossrev/awaiting-review"}, nil)
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("Run = %v, want a ui.FatalError", err)
	}
	if !strings.Contains(fatal.Reason, "not halted") {
		t.Errorf("reason = %q, want it to say the pull request is not halted", fatal.Reason)
	}
	if !strings.Contains(fatal.Action, "crossrev review --pr 42") {
		t.Errorf("action = %q, want it to name the leg that applies", fatal.Action)
	}
	if len(f.calls) != 0 {
		t.Errorf("a pull request that is not halted was written to: %v", f.calls)
	}
}

const (
	restartRepo   = "acme/widget"
	restartPR     = 42
	restartAuthor = "tester"
	restartBase   = "0913bf7b99dcecf746d0e6fcef5a9c1d64aaf3b0"
	restartHead   = "d81a3f2abc0000000000000000000000000000ab"
)

// restartShow answers the configuration read from the base revision, in the
// minimal shape a local-mode repository carries.
func restartShow(_ context.Context, rev core.Revision, path string) ([]byte, config.FileStatus, error) {
	if rev.SHA() == restartBase && path == ".github/crossrev.yml" {
		return []byte("version: 2\nmode: local\n"), config.IsFile, nil
	}
	return nil, config.NotFound, nil
}

// restartForge records the two label writes in the order they were made and
// answers the three reads the restart makes. Anything else panics, so a read
// or write this command should not be making shows up rather than answering
// empty.
type restartForge struct {
	pr       forge.PullRequest
	comments []forge.IssueComment
	calls    []string
	addErr   error
}

func (f *restartForge) record(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *restartForge) RepoSlug(context.Context) (core.Slug, error) {
	return core.ParseSlug(restartRepo)
}

func (f *restartForge) PullRequest(context.Context, core.Slug, int) (forge.PullRequest, error) {
	return f.pr, nil
}

func (f *restartForge) IssueComments(context.Context, core.Slug, int) []forge.IssueComment {
	return f.comments
}

func (f *restartForge) ViewerLogin(context.Context) (string, error) { return restartAuthor, nil }

func (f *restartForge) PullRequestLabelAdd(_ context.Context, _ core.Slug, pr int, label string) error {
	f.record("label-add %d %s", pr, label)
	return f.addErr
}

func (f *restartForge) PullRequestLabelRemove(_ context.Context, _ core.Slug, pr int, label string) {
	f.record("label-remove %d %s", pr, label)
}

func (f *restartForge) DefaultBranch(context.Context, core.Slug) string { return "main" }
func (f *restartForge) PullRequestDiff(context.Context, core.Slug, core.Revision, core.Revision) ([]byte, error) {
	panic("restart does not read the diff")
}
func (f *restartForge) PullRequestLabels(context.Context, core.Slug, int) []string {
	panic("restart reads the labels off the pull request it already loaded")
}
func (f *restartForge) ReviewThreads(context.Context, core.Slug, int) []forge.ReviewThread {
	panic("restart does not read review threads")
}
func (f *restartForge) ReviewComments(context.Context, core.Slug, int) []forge.IssueComment {
	panic("restart does not read review comments")
}
func (f *restartForge) RepoIssueComments(context.Context, core.Slug, time.Time, int) ([]forge.IssueComment, error) {
	panic("restart does not read the repository's comments")
}
func (f *restartForge) AwaitingPullRequests(context.Context, core.Slug) []forge.AwaitingPullRequest {
	panic("restart reads one pull request, not a sweep")
}
func (f *restartForge) WorkflowRunStatus(context.Context, core.Slug, string) forge.RunStatus {
	panic("restart does not read workflow runs")
}
func (f *restartForge) CheckRuns(context.Context, core.Slug, core.Revision) (forge.CheckRuns, error) {
	panic("restart reads no check runs")
}
func (f *restartForge) LabelColour(context.Context, core.Slug, string) string {
	panic("restart mints no labels")
}
func (f *restartForge) LabelEnsure(context.Context, core.Slug, forge.Label) (forge.LabelState, error) {
	panic("restart mints no labels")
}
func (f *restartForge) IssueByFinding(context.Context, core.Slug, string, core.FindingID) (int, bool) {
	panic("restart files no issues")
}
func (f *restartForge) IssueCandidates(context.Context, core.Slug, string, string) []forge.IssueCandidate {
	panic("restart files no issues")
}
func (f *restartForge) CommentCreate(context.Context, core.Slug, int, string) (int64, error) {
	panic("restart writes no comment")
}
func (f *restartForge) CommentEdit(context.Context, core.Slug, int64, string) error {
	panic("restart writes no comment")
}
func (f *restartForge) ReviewCommentCreate(context.Context, forge.ReviewComment) (forge.Placement, error) {
	panic("restart writes no comment")
}
func (f *restartForge) ReviewFileComment(context.Context, forge.ReviewComment) (forge.Placement, error) {
	panic("restart writes no comment")
}
func (f *restartForge) ReviewReply(context.Context, core.Slug, int, int64, string) error {
	panic("restart writes no comment")
}
func (f *restartForge) ThreadResolve(context.Context, string) error {
	panic("restart resolves no threads")
}
func (f *restartForge) IssueCreate(context.Context, core.Slug, string, string, []string) (int, error) {
	panic("restart files no issues")
}
func (f *restartForge) IssueCommentCreate(context.Context, core.Slug, int, string) {
	panic("restart writes no comment")
}

var _ forge.Forge = (*restartForge)(nil)
