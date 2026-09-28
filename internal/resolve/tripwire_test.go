package resolve

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// A resolve transcript carrying a command event trips before any push,
// naming the harness and the command.

func TestResolveTripwireRefusesBeforePush(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	stream := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"rm -rf /tmp/victim"}}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"{\"blocked\":false,\"summary\":\"x\",\"resolutions\":[]}","usage":{"input_tokens":1}}`,
	}, "\n")
	e.runner.result = &exec.Result{ExitCode: 0, Stdout: []byte(stream)}
	got := e.run(t)
	if got.Err == nil {
		t.Fatal("the leg accepted a resolve transcript that ran a command")
	}
	if !strings.Contains(got.Err.Error(), "claude") {
		t.Errorf("err = %q, want the harness named", got.Err)
	}
	if !strings.Contains(got.Err.Error(), "rm -rf /tmp/victim") {
		t.Errorf("err = %q, want the command named", got.Err)
	}
	if e.git.pushCalls != 0 {
		t.Errorf("pushCalls = %d, want no push after a tripped resolve", e.git.pushCalls)
	}
}

// A transcript that ran a command and then failed still trips: the leg
// names the command and puts the tree back rather than reporting a plain
// harness failure.
func TestResolveTripwireTripsOnFailedEnvelope(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.envErr = "boom"
	stream := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"rm -rf /tmp/victim"}}]}}`,
		`{"type":"result","subtype":"success","is_error":true,"result":"","usage":{"input_tokens":1}}`,
	}, "\n")
	e.runner.result = &exec.Result{ExitCode: 1, Stdout: []byte(stream)}
	got := e.run(t)
	if got.Err == nil {
		t.Fatal("the leg accepted a failed resolve transcript that ran a command")
	}
	if !strings.Contains(got.Err.Error(), "claude") {
		t.Errorf("err = %q, want the harness named", got.Err)
	}
	if !strings.Contains(got.Err.Error(), "rm -rf /tmp/victim") {
		t.Errorf("err = %q, want the command named", got.Err)
	}
	if e.git.pushCalls != 0 {
		t.Errorf("pushCalls = %d, want no push after a tripped resolve", e.git.pushCalls)
	}
	if *e.git.restoreCalls == 0 {
		t.Errorf("restoreCalls = 0, want the tripped attempt's edits put back")
	}
}

// The refused harness refuses before any child starts: only the version
// probe runs, never the leg itself.
func TestResolveRefusedHarnessRefusesBeforeChild(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter = nil
	e.runner.stdout = []byte("opencode v2.0.15\n")
	got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman, Harness: "opencode"})
	if got.Err == nil {
		t.Fatal("the leg accepted a refused harness install")
	}
	if len(e.runner.specs) != 1 {
		t.Fatalf("the runner started %d children, want only the version probe", len(e.runner.specs))
	}
}
