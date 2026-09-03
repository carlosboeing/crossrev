package review_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// The run header, measured from lib/run.sh:1072-1073:
//
//	printf '\n  Reviewing %s#%s — %s\n' "$CTX_REPO" "$CTX_PR" "$(_pass_label …)"
//	printf '  Reviewer: %s%s%s\n' "$harness" "${model:+, $model}" "${effort:+, $effort effort}"
//
// Two bare printfs, so the blank line above them is part of the site and the
// two text lines are the `  ` prefix ui_say prints. Nothing in the port emitted
// any of it, so `crossrev review` never said which pull request it was on, what
// pass it was, or which harness was about to answer.
func TestTheRunHeaderNamesThePullRequestPassAndReviewer(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 1\nreviewer:\n  harness: claude\n  model: reviewer-model\n  effort: high\n")
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

	// leg.Run directly rather than runLeg: the helper substitutes
	// HarnessOverride "claude" for an empty one, and an override wipes the
	// configured model, which is half of the line under test.
	req := e.request(t)
	req.HarnessOverride = ""
	leg := e.leg(t)
	got := leg.Run(context.Background(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}

	want := []ui.Line{
		ui.Blank(),
		ui.Say("Reviewing acme/widget#42 — pass 1"),
		ui.Say("Reviewer: claude, reviewer-model, high effort"),
	}
	if !containsRun(got.Messages, want) {
		t.Fatalf("the header is not in the report:\n got %q\nwant %q", ui.Texts(got.Messages), ui.Texts(want))
	}
}

// The optional halves are omitted rather than printed empty: `${model:+, …}`
// expands to nothing when the model is unset, and so does the effort.
func TestTheRunHeaderOmitsAnUnsetModelAndEffort(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 1\nreviewer:\n  harness: claude\n")
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

	req := e.request(t)
	req.HarnessOverride = ""
	leg := e.leg(t)
	got := leg.Run(context.Background(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !containsRun(got.Messages, []ui.Line{ui.Say("Reviewer: claude")}) {
		t.Fatalf("Reviewer line is not bare: %q", ui.Texts(got.Messages))
	}
}

// A pass past the cycle cap says so rather than printing "pass 4 of 3", which
// is _pass_label at lib/run.sh:598-605.
func TestTheRunHeaderNamesTheCycleCapItPassed(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 1\npolicy:\n  max_passes_per_cycle: 3\n")
	writeAppGo(t, e.dir)
	raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":3,"state":"complete","ts":1699950000,"run_id":"x","head_sha":%q,"verdict":"issues-remain","findings":[]}`, oldSHA)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !containsRun(got.Messages, []ui.Line{ui.Say("Reviewing acme/widget#42 — pass 4 (past the cycle cap of 3)")}) {
		t.Fatalf("the header lost the cap note: %q", ui.Texts(got.Messages))
	}
}

// containsRun reports whether want appears as a consecutive run of lines, kind
// and text and action all.
func containsRun(got, want []ui.Line) bool {
	if len(want) == 0 || len(got) < len(want) {
		return false
	}
	for i := 0; i+len(want) <= len(got); i++ {
		match := true
		for j, w := range want {
			if got[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
