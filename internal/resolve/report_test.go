package resolve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// containsRun reports whether want appears as a consecutive run of lines —
// kind, text and action all.
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

// The resolve leg's whole spoken report, in order, for the ordinary pass: one
// finding fixed, pushed, its thread answered and resolved.
//
// Measured from lib/run.sh:
//
//	:1913-1914  printf '\n  Resolving %s#%s — %s\n' / '  Resolver: %s%s%s\n'
//	:2264       ui_ok "pushed ${commit_sha:0:7} to $CTX_HEAD_BRANCH"
//	:2377       ui_ok "resolved $resolved_n thread(s)"
//	:2408       ui_ok "posted a summary comment"
//	:2434       printf '  → resolved pass %s\n\n'
//	:2437-2439  ui_say "To look again with the reviewer:" / "  crossrev review --pr N" / printf '\n'
//
// The leg answered none of it, so `crossrev resolve` printed nothing at all
// between the harness starting and the process exiting.
func TestTheResolveLegReportsWhatItDid(t *testing.T) {
	e := setup(t)
	e.git.staged = true
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != OutcomeComplete {
		t.Fatalf("Outcome = %q, want complete", got.Outcome)
	}

	want := []ui.Line{
		ui.Blank(),
		ui.Say("Resolving acme/widget#42 — pass 1"),
		ui.Say("Resolver: claude"),
		// The commit the fake git wrote, which is what Head answers after it.
		ui.OK("pushed " + shortSHA(e.git.commitSHA) + " to feature"),
		ui.OK("resolved 1 thread(s)"),
		ui.OK("posted a summary comment"),
		ui.Say("→ resolved pass 1"),
		ui.Blank(),
		ui.Say("To look again with the reviewer:"),
		ui.Say("  crossrev review --pr 42"),
		ui.Blank(),
	}
	if !containsRun(got.Messages, want) {
		t.Fatalf("the report is not what the shell prints:\n got %#v\nwant %#v", got.Messages, want)
	}
}

// A deferral that files an issue reports the filing with ui_ok
// (lib/run.sh:2381) and one that matched an existing issue with ui_say
// (lib/run.sh:2382). The two are different helpers on purpose: a filed issue is
// a verified success, a match is a statement of fact.
func TestTheResolveLegReportsWhatItFiledAndWhatItMatched(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.git.show = map[string][]byte{e.base.SHA() + ":.github/crossrev.yml": githubIssuesConfig()}
	e.adapter.payloads = []json.RawMessage{deferredPayload(persistDoc(), nil)}

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !containsRun(got.Messages, []ui.Line{ui.OK("filed 1 issue(s) for deferred work")}) {
		t.Fatalf("no filing line among %#v", got.Messages)
	}
}

// The converged ending: nothing changed, so the loop is over (lib/run.sh:2446).
func TestTheResolveLegSaysWhenTheLoopIsDone(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.payloads = []json.RawMessage{json.RawMessage(
		`{"blocked":false,"blocked_reason":null,"summary":"Not a bug.","commit_subject":null,` +
			`"resolutions":[{"finding_number":1,"resolution":"skipped","reply":"no",` +
			`"persist":null,"duplicate_of":null}]}`)}

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	want := []ui.Line{
		ui.Say("→ resolved pass 1"),
		ui.Blank(),
		ui.Say("Every finding settled without a change to the code, so the loop is done."),
		ui.Blank(),
	}
	if !containsRun(got.Messages, want) {
		t.Fatalf("the converged ending is missing:\n got %#v", got.Messages)
	}
}

// An escalation applies crossrev/stop and says why (lib/run.sh:2433-2434).
func TestTheResolveLegSaysWhenAHumanIsNeeded(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.payloads = []json.RawMessage{json.RawMessage(
		`{"blocked":false,"blocked_reason":null,"summary":"Needs a call.","commit_subject":null,` +
			`"resolutions":[{"finding_number":1,"resolution":"escalated","reply":"ask",` +
			`"persist":null,"duplicate_of":null}]}`)}

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	want := ui.Say("1 finding(s) need a human decision, so crossrev/stop is applied and the loop halts.")
	if !containsRun(got.Messages, []ui.Line{want}) {
		t.Fatalf("the escalation line is missing:\n got %#v", got.Messages)
	}
}

