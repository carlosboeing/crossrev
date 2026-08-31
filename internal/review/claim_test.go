package review_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/review"
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

func TestClaimBodyMatchesTheShell(t *testing.T) {
	root := repoRootPath(t)
	got := review.ClaimBody(1, 3, startedMarkerJSON())
	shell := shellClaimBody(t, root, 1, 3, startedMarkerJSON())
	if got != shell {
		t.Errorf("Go claim body does not match Bash\nGo:\n%s\nBash:\n%s", got, shell)
	}
}

func startedMarkerJSON() string {
	return `{"v":1,"leg":"review","pass":1,"state":"started","ts":1700000000,"done_ts":null,"run_id":"local-test","head_sha":"2c4a46cb321db01826d116b5ef2add6b0284d68c","harness":"claude","model":null,"effort":null,"endpoint":null,"model_reported":null,"tokens":null,"usage":null,"billing":null,"verdict":null,"blocked_reason":null,"findings":[]}`
}

func shellClaimBody(t *testing.T, root string, pass, cap int, marker string) string {
	t.Helper()
	const script = `
set -uo pipefail
ROOT="$1"
export ROOT
# shellcheck source=/dev/null
source "$ROOT/lib/ui.sh"
# shellcheck source=/dev/null
source "$ROOT/lib/state.sh"
# shellcheck source=/dev/null
source "$ROOT/lib/run.sh"
pass="$2"
cap="$3"
marker="$4"
printf '%s' "**crossrev — reviewing, $(_pass_label "$pass" "$cap")**

Reading the diff and any earlier review threads. This comment becomes the pass summary when the review finishes.$(state_marker_encode "$marker")"
`
	return bashOutput(t, script, root, strconv.Itoa(pass), strconv.Itoa(cap), marker)
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
