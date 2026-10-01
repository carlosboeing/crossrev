package verify_test

import (
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/verify"
)

func required(names ...string) []config.RequiredCheck {
	var out []config.RequiredCheck
	for _, name := range names {
		check, err := config.ParseRequiredCheck(name)
		if err != nil {
			panic(err)
		}
		out = append(out, check)
	}
	return out
}

func run(id int64, name, app, status, conclusion string) forge.CheckRun {
	return forge.CheckRun{
		ID:         id,
		Name:       name,
		App:        app,
		Status:     status,
		Conclusion: conclusion,
		URL:        "https://github.com/acme/widget/runs/1",
	}
}

// The mapping table: every status/conclusion pair GitHub reports lands in
// exactly one state.
func TestEvaluateMapsEveryStatusConclusionPair(t *testing.T) {
	cases := []struct {
		status     string
		conclusion string
		want       verify.State
	}{
		{"completed", "success", verify.Passed},
		{"completed", "neutral", verify.Passed},
		{"completed", "skipped", verify.Passed},
		{"completed", "failure", verify.Failed},
		{"completed", "timed_out", verify.Failed},
		{"completed", "cancelled", verify.Failed},
		{"completed", "action_required", verify.Failed},
		{"completed", "stale", verify.Failed},
		{"completed", "startup_failure", verify.Failed},
		{"completed", "", verify.Failed},
		{"completed", "mystery", verify.Failed},
		{"queued", "", verify.Pending},
		{"in_progress", "", verify.Pending},
		{"waiting", "", verify.Pending},
		{"requested", "", verify.Pending},
		{"pending", "", verify.Pending},
		{"", "", verify.Failed},
		{"stuck", "", verify.Failed},
	}
	for _, tc := range cases {
		ev := verify.Evaluate(required("build"),
			forge.CheckRuns{Runs: []forge.CheckRun{run(1, "build", "github-actions", tc.status, tc.conclusion)}}, nil)
		if ev.State != tc.want {
			t.Errorf("status %q conclusion %q = %q, want %q", tc.status, tc.conclusion, ev.State, tc.want)
		}
		if len(ev.Checks) != 1 || ev.Checks[0].State != tc.want {
			t.Errorf("status %q conclusion %q per-check = %+v, want one %q",
				tc.status, tc.conclusion, ev.Checks, tc.want)
		}
	}
}

// A passed run that was skipped or neutral says so beside the pass, and an
// unknown value names its raw text beside the failure.
func TestEvaluateNotesSkippedNeutralAndUnknown(t *testing.T) {
	for _, conclusion := range []string{"skipped", "neutral"} {
		ev := verify.Evaluate(required("build"),
			forge.CheckRuns{Runs: []forge.CheckRun{run(1, "build", "github-actions", "completed", conclusion)}}, nil)
		if ev.Checks[0].Note != conclusion {
			t.Errorf("conclusion %q note = %q", conclusion, ev.Checks[0].Note)
		}
	}
	ev := verify.Evaluate(required("build"),
		forge.CheckRuns{Runs: []forge.CheckRun{run(1, "build", "github-actions", "completed", "mystery")}}, nil)
	if ev.Checks[0].Note == "" || ev.Checks[0].Note == "mystery" {
		t.Errorf("unknown conclusion note = %q, want the raw value named", ev.Checks[0].Note)
	}
	ev = verify.Evaluate(required("build"),
		forge.CheckRuns{Runs: []forge.CheckRun{run(1, "build", "github-actions", "stuck", "")}}, nil)
	if ev.Checks[0].Note == "" {
		t.Error("unknown status carries no note naming the raw value")
	}
}

// Selection matches name and app together, and the newest run wins: a rerun
// supersedes the older result.
func TestEvaluateSelectsNewestRunForNameAndApp(t *testing.T) {
	runs := forge.CheckRuns{Runs: []forge.CheckRun{
		run(1, "build", "github-actions", "completed", "failure"),
		run(2, "build", "github-actions", "completed", "success"),
		run(3, "build", "other-app", "completed", "failure"),
		run(4, "other", "github-actions", "completed", "failure"),
	}}
	ev := verify.Evaluate(required("build"), runs, nil)
	if ev.State != verify.Passed {
		t.Errorf("state = %q, want passed: the id-2 rerun supersedes the id-1 failure", ev.State)
	}
	ev = verify.Evaluate(required("build@other-app"), runs, nil)
	if ev.State != verify.Failed {
		t.Errorf("other-app state = %q, want failed: the app halves the match", ev.State)
	}
}