// A redrive says so, below the run header and above the claim edit
// (lib/run.sh:1945).
func TestTheResolveLegSaysItIsDrivingThePassAgain(t *testing.T) {
	e := setup(t)
	e.git.staged = true
	e.addReview(t, defaultFindings(), "issues-remain")
	e.addResolve(t, prstate.Marker{
		State:       core.PassComplete,
		Blocked:     prstate.Some(false),
		CommitSHA:   prstate.Null[string](),
		Resolutions: json.RawMessage(`[{"resolution":"fixed"}]`),
		HeadSHA:     prstate.Some(e.head.SHA()),
		TS:          e.now.Unix() - 10,
		Harness:     prstate.Some("claude"),
		Summary:     prstate.Some("tried"),
	}, 9002)

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	want := []ui.Line{
		ui.Say("Resolver: claude"),
		ui.Say("Pass 1's resolve leg ended without settling its findings — driving pass 1 again."),
	}
	if !containsRun(got.Messages, want) {
		t.Fatalf("the redrive line is missing or out of order:\n got %#v", got.Messages)
	}
}

// The run log's worktree pair and the invoke duration, measured from
// lib/run.sh:1900, :2451 and :831.
func TestTheResolveLegBracketsTheWorktreeInTheRunLog(t *testing.T) {
	e := setup(t)
	e.git.staged = true
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}

	body, err := os.ReadFile(filepath.Join(e.log.Dir(), "run.log"))
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	log := string(body)
	for _, want := range []string{
		"worktree created ",
		"worktree removed ",
		"invoke harness=claude attempt=1 exit=0 duration=",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the run log has no %q:\n%s", want, log)
		}
	}
}

// A pass whose resolutions are already on the marker says so rather than
// resolving in silence (lib/run.sh:1997).
func TestTheResolveLegSaysItIsNotRunningTheResolverAgain(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.addResolve(t, prstate.Marker{
		State:       core.PassStarted,
		TS:          e.now.Unix(),
		HeadSHA:     prstate.Some(e.head.SHA()),
		Harness:     prstate.Some("claude"),
		CommitSHA:   prstate.Some("cafe000cafe000cafe000cafe000cafe000cafe0"),
		Summary:     prstate.Some("done"),
		Resolutions: json.RawMessage(`[{"finding_id":"` + testFinding + `","resolution":"fixed","reply":"done"}]`),
	}, 9002)

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	want := ui.Say("The previous attempt already recorded its resolutions, so the resolver is not run again.")
	if !containsRun(got.Messages, []ui.Line{want}) {
		t.Fatalf("the recovery line is missing:\n got %#v", got.Messages)
	}
}

// A pass that does not hand back to the reviewer asks for the upgrade tip
// (lib/run.sh:2453: `next != awaiting-review`).
func TestAConvergedResolvePassAsksForTheUpgradeTip(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.payloads = []json.RawMessage{json.RawMessage(
		`{"blocked":false,"blocked_reason":null,"summary":"Not a bug.","commit_subject":null,` +
			`"resolutions":[{"finding_number":1,"resolution":"skipped","reply":"no",` +
			`"persist":null,"duplicate_of":null}]}`)}

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !got.Nudge {
		t.Fatal("a converged pass did not ask for the upgrade tip")
	}
}

// A pass that hands back to the reviewer prints the handover instead.
func TestAResolvePassHandingBackDoesNotAskForTheTip(t *testing.T) {
	e := setup(t)
	e.git.staged = true
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Nudge {
		t.Fatal("a pass handing back to the reviewer asked for the tip too")
	}
}