// No run with the required name and app is missing, and a missing entry
// carries no run URL.
func TestEvaluateMissing(t *testing.T) {
	ev := verify.Evaluate(required("build"),
		forge.CheckRuns{Runs: []forge.CheckRun{run(1, "other", "github-actions", "completed", "success")}}, nil)
	if ev.State != verify.Missing {
		t.Fatalf("state = %q, want missing", ev.State)
	}
	if ev.Checks[0].URL != "" {
		t.Errorf("missing check URL = %q, want none", ev.Checks[0].URL)
	}
}

// The overall state aggregates across entries: a failure outranks a wait,
// a wait outranks an absence, and only all passed is passed.
func TestEvaluateAggregatesAcrossEntries(t *testing.T) {
	passed := run(1, "build", "github-actions", "completed", "success")
	pending := run(2, "lint", "github-actions", "in_progress", "")
	cases := []struct {
		name     string
		required []config.RequiredCheck
		runs     []forge.CheckRun
		want     verify.State
	}{
		{"all passed", required("build"), []forge.CheckRun{passed}, verify.Passed},
		{"failed outranks pending", required("build", "lint"),
			[]forge.CheckRun{run(1, "build", "github-actions", "completed", "failure"), pending}, verify.Failed},
		{"pending outranks missing", required("lint", "gone"),
			[]forge.CheckRun{pending}, verify.Pending},
		{"missing alone", required("gone"), []forge.CheckRun{passed}, verify.Missing},
	}
	for _, tc := range cases {
		ev := verify.Evaluate(tc.required, forge.CheckRuns{Runs: tc.runs}, nil)
		if ev.State != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, ev.State, tc.want)
		}
	}
}

// No required checks is none_required, whatever the runs say.
func TestEvaluateEmptyIsNoneRequired(t *testing.T) {
	ev := verify.Evaluate(nil,
		forge.CheckRuns{Runs: []forge.CheckRun{run(1, "build", "github-actions", "completed", "failure")}}, nil)
	if ev.State != verify.None {
		t.Errorf("state = %q, want none_required", ev.State)
	}
	if len(ev.Checks) != 0 {
		t.Errorf("checks = %+v, want none", ev.Checks)
	}
}

// A refused or partial enumeration is unreadable, and the reason says why:
// the denial names the permission, the truncation the missing runs, and a
// transport error the error itself.
func TestEvaluateUnreadable(t *testing.T) {
	denied := &forge.CheckRunsDenied{Status: 403, Err: errors.New("gh exited 1")}
	ev := verify.Evaluate(required("build"), forge.CheckRuns{}, denied)
	if ev.State != verify.Unreadable {
		t.Fatalf("denied state = %q, want unreadable", ev.State)
	}
	if ev.Reason == "" {
		t.Error("denied reason is empty, want the permission named")
	}

	ev = verify.Evaluate(required("build"),
		forge.CheckRuns{
			Runs:      []forge.CheckRun{run(1, "build", "github-actions", "completed", "success")},
			Truncated: true,
		}, nil)
	if ev.State != verify.Unreadable {
		t.Fatalf("truncated state = %q, want unreadable", ev.State)
	}
	if ev.Reason == "" {
		t.Error("truncated reason is empty, want the missing runs named")
	}

	boom := errors.New("gh could not be started")
	ev = verify.Evaluate(required("build"), forge.CheckRuns{}, boom)
	if ev.State != verify.Unreadable || ev.Reason != boom.Error() {
		t.Errorf("transport = %q/%q, want unreadable carrying the error", ev.State, ev.Reason)
	}
}

// Each blocking state names the halt word the review leg ends the pass
// with; passed and none_required name none.
func TestHaltWord(t *testing.T) {
	for state, want := range map[verify.State]string{
		verify.Pending:    "required_check_pending",
		verify.Missing:    "required_check_missing",
		verify.Failed:     "required_check_failed",
		verify.Unreadable: "required_checks_unreadable",
	} {
		word, ok := verify.Evidence{State: state}.HaltWord()
		if !ok || word != want {
			t.Errorf("state %q halt = %q,%v, want %q,true", state, word, ok, want)
		}
	}
	for _, state := range []verify.State{verify.Passed, verify.None, ""} {
		word, ok := (verify.Evidence{State: state}).HaltWord()
		if ok {
			t.Errorf("state %q halts with %q, want no halt", state, word)
		}
	}
}
